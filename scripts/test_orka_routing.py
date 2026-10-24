#!/usr/bin/env python3
"""Orka package-initialization tripwires (independent of CI scheduling)."""
import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
POLICY = ROOT / "scripts/ci/orka-routing.json"
BOUNDARY = ROOT / "scripts/ci/orka-boundary.go"


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
