"""Offline package contracts; fake-engine tests are not Copilot qualification."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("copilot_package", HERE / "package.py")
package = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(package)


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="plugin fixtures ")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / "selected repository"
        self.root.mkdir()
        self.engine = self.base / "engine"
        self.engine.write_bytes(b"fixture engine, never executed by the packager\n")
        self.digest = hashlib.sha256(self.engine.read_bytes()).hexdigest()
        self.out = self.base / "plugin"

    def build(self, **changes):
        options = dict(binary=self.engine, sha256=self.digest, target="linux/amd64",
                       root=self.root, output=self.out, version="0.1.0")
        options.update(changes)
        return package.build(**options)

    def test_fixed_root_and_bundled_engine(self):
        self.build()
        manifest = json.loads((self.out / "plugin.json").read_text())
        self.assertEqual(set(manifest), {"$schema", "name", "version", "description"})
        self.assertEqual(manifest["name"], "kmx-agent-builder")
        config = json.loads((self.out / "mcp.json").read_text())
        self.assertEqual(set(config), {"$schema", "mcpServers"})
        self.assertEqual(config["mcpServers"]["kmx-agent-builder"], {
            "type": "stdio", "command": "./bin/kmx",
            "args": ["mcp", "--root", str(self.root.resolve())]})
        self.assertEqual((self.out / "bin/kmx").read_bytes(), self.engine.read_bytes())
        metadata = json.loads((self.out / "engine.json").read_text())
        self.assertEqual(metadata["sha256"], self.digest)
        self.assertEqual(metadata["platform"], "linux/amd64")
        if os.name != "nt":
            self.assertEqual((self.out / "bin/kmx").stat().st_mode & 0o777, 0o700)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_windows_command_is_explicit(self):
        self.build(target="windows/amd64")
        config = json.loads((self.out / "mcp.json").read_text())
        self.assertEqual(config["mcpServers"]["kmx-agent-builder"]["command"], "./bin/kmx.exe")
        self.assertTrue((self.out / "bin/kmx.exe").is_file())

    def test_digest_mismatch_has_no_output(self):
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            self.build(sha256="0" * 64)
        self.assertFalse(self.out.exists())

    def test_refuses_existing_output_without_change(self):
        self.out.mkdir()
        marker = self.out / "keep"
        marker.write_text("unchanged")
        with self.assertRaisesRegex(ValueError, "output already exists"):
            self.build()
        self.assertEqual(marker.read_text(), "unchanged")
        self.assertEqual(list(self.out.iterdir()), [marker])

    def test_refuses_invalid_inputs_without_output(self):
        for changes, reason in [({"sha256": "bad"}, "64 lowercase hex"),
                                ({"target": "windows/arm64"}, "unsupported platform"),
                                ({"version": "bad/version"}, "version"),
                                ({"root": self.base / "missing"}, "root must be an existing directory"),
                                ({"root": Path(self.root.anchor)}, "filesystem root"),
                                ({"root": self.base}, "output must be outside")]:
            with self.subTest(changes=changes):
                with self.assertRaisesRegex(ValueError, reason):
                    self.build(**changes)
                self.assertFalse(self.out.exists())

    def test_refuses_source_symlink(self):
        link = self.base / "engine-link"
        try:
            link.symlink_to(self.engine)
        except OSError:
            self.skipTest("symlink creation not permitted by this Windows account")
        with self.assertRaisesRegex(ValueError, "regular non-symlink"):
            self.build(binary=link)
        self.assertFalse(self.out.exists())

    def test_refuses_source_directory(self):
        with self.assertRaisesRegex(ValueError, "regular non-symlink"):
            self.build(binary=self.root)
        self.assertFalse(self.out.exists())

    def test_macos_without_python_xattr_uses_system_attribute_names(self):
        clean = subprocess.CompletedProcess([], 0, b"", b"")
        with mock.patch.object(package.sys, "platform", "darwin"), \
             mock.patch.object(package.os, "getxattr", None, create=True), \
             mock.patch("subprocess.run", return_value=clean) as inspect:
            self.build()
        inspect.assert_called_once_with(["/usr/bin/xattr", str(self.engine.resolve())],
                                        capture_output=True, timeout=5, check=False)

    def test_refuses_quarantined_macos_input_without_removing_attribute(self):
        quarantined = subprocess.CompletedProcess([], 0, b"com.apple.quarantine\n", b"")
        with mock.patch.object(package.sys, "platform", "darwin"), \
             mock.patch("subprocess.run", return_value=quarantined) as inspect:
            with self.assertRaisesRegex(ValueError, "macOS quarantine"):
                self.build()
            inspect.assert_called_once_with(["/usr/bin/xattr", str(self.engine.resolve())],
                                            capture_output=True, timeout=5, check=False)
        self.assertFalse(self.out.exists())

    def test_macos_inspection_failure_is_not_clean_input(self):
        for failure in [subprocess.CompletedProcess([], 1, b"", b"fixture error"),
                        FileNotFoundError("missing system xattr"),
                        subprocess.TimeoutExpired("xattr", 5)]:
            with self.subTest(failure=type(failure).__name__), \
                 mock.patch.object(package.sys, "platform", "darwin"):
                options = {"side_effect": failure} if isinstance(failure, Exception) else {"return_value": failure}
                with mock.patch("subprocess.run", **options):
                    with self.assertRaisesRegex(ValueError, "macOS.*(?:inspect|attribute)|inspect macOS"):
                        self.build()
                self.assertFalse(self.out.exists())

    def test_failed_skill_copy_removes_only_new_output(self):
        with mock.patch.object(package.shutil, "copyfile", side_effect=OSError("fixture copy failure")):
            with self.assertRaisesRegex(OSError, "fixture copy failure"):
                self.build()
        self.assertFalse(self.out.exists())
        self.assertEqual(self.engine.read_bytes(), b"fixture engine, never executed by the packager\n")
        self.assertEqual(list(self.root.iterdir()), [])

    def test_refuses_dangling_output_link(self):
        try:
            self.out.symlink_to(self.base / "absent", target_is_directory=True)
        except OSError:
            self.skipTest("symlink creation not permitted by this Windows account")
        with self.assertRaisesRegex(ValueError, "output already exists"):
            self.build()
        self.assertTrue(self.out.is_symlink())

    def test_refuses_special_input_before_opening(self):
        if not hasattr(os, "mkfifo"):
            self.skipTest("FIFO unavailable on this platform")
        fifo = self.base / "engine-fifo"
        os.mkfifo(fifo)
        with self.assertRaisesRegex(ValueError, "regular non-symlink"):
            self.build(binary=fifo)
        self.assertFalse(self.out.exists())

    def test_source_swap_to_fifo_cannot_bypass_regular_file_check(self):
        if not hasattr(os, "mkfifo"):
            self.skipTest("FIFO unavailable on this platform")
        native_open = os.open
        def swap(path, flags, *args, **kwargs):
            if Path(path) == self.engine:
                self.engine.unlink()
                os.mkfifo(self.engine)
                self.assertTrue(flags & os.O_NONBLOCK)
            return native_open(path, flags, *args, **kwargs)
        with mock.patch.object(package.os, "open", side_effect=swap):
            with self.assertRaisesRegex(ValueError, "engine changed|regular file"):
                self.build()
        self.assertFalse(self.out.exists())

    def test_packaging_never_executes_supplied_engine(self):
        def inspect_only(command, **kwargs):
            self.assertEqual(command, ["/usr/bin/xattr", str(self.engine.resolve())])
            self.assertEqual(kwargs, {"capture_output": True, "timeout": 5, "check": False})
            return subprocess.CompletedProcess(command, 0, b"", b"")
        with mock.patch("subprocess.run", side_effect=inspect_only), \
             mock.patch("subprocess.Popen", side_effect=AssertionError("must not execute")):
            self.build()

    def test_skill_preserves_contract_and_defers_explain(self):
        self.build()
        skill = (self.out / "skills/kmx-agent-builder/SKILL.md").read_text()
        for required in ["schemaVersion", "fullPass", "selection.scope", "reasonCode",
                         "not_requested", "assertion", "unknown", "untrusted", "Explain",
                         "Model", "Examples", "selected root", "Do not"]:
            self.assertIn(required, skill)
        prose = " ".join(skill.split())
        self.assertIn("full pass only when fullPass is true, state is complete and verdict is pass", prose)
        self.assertIn("A subset may have verdict pass but fullPass false.", prose)
        self.assertIn("Missing/unknown versions, states or verdicts are not passes.", prose)
        self.assertNotIn("kmx test explain --file", skill)
        self.assertNotIn("PLUGIN_ROOT", skill)
        self.assertFalse(any(p.suffix in {".py", ".js", ".ps1", ".sh"}
                             for p in self.out.rglob("*")))


class ProcessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="native fake kmx ")
        cls.addClassCleanup(cls.temp.cleanup)
        cls.engine = Path(cls.temp.name) / ("fake-kmx.exe" if os.name == "nt" else "fake-kmx")
        subprocess.run(["go", "build", "-o", str(cls.engine),
                        str(HERE / "testdata/fake-kmx/main.go")], check=True,
                       env={**os.environ, "CGO_ENABLED": "0", "GOWORK": "off"},
                       capture_output=True, timeout=120)
        cls.digest = hashlib.sha256(cls.engine.read_bytes()).hexdigest()
        cls.target = subprocess.check_output(["go", "env", "GOOS", "GOARCH"],
                                             text=True).strip().replace("\n", "/")

    def fixture(self, scenario="pass", relocate=False, tool="test"):
        with tempfile.TemporaryDirectory(prefix="plugin fixture ") as temp:
            base = Path(temp)
            root = base / "selected repository"
            root.mkdir()
            output = base / "built plugin"
            package.build(binary=self.engine, sha256=self.digest, target=self.target,
                          root=root, output=output, version="0.1.0")
            if relocate:
                moved = base / "copilot installed cache"
                output.rename(moved)
                output = moved
            config = json.loads((output / "mcp.json").read_text())
            server = config["mcpServers"]["kmx-agent-builder"]
            # Agent Plugins resolves ./ commands against PLUGIN_ROOT, not cwd.
            command = [str(output / server["command"]), *server["args"]]
            requests = [
                {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}},
                {"jsonrpc": "2.0", "method": "notifications/initialized"},
                {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
                {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": tool, "arguments": {"schemaVersion": 1}}},
            ]
            process = subprocess.run(command, cwd=base, input="".join(json.dumps(r) + "\n" for r in requests),
                                     capture_output=True, text=True, timeout=15,
                                     env={**os.environ, "EXPECTED_ROOT": str(root.resolve()),
                                          "KMX_FIXTURE_CASE": scenario, "UNRELATED_SECRET": "fixture-canary"})
            self.assertEqual(process.returncode, 0, process.stderr)
            self.assertEqual(process.stderr, "")
            self.assertNotIn("fixture-canary", process.stdout)
            replies = [json.loads(line) for line in process.stdout.splitlines()]
            self.assertEqual([r["id"] for r in replies], [1, 2, 3])
            self.assertEqual(replies[0]["result"]["serverInfo"]["name"], "fake-kmx-fixture")
            # Authoring mirrors the proposed MCP schemas; preview inputs are not yet frozen.
            tools = replies[1]["result"]["tools"]
            self.assertEqual([t["name"] for t in tools], ["create", "edit", "run", "test"])
            for author in tools[:2]:
                schema = author["inputSchema"]
                expected = ["path", "content"] + (["expectedRevision"] if author["name"] == "edit" else []) + ["schemaVersion"]
                self.assertEqual(schema["required"], expected)
                self.assertEqual(schema["properties"]["schemaVersion"], {"type": "integer", "enum": [1]})
                self.assertEqual(schema["properties"]["path"]["maxLength"], 256)
                self.assertEqual(schema["properties"]["content"]["maxLength"], 256 << 10)
                self.assertEqual(schema["additionalProperties"], {"not": {}})
            return replies[2]

    def test_native_packaged_process_can_relocate_to_plugin_cache(self):
        result = self.fixture(relocate=True)["result"]
        self.assertEqual(json.loads(result["content"][0]["text"]), result["structuredContent"])
        self.assertTrue(result["structuredContent"]["fullPass"])

    def test_result_contract_v1_scenarios(self):
        required = {"schemaVersion", "kind", "operation", "file", "modelRoute", "selection",
                    "state", "verdict", "fullPass", "reasonCode", "reason", "examples", "retention"}
        for scenario, state, verdict, full, scope in [
            ("pass", "complete", "pass", True, "full"),
            ("fail", "complete", "fail", False, "full"),
            ("unknown", "complete", "unknown", False, "full"),
            ("subset", "complete", "pass", False, "subset"),
            ("cancelled", "cancelled", "unknown", False, "full"),
        ]:
            with self.subTest(scenario=scenario):
                r = self.fixture(scenario)["result"]["structuredContent"]
                self.assertEqual(set(r), required)
                self.assertEqual((r["state"], r["verdict"], r["fullPass"], r["selection"]["scope"]),
                                 (state, verdict, full, scope))
                self.assertEqual(r["schemaVersion"], 1)
                self.assertEqual(r["kind"], "kmx.agent-file-preview")
                self.assertRegex(r["file"]["revision"], r"^sha256:[a-f0-9]{64}$")
                self.assertEqual(r["modelRoute"], {"provider": "fake", "model": "fake"})
                self.assertEqual(r["examples"][0]["id"], "greeting")
                assertion = r["examples"][0]["assertions"][0]
                self.assertEqual(assertion["id"], "greeting-text")
                expected_reason = {"pass": "assertion_satisfied", "fail": "assertion_failed",
                                   "unknown": "judge_unknown", "subset": "assertion_satisfied",
                                   "cancelled": "not_run"}[scenario]
                self.assertEqual(assertion["reasonCode"], expected_reason)
                self.assertEqual(r["retention"], {"status": "not_requested"})
                self.assertNotIn("answerExcerpt", json.dumps(r))

    def test_fixture_has_no_explain_tool(self):
        self.assertEqual(self.fixture(tool="explain")["error"]["code"], -32601)


if __name__ == "__main__":
    unittest.main()
