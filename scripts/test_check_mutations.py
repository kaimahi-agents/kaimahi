#!/usr/bin/env python3
"""Exercise the mutation harness and CI routing with disposable repositories."""
from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import yaml

ROOT = pathlib.Path(__file__).resolve().parents[1]
HARNESS = ROOT / "scripts/check-mutations.py"


def load_harness():
    spec = importlib.util.spec_from_file_location("mutation_harness", HARNESS)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HarnessTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="kmx-harness-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.scripts = self.root / "scripts"
        self.specs = self.scripts / "mutations"
        self.specs.mkdir(parents=True)
        (self.scripts / HARNESS.name).write_text(HARNESS.read_text())
        self.env = dict(os.environ)
        self.env.pop("KMX_MUTATION_JOBS", None)

    def checker(self, name="check-a.py", source=None, mutations=None, verify=None):
        if source is None:
            source = 'VERDICT = 0\nprint("verified")\nraise SystemExit(VERDICT)\n'
        (self.scripts / name).write_text(source)
        spec = {
            "checker": name,
            "verify": verify or [sys.executable, "-B", "scripts/" + name],
            "signature": "verified",
            "mutations": mutations if mutations is not None else [
                {"name": "verdict inverted", "find": "VERDICT = 0", "replace": "VERDICT = 1"}
            ],
        }
        path = self.specs / (name + ".json")
        path.write_text(json.dumps(spec))
        return path

    def run_harness(self, *args):
        return subprocess.run(
            [sys.executable, "-B", str(self.scripts / HARNESS.name), *args],
            cwd=self.root, env=self.env, capture_output=True, text=True, timeout=20, check=False,
        )

    def test_surviving_mutation_exits_nonzero(self):
        self.checker(mutations=[{
            "name": "harmless edit", "find": "VERDICT = 0", "replace": "VERDICT = 0 # unchanged",
        }])
        got = self.run_harness()
        self.assertEqual(got.returncode, 1, got.stderr)
        self.assertIn("[harmless edit]: the checker still passed", got.stderr)

    def test_empty_and_undeclared_mutations_are_failures(self):
        self.checker(mutations=[])
        (self.scripts / "check-missing.py").write_text("pass\n")
        got = self.run_harness()
        self.assertEqual(got.returncode, 1, got.stderr)
        self.assertIn("declares no mutations", got.stderr)
        self.assertIn("check-missing.py: is a checker with no", got.stderr)

    def test_baseline_and_override_must_pass_before_mutants(self):
        for override in (False, True):
            with self.subTest(override=override):
                mutations = [{"name": "verdict inverted", "find": "VERDICT = 0", "replace": "VERDICT = 1"}]
                verify = [sys.executable, "-c", "raise SystemExit(7)"]
                if override:
                    mutations[0]["verify"] = verify
                    verify = None
                self.checker(mutations=mutations, verify=verify)
                got = self.run_harness()
                self.assertEqual(got.returncode, 1, got.stderr)
                self.assertIn("fails on the UNMUTATED checker (exit 7)", got.stderr)
                self.assertNotIn("ok   check-a.py", got.stdout)

    def test_missing_baseline_signature_is_not_proof(self):
        self.checker(source='VERDICT = 0\nraise SystemExit(VERDICT)\n')
        got = self.run_harness()
        self.assertEqual(got.returncode, 1, got.stderr)
        self.assertIn("never printed 'verified'", got.stderr)

    def test_neutered_entry_point_is_not_a_survivor(self):
        self.checker(mutations=[{
            "name": "entry removed", "find": 'print("verified")', "replace": "pass",
        }])
        got = self.run_harness()
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertIn("noticed (it never ran)", got.stdout)

    def test_jobs_flag_overrides_environment(self):
        self.checker()
        self.env["KMX_MUTATION_JOBS"] = "invalid"
        got = self.run_harness("--jobs", "1", "check-a.py")
        self.assertEqual(got.returncode, 0, got.stderr)
        self.assertIn("1 checker(s), 1 deliberate breakages", got.stdout)

    def test_invalid_worker_counts_are_rejected(self):
        self.checker()
        for value in ("0", "-1", "invalid"):
            with self.subTest(value=value):
                got = self.run_harness("--jobs", value)
                self.assertEqual(got.returncode, 2, got.stderr)
                self.assertIn("positive integer", got.stderr)
        self.env["KMX_MUTATION_JOBS"] = "0"
        got = self.run_harness()
        self.assertEqual(got.returncode, 2, got.stderr)
        self.assertIn("positive integer", got.stderr)

    def test_checkers_run_concurrently_but_report_in_declared_order(self):
        # Both baseline and mutant need the other checker to start. A serial
        # runner fails this fixture; each checker still validates its own baseline first.
        for letter in ("a", "b"):
            source = f'''import pathlib, time
VERDICT = 0
root = pathlib.Path({str(self.root)!r})
(root / ("{letter}-" + str(VERDICT))).touch()
other = root / ("{'b' if letter == 'a' else 'a'}-" + str(VERDICT))
deadline = time.monotonic() + 3
while not other.exists():
    if time.monotonic() > deadline:
        raise SystemExit(9)
    time.sleep(0.01)
print("verified")
raise SystemExit(VERDICT)
'''
            self.checker("check-" + letter + ".py", source=source)
        got = self.run_harness("--jobs", "2")
        self.assertEqual(got.returncode, 0, got.stderr)
        lines = [line for line in got.stdout.splitlines() if line.startswith("ok   ")]
        self.assertEqual(lines, [
            "ok   check-a.py [verdict inverted] — noticed (exit 1)",
            "ok   check-b.py [verdict inverted] — noticed (exit 1)",
        ])

    def test_default_cpu_count_bounds_the_pool(self):
        log = self.root / "events"
        for letter in "abcd":
            source = f'''import time
VERDICT = 0
with open({str(log)!r}, "a") as output:
    output.write("start\\n")
time.sleep(0.1)
with open({str(log)!r}, "a") as output:
    output.write("end\\n")
print("verified")
raise SystemExit(VERDICT)
'''
            self.checker("check-" + letter + ".py", source=source)
        harness = load_harness()
        with mock.patch.multiple(harness, ROOT=self.root, SCRIPTS=self.scripts, MUTATIONS=self.specs), \
                mock.patch.object(harness.os, "cpu_count", return_value=2), \
                mock.patch.dict(os.environ, {"KMX_MUTATION_JOBS": "2"}), \
                contextlib.redirect_stdout(io.StringIO()):
            # Remove the override so the production default uses cpu_count().
            del os.environ["KMX_MUTATION_JOBS"]
            self.assertEqual(harness.main([]), 0)
        active = peak = 0
        events = log.read_text().splitlines()
        for event in events:
            active += 1 if event == "start" else -1
            peak = max(peak, active)
        self.assertEqual(events.count("start"), 8)
        self.assertEqual(active, 0)
        self.assertLessEqual(peak, 2)

    def test_mirrors_do_not_share_import_caches(self):
        self.checker()
        cache = self.scripts / "__pycache__"
        cache.mkdir()
        (cache / "stale.pyc").write_bytes(b"stale")
        harness = load_harness()
        with mock.patch.multiple(harness, ROOT=self.root, SCRIPTS=self.scripts):
            mirror = harness.mirror("check-a.py", (self.scripts / "check-a.py").read_text())
        self.addCleanup(shutil.rmtree, mirror)
        self.assertFalse((mirror / "scripts/__pycache__").exists())

    def test_timeout_is_harness_failure_not_noticed_mutation(self):
        self.checker()
        harness = load_harness()
        calls = 0

        def timed_run(command, cwd):
            nonlocal calls
            calls += 1
            if calls == 1:
                return subprocess.CompletedProcess(command, 0, "verified", "")
            raise subprocess.TimeoutExpired(command, 600)

        with mock.patch.multiple(harness, ROOT=self.root, SCRIPTS=self.scripts, MUTATIONS=self.specs), \
                mock.patch.object(harness, "run", timed_run), \
                contextlib.redirect_stdout(io.StringIO()), \
                contextlib.redirect_stderr(io.StringIO()) as errors:
            self.assertEqual(harness.main([]), 1)
        self.assertIn("timed out", errors.getvalue())


class ClassificationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="kmx-routing-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        action = yaml.safe_load((ROOT / ".github/actions/classify-change/action.yml").read_text())
        self.body = action["runs"]["steps"][0]["run"]
        self.git("init", "-q")
        self.git("config", "user.name", "Routing test")
        self.git("config", "user.email", "routing@example.invalid")
        self.change("docs/readme.md", "base")
        self.change("scripts/old.py", "base")
        self.base = self.commit()

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root, text=True, timeout=10).strip()

    def change(self, path, text):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")
        return self.git("rev-parse", "HEAD")

    def classify(self, event="pull_request", base=None, head=None):
        output = self.root / "outputs"
        output.unlink(missing_ok=True)
        env = dict(os.environ, EVENT=event, BASE=base or self.base,
                   HEAD=head or self.git("rev-parse", "HEAD"), GITHUB_OUTPUT=str(output))
        got = subprocess.run(["bash", "-e", "-c", self.body], cwd=self.root,
                             env=env, capture_output=True, text=True, timeout=10, check=False)
        self.assertEqual(got.returncode, 0, got.stderr)
        return dict(line.split("=", 1) for line in output.read_text().splitlines())

    def merge_head(self, base=None):
        head = self.git("rev-parse", "HEAD")
        return self.git("commit-tree", "HEAD^{tree}", "-p", base or self.base, "-p", head, "-m", "merge")

    def test_docs_only_skips_mutations(self):
        self.change("docs/readme.md", "updated")
        self.commit()
        got = self.classify(head=self.merge_head())
        self.assertEqual(got, {"docs_only": "true", "scripts_changed": "false"})

    def test_mixed_paths_do_not_hide_scripts_after_first_non_doc(self):
        self.change("Makefile", "all:")
        self.change("scripts/new.py", "pass")
        self.commit()
        got = self.classify(head=self.merge_head())
        self.assertEqual(got, {"docs_only": "false", "scripts_changed": "true"})

    def test_script_deletion_and_rename_out_are_script_changes(self):
        for rename in (False, True):
            with self.subTest(rename=rename):
                self.git("reset", "--hard", self.base)
                old = self.root / "scripts/old.py"
                if rename:
                    old.rename(self.root / "docs/old.md")
                else:
                    old.unlink()
                self.commit()
                got = self.classify(head=self.merge_head())
                self.assertEqual(got, {"docs_only": "false", "scripts_changed": "true"})

    def test_git_quoted_script_paths_still_run_mutations(self):
        for name in ('scripts/check-é.py', 'scripts/check-"quote.py', 'scripts/check-\ttab.py'):
            with self.subTest(name=name):
                self.git("reset", "--hard", self.base)
                self.change(name, "pass")
                self.commit()
                got = self.classify(head=self.merge_head())
                self.assertEqual(got, {"docs_only": "false", "scripts_changed": "true"})

    def test_stale_event_base_does_not_include_other_peoples_scripts(self):
        self.change("scripts/old.py", "unrelated merge")
        current_base = self.commit()
        self.change("docs/readme.md", "PR docs")
        self.commit()
        got = self.classify(base=self.base, head=self.merge_head(current_base))
        self.assertEqual(got, {"docs_only": "true", "scripts_changed": "false"})

    def test_unresolvable_diff_runs_mutations(self):
        got = self.classify(head="missing-merge")
        self.assertEqual(got, {"docs_only": "false", "scripts_changed": "true"})
        got = self.classify(event="push", base="missing-base")
        self.assertEqual(got, {"docs_only": "false", "scripts_changed": "true"})


class WorkflowRoutingTests(unittest.TestCase):
    def setUp(self):
        self.workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())

    def enabled(self, expression, event, scripts_changed="false"):
        # Evaluate the small boolean guard used here, not the whole Actions
        # expression language. The expression comes from the actual workflow.
        expression = str(expression).strip().removeprefix("${{").removesuffix("}}")
        expression = expression.replace("github.event_name", repr(event))
        expression = expression.replace("steps.mutation_changes.outputs.scripts_changed", repr(scripts_changed))
        expression = expression.replace("github.ref", repr("refs/heads/main"))
        expression = expression.replace("always()", "True").replace("&&", " and ").replace("||", " or ")
        return bool(eval(expression, {"__builtins__": {}}, {}))

    def test_mutations_run_for_scripts_pr_and_all_non_pr_events(self):
        step = next(s for s in self.workflow["jobs"]["hygiene"]["steps"]
                    if s.get("name") == "Every checker notices being broken")
        for event, scripts, wanted in (
            ("pull_request", "false", False),
            ("pull_request", "true", True),
            ("pull_request", "", True),  # no output must fail closed
            ("push", "false", True),
            ("schedule", "false", True),
            ("workflow_dispatch", "false", True),
        ):
            with self.subTest(event=event, scripts=scripts):
                self.assertEqual(self.enabled(step.get("if", "True"), event, scripts), wanted)

    def test_routing_self_tests_run_even_without_script_changes(self):
        steps = self.workflow["jobs"]["hygiene"]["steps"]
        tests = [s for s in steps if "python3 scripts/test_check_mutations.py" in s.get("run", "")]
        self.assertTrue(tests, "routing tests must be invoked")
        self.assertTrue(any(self.enabled(s.get("if", "True"), "pull_request", "false") for s in tests),
                        "workflow/action-only changes must exercise routing tests")

    def test_nightly_runs_only_hygiene_and_preserves_other_events(self):
        for name, job in self.workflow["jobs"].items():
            with self.subTest(job=name):
                expression = job.get("if", "True")
                self.assertEqual(self.enabled(expression, "schedule"), name == "hygiene")
                if name != "kmx-clone-free":
                    self.assertTrue(self.enabled(expression, "pull_request"))
        triggers = self.workflow.get("on", self.workflow.get(True))
        self.assertTrue(triggers.get("schedule"), "a nightly trigger must exist")


if __name__ == "__main__":
    unittest.main()
