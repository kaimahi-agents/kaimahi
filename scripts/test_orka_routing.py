#!/usr/bin/env python3
"""Default-RUN Orka classification and package-initialization tripwires."""
import importlib.util
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

import test_check_mutations

ROOT = test_check_mutations.ROOT
ROUTER = ROOT / "scripts/ci/orka-routing.py"
POLICY = ROOT / "scripts/ci/orka-routing.json"
BOUNDARY = ROOT / "scripts/ci/orka-boundary.go"


class RoutingTests(unittest.TestCase):
    setUp = test_check_mutations.ClassificationTests.setUp
    git = test_check_mutations.ClassificationTests.git
    change = test_check_mutations.ClassificationTests.change
    commit = test_check_mutations.ClassificationTests.commit
    merge_head = test_check_mutations.ClassificationTests.merge_head

    def route(self, base=None, head=None):
        result = subprocess.run([sys.executable, str(ROUTER), "--base", base or self.base,
                                 "--head", head or self.git("rev-parse", "HEAD")],
                                cwd=self.root, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        self.assertIn(result.stdout, ("true\n", "false\n"))
        return result.stdout.strip()

    def test_exact_safe_paths_and_default_run(self):
        safe = [
            "internal/kmx/agentfile/new.go", "internal/kmx/agentpreview/new.go",
            "internal/kmx/mcp/new.go", "internal/kmx/modelaccess/local.go",
            "internal/kmx/modelaccess/local_test.go", "internal/kmx/modelaccess/install.go",
            "internal/kmx/modelaccess/route.go",
            "docs/new.md", "brand/logo.svg", "README.md", "AGENTS.md", "CONTRIBUTING.md",
            "CHANGELOG.md", "LICENSE", "NOTICE", "changes/ci-note.md",
            "cmd/kmx/model_commands_test.go", "cmd/kmx/mcp_commands_test.go",
            "cmd/kmx/agent_file_commands_test.go", "internal/kmx/app/agent_file.go",
            "internal/kmx/app/agent_file_output.go", "internal/kmx/app/agent_file_test.go",
            "internal/kmx/app/agent_file_safety_test.go", "internal/kmx/app/agent_file_direct_test.go",
            ".github/copilot-instructions.md", ".github/workflows/agent-file.yml",
            ".github/workflows/model-access.yml", ".github/workflows/docs-pages.yml",
            ".github/workflows/copilot-plugin.yml",
        ]
        unsafe = [
            "internal/kmx/modelaccess/copilot/auth.go", "internal/kmx/modelaccess/copilot/errors.go",
            "internal/kmx/modelaccess/copilot/store.go", "internal/kmx/modelaccess/copilot/transport.go",
            "internal/kmx/modelaccess/new.go",
            "cmd/kmx/model_commands.go", "cmd/kmx/mcp_commands.go", "cmd/kmx/agent_file_commands.go",
            "internal/kmx/app/agent_file_direct.go", "cmd/kmx/root.go", "cmd/kmx/main.go",
            "cmd/kmx/commands.go", "cmd/kmx/agent_run_commands.go", "cmd/kmx/agent_eval_commands.go",
            "internal/kmx/app/app.go", "internal/kmx/app/agent_file_extra.go",
            "internal/kmx/app/local_models.go", "internal/kmx/runtime/evaluation.go",
            "internal/kmx/app/testdata/instructions.md", "internal/kmx/orkaschema/fixtures/prompt.md",
            "testdata/sharedfixture.md", "sharedfixture.md", "pkg/new.go", "internal/kmx/new/new.go",
            "internal/kmx/agentfile-other/new.go", "go.mod", "go.sum", "go.work", "Makefile",
            ".goreleaser.yaml", "install.sh", "embed.go", "embed_test.go", "k8s/asset.yaml",
            "scripts/docs.md", "scripts/ci/orka-routing.py", ".github/workflows/ci.yml",
            ".github/actions/new/action.yml", "changes/code.py", "changes/nested/note.md",
            "charts/new.yaml", "unknown.md", "readme.md",
        ]
        for path, expected in [(p, "false") for p in safe] + [(p, "true") for p in unsafe]:
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.change(path, "changed")
                self.commit()
                self.assertEqual(self.route(), expected)
        self.change("docs/safe.md", "safe")
        self.change("internal/kmx/runtime/new.go", "unsafe")
        self.commit()
        self.assertEqual(self.route(), "true")

    def test_real_deletions_and_both_sides_of_renames(self):
        for old, new, expected in [
                ("internal/kmx/runtime/old.go", "docs/moved.md", "true"),
                ("docs/old.md", "internal/kmx/runtime/moved.go", "true"),
                ("docs/old.md", "docs/moved.md", "false")]:
            self.git("reset", "--hard", self.base)
            self.change(old, "same")
            source = self.commit()
            for rename in (False, True):
                with self.subTest(old=old, rename=rename):
                    self.git("reset", "--hard", source)
                    if rename:
                        target = self.root / new
                        target.parent.mkdir(parents=True, exist_ok=True)
                        (self.root / old).rename(target)
                    else:
                        (self.root / old).unlink()
                    self.commit()
                    self.assertEqual(self.route(base=source), expected if rename else
                                     ("false" if old.startswith("docs/") else "true"))

    def test_nul_diff_does_not_split_or_unquote_filenames(self):
        for path, expected in [
                ('docs/space name.md', "false"), ('docs/quote-\"é.md', "false"),
                ('docs/new\nline.md', "true"), ('docs/tab\tname.md', "true"),
                ('docs/back\\slash.md', "true"), ('scripts/quote-\"é.md', "true"),
                ('internal/kmx/runtime/new\nline.md', "true")]:
            with self.subTest(path=path):
                self.git("reset", "--hard", self.base)
                self.change(path, "changed")
                self.commit()
                self.assertEqual(self.route(), expected)

    def test_empty_missing_history_and_invalid_commits_default_run(self):
        self.assertEqual(self.route(), "true")
        for base, head in [("f" * 40, self.base), (self.base, "missing"),
                           ("--stat", self.base), ("0" * 40, self.base)]:
            with self.subTest(base=base, head=head):
                self.assertEqual(self.route(base, head), "true")
        result = subprocess.run([sys.executable, str(ROUTER)], cwd=self.root,
                                capture_output=True, text=True, timeout=10)
        self.assertEqual((result.returncode, result.stdout, result.stderr), (0, "true\n", ""))

    def test_malformed_or_failed_diff_never_authorizes_skip(self):
        # Git itself cannot emit these path records. Inject only its external output.
        self.assertTrue(ROUTER.exists(), "routing helper is not implemented")
        spec = importlib.util.spec_from_file_location("orka_routing", ROUTER)
        router = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(router)
        for data in (b"", b"docs/safe.md", b"docs/safe.md\0\0", b"/docs/safe.md\0",
                     b"docs/../sharedfixture.md\0", b"docs//safe.md\0", b"docs/\xff.md\0"):
            with self.subTest(data=data), mock.patch.object(router.subprocess, "run") as run:
                run.return_value = subprocess.CompletedProcess([], 0, data, b"")
                self.assertTrue(router.orka_required(self.base, self.base))
        with mock.patch.object(router.subprocess, "run", side_effect=OSError("private canary")):
            self.assertTrue(router.orka_required(self.base, self.base))


class ActionTests(unittest.TestCase):
    setUp = RoutingTests.setUp
    git = RoutingTests.git
    change = RoutingTests.change
    commit = RoutingTests.commit
    merge_head = RoutingTests.merge_head

    def test_current_merge_parent_is_authoritative(self):
        self.change("scripts/old.py", "unrelated base change")
        current = self.commit()
        self.change("docs/readme.md", "PR docs")
        self.commit()
        self.install_helper()
        self.assertEqual(self.action(base=self.base, head=self.merge_head(current)).get("orka_required"), "false")

    def install_helper(self, replacement=None):
        self.assertTrue(ROUTER.exists(), "routing helper is not implemented")
        target = self.root / "scripts/ci"
        target.mkdir(parents=True, exist_ok=True)
        (target / ROUTER.name).write_text(ROUTER.read_text() if replacement is None else replacement)
        if POLICY.exists():
            shutil.copyfile(POLICY, target / POLICY.name)

    def action(self, base=None, head=None):
        return test_check_mutations.ClassificationTests.classify(self, base=base, head=head)

    def test_helper_failure_invalid_output_or_missing_file_falls_back(self):
        self.change("docs/readme.md", "PR docs")
        self.commit()
        head = self.merge_head()
        for source in ("raise SystemExit(7)", 'print("false"); raise SystemExit(7)',
                       'print("unknown")', 'print("false\\nfalse")', 'print("false")'):
            with self.subTest(source=source):
                self.install_helper(source)
                got = self.action(head=head)
                self.assertEqual(got.get("orka_required"), "false" if source == 'print("false")' else "true")
                self.assertEqual({k: got[k] for k in ("docs_only", "scripts_changed", "kagent_changed")},
                                 {"docs_only": "true", "scripts_changed": "false", "kagent_changed": "false"})
        (self.root / "scripts/ci" / ROUTER.name).unlink()
        self.assertEqual(self.action(head=head).get("orka_required"), "true")

    def test_output_routes_only_the_independent_runtime_worker(self):
        action = yaml.safe_load((ROOT / ".github/actions/classify-change/action.yml").read_text())
        self.assertIn("orka_required", action["outputs"])
        jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text())["jobs"]
        self.assertIn("orka_required", jobs["classify-change"]["outputs"])
        self.assertIn("needs.classify-change.outputs.orka_required", jobs["e2e-orka-runtime"]["if"])
        self.assertNotIn("orka_required", jobs["e2e-kagent-lift"]["if"])


class BoundaryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory(prefix="kmx-orka-boundary-")
        cls.addClassCleanup(cls.tmp.cleanup)
        cls.root = pathlib.Path(cls.tmp.name)
        cls.binary = cls.root / "boundary"
        if BOUNDARY.exists():
            result = subprocess.run(["go", "build", "-o", str(cls.binary), str(BOUNDARY)],
                                    cwd=ROOT, capture_output=True, text=True, timeout=120)
            if result.returncode:
                raise AssertionError(result.stdout + result.stderr)

    def check(self, source=None, name="internal/kmx/agentfile/canary.go"):
        self.assertTrue(self.binary.exists(), "Go initialization boundary checker is not implemented")
        if source is None:
            root = ROOT
        else:
            root = self.root
            policy = root / "scripts/ci/orka-routing.json"
            policy.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(POLICY, policy)
            # Exact production leaves must exist, just as in the checked-out tree.
            for leaf in json.loads(POLICY.read_text())["safe_files"]:
                if leaf.endswith(".go"):
                    target = root / leaf
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text("package fixture\n")
            for prefix in json.loads(POLICY.read_text())["safe_prefixes"]:
                (root / prefix).mkdir(parents=True, exist_ok=True)
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(source)
        return subprocess.run([str(self.binary)], cwd=root, capture_output=True, text=True, timeout=10)

    def test_actual_current_tree_passes(self):
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_init_or_global_side_effect_fails_without_running_canary(self):
        for source in (
                'package fixture\nfunc init() {}',
                'package fixture\nvar _ = register()\nfunc register() int { return 1 }',
                'package fixture\nvar _ = func() int { return 1 }()',
                'package fixture\nvar _ = []int{register()}',
                'package fixture\nvar c chan int\nvar _ = <-c',
                'package fixture\nimport _ "unsafe"',
                'package fixture\nimport e "example.invalid/errors"\nvar _ = e.New("x")',
                'package fixture\nimport "errors"\nvar x = "dynamic"\nvar _ = errors.New(x)',
                'package fixture\nimport "os"\nvar _ = os.Getenv("private canary")',
                'package fixture\nimport "regexp"\nvar _ = regexp.MustCompile(dynamic())',
                'package fixture\nimport . "errors"\nvar _ = New("x")'):
            with self.subTest(source=source):
                result = self.check(source)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertNotIn("private canary", result.stdout + result.stderr)

    def test_passive_declarations_and_verified_literal_setup_pass(self):
        source = '''package fixture
import (e "errors"; r "regexp")
const label = "passive"
var names = []string{"a", "b"}
var states = map[string]int{"ok": 1}
var err = e.New("fixed")
var shape = r.MustCompile(`^fixed$`)
var deferred = func() { register() }
func register() {}
'''
        result = self.check(source)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_exact_safe_app_leaf_is_checked(self):
        result = self.check("package app\nvar _ = register()", "internal/kmx/app/agent_file.go")
        self.assertNotEqual(result.returncode, 0)

    def test_safe_app_tests_cannot_change_live_test_binary_initialization(self):
        for source in ("package app\nfunc init() {}", "package app\nvar _ = register()"):
            result = self.check(source, "internal/kmx/app/agent_file_test.go")
            self.assertNotEqual(result.returncode, 0, "safe app tests are compiled into Orka live tests")

    def test_passive_same_file_pointer_interface_assertion_is_allowed(self):
        result = self.check("package app\ntype runner struct{}\nvar _ any = (*runner)(nil)",
                            "internal/kmx/app/agent_file_test.go")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
