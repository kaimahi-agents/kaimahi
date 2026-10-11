#!/usr/bin/env python3
"""Test qualification validators, privacy and one-shot native A2A behavior."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import socket
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift.py"


class QualificationTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue(SOURCE.exists(), "qualification driver is missing")
        spec = importlib.util.spec_from_file_location("kagent_lift", SOURCE)
        self.driver = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.driver)
        self.sentinel = "kmx-mcp-unit-marker"
        self.evidence = {"model_calls": 2, "tool_calls": 1, "terminal_answers": 1,
                         "sentinel_sha256": hashlib.sha256(self.sentinel.encode()).hexdigest()}

    def response(self, text=None):
        return {"jsonrpc": "2.0", "id": "kmx-lift-once", "result": {
            "kind": "task", "id": "task-1", "contextId": "context-1", "status": {"state": "completed"},
            "artifacts": [{"parts": [{"kind": "text", "text": text or "KMX MCP proof: " + self.sentinel}]}]}}

    def test_terminal_answer_must_contain_actual_mcp_sentinel_and_call_evidence(self):
        result = self.driver.validate_a2a(self.response(), self.evidence)
        self.assertEqual(result["answer_sha256"], hashlib.sha256(("KMX MCP proof: " + self.sentinel).encode()).hexdigest())
        self.assertNotIn(self.sentinel, json.dumps(result))
        for field, value in (("tool_calls", 0), ("tool_calls", 2), ("model_calls", 1), ("terminal_answers", 0)):
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                self.driver.validate_a2a(self.response(), dict(self.evidence, **{field: value}))
        with self.assertRaises(ValueError):
            self.driver.validate_a2a(self.response("plausible but not the actual tool result"), self.evidence)

    def test_pending_failed_rpc_and_history_echo_are_not_terminal_answers(self):
        for state in ("working", "failed", "canceled", "input-required"):
            response = self.response()
            response["result"]["status"]["state"] = state
            with self.subTest(state=state), self.assertRaises(ValueError):
                self.driver.validate_a2a(response, self.evidence)
        for response in ({"jsonrpc": "2.0", "id": "kmx-lift-once", "error": {"code": -1}},
                         {"jsonrpc": "2.0", "id": "wrong", "result": self.response()["result"]}):
            with self.assertRaises(ValueError):
                self.driver.validate_a2a(response, self.evidence)
        response = self.response()
        response["result"]["artifacts"] = []
        response["result"]["history"] = [{"parts": [{"kind": "text", "text": self.sentinel}]}]
        with self.assertRaises(ValueError):
            self.driver.validate_a2a(response, self.evidence)

    def test_native_identity_requires_kind_specific_ids_and_valid_context(self):
        task = self.response()
        for field, value in (("id", None), ("id", ""), ("id", " bad"), ("id", "bad/id"),
                             ("contextId", None), ("contextId", ""), ("contextId", " "),
                             ("contextId", "bad/context"), ("messageId", "conflicting")):
            changed = json.loads(json.dumps(task))
            if value is None:
                changed["result"].pop(field, None)
            else:
                changed["result"][field] = value
            with self.subTest(kind="task", field=field, value=value), self.assertRaises(ValueError):
                self.driver.validate_a2a(changed, self.evidence)
        qualified = self.driver.validate_a2a(task, self.evidence)
        self.assertEqual(qualified.get("native_context_id"), "context-1")
        message = {"jsonrpc": "2.0", "id": "kmx-lift-once", "result": {
            "kind": "message", "role": "agent", "messageId": "message-1", "contextId": "context-1",
            "parts": [{"kind": "text", "text": "KMX MCP proof: " + self.sentinel}]}}
        qualified = self.driver.validate_a2a(message, self.evidence)
        self.assertEqual(qualified["native_id"], "message-1")
        self.assertEqual(qualified.get("native_context_id"), "context-1")
        for field, value in (("messageId", None), ("messageId", ""), ("messageId", " bad"),
                             ("messageId", "bad/id"), ("id", "generic-id"), ("contextId", ""),
                             ("contextId", "bad/context")):
            changed = json.loads(json.dumps(message))
            if value is None:
                changed["result"].pop(field, None)
                changed["result"]["id"] = "fallback-id"
            else:
                changed["result"][field] = value
            with self.subTest(kind="message", field=field, value=value), self.assertRaises(ValueError):
                self.driver.validate_a2a(changed, self.evidence)
        message["result"].pop("contextId")
        self.assertEqual(self.driver.validate_a2a(message, self.evidence)["native_id"], "message-1")

    def test_mutation_audit_ignores_auth_predicate_verbs_but_records_actual_create(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            (work / "bin").mkdir()
            (work / "fail-bin").mkdir()
            real = work / "bin/kubectl"
            real.write_text("#!/usr/bin/env python3\nraise SystemExit(0)\n")
            real.chmod(0o700)
            (work / "fail-bin/kubectl").symlink_to(real)
            audit = self.driver.kubectl_audit(work)
            wrapper = work / "fail-bin/kubectl"
            document = b'{"apiVersion":"kagent.dev/v1alpha2","kind":"Agent","metadata":{"namespace":"agents","name":"lift-proof"},"spec":{"instructions":"private"}}'
            for args in (("auth", "can-i", "create", "agents", "--as=controller", "-n", "agents"),
                         ("auth", "can-i", "delete", "agents"), ("get", "secret", "create"),
                         ("create", "--dry-run=server", "-f", "-"), ("create", "-f", "-")):
                subprocess.run([str(wrapper), "--context", "kind-kmx-kagent-lift", *args], check=True,
                               input=document, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            metadata = {"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent", "namespace": "agents", "name": "lift-proof"}
            self.assertEqual(self.driver.audit_frames(audit), [
                {"verb": "create", "dry_run": "server", "resources": [metadata]},
                {"verb": "create", "dry_run": "none", "resources": [metadata]}])
            self.assertNotIn("private", audit.read_text())
            with self.assertRaisesRegex(ValueError, "mutation audit"):
                self.driver.validate_read_only("plan", {}, {}, {}, {}, self.driver.audit_frames(audit), [])

    def test_actual_audit_wrapper_and_owned_forward_replace_process_and_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory)
            (work / "bin").mkdir()
            (work / "fail-bin").mkdir()
            state = work / "forward.json"
            real = work / "bin/kubectl"
            real.write_text('''#!/usr/bin/env python3
import json, os, pathlib, socket
with socket.socket() as listener:
    listener.bind(("127.0.0.1", 0))
    listener.listen()
    port = listener.getsockname()[1]
    pathlib.Path(os.environ["FORWARD_STATE"]).write_text(json.dumps({"pid": os.getpid(), "parent": os.getppid(), "port": port}))
    print("Forwarding from 127.0.0.1:" + str(port) + " -> private", flush=True)
    while True:
        connection, _ = listener.accept()
        with connection:
            connection.sendall(b"private-loopback-proof")
''')
            real.chmod(0o700)
            (work / "fail-bin/kubectl").symlink_to(real)
            audit = self.driver.kubectl_audit(work)
            with mock.patch.dict(os.environ, PATH=str(work / "fail-bin") + os.pathsep + os.environ["PATH"],
                                 FORWARD_STATE=str(state)):
                for service, remote, namespace in (("lift-fixture", 9000, "agents"), ("kagent-controller", 8083, "kagent")):
                    for abort in (False, True):
                        identity = None
                        try:
                            with self.driver.forward(service, remote, namespace) as url:
                                identity = json.loads(state.read_text())
                                self.assertEqual(identity["parent"], os.getpid(), "wrapper spawned an unowned child")
                                with socket.create_connection(("127.0.0.1", identity["port"]), timeout=2) as connection:
                                    self.assertEqual(connection.recv(64), b"private-loopback-proof")
                                self.assertEqual(url, "http://127.0.0.1:" + str(identity["port"]))
                                if abort:
                                    raise RuntimeError("test body failure")
                        except RuntimeError:
                            if not abort:
                                raise
                        finally:
                            # Contain a regressed non-exec wrapper too; never leave a test orphan.
                            if identity is not None:
                                try:
                                    os.kill(identity["pid"], 0)
                                except ProcessLookupError:
                                    alive = False
                                else:
                                    alive = True
                                    os.kill(identity["pid"], 9)
                                self.assertFalse(alive, "owned forward left an orphan")
                        with self.assertRaises(OSError):
                            socket.create_connection(("127.0.0.1", identity["port"]), timeout=1)
                        self.assertEqual(self.driver.audit_frames(audit), [])
                        self.assertFalse(audit.exists(), "read-only forwards must leave no metadata frames")

    def test_cli_failure_projection_reports_closed_reason_without_raw_stderr(self):
        self.assertTrue(hasattr(self.driver, "cli_failure_reason"), "safe CLI error reasons are missing")
        raw = b"private prompt and credential\nError: Kagent controller workload: pod does not have exactly one controller container using ghcr.io/kagent-dev/kagent/controller:0.10.3; no Kubernetes resources were changed"
        reason = self.driver.cli_failure_reason(raw)
        self.assertEqual(reason, "controller-pod-reference-mismatch")
        self.assertNotIn("private", reason)
        self.assertEqual(self.driver.cli_failure_reason(b"unexpected raw provider response private"), "unclassified-cli-error")

    def test_read_only_check_distinguishes_closed_cluster_local_and_write_failures(self):
        self.assertTrue(hasattr(self.driver, "validate_read_only"), "read-only failure needs a closed boundary reason")
        before = {"RemoteMCPServer/lift-tools": {"uid": "same", "resource_version": "1", "spec_sha256": "a" * 64}}
        local = {"private-local-path": (0o700, None)}
        self.driver.validate_read_only("plan", before, before, local, local, 0, 0)
        changed = {"RemoteMCPServer/lift-tools": dict(before["RemoteMCPServer/lift-tools"], resource_version="2")}
        for cluster, files, writes, reason in ((changed, local, 0, "cluster evidence"),
                                              (before, {}, 0, "local evidence"),
                                              (before, local, 1, "mutation audit")):
            with self.subTest(reason=reason), contextlib.redirect_stdout(io.StringIO()) as out:
                with self.assertRaisesRegex(ValueError, reason):
                    self.driver.validate_read_only("plan", before, cluster, local, files, writes, 0)
                self.assertNotIn("private-local-path", out.getvalue())
                if cluster != before:
                    self.assertIn('"fields": ["resource_version"]', out.getvalue())

    def test_snapshot_never_contains_secret_data_or_agent_instructions(self):
        objects = [{"kind": "Secret", "metadata": {"name": "model-key", "namespace": "agents", "uid": "s1", "resourceVersion": "1"},
                    "data": {"api-key": "raw-private-credential"}},
                   {"kind": "Agent", "metadata": {"name": "lift-proof", "namespace": "agents", "uid": "a1", "generation": 1},
                    "spec": {"declarative": {"systemMessage": "raw-private-instructions"}}}]
        snapshot = self.driver.project_identity(objects)
        text = json.dumps(snapshot)
        self.assertNotIn("raw-private", text)
        self.assertEqual(snapshot["Secret/model-key"]["resource_version"], "1")
        changed = json.loads(json.dumps(objects))
        changed[1]["spec"]["declarative"]["systemMessage"] = "edited"
        self.assertNotEqual(self.driver.project_identity(changed), snapshot)
        changed = json.loads(json.dumps(objects))
        changed[0]["metadata"]["uid"] = "replacement"
        self.assertNotEqual(self.driver.project_identity(changed), snapshot)

    def test_snapshot_binds_ownership_without_payloads_or_status_version_churn(self):
        obj = {"kind": "RemoteMCPServer", "metadata": {
            "name": "lift-tools", "namespace": "agents", "uid": "remote-1", "generation": 1, "resourceVersion": "1",
            "labels": {"app.kubernetes.io/managed-by": "kmx", "unrelated": "private label"},
            "annotations": {"kaimahi.dev/bundle": "lift-proof", "kaimahi.dev/runtime-version": "v0.10.3",
                            "kaimahi.dev/portable-digest": "a" * 64, "kaimahi.dev/rendered-digest": "b" * 64,
                            "unrelated": "private annotation"},
            "ownerReferences": [{"apiVersion": "kagent.dev/v1alpha2", "kind": "Agent", "name": "lift-proof",
                                 "uid": "owner-1", "controller": True, "blockOwnerDeletion": True}]}, "spec": {"url": "private url"}}
        before = self.driver.project_identity([obj])
        changes = [("labels", "app.kubernetes.io/managed-by", None),
                   ("annotations", "kaimahi.dev/bundle", "other"),
                   ("annotations", "kaimahi.dev/runtime-version", None),
                   ("annotations", "kaimahi.dev/portable-digest", "c" * 64),
                   ("annotations", "kaimahi.dev/rendered-digest", None)]
        for section, field, value in changes:
            changed = json.loads(json.dumps(obj))
            if value is None:
                changed["metadata"][section].pop(field)
            else:
                changed["metadata"][section][field] = value
            with self.subTest(section=section, field=field):
                self.assertNotEqual(self.driver.project_identity([changed]), before)
        changed = json.loads(json.dumps(obj))
        changed["metadata"]["ownerReferences"][0]["uid"] = "replacement-owner"
        self.assertNotEqual(self.driver.project_identity([changed]), before)
        changed = json.loads(json.dumps(obj))
        changed["metadata"]["deletionTimestamp"] = "2026-10-10T00:00:00Z"
        self.assertNotEqual(self.driver.project_identity([changed]), before)
        changed = json.loads(json.dumps(obj))
        changed["metadata"]["resourceVersion"] = "2"
        changed["status"] = {"conditions": [{"message": "private status"}]}
        self.assertEqual(self.driver.project_identity([changed]), before)
        self.assertNotIn("private", json.dumps(before))

    def test_receipt_matches_bare_digests_and_rejects_replacement_uid(self):
        references = [{"Kind": kind, "Name": "lift-proof", "Namespace": "agents", "UID": kind + "-uid"}
                      for kind in ("ModelConfig", "Agent", "Deployment", "Service", "ServiceAccount", "Secret")]
        receipt = {"Target": {"Runtime": "kagent", "Context": "kind-kmx-kagent-lift", "Namespace": "agents"},
                   "Bundle": "lift-proof", "RuntimeVersion": "v0.10.3", "ClusterUID": "cluster-1", "Ready": True,
                   "ObservedAt": "2026-10-10T00:00:00Z", "PortableDigest": "a" * 64, "RenderedDigest": "b" * 64,
                   "Resources": references[:2], "Proofs": references[:2], "Workload": references[2:], "Dependencies": []}
        identities = {ref["Kind"] + "/lift-proof": {"uid": ref["UID"]} for ref in references}
        objects = {kind.lower() + "s.kagent.dev": {"metadata": {"labels": {"app.kubernetes.io/managed-by": "kmx"},
                     "annotations": {"kaimahi.dev/bundle": "lift-proof", "kaimahi.dev/runtime-version": "v0.10.3",
                                     "kaimahi.dev/portable-digest": "a" * 64, "kaimahi.dev/rendered-digest": "b" * 64}}}
                   for kind in ("ModelConfig", "Agent")}
        with tempfile.TemporaryDirectory() as directory:
            bundle = pathlib.Path(directory)
            history = bundle / "receipts/kagent-v0.10.3"
            history.mkdir(parents=True, mode=0o700)
            path = history / "receipt.json"
            path.write_text(json.dumps({"receipt": receipt}))
            path.chmod(0o600)
            with mock.patch.object(self.driver, "get", side_effect=lambda kind, name: objects[kind]):
                self.assertEqual(self.driver.validate_receipt(bundle, identities, "cluster-1"), receipt)
                path.write_text(json.dumps({"receipt": dict(receipt, AnswerText="private terminal text")}))
                with self.assertRaisesRegex(ValueError, "payload-free"):
                    self.driver.validate_receipt(bundle, identities, "cluster-1")
                path.write_text(json.dumps({"receipt": receipt}))
                identities["Agent/lift-proof"]["uid"] = "replacement"
                with self.assertRaisesRegex(ValueError, "resource identity"):
                    self.driver.validate_receipt(bundle, identities, "cluster-1")

    def test_status_binds_both_primary_uids_workload_and_cluster(self):
        status = {"bundle": "lift-proof", "runtime": "kagent", "runtimeVersion": "v0.10.3",
                  "portableDigest": "sha256:" + "a" * 64, "renderedDigest": "sha256:" + "b" * 64,
                  "targets": [{"context": "kind-kmx-kagent-lift", "namespace": "agents", "clusterUID": "cluster-1",
                               "state": "ready", "ready": True, "agentUID": "agent-1", "modelConfigUID": "model-1",
                               "deploymentUID": "deployment-1"}]}
        identities = {"Agent/lift-proof": {"uid": "agent-1"}, "ModelConfig/lift-proof": {"uid": "model-1"},
                      "Deployment/lift-proof": {"uid": "deployment-1"}}
        self.assertTrue(self.driver.status_ready(status), "ready target report must be recognized")
        self.driver.validate_status(status, identities, "cluster-1")
        for field, value in (("state", "failed"), ("agentUID", "replacement"), ("modelConfigUID", "replacement"),
                             ("clusterUID", "replacement"), ("noReceipt", True)):
            changed = json.loads(json.dumps(status))
            changed["targets"][0][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.driver.validate_status(changed, identities, "cluster-1")

    def test_native_send_is_not_retried_or_logged_on_rpc_error(self):
        calls = []
        evidence = self.evidence

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                body = json.dumps(dict(evidence, model_calls=0, tool_calls=0, terminal_answers=0)).encode()
                self.send_response(200)
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                calls.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                body = json.dumps({"jsonrpc": "2.0", "id": "kmx-lift-once", "error": {"message": "private answer"}}).encode()
                self.send_response(200)
                self.end_headers()
                self.wfile.write(body)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join, 3)
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        url = "http://127.0.0.1:" + str(server.server_port)
        with contextlib.redirect_stdout(io.StringIO()) as out, contextlib.redirect_stderr(io.StringIO()) as err:
            with self.assertRaises(ValueError):
                self.driver.one_message(url + "/a2a", url)
            self.assertEqual(out.getvalue() + err.getvalue(), "")
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["method"], "message/send")


if __name__ == "__main__":
    unittest.main()
