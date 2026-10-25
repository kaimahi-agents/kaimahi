#!/usr/bin/env python3
"""Hermetic timing/privacy checks; these do not qualify live execution."""
import contextlib
import importlib.util
import io
import json
import math
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import yaml

from test_workflow_helpers import runtime_job

ROOT = pathlib.Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("native_timings_subject", ROOT / "scripts/ci/suite-native.py")
Q = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(Q)
CANARY = "PRIVATE_PROMPT_ANSWER_SECRET https://private.invalid /private/home/person"
PHASES = {
    "preflight", "model", "model-warmup", "model-ready", "author", "evaluate", "evaluate-v2",
    "historical-proof", "candidate-cleanup", "transfer", "registry", "lift-denials",
    "lift-local-plan", "lift-registry-plan", "lift-local-create", "lift-local-answer",
    "lift-registry-answer", "lift-promotion-answer", "destination-status", "promote-setup",
    "promote-plan", "promote-model-mismatch", "promote-create", "promote-answer",
    "public-update-v2", "public-update-original-v1", "public-update-v2-registry",
    "public-update-rv-noop", "refusals", "artifacts",
}


class NativeTimingsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.path = self.root / "native-suite-timings.json"

    def timer(self):
        self.assertTrue(hasattr(Q, "NativeTimings"), "closed-phase timing API missing")
        return Q.NativeTimings(self.path)

    def document(self):
        self.assertTrue(self.path.is_file(), "independent timing file missing")
        raw = self.path.read_text()
        self.assertNotIn(CANARY, raw)
        self.assertNotIn(str(self.root), raw)
        doc = json.loads(raw)
        self.assertEqual(set(doc), {"schemaVersion", "status", "phases"})
        self.assertEqual(doc["schemaVersion"], 1)
        self.assertIn(doc["status"], {"running", "success", "failure", "timeout"})
        self.assertLessEqual(len(doc["phases"]), 128)
        for row in doc["phases"]:
            self.assertEqual(set(row), {"phase", "elapsedSeconds", "status"})
            self.assertIn(row["phase"], PHASES)
            self.assertIn(row["status"], {"success", "failure", "timeout"})
            self.assertIs(type(row["elapsedSeconds"]), float)
            self.assertTrue(math.isfinite(row["elapsedSeconds"]))
            self.assertGreaterEqual(row["elapsedSeconds"], 0)
        return doc

    def test_monotonic_closed_phases_exclude_open_phase_and_wall_clock(self):
        with mock.patch.object(Q.time, "monotonic", side_effect=[10.0, 12.5, 15.0]), \
                mock.patch.object(Q.time, "time", side_effect=AssertionError("wall clock used")):
            timer = self.timer()
            self.assertEqual(self.document(), {"schemaVersion": 1, "status": "running", "phases": []})
            timer.enter("model")
            self.assertEqual(self.document()["phases"], [
                {"phase": "preflight", "elapsedSeconds": 2.5, "status": "success"}])
            timer.finish("success")
        self.assertEqual(self.document(), {"schemaVersion": 1, "status": "success", "phases": [
            {"phase": "preflight", "elapsedSeconds": 2.5, "status": "success"},
            {"phase": "model", "elapsedSeconds": 2.5, "status": "success"}]})

    def test_failure_and_timeout_close_only_active_phase_and_finalize_once(self):
        for status in ("failure", "timeout"):
            with self.subTest(status=status), mock.patch.object(Q.time, "monotonic", side_effect=[1.0, 3.0, 8.0]):
                timer = self.timer()
                timer.enter("evaluate")
                timer.finish(status)
                timer.finish("success")
                self.assertEqual(self.document(), {"schemaVersion": 1, "status": status, "phases": [
                    {"phase": "preflight", "elapsedSeconds": 2.0, "status": "success"},
                    {"phase": "evaluate", "elapsedSeconds": 5.0, "status": status}]})

    def test_unknown_phase_and_nonscalar_status_never_reach_json(self):
        timer = self.timer()
        for phase in (CANARY, {"phase": CANARY}, ["evaluate"], None):
            with self.subTest(phase=phase), self.assertRaisesRegex(ValueError, "^timing-phase$"):
                timer.enter(phase)
        for status in (CANARY, {"status": "success"}, ["success"], None, True):
            with self.subTest(status=status), self.assertRaisesRegex(ValueError, "^timing-status$"):
                timer.finish(status)
        timer.finish("failure")
        self.assertEqual(self.document()["phases"][0]["phase"], "preflight")

    def test_phase_rows_are_bounded_without_truncating_into_success(self):
        timer = self.timer()
        with self.assertRaisesRegex(ValueError, "^timing-limit$"):
            for index in range(200):
                timer.enter("model" if index % 2 else "evaluate")
        timer.finish("failure")
        doc = self.document()
        self.assertEqual(doc["status"], "failure")
        self.assertEqual(len(doc["phases"]), 128)

    def test_existing_phase_assignments_record_all_operational_phase_codes(self):
        timer = self.timer()
        qualifier = Q.Qualification.__new__(Q.Qualification)
        qualifier._timings = timer
        for phase in sorted(PHASES - {"preflight"}):
            qualifier.phase = phase
        timer.finish("success")
        self.assertEqual({row["phase"] for row in self.document()["phases"]}, PHASES)
        self.assertEqual(qualifier.phase, "transfer")
        with self.assertRaisesRegex(ValueError, "^timing-phase$"):
            qualifier.phase = CANARY
        self.assertEqual(qualifier.phase, "transfer")
        # Existing hermetic fixtures bypass __init__ and need no timing setup.
        uninitialized = Q.Qualification.__new__(Q.Qualification)
        uninitialized.phase = "evaluate"
        self.assertEqual(uninitialized.phase, "evaluate")

    def test_nested_status_restores_update_phase_only_after_success(self):
        timer = self.timer()
        qualifier = Q.Qualification.__new__(Q.Qualification)
        qualifier._timings = timer
        qualifier.phase = "public-update-v2"
        qualifier.work, qualifier.product_env, qualifier.kubectl = self.root, {}, "/fixture/kubectl"
        qualifier.cli = lambda *args, **kwargs: self.fail("status fixture must not execute tools")
        with mock.patch.object(Q, "qualify_status", return_value={"ready": True}):
            self.assertEqual(qualifier.status({}, {"runID": "fixture"}, b""), {"ready": True})
        self.assertEqual(qualifier.phase, "public-update-v2")
        with mock.patch.object(Q, "qualify_status", side_effect=ValueError(CANARY)):
            with self.assertRaises(ValueError):
                qualifier.status({}, {"runID": "fixture"}, b"")
        self.assertEqual(qualifier.phase, "destination-status")
        timer.finish("failure")
        doc = self.document()
        self.assertEqual(doc["phases"][-1]["phase"], "destination-status")
        self.assertEqual(doc["phases"][-1]["status"], "failure")

    def test_provision_exposes_model_startup_warmup_and_provider_readiness(self):
        timer = self.timer()
        qualifier = Q.Qualification.__new__(Q.Qualification)
        qualifier._timings, qualifier.repo = timer, ROOT
        qualifier.kube = lambda *args, **kwargs: b""
        qualifier.get = lambda resource, name: ({"spec": {"secretRef": {"name": "local-provider-key"}}}
                                               if name == "local" else {"status": {"ready": True}})
        qualifier.model_forward = contextlib.nullcontext
        response = io.BytesIO(json.dumps({"choices": [CANARY]}).encode())
        response.status = 200
        with mock.patch.object(Q.urllib.request, "urlopen", return_value=response):
            qualifier.provision()
        timer.finish("success")
        self.assertEqual([row["phase"] for row in self.document()["phases"]],
                         ["preflight", "model", "model-warmup", "model-ready"])

    def run_main(self, error=None, constructor_error=False):
        def initialize(instance, *args, **kwargs):
            if constructor_error:
                raise ValueError(CANARY)
            instance._timings = kwargs["timings"]
            instance.phase = "preflight"

        def qualify(instance):
            for phase in ("model", "model-warmup", "model-ready", "evaluate", "candidate-cleanup",
                          "transfer", "evaluate-v2", "candidate-cleanup", "promote-create",
                          "public-update-v2", "public-update-original-v1", "public-update-v2-registry"):
                instance.phase = phase
            if error:
                raise error
            instance.phase = "artifacts"

        out, err = io.StringIO(), io.StringIO()
        with mock.patch.dict(os.environ, {"RUNNER_TEMP": str(self.root), "KIND_CLUSTER": "kmx-orka",
                    "KMX_ORKA_RUNTIME_CREATED": "1", "KMX_ORKA_PROMOTION_CREATED": "1"}), \
                mock.patch.object(sys, "argv", ["suite-native.py", "kind-kmx-orka", "kind-kmx-orka-promote", str(self.root / "work")]), \
                mock.patch.object(Q.Qualification, "__init__", initialize), \
                mock.patch.object(Q.Qualification, "qualify", qualify), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            result = Q.main()
        self.assertEqual(out.getvalue(), "")
        self.assertNotIn(CANARY, err.getvalue())
        self.assertFalse((self.root / "work/artifacts").exists(), "timings must not create passing evidence")
        return result, err.getvalue(), self.document()

    def test_main_success_overwrites_stale_timing_file(self):
        self.path.write_text(CANARY)
        result, stderr, doc = self.run_main()
        self.assertEqual((result, stderr, doc["status"]), (0, "", "success"))
        self.assertEqual(doc["phases"][-1]["phase"], "artifacts")
        self.assertTrue(all(row["status"] == "success" for row in doc["phases"]))

    def test_main_failure_and_timeout_emit_safe_status_without_exception_payload(self):
        for error, status in ((ValueError(CANARY), "failure"),
                              (subprocess.TimeoutExpired([CANARY], 660, output=CANARY, stderr=CANARY), "timeout"),
                              (TimeoutError(CANARY), "timeout")):
            with self.subTest(status=status):
                result, stderr, doc = self.run_main(error=error)
                self.assertEqual(result, 1)
                self.assertEqual(stderr, "native qualification: phase=public-update-v2-registry reason=failed\n")
                self.assertEqual(doc["status"], status)
                self.assertEqual(doc["phases"][-1]["status"], status)
                self.assertTrue(all(row["status"] == "success" for row in doc["phases"][:-1]))

    def test_failed_final_write_cannot_leave_running_snapshot_as_success(self):
        write = Q.NativeTimings.write

        def fail_final(timer):
            if timer.status != "running":
                raise OSError(CANARY)
            return write(timer)

        with mock.patch.object(Q.NativeTimings, "write", fail_final):
            result, stderr, doc = self.run_main()
        self.assertEqual(result, 1)
        self.assertEqual(stderr, "native qualification: timing-finalization=failed\n")
        self.assertEqual(doc["status"], "running")

    def test_constructor_failure_still_closes_preflight_privately(self):
        result, stderr, doc = self.run_main(constructor_error=True)
        self.assertEqual((result, stderr, doc["status"]),
                         (1, "native qualification: phase=preflight reason=failed\n", "failure"))
        self.assertEqual(len(doc["phases"]), 1)
        self.assertEqual(doc["phases"][0]["phase"], "preflight")

    def test_real_constructor_failure_leaves_only_safe_preflight_timing(self):
        result = subprocess.run([sys.executable, str(ROOT / "scripts/ci/suite-native.py"),
                                 "kind-kmx-orka", "kind-kmx-orka-promote", str(self.root / "work")],
                                env=dict(os.environ, RUNNER_TEMP=str(self.root), KIND_CLUSTER="kmx-orka",
                                         KMX_ORKA_RUNTIME_CREATED="1", KMX_ORKA_PROMOTION_CREATED="1",
                                         KUBECONFIG=str(self.root / "private-missing-kubeconfig")),
                                capture_output=True, timeout=10)
        self.assertEqual((result.returncode, result.stdout, result.stderr),
                         (1, b"", b"native qualification: phase=preflight reason=failed\n"))
        doc = self.document()
        self.assertEqual(doc["status"], "failure")
        self.assertEqual(len(doc["phases"]), 1)
        self.assertEqual(doc["phases"][0]["phase"], "preflight")
        self.assertFalse((self.root / "work").exists())

    def test_wrong_context_subprocess_keeps_stdout_and_stderr_contract(self):
        result = subprocess.run([sys.executable, str(ROOT / "scripts/ci/suite-native.py"), "kind-other", str(self.root / "work")],
                                env=dict(os.environ, RUNNER_TEMP=str(self.root)), capture_output=True, timeout=10)
        self.assertEqual((result.returncode, result.stdout, result.stderr),
                         (1, b"", b"native qualification: phase=preflight reason=failed\n"))
        self.assertEqual(self.document()["status"], "failure")
        self.assertFalse((self.root / "work").exists())

    def test_workflow_uploads_only_fixed_timing_file_even_when_qualification_fails(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        steps = runtime_job(ROOT)["steps"]
        uploads = [step for step in steps if step.get("with", {}).get("name") == "native-suite-timings"]
        self.assertEqual(len(uploads), 1, "independent timing artifact missing")
        upload = uploads[0]
        self.assertEqual(upload["uses"], "actions/upload-artifact@v4")
        self.assertEqual(upload["with"]["path"], "${{ runner.temp }}/native-suite-timings.json")
        self.assertEqual(upload["if"], "always()")
        self.assertEqual(upload["with"]["if-no-files-found"], "error")
        passing = next(step for step in steps if step.get("with", {}).get("name") == "native-suite-qualification")
        self.assertEqual(passing["with"]["path"].splitlines(), [
            "${{ runner.temp }}/kmx-suite-native/artifacts/passing.json",
            "${{ runner.temp }}/kmx-suite-native/artifacts/qualification.json"])
        hygiene = workflow["jobs"]["hygiene-checks"]["steps"]
        self.assertTrue(any("python3 scripts/test_native_timings.py" in step.get("run", "") for step in hygiene))


if __name__ == "__main__":
    unittest.main()
