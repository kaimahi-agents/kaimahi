#!/usr/bin/env python3
"""Execute the fragment hygiene step against disposable Git merge checkouts."""
import os
import pathlib
import shutil
import subprocess
import unittest

import yaml

import test_check_mutations

ROOT = pathlib.Path(__file__).resolve().parents[1]
POLICY_STEP = "Changelog fragments validate and cover this PR"


class ChangelogWorkflowTests(unittest.TestCase):
    git = test_check_mutations.ClassificationTests.git
    change = test_check_mutations.ClassificationTests.change
    commit = test_check_mutations.ClassificationTests.commit
    merge_head = test_check_mutations.ClassificationTests.merge_head

    def setUp(self):
        test_check_mutations.ClassificationTests.setUp(self)
        self.change("changes/README.md", "# Pending release notes\n")
        self.base = self.commit()
        workflow = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())
        self.steps = workflow["jobs"]["hygiene"]["steps"]

    def gate(self, event="pull_request", merge=True, base=None):
        step = next((s for s in self.steps if s.get("name") == POLICY_STEP), None)
        self.assertIsNotNone(step, "fragment policy is missing from required hygiene")
        self.assertEqual(step.get("env", {}).get("EVENT"), "${{ github.event_name }}")
        source = ROOT / "scripts/check-changelog-fragments.py"
        self.assertTrue(source.is_file(), "fragment checker is missing")
        if merge:
            self.git("update-ref", "HEAD", self.merge_head(base))
        target = self.root / "scripts/check-changelog-fragments.py"
        shutil.copyfile(source, target)
        return subprocess.run(
            ["bash", "-e", "-c", step["run"]], cwd=self.root,
            env=dict(os.environ, EVENT=event, BEFORE=self.base),
            text=True, capture_output=True, timeout=20, check=False,
        )

    def test_product_without_new_fragment_refuses(self):
        self.change("internal/feature.go", "package feature\n")
        self.commit()
        got = self.gate()
        self.assertNotEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertIn("fragment", (got.stdout + got.stderr).lower())

    def test_product_with_new_fragment_passes(self):
        self.change("internal/feature.go", "package feature\n")
        self.change("changes/123.md", "### Changed\n\n- Add the operator feature.\n")
        self.commit()
        got = self.gate()
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)

    def test_docs_and_tests_are_automatically_exempt(self):
        self.change("docs/guide.md", "Guide.\n")
        self.change("internal/feature_test.go", "package feature\n")
        self.commit()
        got = self.gate()
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)

    def test_new_note_requirement_covers_earlier_topic_commits(self):
        self.change("internal/feature.go", "package feature\n")
        self.commit()
        self.change("docs/guide.md", "Later documentation.\n")
        self.commit()
        got = self.gate()
        self.assertNotEqual(got.returncode, 0, got.stdout + got.stderr)

    def test_actual_merge_parent_excludes_unrelated_main_changes(self):
        self.change("scripts/other.py", "print('main-only change')\n")
        current_main = self.commit()
        self.change("docs/guide.md", "Topic documentation.\n")
        self.commit()
        got = self.gate(base=current_main)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)

    def test_pr_nonmerge_checkout_refuses_even_with_note(self):
        self.change("internal/feature.go", "package feature\n")
        self.change("changes/123.md", "### Changed\n\n- Add the operator feature.\n")
        self.commit()
        got = self.gate(merge=False)
        self.assertNotEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertIn("merge", (got.stdout + got.stderr).lower())

    def test_main_push_validates_without_requiring_a_new_note(self):
        self.change("internal/feature.go", "package feature\n")
        self.commit()
        got = self.gate(event="push", merge=False)
        self.assertEqual(got.returncode, 0, got.stdout + got.stderr)

    def test_docs_exemption_does_not_skip_invalid_fragment_validation(self):
        self.change("changes/invalid.md", "### Changed\n\n")
        self.commit()
        got = self.gate()
        self.assertNotEqual(got.returncode, 0, got.stdout + got.stderr)
        self.assertIn("invalid.md", got.stdout + got.stderr)

    def test_hygiene_runs_fragment_and_workflow_regressions(self):
        commands = "\n".join(s.get("run", "") for s in self.steps)
        self.assertTrue("python3 scripts/check-changelog-fragments.py --selftest" in commands,
                        "hygiene must exercise fragment-policy self-tests")
        self.assertTrue("python3 scripts/test_changelog_ci.py" in commands,
                        "hygiene must exercise its real Git routing regressions")


if __name__ == "__main__":
    unittest.main()
