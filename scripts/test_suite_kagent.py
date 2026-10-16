#!/usr/bin/env python3
"""Hermetic checks for the source-free suite lift qualification contract."""
import importlib.util
import json
import pathlib
import subprocess
import tempfile
import unittest
from unittest import mock

SOURCE = pathlib.Path(__file__).resolve().parent / "ci/suite-kagent.py"
spec = importlib.util.spec_from_file_location("suite_kagent", SOURCE)
suite = importlib.util.module_from_spec(spec)
spec.loader.exec_module(suite)


class SuiteLiftTests(unittest.TestCase):
    def test_cli_refusal_requires_exit_one_and_closed_reason(self):
        for code, stderr, accepted in [(1, b"occupied", True), (0, b"occupied", False),
                                       (2, b"occupied", False), (1, b"unreadable", False)]:
            with self.subTest(code=code, stderr=stderr), mock.patch.object(suite.sys, "stderr"), mock.patch.object(suite.subprocess, "run", return_value=subprocess.CompletedProcess([], code, b"", stderr)):
                if accepted:
                    suite.cli("kmx", ["lift"], refusal=b"occupied")
                else:
                    with self.assertRaisesRegex(suite.driver.QualificationError, "exit"):
                        suite.cli("kmx", ["lift"], refusal=b"occupied")

    def test_source_free_lift_checks_exact_package_placement_and_zero_native_traffic(self):
        digest = "sha256:" + "a" * 64
        evidence = {"runID": "d" * 32, "kagent": {"observation": {"Subject": {"SuiteDigest": digest, "Member": "proof", "Model": "fixture-suite"}}}}
        for defect in (None, "model", "suite", "ready", "counts"):
            with self.subTest(defect=defect), tempfile.TemporaryDirectory() as temp:
                work = pathlib.Path(temp)
                (work / "receipt.json").write_bytes(b"receipt")
                bindings = work / "bindings.yaml"
                bindings.write_text("namespace: agents\n")
                target = {"namespace": "suite-destination", "namespaceUID": "destination-ns"}
                summary = {"runtime": "kagent", "suiteDigest": digest, "member": "proof", "model": "fixture-suite",
                           "gate": "promotion", "target": target, "sourceTarget": {"namespace": "agents"},
                           "resources": [{"kind": k, "name": "proof", "action": "create"} for k in ("ModelConfig", "Agent")]}
                result = {"summary": dict(summary), "state": "ready", "resources": [dict(row, uid=k, generation=1,
                           outcome="created", ready=True) for k, row in zip(("model-uid", "agent-uid"), summary["resources"])]}
                if defect in ("model", "suite"):
                    result["summary"]["model" if defect == "model" else "suiteDigest"] = "wrong"
                if defect == "ready":
                    result["resources"][1]["ready"] = False
                calls, frames = [], []
                def cli(binary, args, refusal=None):
                    calls.append((args, refusal))
                    body = summary if "--plan" in args else result
                    if refusal is None and "--plan" not in args:
                        frames.extend({"verb": "create", "dry_run": strategy, "resources": [{"apiVersion": "kagent.dev/v1alpha2", "kind": kind, "namespace": "suite-destination", "name": "proof"}]}
                                      for strategy in ("server", "none") for kind in ("ModelConfig", "Agent"))
                    return subprocess.CompletedProcess([], 0 if refusal is None else 1, json.dumps(body).encode(), b"")
                def get(kind, name, namespace):
                    row = result["resources"][0 if kind.startswith("modelconfig") else 1]
                    return {"metadata": {"name": "proof", "namespace": namespace, "uid": row["uid"], "generation": 1}}
                with mock.patch.object(suite.status, "qualify_status") as observe, mock.patch.object(suite, "cli", side_effect=cli), mock.patch.object(suite.driver, "get", side_effect=get), \
                     mock.patch.object(suite.driver, "audit_frames", side_effect=lambda _: list(frames)), mock.patch.object(suite.driver, "http_json", return_value={"model_calls": 1 if defect == "counts" else 0}):
                    if defect:
                        with self.assertRaises(suite.driver.QualificationError):
                            suite.qualify_lift("kmx", work, work / "layout", work / "receipt.json", bindings, evidence, "http://fixture", {"model_calls": 0})
                    else:
                        suite.qualify_lift("kmx", work, work / "layout", work / "receipt.json", bindings, evidence, "http://fixture", {"model_calls": 0})
                        observe.assert_called_once()
                        self.assertEqual(observe.call_args.args[1], result)
                        self.assertEqual(observe.call_args.args[2:4], (b"receipt", "d" * 32))
                        self.assertEqual(len(calls), 4)
                        self.assertTrue(all("suite:v1@" + digest in args and "--from-layout" in args for args, _ in calls))
                        self.assertIn("--plan", calls[1][0])
                        self.assertIn("--promote", calls[2][0])
                        self.assertIsNotNone(calls[0][1])
                        self.assertIsNotNone(calls[3][1])


if __name__ == "__main__":
    unittest.main()
