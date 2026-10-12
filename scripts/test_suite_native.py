#!/usr/bin/env python3
"""Hermetic ownership, image, context and payload-free metadata controls."""
import importlib.util
import pathlib
import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import threading
import http.server
import urllib.error
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[1]


class GuardTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("suite_native", ROOT / "scripts/ci/suite-native.py")
        cls.runner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.runner)

    def test_refuses_preexisting_cluster(self):
        for clusters in (["kmx-suite-native"], ["other", "kmx-suite-native"]):
            with self.assertRaises(ValueError):
                self.runner.require_absent(clusters)
        self.runner.require_absent(["agentsessions-w193-live"])

    def test_cleanup_requires_exact_node_identity_and_owner_label(self):
        node = {"Id": "owned-id", "Config": {"Labels": {"io.x-k8s.kind.cluster": "kmx-suite-native",
                                                        }}, "QualificationOwner": "owner"}
        self.assertTrue(self.runner.owned_node(node, "owned-id", "owner"))
        self.assertFalse(self.runner.owned_node(node, "different-id", "owner"))
        self.assertFalse(self.runner.owned_node(node, "owned-id", "other-owner"))
        node.pop("QualificationOwner")
        self.assertFalse(self.runner.owned_node(node, "owned-id", "owner"))

    def test_image_pin_is_not_a_tag_or_caller_override(self):
        pin = self.runner.AIKIT_IMAGE.split("@", 1)[1]
        info = {"Id": "sha256:07a421e27f3e3dc265803fbe04995b27ed07c91b62452439a7253224ad9d4902",
                "RepoDigests": ["ghcr.io/kaito-project/aikit/qwen3.5@" + pin]}
        self.runner.verify_image(info)
        for change in ({"RepoDigests": []}, {"Id": "unknown"}, {"RepoDigests": ["example.invalid/model@" + pin]}):
            with self.assertRaises(ValueError):
                self.runner.verify_image(info | change)

    def test_private_environment_never_uses_caller_auth_or_confirmation(self):
        with tempfile.TemporaryDirectory() as directory:
            env = self.runner.private_environment(pathlib.Path(directory), {"PATH": "/bin", "HOME": "/private",
                "KUBECONFIG": "/unowned", "HELM_KUBETOKEN": "PRIVATE_CANARY", "KAIMAHI_CONFIRM": "wrong",
                "OPENAI_API_KEY": "PRIVATE_CANARY", "DOCKER_HOST": "unix:///var/run/docker.sock"})
            self.assertNotIn("HELM_KUBETOKEN", env)
            self.assertNotIn("PRIVATE_CANARY", str(env))
            self.assertEqual(env["KAIMAHI_CONFIRM"], "kind-kmx-suite-native")
            self.assertEqual(env["KUBECONFIG"], directory + "/kubeconfig")
            self.assertEqual(env["DOCKER_HOST"], "unix:///var/run/docker.sock")

    def test_auth_can_i_operand_is_not_a_second_command(self):
        self.assertEqual(self.runner.kubectl_verb(["--context", "kind-kmx-suite-native", "auth", "can-i", "get", "tasks"]), "auth")
        self.assertEqual(self.runner.kubectl_verb(["--context", "kind-kmx-suite-native", "create", "token", "reader"]), "create")
        self.assertEqual(self.runner.kubectl_verb(["--context", "kind-kmx-suite-native", "rollout", "status", "deploy/controller"]), "rollout")

    def test_cleanup_removes_owned_read_only_go_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            work = pathlib.Path(directory) / "work"
            work.mkdir()
            cache = work / "cache"
            cache.mkdir()
            (cache / "file").write_text("owned")
            cache.chmod(0o500)
            self.runner.remove_private_work(work)
            self.assertFalse(work.exists())

    def test_private_cleanup_never_chmods_a_link_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            outside = root / "outside"
            outside.mkdir(mode=0o500)
            work = root / "work"
            work.mkdir()
            cache = work / "cache"
            cache.mkdir()
            (cache / "link").symlink_to(outside, target_is_directory=True)
            cache.chmod(0o500)
            self.runner.remove_private_work(work)
            self.assertEqual(outside.stat().st_mode & 0o777, 0o500)

    def test_audit_effective_dry_run_and_write_domain(self):
        import json
        document = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": {"name": "native-proof", "namespace": "orka-system"}, "spec": {"defaultModel": "exact"}}
        base = ["--context", "kind-kmx-suite-native", "-n", "orka-system", "create", "--validate=strict", "-f", "-", "-o", "json"]
        frame = self.runner.audit_frame(base + ["--dry-run", "server"], json.dumps(document).encode())
        self.assertTrue(frame["dryRun"])
        self.assertTrue(self.runner.audit_frame(base, json.dumps(document).encode())["persistentWrite"])
        for flags in (["--dry-run=server", "--dry-run=none"], ["--dry-run=none", "--dry-run=server"],
                      ["--dry-run=true"], ["--dry-run=client"], ["--context", "other"], ["--kubeconfig", "/unowned"],
                      ["--filename", "/unowned"], ["--raw=/api/v1/secrets"], ["--namespace", "other"]):
            with self.subTest(flags=flags), self.assertRaises(ValueError):
                self.runner.audit_frame(base + flags, json.dumps(document).encode())
        for kind in ("Secret", "Deployment", "Role", "ServiceAccount", "AgentRuntime"):
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                self.runner.audit_frame(base, json.dumps(document | {"kind": kind}).encode())
        task = document | {"kind": "Task", "metadata": {"name": "native-proof-eval-" + "a" * 16, "namespace": "orka-system"}, "spec": {"type": "agent", "agentRef": {"name": "native-proof", "namespace": "orka-system"}}}
        with self.assertRaises(ValueError):
            self.runner.audit_frame(base, json.dumps(task).encode())
        for command in (["rollout", "restart", "deploy/controller"], ["delete", "agent", "native-proof"],
                        ["apply", "-f", "-"], ["replace", "-f", "-"]):
            with self.subTest(command=command), self.assertRaises(ValueError):
                self.runner.audit_frame(base[:2] + command, json.dumps(document).encode())
        token = self.runner.audit_frame(base[:4] + ["create", "token", "orka-result-reader", "--duration=10m", "-o", "json"], b"")
        self.assertFalse(token["persistentWrite"])
        self.assertTrue(token["tokenRequest"])

    def test_audit_execution_refuses_before_reaching_real_tool_and_never_logs_payload(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            audit = root / "audit.jsonl"
            real = root / "real"
            reached = root / "reached"
            real.write_text('#!/bin/sh\n: > "' + str(reached) + '"\nexit 0\n')
            real.chmod(0o700)
            env = dict(os.environ, KMX_SUITE_AUDIT_CONTEXT="kind-kmx-suite-native", KMX_SUITE_AUDIT_FILE=str(audit), KMX_SUITE_REAL_KUBECTL=str(real))
            invoke = [sys.executable, "-c", 'import runpy,sys; m=runpy.run_path(sys.argv[1]); sys.exit(m["audit_kubectl"](sys.argv[2:]))', str(ROOT / "scripts/ci/suite-native.py")]
            args = ["--context", "kind-kmx-suite-native", "-n", "orka-system", "create", "--validate=strict", "-f", "-", "-o", "json"]
            body = json.dumps({"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": {"name": "native-proof", "namespace": "orka-system"}, "spec": {"instructions": "PRIVATE_CANARY"}}).encode()
            self.assertEqual(subprocess.run(invoke + args, input=body, env=env, capture_output=True).returncode, 0)
            self.assertTrue(reached.exists())
            self.assertNotIn("PRIVATE_CANARY", audit.read_text())
            reached.unlink()
            for extra in (["--dry-run=server", "--dry-run=none"], ["--context", "other"]):
                result = subprocess.run(invoke + args + extra, input=body, env=env, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(reached.exists())
                self.assertNotIn(b"PRIVATE_CANARY", result.stdout + result.stderr)
            result = subprocess.run(invoke + args, input=b'PRIVATE_CANARY: [', env=env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(reached.exists())
            self.assertNotIn(b"PRIVATE_CANARY", result.stdout + result.stderr)

    def test_timeout_kills_owned_process_group_including_term_ignoring_child(self):
        with tempfile.TemporaryDirectory() as directory:
            pid = pathlib.Path(directory) / "pid"
            code = ('import subprocess,sys,time; child=subprocess.Popen([sys.executable,"-c",'
                    '"import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(60)"]); '
                    'open(sys.argv[1],"w").write(str(child.pid)); time.sleep(60)')
            with self.assertRaises(subprocess.TimeoutExpired):
                self.runner.bounded_run([sys.executable, "-c", code, str(pid)], timeout=0.3, grace=0.1)
            self.assert_child_stopped(int(pid.read_text()))

    def assert_child_stopped(self, pid):
        status = pathlib.Path(f"/proc/{pid}/stat")
        for _ in range(30):
            if not status.exists() or status.read_text().split()[2] == "Z":
                return
            time.sleep(0.05)
        self.fail("owned child survived cancellation")

    def test_signal_cancellation_stops_owned_child_group(self):
        with tempfile.TemporaryDirectory() as directory:
            pid = pathlib.Path(directory) / "pid"
            worker = ('import runpy,signal,sys; m=runpy.run_path(sys.argv[1]); '
                      'signal.signal(signal.SIGTERM,lambda *_:sys.exit(143)); '
                      'm["bounded_run"]([sys.executable,"-c","import os,sys,time; open(sys.argv[1],\\"w\\").write(str(os.getpid())); time.sleep(60)",sys.argv[2]],timeout=60,grace=0.1)')
            process = subprocess.Popen([sys.executable, "-c", worker, str(ROOT / "scripts/ci/suite-native.py"), str(pid)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                for _ in range(100):
                    if pid.exists():
                        break
                    time.sleep(0.02)
                self.assertTrue(pid.exists(), "signal-test child did not start")
                process.send_signal(signal.SIGTERM)
                self.assertEqual(process.wait(timeout=5), 143)
                self.assert_child_stopped(int(pid.read_text()))
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()

    def test_cleanup_authority_loss_refuses_cluster_or_model_deletion(self):
        import json
        for kind in ("node", "model"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                qualifier = self.runner.Qualification(pathlib.Path(directory))
                identity = "a" * 64
                qualifier.owner = "owner"
                if kind == "node":
                    qualifier.node_id, qualifier.cluster_uid = identity, "cluster-uid"
                    qualifier.nodes = {identity: {"name": "kmx-suite-native-control-plane", "uid": "node-uid"}}
                else:
                    qualifier.model_id = identity
                calls = []
                def fake_run(*args, **_kwargs):
                    calls.append(args)
                    if args[:3] == ("docker", "ps", "-a"):
                        return subprocess.CompletedProcess(args, 0, (identity + "\n").encode(), b"")
                    if args[:2] == ("docker", "inspect"):
                        return subprocess.CompletedProcess(args, 0, json.dumps([{"Id": "replaced-id", "Config": {"Labels": {}}}]).encode(), b"")
                    raise AssertionError("deletion was attempted without authority")
                qualifier.run = fake_run
                qualifier.k = lambda *_args, **_kwargs: subprocess.CompletedProcess([], 0, b'{"metadata":{"labels":{}}}', b"")
                self.assertFalse(qualifier.cleanup())
                self.assertTrue(any(c[:2] == ("docker", "inspect") for c in calls))
                self.assertFalse(json.loads((pathlib.Path(directory) / "qualification.json").read_bytes())["qualified"])

    def test_namespace_uid_loss_blocks_owned_cluster_deletion(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            qualifier = self.runner.Qualification(pathlib.Path(directory))
            identity = "a" * 64
            qualifier.node_id, qualifier.owner, qualifier.namespace_uid = identity, "owner", "original-namespace"
            qualifier.nodes = {identity: {"name": "kmx-suite-native-control-plane", "uid": "node-uid"}}
            qualifier.cluster_uid = "cluster-uid"
            calls = []
            def fake_run(*args, **_kwargs):
                calls.append(args)
                if args[:3] == ("docker", "ps", "-a"):
                    return subprocess.CompletedProcess(args, 0, (identity + "\n").encode(), b"")
                self.assertEqual(args[:2], ("docker", "inspect"))
                return subprocess.CompletedProcess(args, 0, json.dumps([{"Id": identity, "Name": "/kmx-suite-native-control-plane", "Config": {"Labels": {"io.x-k8s.kind.cluster": "kmx-suite-native"}}}]).encode(), b"")
            qualifier.run = fake_run
            qualifier.k = lambda *_args, **_kwargs: subprocess.CompletedProcess([], 0, b'{"metadata":{"name":"kmx-suite-native-control-plane","uid":"node-uid","labels":{"kmx.kaimahi.ai/native-qualification":"owner"}}}', b"")
            qualifier.obj = lambda _kind, name: {"metadata": {"uid": "cluster-uid" if name == "kube-system" else "replaced-namespace"}}
            self.assertFalse(qualifier.cleanup())
            self.assertEqual(len(calls), 2)
            self.assertFalse(json.loads((pathlib.Path(directory) / "qualification.json").read_bytes())["qualified"])

    def test_unverified_receipt_and_raw_output_are_never_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            artifact = pathlib.Path(directory)
            qualifier = self.runner.Qualification(artifact)
            work = qualifier.work
            (work / "receipt.json").write_text('{"answer":"PRIVATE_CANARY"}')
            qualifier.log.write(b"PRIVATE_CANARY")
            self.assertTrue(qualifier.cleanup())
            self.assertFalse(work.exists())
            self.assertFalse((artifact / "receipt.json").exists())
            self.assertNotIn("PRIVATE_CANARY", (artifact / "qualification.json").read_text())

    def test_warmup_cannot_use_inherited_proxy_or_follow_redirect(self):
        calls = []
        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass
            def do_GET(self):
                calls.append(self.path)
                if self.path == "/redirect":
                    self.send_response(302)
                    self.send_header("Location", "/unowned")
                else:
                    self.send_response(200)
                self.end_headers()
                self.wfile.write(b"fixture")
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        base = f"http://127.0.0.1:{server.server_port}"
        try:
            with mock.patch.dict(os.environ, {"http_proxy": "http://127.0.0.1:1", "HTTP_PROXY": "http://127.0.0.1:1", "no_proxy": "", "NO_PROXY": ""}):
                with self.runner.model_http(base + "/readyz", timeout=2) as response:
                    self.assertEqual(response.read(), b"fixture")
                with self.assertRaises(urllib.error.HTTPError) as refused:
                    self.runner.model_http(base + "/redirect", timeout=2)
                self.assertTrue(refused.exception.fp is None or refused.exception.fp.closed)
            self.assertEqual(calls, ["/readyz", "/redirect"])
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_executable_audit_refuses_mutating_subcommands_foreign_targets_and_flag_escapes(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            audit, reached, real = root / "audit", root / "reached", root / "real"
            real.write_text('#!/bin/sh\n: > "' + str(reached) + '"\nexit 0\n')
            real.chmod(0o700)
            env = dict(os.environ, KMX_SUITE_AUDIT_CONTEXT="kind-kmx-suite-native", KMX_SUITE_AUDIT_FILE=str(audit), KMX_SUITE_REAL_KUBECTL=str(real))
            invoke = [sys.executable, "-c", 'import runpy,sys; m=runpy.run_path(sys.argv[1]); sys.exit(m["audit_kubectl"](sys.argv[2:]))', str(ROOT / "scripts/ci/suite-native.py")]
            prefix = ["--context", "kind-kmx-suite-native"]
            provider = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider", "metadata": {"name": "PRIVATE_CANARY", "namespace": "orka-system"}, "spec": {}}
            commands = [
                (["auth", "reconcile", "-f", "-"], b'{"kind":"Role","rules":[{"resources":["secrets"],"verbs":["get"]}]}'),
                (["config", "set-cluster", "foreign", "--server=https://PRIVATE_CANARY.invalid"], b""),
                (["config", "set", "clusters.foreign.server", "https://PRIVATE_CANARY.invalid"], b""),
                (["-n", "orka-system", "create", "--validate=strict", "-f", "-", "-o", "json"], json.dumps(provider).encode()),
                (["create", "token", "orka-result-reader", "-nother", "--duration=10m", "-o", "json"], b""),
                (["-n", "orka-system", "create", "token", "foreign", "--duration=10m", "-o", "json"], b""),
                (["-n", "orka-system", "create", "token", "orka-result-reader", "--dry-run=server", "--duration=10m", "-o", "json"], b""),
                (["-n", "orka-system", "get", "secret", "qualification-model-key", "-o", "json"], b""),
                (["patch", "provider", "native-proof", "-n", "orka-system", "--type=json", "-p", "[]"], b""),
                (["-n", "orka-system", "get", "deploy", "--dry-run=server", "-o", "json"], b""),
                (["-n", "orka-system", "get", "deploy", "-n", "orka-system", "-o", "json"], b""),
                (["config", "view", "--raw", "-o", "json"], b""),
                (["-n", "orka-system", "port-forward", "--address", "0.0.0.0", "svc/orka-api", "19187:8080"], b""),
            ]
            for args, body in commands:
                reached.unlink(missing_ok=True)
                audit.unlink(missing_ok=True)
                with self.subTest(command=args[:3]):
                    result = subprocess.run(invoke + prefix + args, input=body, env=env, capture_output=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(reached.exists(), "refused command reached real kubectl")
                    self.assertEqual(result.stdout + result.stderr, b"")
                    self.assertFalse(audit.exists(), "invalid frame became an artifact")

    def test_cleanup_refuses_extra_or_replaced_cluster_inventory_before_any_node_deletion(self):
        import json
        owned, foreign = "a" * 64, "b" * 64
        for inventory in (owned + "\n" + foreign + "\n", foreign + "\n", ""):
            with self.subTest(inventory_count=len(inventory.splitlines())), tempfile.TemporaryDirectory() as directory:
                qualifier = self.runner.Qualification(pathlib.Path(directory))
                qualifier.node_id, qualifier.owner = owned, "owner"
                qualifier.nodes = {owned: {"name": "kmx-suite-native-control-plane", "uid": "node-uid"}}
                qualifier.cluster_uid, qualifier.namespace_uid = "cluster-uid", "namespace-uid"
                qualifier.summary["qualified"] = True
                deleted = []
                def fake_run(*args, **_kwargs):
                    if args[:3] == ("docker", "ps", "-a"):
                        return subprocess.CompletedProcess(args, 0, inventory.encode(), b"")
                    if args[:2] == ("docker", "inspect"):
                        return subprocess.CompletedProcess(args, 0, json.dumps([{"Id": owned, "Name": "/kmx-suite-native-control-plane", "Config": {"Labels": {"io.x-k8s.kind.cluster": "kmx-suite-native"}}}]).encode(), b"")
                    if args[:3] == ("kind", "get", "clusters"):
                        return subprocess.CompletedProcess(args, 0, b"", b"")
                    if args[:2] == ("docker", "rm") or args[:3] == ("kind", "delete", "cluster"):
                        deleted.append(args)
                        return subprocess.CompletedProcess(args, 0, b"", b"")
                    raise AssertionError("unexpected external operation")
                qualifier.run = fake_run
                qualifier.k = lambda *_args, **_kwargs: subprocess.CompletedProcess([], 0, b'{"metadata":{"name":"kmx-suite-native-control-plane","uid":"node-uid","labels":{"kmx.kaimahi.ai/native-qualification":"owner"}}}', b"")
                qualifier.obj = lambda _kind, name: {"metadata": {"uid": "cluster-uid" if name == "kube-system" else "namespace-uid"}}
                self.assertFalse(qualifier.cleanup())
                self.assertEqual(deleted, [])
                self.assertFalse(json.loads((pathlib.Path(directory) / "qualification.json").read_bytes())["qualified"])

    def test_preexisting_stopped_cluster_label_container_refuses_before_kind_create(self):
        with tempfile.TemporaryDirectory() as directory:
            qualifier = self.runner.Qualification(pathlib.Path(directory))
            created = []
            def fake_run(*args, **_kwargs):
                if args[:3] == ("kind", "get", "clusters"):
                    return subprocess.CompletedProcess(args, 0, b"", b"")
                if args[:3] == ("docker", "ps", "-a"):
                    return subprocess.CompletedProcess(args, 0, ("b" * 64 + "\n").encode(), b"")
                created.append(args)
                raise ValueError("creation-reached")
            qualifier.run = fake_run
            try:
                with self.assertRaises(ValueError):
                    qualifier.infrastructure()
                self.assertEqual(created, [])
                self.assertFalse(qualifier.cluster_create_attempted)
            finally:
                qualifier.cluster_create_attempted = False
                qualifier.cleanup()

    def test_failed_kind_create_retains_concurrent_foreign_inventory_for_manual_inspection(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            tools = root / "bin"
            tools.mkdir()
            inventory, invocation, deleted = root / "foreign-inventory", root / "create-args", root / "automatic-delete"
            # Model the pinned kind failed-create cleanup contract without ever
            # running kind/Docker against real caller resources. The competing
            # winner appears after this runner's two successful absence checks.
            kind = tools / "kind"
            kind.write_text('#!' + sys.executable + '\nimport json,pathlib,sys\n'
                'args=sys.argv[1:]\n'
                'if args == ["get","clusters"]: sys.exit(0)\n'
                'if args[:2] != ["create","cluster"]: sys.exit(97)\n'
                'pathlib.Path(' + repr(str(invocation)) + ').write_text(json.dumps(args))\n'
                'inventory=pathlib.Path(' + repr(str(inventory)) + ')\n'
                'inventory.write_text("' + "b" * 64 + '\\n")\n'
                'if "--retain" not in args:\n'
                '    pathlib.Path(' + repr(str(deleted)) + ').write_text("label-selected-delete")\n'
                '    inventory.unlink()\n'
                'sys.exit(1)\n')
            kind.chmod(0o700)
            docker = tools / "docker"
            docker.write_text('#!' + sys.executable + '\nimport pathlib,sys\n'
                'if sys.argv[1:4] != ["ps","-a","--no-trunc"]: sys.exit(97)\n'
                'inventory=pathlib.Path(' + repr(str(inventory)) + ')\n'
                'if inventory.exists(): sys.stdout.write(inventory.read_text())\n')
            docker.chmod(0o700)
            qualifier = self.runner.Qualification(root)
            qualifier.env["PATH"] = str(tools) + ":" + qualifier.env["PATH"]
            try:
                with self.assertRaises(ValueError):
                    qualifier.infrastructure()
                self.assertTrue(qualifier.cluster_create_attempted)
                self.assertEqual(qualifier.nodes, {})
                self.assertFalse(qualifier.cleanup())
                self.assertFalse(deleted.exists(), "kind automatically deleted competing inventory")
                self.assertEqual(inventory.read_text(), "b" * 64 + "\n")
                self.assertIn("--retain", json.loads(invocation.read_text()))
                summary = json.loads((root / "qualification.json").read_bytes())
                self.assertFalse(summary["qualified"])
                self.assertEqual(summary["reason"], "ambiguous-create-left-for-inspection")
                self.assertEqual(summary["cleanup"], "refused-or-failed-inspect-owned-resources")
                self.assertFalse(qualifier.work.exists())
            finally:
                if not qualifier.log.closed:
                    qualifier.cleanup()

    def test_current_native_read_forms_are_allowed_but_unknown_options_are_not(self):
        prefix = ["--context", "kind-kmx-suite-native", "--request-timeout=10s"]
        reads = [
            ["config", "view", "-o", "json"],
            ["get", "namespace", "kube-system", "-o", "json"],
            ["get", "namespace", "orka-system", "--ignore-not-found=true", "-o", "json"],
            ["get", "crd", "agents.core.orka.ai", "-o", "json"],
            ["get", "crd", "agents.core.orka.ai", "--ignore-not-found=true", "-o", "name"],
            ["-n", "orka-system", "get", "deploy", "-o", "json"],
            ["-n", "orka-system", "get", "providers.core.orka.ai", "qualification-inference", "-o", "json"],
            ["-n", "orka-system", "get", "secret", "qualification-model-key", "--ignore-not-found=true", "-o", self.runner.SECRET_METADATA_OUTPUT],
            ["-n", "orka-system", "get", "secret", "qualification-model-key", "--ignore-not-found=true", "-o", self.runner.SECRET_KEY_OUTPUT],
            ["-n", "orka-system", "get", "serviceaccount", "orka-api-ai-worker", "-o", self.runner.WORKER_METADATA_OUTPUT],
            ["-n", "orka-system", "get", "serviceaccounts", "-o", "json"],
            ["-n", "orka-system", "get", "services", "-o", "json"],
            ["-n", "orka-system", "get", "service", "orka-api", "-o", "json"],
            ["-n", "orka-system", "get", "serviceaccount", "orka-result-reader", "--ignore-not-found=true", "-o", "name"],
            ["-n", "orka-system", "rollout", "status", "deploy/orka-api-controller", "--timeout=10s"],
            ["-n", "orka-system", "create", "token", "orka-result-reader", "--duration=10m", "-o", "json"],
            ["-n", "orka-system", "port-forward", "--address", "127.0.0.1", "svc/orka-api", "19187:8080"],
        ]
        for command in reads:
            with self.subTest(command=command[:3]):
                frame = self.runner.audit_frame(prefix + command, b"")
                self.assertFalse(frame["persistentWrite"])
                self.assertTrue(frame["contextPinned"])
                for extra in (["--dry-run=server"], ["--future-option=true"], ["--context", "kind-kmx-suite-native"]):
                    with self.assertRaises(ValueError):
                        self.runner.audit_frame(prefix + command + extra, b"")

    def test_task_write_domain_and_recorded_identity_bound_read_execution(self):
        import json
        task = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Task", "metadata": {"name": "native-proof-eval-" + "a" * 16, "namespace": "orka-system"}, "spec": {"type": "ai", "agentRef": {"name": "native-proof", "namespace": "orka-system"}}}
        prefix = ["--context", "kind-kmx-suite-native", "-n", "orka-system"]
        create = prefix + ["create", "--validate=strict", "-f", "-", "-o", "json"]
        self.assertTrue(self.runner.audit_frame(create, json.dumps(task).encode())["persistentWrite"])
        for change in ({"kind": "Secret"}, {"apiVersion": "foreign/v1"}, {"metadata": {"name": "foreign", "namespace": "orka-system"}}, {"spec": {"type": "agent"}}, {"spec": {"type": "ai", "agentRef": {"name": "foreign", "namespace": "orka-system"}}}):
            with self.assertRaises(ValueError):
                self.runner.audit_frame(create, json.dumps(task | change).encode())
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            audit, reached, real = root / "audit", root / "reached", root / "real"
            real.write_text('#!/bin/sh\n: > "' + str(reached) + '"\nexit 0\n')
            real.chmod(0o700)
            env = dict(os.environ, KMX_SUITE_AUDIT_CONTEXT="kind-kmx-suite-native", KMX_SUITE_AUDIT_FILE=str(audit), KMX_SUITE_REAL_KUBECTL=str(real))
            invoke = [sys.executable, "-c", 'import runpy,sys; m=runpy.run_path(sys.argv[1]); sys.exit(m["audit_kubectl"](sys.argv[2:]))', str(ROOT / "scripts/ci/suite-native.py")]
            get = prefix + ["get", "tasks.core.orka.ai", task["metadata"]["name"], "-o", "json"]
            self.assertEqual(subprocess.run(invoke + get, env=env, capture_output=True).returncode, 96)
            self.assertFalse(reached.exists())
            self.assertEqual(subprocess.run(invoke + create, input=json.dumps(task).encode(), env=env, capture_output=True).returncode, 0)
            reached.unlink()
            self.assertEqual(subprocess.run(invoke + create, input=json.dumps(task).encode(), env=env, capture_output=True).returncode, 96)
            self.assertFalse(reached.exists())
            self.assertEqual(subprocess.run(invoke + get, env=env, capture_output=True).returncode, 0)
            reached.unlink()
            get[6] = "native-proof-eval-" + "b" * 16
            self.assertEqual(subprocess.run(invoke + get, env=env, capture_output=True).returncode, 96)
            self.assertFalse(reached.exists())

    def test_cleanup_deletes_only_verified_ids_and_refuses_node_or_cluster_uid_loss(self):
        import json
        identity = "a" * 64
        for drift in ("none", "node-uid", "cluster-uid", "owner", "late-foreign"):
            with self.subTest(drift=drift), tempfile.TemporaryDirectory() as directory:
                qualifier = self.runner.Qualification(pathlib.Path(directory))
                qualifier.node_id, qualifier.owner, qualifier.cluster_uid, qualifier.namespace_uid = identity, "owner", "cluster-uid", "namespace-uid"
                qualifier.nodes = {identity: {"name": "kmx-suite-native-control-plane", "uid": "node-uid"}}
                qualifier.summary["qualified"] = True
                deleted = []
                def fake_run(*args, **_kwargs):
                    if args[:3] == ("docker", "ps", "-a"):
                        inventory = (("b" * 64 + "\n") if drift == "late-foreign" else "") if deleted else identity + "\n"
                        return subprocess.CompletedProcess(args, 0, inventory.encode(), b"")
                    if args[:2] == ("docker", "inspect"):
                        return subprocess.CompletedProcess(args, 0, json.dumps([{"Id": identity, "Name": "/kmx-suite-native-control-plane", "Config": {"Labels": {"io.x-k8s.kind.cluster": "kmx-suite-native"}}}]).encode(), b"")
                    if args == ("docker", "rm", "--force", "--volumes", identity):
                        deleted.append(identity)
                        return subprocess.CompletedProcess(args, 0, b"", b"")
                    if args == ("kind", "get", "clusters"):
                        self.assertEqual(deleted, [identity], "absence was claimed before own ID removal")
                        return subprocess.CompletedProcess(args, 0, b"agentsessions-w193-live\n", b"")
                    raise AssertionError("name-based deletion or shared-network operation attempted")
                qualifier.run = fake_run
                live = {"metadata": {"name": "kmx-suite-native-control-plane", "uid": "changed" if drift == "node-uid" else "node-uid", "labels": {"kmx.kaimahi.ai/native-qualification": "changed" if drift == "owner" else "owner"}}}
                qualifier.k = lambda *_args, **_kwargs: subprocess.CompletedProcess([], 0, json.dumps(live).encode(), b"")
                qualifier.obj = lambda _kind, name: {"metadata": {"uid": ("changed" if drift == "cluster-uid" else "cluster-uid") if name == "kube-system" else "namespace-uid"}}
                self.assertEqual(qualifier.cleanup(), drift == "none")
                self.assertEqual(deleted, [identity] if drift in ("none", "late-foreign") else [])
                self.assertEqual(json.loads((pathlib.Path(directory) / "qualification.json").read_bytes())["qualified"], drift == "none")

    def ownership_api_fixture(self, root, drift="status", at=1):
        """Run the real ownership loop/exec path; unrelated CLI/Go work is inert."""
        import json
        qualifier = self.runner.Qualification(root)
        original = {"apiVersion": "core.orka.ai/v1alpha1", "kind": "Provider",
            "metadata": {"name": "native-proof", "namespace": "orka-system", "uid": "owned-provider",
                "resourceVersion": "1", "annotations": {"kaimahi.dev/suite": "owned", "unrelated": "keep"}},
            "spec": {"defaultModel": "qwen-3.5-2b"}, "status": {"ready": True, "updates": 0}}
        state = root / "api.json"
        state.write_text(json.dumps({"provider": original, "attempts": [], "writes": [], "drift": drift, "at": at}))
        tool = qualifier.work / "bin/kubectl-real"
        tool.write_text('#!' + sys.executable + '\n' + '''import copy,json,pathlib,sys
state_path=pathlib.Path(''' + repr(str(state)) + ''')
s=json.loads(state_path.read_text())
a=sys.argv[1:]
assert a[:2] == ["--context","kind-kmx-suite-native"]
assert a[2] == "--cache-dir"
a=a[4:]
if a[:2] == ["-n","orka-system"]: a=a[2:]
p=s["provider"]
if a[:1] == ["get"]:
    if a[1:3] == ["provider","native-proof"]: print(json.dumps(p))
    elif a[1:3] == ["provider","qualification-inference"]: print('{"spec":{"baseURL":"http://fixture.invalid/v1"}}')
    elif a[1:3] == ["agent","native-proof"]: print(json.dumps({"spec":{"systemPrompt":{"inline":"Answer arithmetic questions with only the decimal integer. Do not explain.\\n"}}}))
    elif a[1:3] == ["secret","qualification-model-key"]: sys.stdout.write("secret-uid:1")
    else: sys.exit(97)
    sys.exit(0)
assert a[0] in ("replace","patch")
if "--patch-file=-" in a:
    sys.stderr.write("patch file is not stdin")
    sys.exit(1)
body=json.loads(sys.stdin.buffer.read() if a[0] == "replace" else a[-1])
s["attempts"].append({"args":a,"body":body})
p["metadata"]["resourceVersion"]=str(int(p["metadata"]["resourceVersion"])+1)
p["status"]["updates"]+=1
if len(s["attempts"]) == s["at"]:
    if s["drift"] == "uid": p["metadata"]["uid"]="foreign-provider"
    elif s["drift"] == "annotations": p["metadata"]["annotations"]["foreign-owner"]="do-not-adopt"
before=copy.deepcopy(p)
if a[0] == "replace":
    assert a == ["replace","-f","-"]
    if body["metadata"]["resourceVersion"] != p["metadata"]["resourceVersion"]:
        state_path.write_text(json.dumps(s))
        sys.stderr.write("the object has been modified")
        sys.exit(1)
    s["provider"]=body
    s["writes"].append({"before":before,"after":body})
    state_path.write_text(json.dumps(s))
    sys.exit(0)
assert a[:-1] == ["patch","provider","native-proof","-n","orka-system","--type=json","-p"]
candidate=copy.deepcopy(p)
for op in body:
    section,key=op["path"].strip("/").split("/")
    if op["op"] == "test":
        if section not in candidate or key not in candidate[section] or candidate[section][key] != op["value"]:
            s["refusal"]="atomic-test"
            state_path.write_text(json.dumps(s))
            sys.stderr.write("atomic prerequisite test refused")
            sys.exit(1)
    elif op["op"] == "replace":
        assert section in candidate and key in candidate[section]
        candidate[section][key]=op["value"]
    else: sys.exit(97)
s["provider"]=candidate
s["writes"].append({"before":before,"after":candidate})
state_path.write_text(json.dumps(s))
''')
        tool.chmod(0o700)
        qualifier.source = {"spec": {"baseURL": "http://fixture.invalid/v1"}}
        qualifier.secret_metadata = b"secret-uid:1"
        digest = "sha256:" + "a" * 64
        receipt = {"subject": {"suiteDigest": digest, "requiredImages": []}, "runID": "b" * 32,
                   "cases": [{"id": "arithmetic", "verdict": "pass"}]}
        audit = qualifier.work / "audit.jsonl"
        audit.write_bytes(b"".join(json.dumps({"verb": "create", "dryRun": False, "contextPinned": True,
            "persistentWrite": True, "kind": kind}).encode() + b"\n" for kind in ("Provider", "Agent", "Task")))
        def fake_cli(*args, **_kwargs):
            stdout, stderr = b"", b""
            if args[:2] == ("agent", "create"):
                workspace = qualifier.work / "workspace"
                (workspace / "eval").mkdir(parents=True)
                (workspace / "eval/case.yaml").write_text("fixture")
                member = workspace / "agents/native-proof"
                member.mkdir(parents=True)
                (member / "instructions.md").write_text("fixture")
                (member / "member.yaml").write_text('model:\n  model: qwen-3.5-2b\n')
            elif args[:2] == ("suite", "push"):
                layout = pathlib.Path(args[args.index("--to-layout") + 1])
                if layout.name.startswith("drift-"):
                    return subprocess.CompletedProcess(args, 1, b"", b"rerun evaluation")
                (layout / "blobs/sha256").mkdir(parents=True)
                (layout / "blobs/sha256/fixture").write_bytes(b"exact packaged bytes")
                (layout / "index.json").write_text(json.dumps({"manifests": [{"digest": digest}]}))
            elif "evaluate" in args:
                out = pathlib.Path(args[args.index("--out") + 1])
                if out.name != "receipt.json":
                    return subprocess.CompletedProcess(args, 1, b"", b"native preparation unavailable; no placement attempted")
                out.write_text(json.dumps(receipt))
                history = qualifier.work / ("kmx-home/suite-evidence/runs/eval-" + receipt["runID"] + ".json")
                stdout = ("arithmetic: pass\nsuite evaluation full: pass\nS: " + digest + "\nrunID: " + receipt["runID"] +
                    "\nrequiredImages: []\nhistory: " + str(history) + "\nreceipt: " + str(out) + "\n").encode()
            elif "agent" in args and "create" in args:
                return subprocess.CompletedProcess(args, 1, b"", b"has suite ownership markers")
            elif args[:2] not in (("suite", "pull"), ("suite", "validate")):
                raise AssertionError("unexpected CLI boundary call")
            return subprocess.CompletedProcess(args, 0, stdout, stderr)
        qualifier.cli = fake_cli
        real_run = qualifier.run
        def fake_verifier(*args, **kwargs):
            if args[:3] != ("go", "test", "./internal/kmx/app"):
                return real_run(*args, **kwargs)
            mode = kwargs["env"].get("KMX_SUITE_NATIVE_REFUSAL")
            (qualifier.work / ("refused-" + mode + ".json" if mode else "verified.json")).write_text("{}")
            return subprocess.CompletedProcess(args, 0, b"--- PASS: TestLiveSuiteNativeEvidence\n", b"")
        qualifier.run = fake_verifier
        return qualifier, state, original

    def test_ownership_prerequisites_and_restores_ignore_status_churn_without_other_writes(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            qualifier, state, original = self.ownership_api_fixture(pathlib.Path(directory))
            try:
                try:
                    qualifier.qualify()
                except ValueError:
                    self.fail("status-only churn blocked ownership prerequisite: " + qualifier.summary.get("reason", "unknown"))
                observed = json.loads(state.read_text())
                self.assertTrue(qualifier.summary["qualified"])
                self.assertEqual(len(observed["attempts"]), 6)
                self.assertEqual(len(observed["writes"]), 6)
                self.assertEqual(observed["provider"]["metadata"]["annotations"], original["metadata"]["annotations"])
                self.assertEqual(observed["provider"]["status"]["updates"], 6)
                self.assertEqual([w["after"]["metadata"]["annotations"] for w in observed["writes"]], [
                    {"unrelated": "keep"}, original["metadata"]["annotations"],
                    {"unrelated": "keep", "kaimahi.dev/bundle": "foreign"}, original["metadata"]["annotations"],
                    {"kaimahi.dev/suite": "owned", "unrelated": "keep", "kaimahi.dev/bundle": "foreign"},
                    original["metadata"]["annotations"]])
                for attempt, write in zip(observed["attempts"], observed["writes"]):
                    self.assertEqual([op["path"] for op in attempt["body"]],
                        ["/metadata/uid", "/metadata/annotations", "/metadata/annotations"])
                    self.assertEqual([op["op"] for op in attempt["body"]], ["test", "test", "replace"])
                    self.assertEqual(attempt["body"][0]["value"], "owned-provider")
                    self.assertEqual(attempt["body"][1]["value"], write["before"]["metadata"]["annotations"])
                    before, after = write["before"], write["after"]
                    before["metadata"].pop("annotations")
                    after["metadata"].pop("annotations")
                    self.assertEqual(before, after, "prerequisite changed more than annotations")
            finally:
                qualifier.cleanup()

    def test_ownership_prerequisite_and_restore_refuse_uid_or_annotation_drift_atomically_without_retry(self):
        import json
        for drift in ("uid", "annotations"):
            for at in range(1, 7):
                mode = ("unmarked", "bundle", "mixed")[(at - 1) // 2]
                phase = "prerequisite" if at % 2 else "restore-prerequisite"
                with self.subTest(drift=drift, mode=mode, phase=phase), tempfile.TemporaryDirectory() as directory:
                    qualifier, state, original = self.ownership_api_fixture(pathlib.Path(directory), drift, at)
                    try:
                        with self.assertRaisesRegex(ValueError, "^command-refused$"):
                            qualifier.qualify()
                        observed = json.loads(state.read_text())
                        self.assertEqual(qualifier.phase, "ownership-" + mode + "-" + phase)
                        self.assertEqual(len(observed["attempts"]), at, "refused prerequisite retried")
                        self.assertEqual(observed["refusal"], "atomic-test", "unrelated failure accepted as ownership refusal")
                        self.assertEqual(len(observed["writes"]), at - 1, "failed atomic guard mutated provider")
                        expected = original if at == 1 else observed["writes"][-1]["after"]
                        expected["metadata"]["resourceVersion"] = str(at + 1)
                        expected["status"]["updates"] = at
                        if drift == "uid":
                            expected["metadata"]["uid"] = "foreign-provider"
                        else:
                            expected["metadata"]["annotations"]["foreign-owner"] = "do-not-adopt"
                        self.assertEqual(observed["provider"], expected, "foreign object was adopted or restored")
                        self.assertFalse(qualifier.summary["qualified"])
                        self.assertEqual(qualifier.summary["reason"], "command-refused")
                        self.assertNotIn("atomic prerequisite test refused", str(qualifier.summary))
                    finally:
                        qualifier.cleanup()

    def test_summary_never_retains_receipt_or_model_payload(self):
        receipt = {"subject": {"suiteDigest": "sha256:" + "a" * 64, "requiredImages": []},
                   "runID": "b" * 32, "cases": [{"taskUID": "observed", "answerSHA256": "c" * 64}],
                   "input": "PRIVATE_CANARY", "answer": "PRIVATE_CANARY"}
        summary = self.runner.public_summary(receipt, "sha256:" + "d" * 64)
        self.assertNotIn("PRIVATE_CANARY", str(summary))
        self.assertEqual(summary["requiredImages"], [])
        self.assertEqual(summary["cases"], 1)
        self.assertNotIn("runID", summary)


if __name__ == "__main__":
    unittest.main()
