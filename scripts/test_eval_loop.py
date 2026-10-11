#!/usr/bin/env python3
"""Regression tests for portable CI gates, provenance and safe diagnostics."""
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
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
        return {"result": "pass", "fullCaseSet": True,
                "cases": [{"id": "user-first", "verdict": "pass"},
                          {"id": "user-second", "verdict": "pass"}]}

    def test_generic_gate_accepts_user_cases_and_refuses_incomplete_evidence(self):
        runner.check_evaluation(self.receipt())
        for mutate in (
            lambda r: r.update(result="fail"),
            lambda r: r.update(fullCaseSet=False),
            lambda r: r["cases"][0].update(verdict="unknown"),
            lambda r: r.update(cases=[]),
            lambda r: r["cases"].append(r["cases"][0]),
        ):
            with self.subTest(mutate=mutate):
                changed = self.receipt()
                mutate(changed)
                with self.assertRaises(runner.GateError):
                    runner.check_evaluation(changed)

    def test_verify_checks_same_cases_and_zero_calls(self):
        report = {"result": "equivalent", "cases": [
            {"id": "user-first", "status": "equivalent", "modelCalls": 0},
            {"id": "user-second", "status": "equivalent", "modelCalls": 0}]}
        runner.check_verification(report, {"user-first", "user-second"})
        for field, value in (("modelCalls", 1), ("modelCalls", False),
                             ("status", "unknown"), ("id", "wrong-case")):
            changed = json.loads(json.dumps(report))
            changed["cases"][0][field] = value
            with self.assertRaises(runner.GateError):
                runner.check_verification(changed, {"user-first", "user-second"})

    def report(self, answer, output=None, digest=None):
        receipt = self.receipt()
        receipt["cases"][1].update(verdict="fail", answerSHA256=digest or
                                   hashlib.sha256(answer.encode()).hexdigest())
        if output is None:
            output = ("\ncase user-first\nprivate passing answer\nuser-first: pass\n"
                      "\ncase user-second\n" + answer + "\nuser-second: fail\n"
                      "\nevaluation fail: summary\n")
        stream = io.StringIO()
        with contextlib.redirect_stderr(stream):
            runner.report_failed_cases(receipt, output.encode())
        return stream.getvalue()

    def test_failure_preserves_newlines_and_only_discloses_failed_answer(self):
        answer = "wrong\r\nanswer\n"
        result = self.report(answer)
        self.assertIn(json.dumps(answer), result)
        self.assertNotIn("private passing", result)
        self.assertNotIn("evaluation fail", result)

    def test_digest_mismatch_or_ambiguous_boundary_hides_answer(self):
        for output in ("unstructured secret", "\ncase user-second\nwrong\nuser-second: fail\n"
                       "\ncase user-second\nwrong\nuser-second: fail\n"):
            result = self.report("wrong", output=output)
            self.assertIn("answer unavailable", result)
            self.assertNotIn("unstructured", result)
        self.assertIn("answer unavailable", self.report("wrong", digest="a" * 64))

    def test_selected_key_is_redacted_and_workflow_commands_are_inert(self):
        answer = "::error::toy\n\x1b[31msecret-canary"
        with patch.dict(os.environ, {"MODEL_KEY_ENV": "MY_KEY", "MY_KEY": "secret-canary"}):
            result = self.report(answer)
        self.assertEqual(len(result.splitlines()), 1)
        self.assertNotIn("secret-canary", result)
        self.assertNotIn("\x1b", result)
        self.assertIn("[REDACTED]", result)
        self.assertIn("\\n", result)

    def git(self, bundle, *args):
        return subprocess.check_output(["git", "-C", str(bundle), *args], stderr=subprocess.DEVNULL)

    def make_bundle(self, tmp):
        bundle = Path(tmp) / "bundle"
        (bundle / "eval").mkdir(parents=True)
        (bundle / "agent.yaml").write_text("public agent\n")
        (bundle / "eval" / "one.yaml").write_text("public case\n")
        self.git(bundle, "init", "-q")
        self.git(bundle, "add", "agent.yaml", "eval")
        self.git(bundle, "-c", "user.name=CI", "-c", "user.email=ci@example.invalid", "commit", "-qm", "fixture")
        return bundle

    def args(self, bundle, artifacts, mode="prepare"):
        return SimpleNamespace(mode=mode, kmx="kmx", bundle=bundle,
                               sessions="127.0.0.1:18088", artifacts=artifacts,
                               case_timeout="2m", verify_timeout="5m", command_timeout=600)

    def test_prepare_keeps_provenance_and_only_removes_own_endpoint_receipts(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = self.make_bundle(tmp)
            receipts = bundle / "receipts"
            receipts.mkdir()
            own = runner.evaluation_path(bundle, "127.0.0.1:18088")
            own.write_text("stale")
            report = own.with_name("verify-" + own.name + ".json")
            report.write_text("stale")
            foreign = receipts / "eval-other-target.json"
            foreign.write_text("keep")
            artifacts = Path(tmp) / "artifacts"
            runner.run(self.args(bundle, artifacts))
            self.assertFalse(own.exists())
            self.assertFalse(report.exists())
            self.assertEqual(foreign.read_text(), "keep")
            self.assertEqual(runner.source_revision(bundle), self.git(bundle, "rev-parse", "HEAD").decode().strip())

    def test_git_preflight_rejects_dirty_staged_deleted_and_untracked_cases(self):
        for change in ("dirty", "staged", "deleted", "untracked", "hint-hidden"):
            with self.subTest(change=change), tempfile.TemporaryDirectory() as tmp:
                bundle = self.make_bundle(tmp)
                case = bundle / "eval" / "one.yaml"
                if change == "deleted":
                    case.unlink()
                elif change == "untracked":
                    (case.parent / "extra.yaml").write_text("extra")
                else:
                    if change == "hint-hidden":
                        self.git(bundle, "update-index", "--assume-unchanged", "eval/one.yaml")
                    case.write_text("changed")
                    if change == "staged":
                        self.git(bundle, "add", "eval")
                with self.assertRaises(runner.GateError):
                    runner.source_revision(bundle)

    def test_source_revision_accepts_nested_bundle_in_shallow_checkout(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = self.make_bundle(tmp)
            nested = repo / "agents" / "my-agent"
            (nested / "eval").mkdir(parents=True)
            (nested / "agent.yaml").write_text("nested agent\n")
            (nested / "eval" / "one.yaml").write_text("nested case\n")
            self.git(repo, "add", "agents")
            self.git(repo, "-c", "user.name=CI", "-c", "user.email=ci@example.invalid", "commit", "-qm", "nested fixture")
            clone = Path(tmp) / "shallow"
            subprocess.run(["git", "clone", "-q", "--depth=1", repo.as_uri(), str(clone)], check=True)
            self.assertEqual(runner.source_revision(clone / "agents/my-agent"), self.git(repo, "rev-parse", "HEAD").decode().strip())

    def test_linked_receipts_are_refused_without_deleting_target_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            bundle = self.make_bundle(tmp)
            elsewhere = Path(tmp) / "elsewhere"
            elsewhere.mkdir()
            target = elsewhere / "eval-private.json"
            target.write_text("keep")
            (bundle / "receipts").symlink_to(elsewhere, target_is_directory=True)
            with self.assertRaises(runner.GateError):
                runner.run(self.args(bundle, Path(tmp) / "artifacts"))
            self.assertEqual(target.read_text(), "keep")

    def evidence_run(self, tmp, returncode=0, commit=None, verdict="pass", mode="evaluate", report_status="equivalent"):
        bundle = self.make_bundle(tmp)
        artifacts = Path(tmp) / "artifacts"
        runner.run(self.args(bundle, artifacts))
        receipt = {"gitCommit": commit or runner.source_revision(bundle),
                   "result": verdict, "fullCaseSet": True,
                   "target": {"runtime": "agentsessions", "identity": {"address": "127.0.0.1:18088"}},
                   "cases": [{"id": "user-first", "verdict": verdict}]}
        own = runner.evaluation_path(bundle, "127.0.0.1:18088")
        own.parent.mkdir(exist_ok=True)
        if mode == "verify":
            own.write_text(json.dumps(receipt))
        def kmx(*args, **kwargs):
            if mode == "evaluate":
                own.write_text(json.dumps(receipt))
            else:
                own.with_name("verify-" + own.name + ".json").write_text(json.dumps({
                    "result": report_status, "cases": [{"id": "user-first", "status": report_status, "modelCalls": 0}]}))
            return SimpleNamespace(returncode=returncode, stdout=b"private answer", stderr=b"private provider log")
        with patch.object(runner, "invoke_kmx", side_effect=kmx):
            runner.run(self.args(bundle, artifacts, mode))
        return artifacts

    def test_kmx_child_does_not_inherit_provider_credentials(self):
        with patch.dict(os.environ, {"MODEL_KEY_ENV": "MY_KEY", "MY_KEY": "canary",
                                     "MODEL_API_KEY": "canary", "OPENAI_API_KEY": "canary"}):
            result = runner.invoke_kmx(["python3", "-c", "import os; print([os.getenv(n) for n in ['MY_KEY', 'MODEL_API_KEY', 'OPENAI_API_KEY']])"], 10)
        self.assertEqual(result.stdout, b"[None, None, None]\n")

    def test_success_is_quiet_and_uploads_receipt_with_commit(self):
        with tempfile.TemporaryDirectory() as tmp:
            out, err = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                artifacts = self.evidence_run(tmp)
            self.assertEqual(out.getvalue(), "evaluation: 1/1 cases passed\n")
            self.assertEqual(err.getvalue(), "")
            files = list(artifacts.iterdir())
            self.assertEqual(len(files), 1)
            self.assertRegex(json.loads(files[0].read_text())["gitCommit"], "^[a-f0-9]{40}$")

    def test_cli_failure_wrong_revision_and_verify_failure_cannot_pass(self):
        for kwargs, message in (
            ({"returncode": 1}, "kmx evaluation failed"),
            ({"commit": "b" * 40}, "receipt does not describe this checkout"),
            ({"mode": "verify", "returncode": 1}, "kmx verification failed"),
            ({"mode": "verify", "report_status": "unknown"}, "verification requires equivalent"),
        ):
            with self.subTest(kwargs=kwargs), tempfile.TemporaryDirectory() as tmp:
                with self.assertRaisesRegex(runner.GateError, message):
                    self.evidence_run(tmp, **kwargs)
                artifacts = list((Path(tmp) / "artifacts").iterdir())
                self.assertTrue(artifacts)
                self.assertNotIn("private answer", "".join(p.read_text() for p in artifacts))


if __name__ == "__main__":
    unittest.main()
