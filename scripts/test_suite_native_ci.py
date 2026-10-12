#!/usr/bin/env python3
"""Execute the native-suite classifier and required aggregator without clusters."""
import json
import os
import pathlib
import tempfile
import subprocess
import unittest

import yaml
import test_check_mutations as routing

ROOT = routing.ROOT


class ClassificationTests(unittest.TestCase):
    setUp = routing.ClassificationTests.setUp
    git = routing.ClassificationTests.git
    change = routing.ClassificationTests.change
    commit = routing.ClassificationTests.commit
    classify = routing.ClassificationTests.classify
    merge_head = routing.ClassificationTests.merge_head

    def test_native_dependencies_and_unrelated_paths(self):
        for path, expected in [(p, "true") for p in (
            "internal/kmx/app/suite_native.go", "internal/kmx/agentsuite/workspace/load.go",
            "internal/kmx/agentsuite/oras/layout.go", "agentsuite/model.go",
            "internal/kmx/runtime/evaluation.go", "internal/kmx/scaffold/orka.go",
            "internal/kmx/orkaschema/schema.go", "internal/kmx/config/config.go",
            "internal/kmx/run/run.go", "internal/kmx/guard/guard.go",
            "internal/kmx/portforward/forward.go", "internal/kmx/toolchain/tools.go",
            "internal/kmx/secretshapes/shapes.json", "cmd/kmx/root.go", "go.mod", "go.sum",
            "embed.go", "scripts/ci/suite-native.sh", "scripts/ci/suite-native.py",
            "scripts/ci/eval-loop-model.yaml", "scripts/ci/eval-loop-warmup.json",
            "scripts/test_suite_native_ci.py", ".github/actions/classify-change/action.yml",
            ".github/workflows/ci.yml",
        )] + [(p, "false") for p in ("docs/suite-workspace.md", "Makefile", "pkg/kmx/model.go",
                                        "scripts/unrelated.py", "ax-harness/activity.py")]:
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.change(path, "changed")
                self.commit()
                self.assertEqual(self.classify(head=self.merge_head()).get("suite_native_changed"), expected)

    def test_unknown_empty_stale_base_deletion_and_rename(self):
        for opts in ({"head": "missing"}, {"event": "push", "base": "missing"},
                     {"event": "push", "base": "0" * 40}, {"event": "push"}):
            self.assertEqual(self.classify(**opts).get("suite_native_changed"), "true")
        self.change("internal/kmx/app/shared-é.go", "base")
        base = self.commit()
        for rename in (False, True):
            self.git("reset", "--hard", base)
            source = self.root / "internal/kmx/app/shared-é.go"
            if rename:
                source.rename(self.root / "docs/moved.md")
            else:
                source.unlink()
            self.commit()
            self.assertEqual(self.classify(head=self.merge_head(base)).get("suite_native_changed"), "true")
        self.git("reset", "--hard", base)
        self.change("internal/kmx/app/shared-é.go", "other merge")
        newer_base = self.commit()
        self.change("docs/readme.md", "PR docs")
        self.commit()
        self.assertEqual(self.classify(base=base, head=self.merge_head(newer_base)).get("suite_native_changed"), "false")


class WorkflowTests(unittest.TestCase):
    def setUp(self):
        self.jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())["jobs"]

    def test_shard_routing_and_pinned_proof(self):
        job = self.jobs.get("e2e-suite-native")
        self.assertIsNotNone(job, "dedicated native-suite shard is absent")
        self.assertEqual(job["needs"], ["classify-change"])
        for event, changed, expected in (("pull_request", "false", False), ("pull_request", "true", True),
                                         ("pull_request", "", True), ("pull_request", "unknown", True),
                                         ("push", "false", True), ("schedule", "false", True),
                                         ("workflow_dispatch", "false", True)):
            expression = job["if"].replace("github.event_name", repr(event)).replace(
                "needs.classify-change.outputs.suite_native_changed", repr(changed)).replace(
                "always()", "True").replace("&&", " and ").replace("||", " or ")
            self.assertEqual(bool(eval(expression, {"__builtins__": {}}, {})), expected)
        self.assertIn("suite_native_changed", self.jobs["classify-change"]["outputs"])
        self.assertTrue(any("bash scripts/ci/suite-native.sh" in s.get("run", "") for s in job["steps"]))
        for step in job["steps"]:
            self.assertNotIn("docs_only", step.get("if", ""))
        self.assertTrue(any("python3 scripts/test_suite_native_ci.py" in s.get("run", "") and not s.get("if")
                            for s in self.jobs["hygiene"]["steps"]))

    def aggregate(self, event, changed, result, classifier="success"):
        agg = self.jobs["e2e-hello-world"]
        needs = {n: {"result": "success"} for n in agg["needs"]}
        needs["classify-change"] = {"result": classifier, "outputs": {
            "kagent_changed": "true", "suite_native_changed": changed}}
        if event == "schedule":
            for name in needs:
                if name.startswith("e2e-") and name not in ("e2e-kagent-lift", "e2e-suite-native"):
                    needs[name]["result"] = "skipped"
        needs["e2e-suite-native"] = {"result": result}
        return subprocess.run(["bash", "-e", "-c", agg["steps"][0]["run"]],
                              env=dict(os.environ, SHARDS=json.dumps(needs), EVENT=event),
                              cwd=ROOT, capture_output=True, timeout=10).returncode

    def test_inline_cluster_guard_rejects_native_bypasses(self):
        step = next(s for s in self.jobs["hygiene"]["steps"] if s.get("name", "").startswith("Every cluster step carries"))
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            path = root / ".github/workflows/ci.yml"
            path.parent.mkdir(parents=True)
            for mutation in ("none", "native-docs-guard", "missing-classifier", "wrong-classification", "skipped-aggregator", "missing-native-dependency"):
                jobs = json.loads(json.dumps(self.jobs))
                if mutation == "native-docs-guard":
                    jobs["e2e-suite-native"]["steps"][2]["if"] = "steps.changes.outputs.docs_only != 'true'"
                elif mutation == "missing-classifier":
                    jobs["e2e-suite-native"]["needs"] = []
                elif mutation == "wrong-classification":
                    jobs["e2e-suite-native"]["if"] = jobs["e2e-suite-native"]["if"].replace("suite_native_changed", "kagent_changed")
                elif mutation == "skipped-aggregator":
                    jobs["e2e-hello-world"]["if"] = "always() && github.event_name != 'schedule'"
                elif mutation == "missing-native-dependency":
                    jobs["e2e-hello-world"]["needs"].remove("e2e-suite-native")
                path.write_text(yaml.safe_dump({"jobs": jobs}))
                result = subprocess.run(["bash", "-e", "-c", step["run"]], cwd=root, capture_output=True, timeout=10)
                with self.subTest(mutation=mutation):
                    self.assertEqual(result.returncode == 0, mutation == "none")

    def test_aggregator_truth_table(self):
        self.assertIn("e2e-suite-native", self.jobs["e2e-hello-world"]["needs"])
        for event in ("pull_request", "push", "schedule", "workflow_dispatch", "unknown"):
            for changed in ("false", "true", "", "unknown"):
                for result in ("success", "skipped", "failure", "cancelled", "unknown"):
                    with self.subTest(event=event, changed=changed, result=result):
                        expected = result == "success" or (event == "pull_request" and changed == "false" and result == "skipped")
                        self.assertEqual(self.aggregate(event, changed, result) == 0, expected)
            for result in ("failure", "cancelled", "skipped", "unknown"):
                self.assertNotEqual(self.aggregate(event, "false", "skipped", result), 0)


if __name__ == "__main__":
    unittest.main()
