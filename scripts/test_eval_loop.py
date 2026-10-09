#!/usr/bin/env python3
"""Regression tests for the live CI gate and case-scoped failure diagnostics."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location(
    "eval_loop", Path(__file__).parent / "ci" / "eval-loop.py"
)
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class EvalLoopTests(unittest.TestCase):
    def receipt(self):
        return {
            "result": "pass", "fullCaseSet": True,
            "cases": [{"id": "capital-france", "verdict": "pass"},
                      {"id": "arithmetic", "verdict": "pass"}],
        }

    def test_eval_gate_refuses_failed_partial_missing_and_duplicate_cases(self):
        receipt = self.receipt()
        runner.check_evaluation(receipt)
        for mutate in (
            lambda r: r.update(result="fail"),
            lambda r: r.update(fullCaseSet=False),
            lambda r: r["cases"][0].update(verdict="unknown"),
            lambda r: r["cases"].pop(),
            lambda r: r["cases"].append(r["cases"][0]),
        ):
            with self.subTest(mutate=mutate):
                changed = self.receipt()
                mutate(changed)
                with self.assertRaises(runner.GateError):
                    runner.check_evaluation(changed)

    def test_verify_gate_refuses_live_calls_and_non_equivalent_cases(self):
        report = {"result": "equivalent", "cases": [
            {"id": "capital-france", "status": "equivalent", "modelCalls": 0},
            {"id": "arithmetic", "status": "equivalent", "modelCalls": 0},
        ]}
        runner.check_verification(report)
        for field, value in (("modelCalls", 1), ("status", "unknown")):
            changed = json.loads(json.dumps(report))
            changed["cases"][0][field] = value
            with self.assertRaises(runner.GateError):
                runner.check_verification(changed)
        report["cases"].pop()
        with self.assertRaises(runner.GateError):
            runner.check_verification(report)

    def test_mixed_failure_only_prints_failing_answer(self):
        receipt = self.receipt()
        receipt["cases"][1]["verdict"] = "fail"
        output = "\ncase capital-france\nParis: passing answer\ncapital-france: pass\n\ncase arithmetic\nWrong toy answer\narithmetic: fail\n\nevaluation fail: summary\n"
        stream = io.StringIO()
        with contextlib.redirect_stderr(stream):
            runner.report_failed_cases(receipt, output)
        self.assertIn('arithmetic answer: "Wrong toy answer"', stream.getvalue())
        self.assertNotIn("Paris", stream.getvalue())
        self.assertNotIn("evaluation fail", stream.getvalue())

    def test_malformed_output_does_not_dump_other_cases(self):
        receipt = self.receipt()
        receipt["cases"][1]["verdict"] = "fail"
        stream = io.StringIO()
        with contextlib.redirect_stderr(stream):
            runner.report_failed_cases(receipt, "unstructured passing answer")
        self.assertIn("answer unavailable", stream.getvalue())
        self.assertNotIn("unstructured", stream.getvalue())

    def test_answer_cannot_emit_workflow_commands(self):
        receipt = self.receipt()
        receipt["cases"][1]["verdict"] = "fail"
        stream = io.StringIO()
        output = "\ncase arithmetic\n::error::toy\n\x1b[31msecond line\narithmetic: fail\n"
        with contextlib.redirect_stderr(stream):
            runner.report_failed_cases(receipt, output)
        self.assertEqual(len(stream.getvalue().splitlines()), 1)
        self.assertIn('\\n', stream.getvalue())
        self.assertNotIn('\x1b', stream.getvalue())

    def test_run_is_quiet_on_success_and_keeps_only_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = Path(tmp) / "bundle"
            receipts = bundle / "receipts"
            receipts.mkdir(parents=True)
            (receipts / "eval-test.json").write_text(json.dumps(self.receipt()))
            artifacts = Path(tmp) / "artifacts"
            args = SimpleNamespace(mode="evaluate", kmx="kmx", bundle=bundle,
                                   sessions="127.0.0.1:18088", artifacts=artifacts)
            stdout, stderr = io.StringIO(), io.StringIO()
            result = SimpleNamespace(returncode=0, stdout="toy answers", stderr="provider text")
            with patch.object(runner.subprocess, "run", return_value=result), \
                    contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                runner.run(args)
            self.assertEqual(stdout.getvalue(), "evaluation: 2/2 cases passed\n")
            self.assertEqual(stderr.getvalue(), "")
            self.assertEqual([p.name for p in artifacts.iterdir()], ["eval-test.json"])

    def test_cli_failure_cannot_be_hidden_by_a_passing_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = Path(tmp) / "bundle"
            receipts = bundle / "receipts"
            receipts.mkdir(parents=True)
            (receipts / "eval-test.json").write_text(json.dumps(self.receipt()))
            args = SimpleNamespace(mode="evaluate", kmx="kmx", bundle=bundle,
                                   sessions="127.0.0.1:18088", artifacts=Path(tmp) / "artifacts")
            result = SimpleNamespace(returncode=1, stdout="toy answers", stderr="provider text")
            with patch.object(runner.subprocess, "run", return_value=result), \
                    self.assertRaisesRegex(runner.GateError, "kmx evaluation failed"):
                runner.run(args)

    def test_verify_failure_still_preserves_report_for_review(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = Path(tmp) / "bundle"
            receipts = bundle / "receipts"
            receipts.mkdir(parents=True)
            (receipts / "eval-test.json").write_text(json.dumps(self.receipt()))
            report = {"result": "different", "cases": []}
            (receipts / "verify-eval-test.json.json").write_text(json.dumps(report))
            artifacts = Path(tmp) / "artifacts"
            args = SimpleNamespace(mode="verify", kmx="kmx", bundle=bundle,
                                   sessions="127.0.0.1:18088", artifacts=artifacts)
            result = SimpleNamespace(returncode=1, stdout="safe summary", stderr="")
            with patch.object(runner.subprocess, "run", return_value=result), \
                    self.assertRaisesRegex(runner.GateError, "kmx verification failed"):
                runner.run(args)
            self.assertEqual(json.loads((artifacts / "verify-eval-test.json.json").read_text()), report)

    def test_receipt_selection_rejects_absent_and_multiple_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = Path(tmp)
            (bundle / "receipts").mkdir()
            with self.assertRaises(runner.GateError):
                runner.evaluation_path(bundle)
            first = bundle / "receipts" / "eval-first.json"
            first.write_text("{}")
            self.assertEqual(runner.evaluation_path(bundle), first)
            (bundle / "receipts" / "eval-second.json").write_text("{}")
            with self.assertRaises(runner.GateError):
                runner.evaluation_path(bundle)


if __name__ == "__main__":
    unittest.main()
