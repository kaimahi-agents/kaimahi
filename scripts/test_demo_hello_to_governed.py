#!/usr/bin/env python3
"""Cluster-free safety checks; live behavior is verified by the two recordings."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("demo-hello-to-governed.sh")


class DemoSafetyTest(unittest.TestCase):
    def run_demo(self, *args, **env):
        # No cluster tools are needed for argument/precondition failures.
        return subprocess.run(
            ["/bin/bash", str(SCRIPT), *args],
            env={**os.environ, **env}, text=True, capture_output=True,
            timeout=10,
        )

    def test_help_has_no_cluster_side_effect(self):
        result = self.run_demo("--help")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prepare", result.stdout)
        self.assertIn("record", result.stdout)
        self.assertIn("teardown", result.stdout)

    def test_unknown_action_refused(self):
        result = self.run_demo("install")
        self.assertEqual(result.returncode, 64)
        self.assertIn("Usage:", result.stderr)

    def test_prepare_refuses_existing_output_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            sentinel = Path(directory, "keep")
            sentinel.write_text("previous evidence")
            result = self.run_demo("prepare", directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("already exists", result.stderr)
            self.assertEqual(sentinel.read_text(), "previous evidence")

    def test_unowned_cluster_name_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("prepare", directory + "/run", KIND_CLUSTER="production")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("kmx-hello-governed", result.stderr)
            self.assertFalse(Path(directory, "run").exists())

    def test_record_refuses_unprepared_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("record", directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("prepared", result.stderr)

    def test_teardown_refuses_unowned_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("teardown", directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("prepared", result.stderr)

    def test_verify_requires_successful_rows_with_actual_tokens(self):
        # The real ledger timestamp is one field, not a date and time pair.
        # Wrong column offsets, accepting failed calls, or trusting only an
        # application's plausible answer must all fail this test.
        row = '2026-09-14T18:58:12 concierge orka local/qwen2.5:3b 186 59 0 unpriced 200 caller 10.0.0.1 none\n'
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            (p / "cluster").write_text("kmx-hello-governed\n")
            (p / "kubeconfig").touch()
            (p / "app-answer.json").write_text(json.dumps({"reply": "Hello!"}))
            for ledger, success in [
                (row, True),
                (row.replace("200 caller", "500 caller"), False),
                (row.replace("186 59", "0 0"), False),
                ("-- month to date: 0 cents, 0 tokens\n", False),
            ]:
                with self.subTest(ledger=ledger):
                    (p / "ledger.txt").write_text(ledger)
                    for optimize in ("", "1"):
                        result = self.run_demo("verify", directory,
                                               KIND_CLUSTER="kmx-hello-governed",
                                               PYTHONOPTIMIZE=optimize)
                        self.assertEqual(result.returncode == 0, success, result.stderr)

    def test_watch_failure_is_preserved_for_the_presenter(self):
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            (p / "cluster").write_text("kmx-hello-governed\n")
            (p / "kubeconfig").touch()
            (p / "watch-ready").touch()
            (p / "bin").mkdir()
            kmx = p / "bin/kmx"
            kmx.write_text("#!/bin/sh\necho 'model ledger unavailable' >&2\nexit 23\n")
            kmx.chmod(0o700)
            result = self.run_demo("_watch", directory, KIND_CLUSTER="kmx-hello-governed")
            self.assertEqual(result.returncode, 23, result.stderr)
            self.assertTrue((p / "watch-exit").exists(), "watch lost its exit status")
            self.assertEqual((p / "watch-exit").read_text().strip(), "23")

    def test_unknown_profile_refused_before_preparation(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("prepare", directory + "/run", DEMO_RECORD_PROFILE="compressed")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("presenter", result.stderr)
            self.assertFalse(Path(directory, "run").exists())


if __name__ == "__main__":
    unittest.main()
