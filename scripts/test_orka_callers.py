#!/usr/bin/env python3
"""Real Go modules prove unrelated/live caller drift fails closed."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts/ci/orka-callers.go"
MODULE = "github.com/kaimahi-agents/kaimahi"
CANARY = "CALLER_GUARD_PRIVATE_CANARY"


class CallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tools = tempfile.TemporaryDirectory(prefix="orka-caller-tool-")
        cls.addClassCleanup(cls.tools.cleanup)
        cls.binary = Path(cls.tools.name) / "check"
        if CHECKER.exists():
            subprocess.run(["go", "build", "-o", str(cls.binary), str(CHECKER)],
                           cwd=ROOT, check=True, capture_output=True, timeout=120)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="orka-caller-module-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.put("go.mod", f"module {MODULE}\n\ngo 1.26.0\n")
        self.put("cmd/kmx/main.go", f'package main\nimport "{MODULE}/internal/kmx/app"\nfunc main() {{ app.Orka() }}\n')
        self.put("internal/kmx/app/core.go", "package app\nfunc Orka() {}\ntype Core struct{}\nfunc (Core) Ping() {}\n")
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() {}\n")
        self.put("internal/kmx/app/unrelated_test.go", "package app\n")
        self.put("internal/kmx/unrelated/leaf.go", "package unrelated\nfunc Leaf() {}\n")
        self.policy = {"safe_prefixes": ["internal/kmx/unrelated/"],
                       "safe_files": ["internal/kmx/app/unrelated.go", "internal/kmx/app/unrelated_test.go"],
                       "safe_direct_markdown": ["changes/"]}
        self.put("scripts/ci/orka-routing.json", json.dumps(self.policy))
        self.manifest([])

    def put(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def manifest(self, edges, *, from_run=None, to_run=None, imports=None):
        # Hand-checked fixture declarations, not values derived from the checker.
        edges = [{"caller_object": MODULE + "/internal/kmx/app." +
                  ("Orka" if e["caller"].endswith("/core.go") else "Preview"), **e} for e in edges]
        self.put("scripts/ci/orka-callers.json", json.dumps({"version": 1, "imports": imports or [], "edges": edges,
                 "safe_from_run": from_run or [], "safe_to_run": to_run or []}))

    def invoke(self, *args, extra_env=None):
        self.assertTrue(self.binary.exists(), "automatic caller boundary checker not implemented")
        result = subprocess.run([str(self.binary), *args], cwd=self.root,
                                capture_output=True, text=True, timeout=120,
                                env={**os.environ, "GOWORK": "off", **(extra_env or {})})
        self.assertNotIn(CANARY, result.stdout + result.stderr)
        return result

    def test_no_boundary_edges_passes_and_harmless_edit_stays_green(self):
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/core.go", "package app\nfunc Orka() { _ = 12 + 3 }\n")
        self.assertEqual(self.invoke().returncode, 0)

    def test_new_unrelated_caller_of_relevant_code_fails_even_unimported_package(self):
        self.put("internal/kmx/unrelated/leaf.go", f'package unrelated\nimport alias "{MODULE}/internal/kmx/app"\nfunc Leaf() {{ alias.Orka() }}\n')
        self.manifest([], imports=[{"file": "internal/kmx/unrelated/leaf.go", "path": MODULE + "/internal/kmx/app"}])
        self.assertEqual(self.invoke().returncode, 1)

    def test_new_runtime_caller_of_unrelated_code_fails(self):
        self.put("internal/kmx/app/core.go", f'package app\nimport renamed "{MODULE}/internal/kmx/unrelated"\nfunc Orka() {{ renamed.Leaf() }}\n')
        self.assertEqual(self.invoke().returncode, 1)

    def test_same_package_private_function_reference_fails(self):
        self.put("internal/kmx/app/core.go", "package app\nfunc Orka() {}\nfunc privateProof() {}\n")
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { callback := privateProof; callback() }\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_bound_method_value_reference_fails(self):
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { value := Core{}; call := value.Ping; call() }\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_linked_app_test_caller_fails(self):
        self.put("internal/kmx/app/unrelated_test.go", 'package app\nimport "testing"\nfunc TestPreview(t *testing.T) { Orka() }\n')
        self.manifest([], imports=[{"file": "internal/kmx/app/unrelated_test.go", "path": "testing"}])
        self.assertEqual(self.invoke().returncode, 1)

    def test_other_packages_test_only_callers_are_not_native_binary_callers(self):
        self.put("internal/kmx/unrelated/leaf_test.go", f'package unrelated\nimport ("testing"; "{MODULE}/internal/kmx/app")\nfunc TestLeaf(t *testing.T) {{ app.Orka() }}\n')
        self.assertEqual(self.invoke().returncode, 0)

    def test_reviewed_edge_passes_and_deleted_edge_cannot_stay_authorized(self):
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka() }\n")
        edge = {"caller": "internal/kmx/app/unrelated.go", "callee": "internal/kmx/app/core.go",
                "object": MODULE + "/internal/kmx/app.Orka"}
        self.manifest([edge], to_run=["internal/kmx/app/unrelated.go"])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() {}\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_audit_is_explicit_read_only_metadata_not_automatic_acceptance(self):
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka() }\n")
        path = self.root / "scripts/ci/orka-callers.json"
        before = path.read_bytes()
        result = self.invoke("--audit")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"version": 1, "imports": [], "edges": [
            {"caller": "internal/kmx/app/unrelated.go", "caller_object": MODULE + "/internal/kmx/app.Preview",
             "callee": "internal/kmx/app/core.go", "object": MODULE + "/internal/kmx/app.Orka"}],
             "safe_from_run": [], "safe_to_run": ["internal/kmx/app/unrelated.go"]})
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(self.invoke().returncode, 1)

    def test_new_caller_function_in_same_safe_file_fails(self):
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka() }\n")
        self.manifest([{"caller": "internal/kmx/app/unrelated.go", "callee": "internal/kmx/app/core.go",
                        "object": MODULE + "/internal/kmx/app.Orka"}],
                      to_run=["internal/kmx/app/unrelated.go"])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka() }\nfunc NewCaller() { Orka() }\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_new_closure_caller_in_same_function_fails(self):
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka() }\n")
        self.manifest([{"caller": "internal/kmx/app/unrelated.go", "callee": "internal/kmx/app/core.go",
                        "object": MODULE + "/internal/kmx/app.Orka"}],
                      to_run=["internal/kmx/app/unrelated.go"])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka(); later := func() { Orka() }; later() }\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_new_object_in_already_authorized_target_file_fails(self):
        self.put("internal/kmx/app/core.go", "package app\nfunc Orka() {}\nfunc AnotherProof() {}\n")
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { Orka(); AnotherProof() }\n")
        self.manifest([{"caller": "internal/kmx/app/unrelated.go", "callee": "internal/kmx/app/core.go",
                        "object": MODULE + "/internal/kmx/app.Orka"}],
                      to_run=["internal/kmx/app/unrelated.go"])
        self.assertEqual(self.invoke().returncode, 1)

    def test_safe_to_safe_extension_of_runtime_dependency_fails(self):
        self.put("internal/kmx/app/core.go", "package app\nfunc Orka() { Preview() }\n")
        self.manifest([{"caller": "internal/kmx/app/core.go", "callee": "internal/kmx/app/unrelated.go",
                        "object": MODULE + "/internal/kmx/app.Preview"}],
                      from_run=["internal/kmx/app/unrelated.go"])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go",
                 f'package app\nimport "{MODULE}/internal/kmx/unrelated"\nfunc Preview() {{ unrelated.Leaf() }}\n')
        # Approve only the import; stale reachability must still fail.
        data = json.loads((self.root / "scripts/ci/orka-callers.json").read_text())
        data["imports"].append({"file": "internal/kmx/app/unrelated.go", "path": MODULE + "/internal/kmx/unrelated"})
        self.put("scripts/ci/orka-callers.json", json.dumps(data))
        self.assertEqual(self.invoke().returncode, 1)

    def test_safe_to_safe_extension_of_core_caller_fails(self):
        self.put("internal/kmx/unrelated/leaf.go",
                 f'package unrelated\nimport "{MODULE}/internal/kmx/app"\nfunc Leaf() {{ app.Orka() }}\n')
        self.manifest([{"caller": "internal/kmx/unrelated/leaf.go",
                        "caller_object": MODULE + "/internal/kmx/unrelated.Leaf",
                        "callee": "internal/kmx/app/core.go", "object": MODULE + "/internal/kmx/app.Orka"}],
                      to_run=["internal/kmx/unrelated/leaf.go"],
                      imports=[{"file": "internal/kmx/unrelated/leaf.go", "path": MODULE + "/internal/kmx/app"}])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go",
                 f'package app\nimport "{MODULE}/internal/kmx/unrelated"\nfunc Preview() {{ unrelated.Leaf() }}\n')
        # Approve only the import; stale reachability must still fail.
        data = json.loads((self.root / "scripts/ci/orka-callers.json").read_text())
        data["imports"].append({"file": "internal/kmx/app/unrelated.go", "path": MODULE + "/internal/kmx/unrelated"})
        self.put("scripts/ci/orka-callers.json", json.dumps(data))
        self.assertEqual(self.invoke().returncode, 1)

    def test_named_struct_fields_have_distinct_stable_keys(self):
        self.put("internal/kmx/app/core.go", "package app\nfunc Orka() {}\ntype Core struct { Flag, Other int }\n")
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { c := Core{}; _ = c.Flag }\n")
        audited = self.invoke("--audit")
        self.assertEqual(audited.returncode, 0, audited.stderr)
        self.assertEqual({e["object"] for e in json.loads(audited.stdout)["edges"]},
                         {MODULE + "/internal/kmx/app.Core", MODULE + "/internal/kmx/app.Core.Flag"})

    def test_methods_include_the_declared_receiver_in_the_object_key(self):
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() { c := Core{}; call := c.Ping; call() }\n")
        audited = self.invoke("--audit")
        self.assertEqual(audited.returncode, 0, audited.stderr)
        self.assertIn(MODULE + "/internal/kmx/app.Core.Ping",
                      {e["object"] for e in json.loads(audited.stdout)["edges"]})

    def test_unmapped_export_definition_position_fails_closed(self):
        self.put("internal/kmx/app/alien-source.txt", "not a compiled Go source file")
        self.put("internal/kmx/app/core.go", "package app\n//line alien-source.txt:1\nfunc Orka() {}\n")
        # The source is compilable; rejecting it must be our position-mapping gate.
        compiled = subprocess.run(["go", "list", "-deps", "-export", "./..."], cwd=self.root,
                                  capture_output=True, text=True, timeout=120)
        self.assertEqual(compiled.returncode, 0, compiled.stderr)
        self.assertEqual(self.invoke().returncode, 1)

    def test_trimpath_export_positions_map_to_owned_definitions(self):
        self.put("internal/kmx/unrelated/leaf.go",
                 f'package unrelated\nimport "{MODULE}/internal/kmx/app"\nfunc Leaf() {{ app.Orka() }}\n')
        result = self.invoke("--audit", extra_env={"GOFLAGS": "-trimpath"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn({"caller": "internal/kmx/unrelated/leaf.go",
                       "caller_object": MODULE + "/internal/kmx/unrelated.Leaf",
                       "callee": "internal/kmx/app/core.go", "object": MODULE + "/internal/kmx/app.Orka"},
                      json.loads(result.stdout)["edges"])

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

    def test_source_symlink_cannot_change_the_routing_identity(self):
        path = self.root / "internal/kmx/unrelated/leaf.go"
        path.unlink()
        path.symlink_to(self.root / "internal/kmx/app/core.go")
        self.assertEqual(self.invoke().returncode, 1)

    def test_nested_module_cannot_silently_escape_the_source_graph(self):
        self.put("internal/kmx/unrelated/nested/go.mod", "module example.invalid/nested\n\ngo 1.26.0\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_missing_malformed_and_unknown_version_manifest_fail(self):
        path = self.root / "scripts/ci/orka-callers.json"
        for content in (None, "not-json", '{}', '{"version": 2, "edges": []}',
                        '{"version": 1}', '{"version": 1, "edges": [{"caller":"../x","callee":"x"}]}'):
            with self.subTest(content=content):
                if content is None:
                    path.unlink(missing_ok=True)
                else:
                    path.write_text(content)
                self.assertEqual(self.invoke().returncode, 1)

    def test_named_standard_import_with_startup_registration_fails(self):
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go",
                 'package app\nimport pprof "net/http/pprof"\nfunc Preview() { _ = pprof.Handler }\n')
        self.assertEqual(self.invoke().returncode, 1)
        self.manifest([], imports=[{"file": "internal/kmx/app/unrelated.go", "path": "net/http/pprof"}])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/unrelated.go", "package app\nfunc Preview() {}\n")
        self.assertEqual(self.invoke().returncode, 1)

    def test_line_directive_cannot_hide_caller_as_existing_core_file(self):
        for directive in ("//line core.go:1", "/*line core.go:1*/"):
            with self.subTest(directive=directive):
                self.put("internal/kmx/app/unrelated.go",
                         "package app\n" + directive + "\nfunc NewCaller() { _ = Core{} }\n")
                self.assertEqual(self.invoke().returncode, 1)

    def test_line_directive_cannot_relabel_imported_definition_as_safe(self):
        self.put("internal/kmx/app/core.go", "package app\n//line unrelated.go:1\nfunc Orka() {}\n")
        self.assertEqual(self.invoke("--audit").returncode, 1)

    def test_second_multi_value_initializer_has_a_distinct_caller(self):
        self.put("internal/kmx/unrelated/leaf.go", "package unrelated\nfunc Leaf() int { return 1 }\n")
        self.put("internal/kmx/app/core.go",
                 f'package app\nimport "{MODULE}/internal/kmx/unrelated"\nvar a, b = unrelated.Leaf(), 1\nfunc Orka() {{}}\n')
        self.manifest([{"caller": "internal/kmx/app/core.go",
                        "caller_object": MODULE + "/internal/kmx/app.a," + MODULE + "/internal/kmx/app.b/value0",
                        "callee": "internal/kmx/unrelated/leaf.go", "object": MODULE + "/internal/kmx/unrelated.Leaf"}],
                      from_run=["internal/kmx/unrelated/leaf.go"])
        self.assertEqual(self.invoke().returncode, 0)
        self.put("internal/kmx/app/core.go",
                 f'package app\nimport "{MODULE}/internal/kmx/unrelated"\nvar a, b = unrelated.Leaf(), unrelated.Leaf()\nfunc Orka() {{}}\n')
        self.assertEqual(self.invoke().returncode, 1)

    def test_type_errors_fail_without_source_or_initializer_execution(self):
        marker = self.root / "executed"
        self.put("internal/kmx/app/core.go", f'package app\nimport "os"\nfunc init() {{ os.WriteFile({json.dumps(str(marker))}, []byte("{CANARY}"), 0600) }}\nfunc Orka() {{}}\n')
        self.assertEqual(self.invoke().returncode, 0)
        self.assertFalse(marker.exists())
        self.put("internal/kmx/app/unrelated.go", f'package app\nfunc Preview() {{ missing("{CANARY}") }}\n')
        self.assertEqual(self.invoke().returncode, 1)
        self.assertFalse(marker.exists())


if __name__ == "__main__":
    unittest.main()
