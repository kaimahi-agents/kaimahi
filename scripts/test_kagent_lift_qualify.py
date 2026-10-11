#!/usr/bin/env python3
"""Exercise the actual qualifier with narrow external boundary doubles."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/kagent-lift.py"
REFUSALS = [
    "Kagent receipt-bound reuse conflict: drift (desired source or bindings differ from saved deployment)",
    "open Kagent bounded regular file, not a link: no such file or directory",
    "open Kagent bounded regular file, not a link: no such file or directory",
    "Kagent lift conflict: primary already exists without a complete matching receipt; no adoption or updates",
    "Kagent receipt-bound reuse conflict: drift (primary ownership, digest or admitted spec differs from desired)",
]


class QualifyTests(unittest.TestCase):
    def run_qualify(self, defect=None, negative=0):
        spec = importlib.util.spec_from_file_location("driver", SOURCE)
        driver = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(driver)
        with tempfile.TemporaryDirectory() as temp:
            work = pathlib.Path(temp)
            home = work / "home"
            home.mkdir()
            binary = work / "kmx"
            binary.touch()
            (work / "bin").mkdir()
            real = work / "bin/kubectl"
            real.write_text("#!/bin/sh\nexit 0\n")
            real.chmod(0o700)
            (work / "fail-bin").mkdir()
            (work / "fail-bin/kubectl").symlink_to(real)
            state = {"live": False, "drift": False, "negative": 0}
            identities = {kind + "/lift-proof": {"uid": kind + "-uid"} for kind in
                          ("ModelConfig", "Agent", "Deployment", "Service", "ServiceAccount", "Secret")}
            receipt = {"PortableDigest": "a" * 64, "RenderedDigest": "b" * 64}
            def snapshot():
                return dict(identities, drift={"uid": "operator"}) if state["drift"] else identities if state["live"] else {}
            def frame(kind, strategy, verb="create"):
                return {"verb": verb, "dry_run": strategy, "resources": [{"apiVersion": "kagent.dev/v1alpha2", "kind": kind, "namespace": "agents", "name": "lift-proof"}]}
            def append(frames):
                with (work / "kubectl-mutations.jsonl").open("a") as out:
                    for item in frames:
                        out.write(json.dumps(item) + "\n")
            def status():
                target = {"context": driver.CONTEXT, "namespace": "agents", "clusterUID": "cluster-1", "ready": True, "state": "ready", "agentUID": "Agent-uid", "modelConfigUID": "ModelConfig-uid", "deploymentUID": "Deployment-uid"}
                if not state["live"]:
                    target.update(state="absent", ready=False, noReceipt=True)
                elif state["drift"]:
                    target.update(state="drift", ready=False, detail="primary ownership, digest or admitted spec differs from desired")
                elif not (work / "bundle/receipts/kagent-v0.10.3").exists():
                    target.update(state="failed", ready=False, noReceipt=True, detail="unbound: no complete selected-target deployment receipt; live readiness is not a saved deployment")
                elif "unused.invalid" in (work / "external-bindings.yaml").read_text():
                    target.update(state="drift", ready=False, detail="desired source or bindings differ from saved deployment")
                return dict(receipt, bundle="lift-proof", runtime="kagent", runtimeVersion="v0.10.3", portableDigest="a" * 64, renderedDigest="b" * 64, targets=[target])
            def cli(argv, **kwargs):
                verb = argv[2]
                missing = not (work / "external-bindings.yaml").exists()
                report = status() if not missing else None
                refused = missing or (verb == "lift" and state["live"] and report["targets"][0]["state"] != "ready")
                if refused:
                    index = state["negative"]
                    state["negative"] += 1
                    code, stderr = 1, REFUSALS[index].encode() + b"\n"
                    if index == negative:
                        if defect == "unrelated": stderr = b"unexpected selector failure\n"
                        elif defect == "panic": code = 2
                        elif defect == "signal": code = -15
                        elif defect == "installer": code = 97
                        elif defect == "trailing": stderr += b"unexpected final error\n"
                    return subprocess.CompletedProcess(argv, code, b"", stderr)
                if verb == "lift" and not state["live"]:
                    frames = [frame(kind, "server") for kind in ("ModelConfig", "Agent")]
                    if defect == "reordered": frames.reverse()
                    elif defect == "persistent-plan": frames[0]["dry_run"] = "none"
                    elif defect == "wrong-resource": frames[0]["resources"][0]["name"] = "outside-snapshot"
                    elif defect == "rejected": frames.append({"rejected": "unsupported-kubectl-invocation"})
                    append(frames)
                    if "--plan" not in argv:
                        append([frame(kind, "none") for kind in ("ModelConfig", "Agent")])
                        state["live"] = True
                        (work / "bundle/receipts/kagent-v0.10.3").mkdir(parents=True)
                return subprocess.CompletedProcess(argv, 0, json.dumps(status()).encode(), b"")
            def kube(*args, **kwargs):
                self.assertIn("patch", args)
                append([frame("Agent", "none", "patch")])
                state["drift"] = True
                return b""
            sentinel = "kmx-mcp-hermetic-sentinel"
            response = {"jsonrpc": "2.0", "id": "kmx-lift-once", "result": {"kind": "task", "id": "task-1", "contextId": "context-1", "status": {"state": "completed"}, "artifacts": [{"parts": [{"kind": "text", "text": sentinel}]}]}}
            evidence = {"tool_calls": 1, "model_calls": 2, "terminal_answers": 1, "sentinel_sha256": hashlib.sha256(sentinel.encode()).hexdigest()}
            http_calls = []
            def http(url, data=None):
                http_calls.append(data["method"] if data else "evidence")
                return response if data else evidence if len(http_calls) > 1 else dict(evidence, tool_calls=0, model_calls=0, terminal_answers=0)
            with mock.patch.dict(os.environ, KMX_HOME=str(home)), mock.patch.object(driver.subprocess, "run", side_effect=cli), mock.patch.object(driver, "snapshot", side_effect=snapshot), mock.patch.object(driver, "verify_images", return_value={"kagent": "c" * 64}), mock.patch.object(driver, "get", return_value={"metadata": {"uid": "cluster-1"}}), mock.patch.object(driver, "validate_receipt", return_value=receipt), mock.patch.object(driver, "kube", side_effect=kube), mock.patch.object(driver, "forward", return_value=contextlib.nullcontext("http://127.0.0.1")), mock.patch.object(driver, "http_json", side_effect=http), contextlib.redirect_stdout(io.StringIO()) as out:
                error = None
                try:
                    driver.qualify(binary, work)
                except ValueError as failure:
                    error = str(failure)
            artifact = work / "qualification.json"
            return error, json.loads(artifact.read_text()) if artifact.exists() else None, http_calls, out.getvalue()

    def test_actual_qualifier_baseline_is_complete_and_payload_free(self):
        error, artifact, calls, output = self.run_qualify()
        self.assertIsNone(error, error)
        self.assertEqual(set(artifact), {"kind", "runtime", "runtime_version", "engine", "cluster_uid", "context", "namespace", "bundle", "portable_digest", "rendered_digest", "identities", "images", "execution", "proofs"})
        self.assertEqual(calls, ["evidence", "message/send", "evidence"])
        self.assertEqual(artifact["execution"]["tool_calls"], 1)
        self.assertEqual(artifact["execution"]["model_calls"], 2)
        self.assertNotIn("kmx-mcp-hermetic-sentinel", json.dumps(artifact) + output)

    def test_all_five_unrelated_negative_failures_refuse_artifact(self):
        for index in range(5):
            with self.subTest(index=index):
                error, artifact, _, output = self.run_qualify("unrelated", index)
                self.assertIsNotNone(error, "unrelated failure was accepted")
                self.assertIsNone(artifact)
                self.assertNotIn("unexpected selector failure", error + output)

    def test_complete_ordered_metadata_including_swallowed_rejections_is_required(self):
        for defect in ("reordered", "persistent-plan", "wrong-resource", "rejected"):
            with self.subTest(defect=defect):
                error, artifact, _, _ = self.run_qualify(defect)
                self.assertIn("mutation audit", error)
                self.assertIsNone(artifact)

    def test_wrong_exit_and_later_final_error_are_not_expected_refusal(self):
        for defect in ("panic", "signal", "installer", "trailing"):
            with self.subTest(defect=defect):
                error, artifact, _, _ = self.run_qualify(defect)
                self.assertIsNotNone(error, "incorrect failure was accepted")
                self.assertIsNone(artifact)


if __name__ == "__main__":
    unittest.main()
