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
            state = {"live": False, "drift": False, "negative": 0, "evals": 0}
            identities = {kind + "/lift-proof": {"uid": kind + "-uid", "generation": int(kind in {"ModelConfig", "Agent", "Deployment"})} for kind in
                          ("ModelConfig", "Agent", "Deployment", "Service", "ServiceAccount", "Secret")}
            identities.update({kind + "/kagent-controller": {"uid": "controller-" + kind, "generation": int(kind == "Deployment")} for kind in ("Service", "Deployment", "ConfigMap")})
            references = [{"Kind": kind, "Name": "lift-proof", "Namespace": "agents", "UID": kind + "-uid", **({"Generation": 1, "SpecDigest": "c" * 64} if kind in {"ModelConfig", "Agent", "Deployment"} else {}), **({"ResourceVersion": "1"} if kind not in {"Agent", "ModelConfig"} else {})} for kind in ("ModelConfig", "Agent", "Deployment", "Service", "ServiceAccount", "Secret")]
            receipt = {"PortableDigest": "a" * 64, "RenderedDigest": "b" * 64, "Proofs": references[:2], "Workload": references[2:], "Dependencies": [{"Kind": "Secret", "Name": "model-key", "Namespace": "agents", "UID": "key-uid", "ResourceVersion": "1", "Key": "api-key"}]}
            sentinel = "kmx-mcp-hermetic-sentinel"
            answer = "KMX MCP proof: " + sentinel
            counts = {"tool_calls": 0, "model_calls": 0, "terminal_answers": 0, "judge_calls": 0, "cases": [], "sentinel_sha256": hashlib.sha256(sentinel.encode()).hexdigest()}
            def sha(data):
                return hashlib.sha256(data).hexdigest()
            def frames(entries):
                return b"".join(name.encode() + b" " + str(len(data)).encode() + b"\n" + data + b"\n" for name, data in entries)
            def observed_case(prompt):
                counts["model_calls"] += 2
                counts["tool_calls"] += 1
                counts["terminal_answers"] += 1
                counts["cases"].append({"call_id": "call-" + str(counts["tool_calls"]), "input_sha256": sha(prompt.encode()), "answer_sha256": sha(answer.encode()), "model_calls": 2, "tool_calls": 1, "terminal_answers": 1, "user_turns": 1})
            def evaluation(argv):
                import yaml
                state["evals"] += 1
                n = state["evals"]
                self.assertEqual(argv[4:12], ["--runtime", "kagent", "--runtime-version", "v0.10.3", "--to-context", "kind-kmx-kagent-lift", "--bindings", str(work / "external-bindings.yaml")])
                self.assertIn("--judge-base-url", argv)
                cases = [(p, p.read_bytes(), yaml.safe_load(p.read_text())) for p in sorted((work / "bundle/eval").glob("*.yaml"))]
                if "--case" in argv:
                    cases = [entry for entry in cases if entry[2]["id"] == argv[argv.index("--case") + 1]]
                rows = []
                for index, (_, raw, case) in enumerate(cases):
                    observed_case(case["input"])
                    assertions = [{"id": "expectContains.0", "type": "contains", "verdict": "pass", "reason": "assertion satisfied", "definitionDigest": sha(frames([("type", b"contains"), ("operand", b"KMX MCP proof:")]))}]
                    for assertion in case.get("assertions", []):
                        counts["judge_calls"] += 2
                        policy = "d" * 64
                        assertions.append({"id": assertion["id"], "type": "llmJudge", "verdict": "pass", "reason": "judge threshold satisfied", "definitionDigest": sha(frames([("type", b"llmJudge"), ("operand", assertion["rubric"].encode()), ("minScore", b"50"), ("judgeModel", b"fixture-judge"), ("judgeVersion", b"1"), ("evaluatorDigest", policy.encode())])), "judge": {"model": "fixture-judge", "modelVersion": "1", "provenance": "user-declared", "minScore": 50, "scores": [100, 100], "evaluatorDigest": policy, "configurationDigest": sha(frames([("endpoint", b"http://127.0.0.1/v1"), ("model", b"fixture-judge"), ("modelVersion", b"1"), ("evaluatorDigest", policy.encode())]))}})
                    token = str(n) + "-" + str(index)
                    rows.append({"id": case["id"], "verdict": "pass", "caseDigest": sha(raw), "inputDigest": sha(case["input"].encode()), "answerSHA256": sha(answer.encode()), "assertions": assertions, "execution": {"rpcID": "rpc-" + token, "requestMessageID": "message-" + token, "taskID": "task-" + token, "contextID": "context-" + token, "state": "completed"}})
                all_cases = [entry[0] for entry in cases]
                target = {"runtime": "kagent", "runtimeVersion": "v0.10.3", "engine": "go", "context": driver.CONTEXT, "namespace": "agents", "clusterUID": "cluster-1", "agent": "lift-proof", "agentUID": "Agent-uid", "agentGeneration": 1, "modelConfigUID": "ModelConfig-uid", "modelConfigGeneration": 1, "model": "fixture", "platform": {"sourceCommit": "8878c39ac64421cb665c8e62ecc6411dd12f2fc2", "controllerImage": {"os": "linux", "architecture": "amd64", "digest": driver.CONTROLLER}, "adkImage": {"os": "linux", "architecture": "amd64", "digest": driver.GO_ADK}, "controller": [{"Kind": kind, "Name": "kagent-controller", "Namespace": "kagent", "UID": "controller-" + kind, "ResourceVersion": "1", **({"Generation": 1} if kind == "Deployment" else {})} for kind in ("Service", "Deployment", "ConfigMap")]}, "snapshot": {"proofs": receipt["Proofs"], "workload": receipt["Workload"], "dependencies": receipt["Dependencies"]}}
                run_id = str(n) * 32
                report = {"schemaVersion": 2, "runID": run_id, "bundle": "lift-proof", "portableDigest": "a" * 64, "renderedDigest": "b" * 64, "deploymentReceiptDigest": sha((work / "bundle/receipts/kagent-v0.10.3/lift.json").read_bytes()), "casesDigest": sha(frames([("eval/" + p.name, p.read_bytes()) for p in all_cases])), "fullCaseSet": "--case" not in argv, "gitCommit": "uncommitted", "result": "pass", "target": target, "cases": rows}
                if defect == "reused-native" and n == 2: rows[0]["execution"]["taskID"] = "task-1-0"
                elif defect == "answer-hash": rows[0]["answerSHA256"] = "e" * 64
                elif defect == "partial-marker" and n == 3: report["fullCaseSet"] = True
                elif defect == "payload": rows[0]["answer"] = sentinel
                elif defect == "platform": target["platform"]["sourceCommit"] = "e" * 40
                elif defect == "snapshot": target["snapshot"]["dependencies"] = []
                elif defect == "case-counts": counts["cases"][-1]["model_calls"] = 1
                elif defect == "extra-user": counts["cases"][-1]["user_turns"] = 2
                root = work / "bundle/receipts"
                runs = root / "runs"
                runs.mkdir(exist_ok=True, mode=0o700)
                latest_key = sha(b"kagent\x00v0.10.3\x00kind-kmx-kagent-lift\x00agents\x00cluster-1")
                latest = root / ("eval-kagent-v0.10.3-" + latest_key + ".json")
                archive = runs / ("eval-" + run_id + ".json")
                raw = json.dumps(report).encode()
                for path in (archive, latest):
                    path.write_bytes(raw)
                    path.chmod(0o600 if defect != "public-file" else 0o644)
                if defect == "archive-overwrite" and n == 2:
                    (runs / ("eval-" + "1" * 32 + ".json")).write_bytes(raw)
                return subprocess.CompletedProcess(argv, 0, (answer + "\nhistory: " + str(archive) + "\nreceipt: " + str(latest) + "\n").encode(), b"")
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
                if verb == "evaluate":
                    return evaluation(argv)
                if verb == "eval":
                    self.assertEqual(argv[3], "diff")
                    self.assertEqual(len(argv), 6, "diff requires two positional receipts, not flags")
                    paths = [pathlib.Path(path) for path in argv[4:]]
                    self.assertTrue(all(p.parent.name == "runs" for p in paths), "diff must compare archives, not overwritten latest")
                    return subprocess.CompletedProcess(argv, 0, b"unchanged\n", b"")
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
                        (work / "bundle/receipts/kagent-v0.10.3").mkdir(parents=True, mode=0o700)
                        (work / "bundle/receipts").chmod(0o700)
                        lift = work / "bundle/receipts/kagent-v0.10.3/lift.json"
                        lift.write_text(json.dumps({"receipt": receipt}))
                        lift.chmod(0o600)
                return subprocess.CompletedProcess(argv, 0, json.dumps(status()).encode(), b"")
            def kube(*args, **kwargs):
                self.assertIn("patch", args)
                append([frame("Agent", "none", "patch")])
                state["drift"] = True
                return b""
            response = {"jsonrpc": "2.0", "id": "kmx-lift-once", "result": {"kind": "task", "id": "task-1", "contextId": "context-1", "status": {"state": "completed"}, "artifacts": [{"parts": [{"kind": "text", "text": answer}]}]}}
            http_calls = []
            def http(url, data=None):
                http_calls.append(data["method"] if data else "evidence")
                if data:
                    observed_case(data["params"]["message"]["parts"][0]["text"])
                    return response
                return json.loads(json.dumps(counts))
            with mock.patch.dict(os.environ, KMX_HOME=str(home)), mock.patch.object(driver.subprocess, "run", side_effect=cli), mock.patch.object(driver, "snapshot", side_effect=snapshot), mock.patch.object(driver, "verify_images", return_value={"controller": driver.CONTROLLER, "kagent": driver.GO_ADK}), mock.patch.object(driver, "get", return_value={"metadata": {"uid": "cluster-1"}}), mock.patch.object(driver, "validate_receipt", return_value=receipt), mock.patch.object(driver, "kube", side_effect=kube), mock.patch.object(driver, "forward", return_value=contextlib.nullcontext("http://127.0.0.1")), mock.patch.object(driver, "http_json", side_effect=http), contextlib.redirect_stdout(io.StringIO()) as out:
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
        self.assertEqual(set(artifact), {"kind", "runtime", "runtime_version", "engine", "cluster_uid", "context", "namespace", "bundle", "portable_digest", "rendered_digest", "identities", "images", "execution", "evaluation", "proofs"})
        self.assertEqual(calls.count("message/send"), 1)
        self.assertEqual(artifact["execution"]["tool_calls"], 1)
        self.assertEqual(artifact["execution"]["model_calls"], 2)
        self.assertNotIn("kmx-mcp-hermetic-sentinel", json.dumps(artifact) + output)
        self.assertEqual(artifact["evaluation"]["fixture_counts"], {"tool_calls": 6, "model_calls": 12, "terminal_answers": 6, "judge_calls": 6})
        self.assertEqual([r["full_case_set"] for r in artifact["evaluation"]["runs"]], [True, True, False])
        self.assertEqual([len(r["cases"]) for r in artifact["evaluation"]["runs"]], [2, 2, 1])

    def test_product_proof_refuses_uncorrelated_private_or_incomplete_evidence(self):
        for defect, reason in (("reused-native", "fresh"), ("answer-hash", "answer"),
                               ("partial-marker", "case set"), ("payload", "payload-free"),
                               ("platform", "platform"), ("snapshot", "snapshot"),
                               ("case-counts", "case counts"), ("extra-user", "case counts"),
                               ("public-file", "private"), ("archive-overwrite", "archive")):
            with self.subTest(defect=defect):
                error, artifact, _, output = self.run_qualify(defect)
                self.assertIsNotNone(error, "defective product evidence was accepted")
                self.assertIn(reason, error)
                self.assertIsNone(artifact)
                self.assertNotIn("kmx-mcp-hermetic-sentinel", error + output)

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
