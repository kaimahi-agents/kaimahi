#!/usr/bin/env python3
"""Behavioral change classification, shard routing and required-check tests."""
import json
import os
import pathlib
import subprocess
import tempfile
import unittest

import yaml

import test_check_mutations

ROOT = test_check_mutations.ROOT


class KagentClassificationTests(unittest.TestCase):
    setUp = test_check_mutations.ClassificationTests.setUp
    git = test_check_mutations.ClassificationTests.git
    change = test_check_mutations.ClassificationTests.change
    commit = test_check_mutations.ClassificationTests.commit
    classify = test_check_mutations.ClassificationTests.classify
    merge_head = test_check_mutations.ClassificationTests.merge_head

    def test_shared_dependencies_route_independently(self):
        relevant = [
            "internal/kmx/app/command.go", "internal/kmx/app/persist.go",
            "internal/kmx/runtime/registry.go", "internal/kmx/scaffold/names.go",
            "internal/kmx/guard/guard.go", "internal/kmx/run/run.go",
            "internal/kmx/config/config.go", "internal/kmx/secretshapes/shapes.json",
            "internal/kmx/toolchain/tools.go", "cmd/kmx/main.go", "go.mod", "go.sum", "embed.go",
            "internal/kmx/app/testdata/model.json", "scripts/ci/kagent-lift.sh",
            "scripts/ci/kagent-lift-fixture.py", "scripts/test_kagent_lift_fixture.py",
            "scripts/check-legacy-runtime.py", "scripts/legacy-runtime-allowlist.json",
            ".github/actions/classify-change/action.yml", ".github/workflows/ci.yml",
        ]
        for path in relevant:
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.change(path, "changed")
                self.commit()
                self.assertEqual(self.classify(head=self.merge_head()).get("kagent_changed"), "true")
        for path in ("Makefile", "docs/readme.md", "scripts/unrelated.py", "pkg/kmx/example.go"):
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.change(path, "changed")
                self.commit()
                self.assertEqual(self.classify(head=self.merge_head()).get("kagent_changed"), "false")

    def test_deletion_rename_and_quoted_paths_run_proof(self):
        for name in ('internal/kmx/app/shared.go', 'internal/kmx/app/shared-é.go',
                     'internal/kmx/app/shared-"quote.go', 'internal/kmx/app/shared-\ttab.go'):
            self.git("reset", "--hard", self.base)
            self.change(name, "base")
            source = self.commit()
            for rename in (False, True):
                with self.subTest(name=name, rename=rename):
                    self.git("reset", "--hard", source)
                    old = self.root / name
                    if rename:
                        old.rename(self.root / "docs/moved.md")
                    else:
                        old.unlink()
                    self.commit()
                    self.assertEqual(self.classify(head=self.merge_head(source)).get("kagent_changed"), "true")

    def test_unknown_classification_runs_proof(self):
        self.assertEqual(self.classify(head="missing").get("kagent_changed"), "true")
        self.assertEqual(self.classify(event="push", base="missing").get("kagent_changed"), "true")
        self.assertEqual(self.classify(event="push", base="0" * 40).get("kagent_changed"), "true")
        self.assertEqual(self.classify(event="push").get("kagent_changed"), "true")  # empty diff


class WorkflowTests(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())["jobs"]

    def enabled(self, expression, event, changed="false"):
        expression = str(expression).strip().removeprefix("${{").removesuffix("}}")
        expression = expression.replace("github.event_name", repr(event))
        expression = expression.replace("needs.classify-change.outputs.kagent_changed", repr(changed))
        expression = expression.replace("github.ref", repr("refs/heads/main"))
        expression = expression.replace("always()", "True").replace("&&", " and ").replace("||", " or ")
        return bool(eval(expression, {"__builtins__": {}}, {}))

    def test_relevant_prs_unknown_main_and_nightly_run_new_shard(self):
        job = self.jobs.get("e2e-kagent-lift", {})
        self.assertTrue(job, "new proof shard is missing")
        for event, changed, wanted in (
                ("pull_request", "false", False), ("pull_request", "true", True),
                ("pull_request", "", True), ("pull_request", "unknown", True),
                ("push", "false", True), ("schedule", "false", True),
                ("workflow_dispatch", "false", True)):
            with self.subTest(event=event, changed=changed):
                self.assertEqual(self.enabled(job.get("if", "True"), event, changed), wanted)
        self.assertIn("kagent_changed", self.jobs.get("classify-change", {}).get("outputs", {}))
        self.assertIn("classify-change", job.get("needs", []))
        for step in job["steps"]:
            self.assertNotIn("docs_only", step.get("if", ""), "main/nightly must bypass docs guards")

    def aggregate(self, event, changed="false", overrides=None):
        agg = self.jobs["e2e-hello-world"]
        needs = {name: {"result": "success", "outputs": {}} for name in agg["needs"]}
        needs["classify-change"] = {"result": "success", "outputs": {"kagent_changed": changed}}
        if event == "schedule":
            for name in needs:
                if name.startswith("e2e-") and name != "e2e-kagent-lift":
                    needs[name]["result"] = "skipped"
        needs.update(overrides or {})
        env = dict(os.environ, SHARDS=json.dumps(needs), EVENT=event)
        return subprocess.run(["bash", "-e", "-c", agg["steps"][0]["run"]], env=env,
                              cwd=ROOT, text=True, capture_output=True, timeout=10)

    def test_only_intentional_new_shard_skip_is_accepted(self):
        skipped = {"e2e-kagent-lift": {"result": "skipped"}}
        self.assertEqual(self.aggregate("pull_request", overrides=skipped).returncode, 0)
        for event, changed in (("pull_request", "true"), ("pull_request", ""),
                               ("push", "false"), ("schedule", "false")):
            with self.subTest(event=event, changed=changed):
                self.assertNotEqual(self.aggregate(event, changed, skipped).returncode, 0)
        for result in ("failure", "cancelled"):
            self.assertNotEqual(self.aggregate("pull_request", overrides={
                "e2e-kagent-lift": {"result": result}}).returncode, 0)
        for event in ("pull_request", "schedule", "workflow_dispatch", "unknown"):
            self.assertEqual(self.aggregate(event).returncode, 0)
            for result in ("failure", "cancelled", "skipped"):
                self.assertNotEqual(self.aggregate(event, overrides={
                    "classify-change": {"result": result, "outputs": {"kagent_changed": "false"}}}
                ).returncode, 0)
            self.assertNotEqual(self.aggregate(event, overrides={
                "e2e-orka-runtime": {"result": "failure"}}).returncode, 0)
        self.assertNotEqual(self.aggregate("pull_request", overrides={
            "e2e-orka-runtime": {"result": "skipped"}}).returncode, 0)
        for missing in self.jobs["e2e-hello-world"]["needs"]:
            if missing == "classify-change":
                continue  # Classification result failures are exercised above.
            agg = self.jobs["e2e-hello-world"]
            original = agg["needs"]
            agg["needs"] = [name for name in original if name != missing]
            try:
                self.assertNotEqual(self.aggregate("pull_request").returncode, 0, missing)
            finally:
                agg["needs"] = original

    def test_inline_guards_execute_and_reject_missing_or_docs_only_new_routing(self):
        step = next(s for s in self.jobs["hygiene"]["steps"]
                    if s.get("name", "").startswith("Every cluster step carries"))
        with tempfile.TemporaryDirectory(prefix="kmx-ci-guards-") as directory:
            root = pathlib.Path(directory)
            workflow = root / ".github/workflows/ci.yml"
            workflow.parent.mkdir(parents=True)
            for mutation in ("none", "new-docs-guard", "missing-dependency", "old-missing-guard", "nightly-aggregator-skip"):
                jobs = json.loads(json.dumps(self.jobs))
                if mutation == "new-docs-guard":
                    jobs["e2e-kagent-lift"]["steps"][2]["if"] = "steps.changes.outputs.docs_only != 'true'"
                elif mutation == "missing-dependency":
                    jobs["e2e-hello-world"]["needs"].remove("classify-change")
                elif mutation == "old-missing-guard":
                    jobs["e2e-orka-runtime"]["steps"][2].pop("if")
                elif mutation == "nightly-aggregator-skip":
                    jobs["e2e-hello-world"]["if"] = "always() && github.event_name != 'schedule'"
                workflow.write_text(yaml.safe_dump({"jobs": jobs}))
                result = subprocess.run(["bash", "-e", "-c", step["run"]], cwd=root, text=True,
                                        capture_output=True, timeout=10)
                with self.subTest(mutation=mutation):
                    self.assertEqual(result.returncode == 0, mutation == "none", result.stdout + result.stderr)

    def test_native_tripwires_execute_with_supported_lift_status_and_refused_chat_eval(self):
        for step in self.jobs["hygiene"]["steps"]:
            if step.get("name", "").startswith(("Converted shards install", "The clone-free journey sets up")):
                result = subprocess.run(["bash", "-e", "-c", step["run"]], cwd=ROOT, text=True,
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_required_check_always_reports_and_needs_classification_and_all_shards(self):
        agg = self.jobs["e2e-hello-world"]
        for event in ("pull_request", "push", "schedule"):
            self.assertTrue(self.enabled(agg["if"], event))
        expected = {name for name in self.jobs if name.startswith("e2e-") and name != "e2e-hello-world"}
        self.assertEqual(set(agg["needs"]), expected | {"classify-change"})


if __name__ == "__main__":
    unittest.main()
