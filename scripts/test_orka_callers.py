#!/usr/bin/env python3
"""Real Go modules exercise reviewed references across the selected boundary."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts/ci/orka-callers.go"
MODULE = "github.com/kaimahi-agents/kaimahi"
CANARY = "CALLER_GUARD_PRIVATE_CANARY"
SAFE = "internal/kmx/app/unrelated.go"
CORE = "internal/kmx/app/core.go"
APP = MODULE + "/internal/kmx/app"


class CallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tools = tempfile.TemporaryDirectory(prefix="orka-caller-tool-")
        cls.addClassCleanup(cls.tools.cleanup)
        cls.binary = Path(cls.tools.name) / "check"
        subprocess.run(["go", "build", "-o", str(cls.binary), str(CHECKER)],
                       cwd=ROOT, check=True, capture_output=True, timeout=120)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="orka-caller-module-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.put("go.mod", f"module {MODULE}\n\ngo 1.26.0\n")
        self.put("cmd/kmx/main.go", f'package main\nimport "{MODULE}/internal/kmx/app"\nfunc main() {{ app.Orka() }}\n')
        self.put(CORE, "package app\nfunc Orka() {}\ntype Core struct { Flag, Other int }\nfunc (Core) Ping() {}\n")
        self.put(SAFE, "package app\nfunc Preview() {}\n")
        self.put("internal/kmx/app/unrelated_test.go", "package app\n")
        self.put("internal/kmx/unrelated/leaf.go", "package unrelated\nfunc Leaf() {}\n")
        self.policy = {"safe_prefixes": ["internal/kmx/unrelated/"],
                       "safe_files": [SAFE, "internal/kmx/app/unrelated_test.go"],
                       "safe_direct_markdown": ["changes/"]}
        self.put("scripts/ci/orka-routing.json", json.dumps(self.policy))
        self.manifest([])

    def put(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def manifest(self, packages, *, imports=None):
        # Reviewed package dependencies, independent of files and objects.
        self.put("scripts/ci/orka-callers.json", json.dumps({
            "version": 3, "imports": imports or [], "packages": packages}))

    def invoke(self, *args, extra_env=None):
        result = subprocess.run([str(self.binary), *args], cwd=self.root,
                                capture_output=True, text=True, timeout=120, check=False,
                                env={**os.environ, "GOWORK": "off", **(extra_env or {})})
        self.assertNotIn(CANARY, result.stdout + result.stderr)
        return result

    def test_ordinary_imports_and_safe_helpers_do_not_need_approvals(self):
        self.assertEqual(self.invoke().returncode, 0)
        self.put(CORE, "package app\nfunc Orka() { Preview() }\n")
        self.put(SAFE, f'package app\nimport ("strings"; "{MODULE}/internal/kmx/unrelated")\nfunc Preview() {{ unrelated.Leaf(); _ = strings.TrimSpace("ok") }}\n')
        self.assertEqual(self.invoke().returncode, 0)
        # Importing a mixed package's selected definition is still safe-to-safe.
        self.put(SAFE, "package app\nfunc Preview() {}\n")
        self.put("internal/kmx/unrelated/leaf.go",
                 f'package unrelated\nimport "{MODULE}/internal/kmx/app"\nfunc Leaf() {{ app.Preview() }}\n')
        result = self.invoke("--audit")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["packages"], [])
        self.assertEqual(self.invoke().returncode, 0)

    def test_new_owned_package_reference_fails_even_in_unimported_package(self):
        self.put("internal/kmx/unrelated/leaf.go", f'package unrelated\nimport alias "{MODULE}/internal/kmx/app"\nfunc Leaf() {{ alias.Orka() }}\n')
        result = self.invoke()
        self.assertEqual(result.returncode, 1)
        self.assertIn("new package dependency", result.stderr)
        self.assertIn(APP, result.stderr)
        self.assertIn("--audit", result.stderr)
        self.manifest([APP])
        self.assertEqual(self.invoke().returncode, 0)

    def test_owned_root_package_can_be_reviewed_like_subpackages(self):
        self.put("root.go", "package kaimahi\nfunc Root() {}\n")
        self.put(SAFE, f'package app\nimport root "{MODULE}"\nfunc Preview() {{ root.Root() }}\n')
        self.assertEqual(self.invoke().returncode, 1)
        self.manifest([MODULE])
        self.assertEqual(self.invoke().returncode, 0)

    def test_inbound_callers_do_not_need_outbound_approvals(self):
        self.put(CORE, f'package app\nimport renamed "{MODULE}/internal/kmx/unrelated"\nfunc Orka() {{ renamed.Leaf() }}\n')
        self.assertEqual(self.invoke().returncode, 0)


    def test_approved_package_allows_new_objects_files_and_calling_declarations(self):
        self.put(CORE, "package app\nfunc Orka() {}\nfunc privateProof() {}\ntype Core struct { Flag int }\nfunc (Core) Ping() {}\n")
        self.manifest([APP])
        for body in ("call := privateProof; call()", "c := Core{}; call := c.Ping; call()", "_ = Core{}.Flag"):
            with self.subTest(body=body):
                self.put(SAFE, f"package app\nfunc Preview() {{ {body} }}\n")
                self.assertEqual(self.invoke().returncode, 0)
        helper = "internal/kmx/app/new_safe_file.go"
        self.policy["safe_files"].append(helper)
        self.put("scripts/ci/orka-routing.json", json.dumps(self.policy))
        self.put(helper, "package app\nfunc NewHelper() { later := func() { Orka() }; later() }\n")
        self.assertEqual(self.invoke().returncode, 0)
        audited = self.invoke("--audit")
        self.assertEqual(audited.returncode, 0, audited.stderr)
        self.assertEqual(json.loads(audited.stdout)["packages"], [APP])

    def test_linked_app_tests_are_checked_but_other_test_packages_are_not(self):
        self.put("internal/kmx/unrelated/leaf_test.go", f'package unrelated\nimport ("testing"; "{MODULE}/internal/kmx/app")\nfunc TestLeaf(t *testing.T) {{ app.Orka() }}\n')
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated_test.go", 'package app\nimport "testing"\nfunc TestPreview(t *testing.T) { Orka() }\n')
        self.assertEqual(self.invoke().returncode, 1)
        self.manifest([APP])
        self.assertEqual(self.invoke().returncode, 0)

    def test_approvals_persist_after_removal_until_pruned(self):
        source = "package app\nfunc Preview() { Orka() }\n"
        self.put(SAFE, source)
        self.manifest([APP])
        self.assertEqual(self.invoke().returncode, 0)
        self.put(SAFE, "package app\nfunc Preview() {}\n")
        self.assertEqual(self.invoke().returncode, 0)
        self.put(SAFE, source)
        self.assertEqual(self.invoke().returncode, 0)
        self.manifest([])
        self.assertEqual(self.invoke().returncode, 1)

    def test_new_package_still_fails_when_an_existing_package_is_approved(self):
        self.manifest([APP])
        self.put("internal/kmx/newruntime/orka.go", "package newruntime\nfunc Run() {}\n")
        self.put(SAFE, f'package app\nimport "{MODULE}/internal/kmx/newruntime"\nfunc Preview() {{ Orka(); newruntime.Run() }}\n')
        result = self.invoke()
        self.assertEqual(result.returncode, 1)
        self.assertIn(MODULE + "/internal/kmx/newruntime", result.stderr)
        self.manifest([APP, MODULE + "/internal/kmx/newruntime"])
        self.assertEqual(self.invoke().returncode, 0)

    def test_audit_is_read_only_not_automatic_acceptance(self):
        self.put(SAFE, "package app\nfunc Preview() { Orka() }\n")
        path = self.root / "scripts/ci/orka-callers.json"
        before = path.read_bytes()
        result = self.invoke("--audit")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"version": 3, "imports": [], "packages": [APP]})
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(self.invoke().returncode, 1)

    def test_only_shared_http_startup_imports_need_approval(self):
        for package, object_name in (("net/http/pprof", "Handler"), ("expvar", "Get")):
            with self.subTest(package=package):
                self.manifest([])
                self.put(SAFE, f'package app\nimport startup "{package}"\nfunc Preview() {{ _ = startup.{object_name} }}\n')
                self.assertEqual(self.invoke().returncode, 1)
                self.manifest([], imports=[{"file": SAFE, "path": package}])
                self.assertEqual(self.invoke().returncode, 0)
                self.put(SAFE, "package app\nfunc Preview() {}\n")
                self.assertEqual(self.invoke().returncode, 0)

    def test_physical_source_provenance_cannot_be_remapped(self):
        for directive in ("//line core.go:1", "/*line core.go:1*/", "//line alien-source.txt:1"):
            with self.subTest(directive=directive):
                self.put(SAFE, "package app\n" + directive + "\nfunc Preview() { Orka() }\n")
                self.assertEqual(self.invoke("--audit").returncode, 1)
        self.put(SAFE, "package app\nfunc Preview() {}\n")
        self.put(CORE, "package app\n//line unrelated.go:1\nfunc Orka() {}\n")
        self.assertEqual(self.invoke("--audit").returncode, 1)

    def test_trimpath_export_positions_map_to_owned_definitions(self):
        self.put("internal/kmx/unrelated/leaf.go", f'package unrelated\nimport "{MODULE}/internal/kmx/app"\nfunc Leaf() {{ app.Orka() }}\n')
        result = self.invoke("--audit", extra_env={"GOFLAGS": "-trimpath"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["packages"], [APP])

    def test_missing_production_export_fails_closed(self):
        real_go = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip()) / "bin/go"
        wrapper = self.root / "tools/go"
        self.put("tools/go", f'''#!/usr/bin/python3
import json,subprocess,sys
p=subprocess.run([{str(real_go)!r},*sys.argv[1:]],capture_output=True,text=True)
if p.returncode:
 sys.stderr.write(p.stderr);raise SystemExit(p.returncode)
decoder=json.JSONDecoder();data=p.stdout
while data.strip():
 obj,end=decoder.raw_decode(data.lstrip());data=data.lstrip()[end:]
 if obj.get("ImportPath")=={MODULE + "/internal/kmx/app"!r}:obj.pop("Export",None)
 print(json.dumps(obj))
''')
        wrapper.chmod(0o700)
        self.assertEqual(self.invoke(extra_env={"PATH": str(wrapper.parent) + os.pathsep + os.environ["PATH"]}).returncode, 1)

    def test_aliases_and_nested_modules_fail_closed(self):
        path = self.root / "internal/kmx/unrelated/leaf.go"
        path.unlink()
        path.symlink_to(self.root / CORE)
        self.assertEqual(self.invoke().returncode, 1)
        path.unlink()
        self.put("internal/kmx/unrelated/leaf.go", "package unrelated\n")
        self.put("internal/kmx/unrelated/nested/go.mod", "module example.invalid/nested\n\ngo 1.26.0\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_missing_malformed_and_invalid_approvals_fail(self):
        path = self.root / "scripts/ci/orka-callers.json"
        for content in (None, "not-json", '{}', '{"version": 2, "imports": [], "edges": []}',
                        '{"version": 3}', '{"version": 3, "imports": [], "packages": [], "extra": 1}',
                        json.dumps({"version": 3, "imports": [], "packages": ["../x"]}),
                        json.dumps({"version": 3, "imports": [], "packages": [APP, APP]}),
                        json.dumps({"version": 3, "imports": [], "packages": ["example.invalid/external"]}),
                        json.dumps({"version": 3, "imports": [{"file": SAFE, "path": "fmt"}], "packages": []})):
            with self.subTest(content=content):
                if content is None:
                    path.unlink(missing_ok=True)
                else:
                    path.write_text(content)
                self.assertEqual(self.invoke().returncode, 1)

    def test_type_errors_are_private_and_no_initializers_execute(self):
        marker = self.root / "executed"
        self.put(CORE, f'package app\nimport "os"\nfunc init() {{ os.WriteFile({json.dumps(str(marker))}, []byte("{CANARY}"), 0600) }}\nfunc Orka() {{}}\n')
        self.assertEqual(self.invoke().returncode, 0)
        self.assertFalse(marker.exists())
        self.put(SAFE, f'package app\nfunc Preview() {{ var value int = "{CANARY}"; _ = value }}\n')
        compiler = subprocess.run(["go", "list", "-deps", "-export", "./..."], cwd=self.root,
                                  capture_output=True, text=True, timeout=120, check=False)
        self.assertNotEqual(compiler.returncode, 0)
        self.assertIn(CANARY, compiler.stderr)
        self.assertEqual(self.invoke().returncode, 1)
        self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
