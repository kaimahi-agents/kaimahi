"""Offline packaging and real bundled MCP; not installed Copilot qualification."""
from __future__ import annotations

import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
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

    def test_refuses_host_placeholders_in_resolved_root(self):
        for index, token in enumerate(("${PLUGIN_ROOT}", "${PLUGIN_DATA}")):
            with self.subTest(token=token):
                selected = self.base / ("repository-" + token)
                selected.mkdir()
                output = self.base / f"plugin-{index}"
                with self.assertRaisesRegex(ValueError, "root cannot contain plugin placeholders"):
                    self.build(root=selected, output=output)
                self.assertFalse(output.exists())
                self.assertEqual(list(selected.iterdir()), [])

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


@unittest.skipIf(os.name == "nt", "main still blocks native Windows CLI; packaging checks run above")
class ProcessTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="native kmx ")
        cls.addClassCleanup(cls.temp.cleanup)
        cls.engine = Path(cls.temp.name) / "kmx"
        subprocess.run(["go", "build", "-o", str(cls.engine), "./cmd/kmx"],
                       cwd=HERE.parents[1], check=True,
                       env={**os.environ, "CGO_ENABLED": "0", "GOWORK": "off"},
                       capture_output=True, timeout=120)
        cls.digest = hashlib.sha256(cls.engine.read_bytes()).hexdigest()
        cls.target = subprocess.check_output(["go", "env", "GOOS", "GOARCH"],
                                             text=True).strip().replace("\n", "/")

    def test_relocated_bundle_runs_real_rooted_fail_fix_loop(self):
        # A wrong bundled command/root/schema or fixture-only result breaks this loop.
        with tempfile.TemporaryDirectory(prefix="plugin integration ") as temp:
            base = Path(temp)
            root = base / "selected repository"
            root.mkdir()
            output = base / "built plugin"
            package.build(binary=self.engine, sha256=self.digest, target=self.target,
                          root=root, output=output, version="0.1.0")
            cache = base / "copilot installed cache"
            output.rename(cache)
            server = json.loads((cache / "mcp.json").read_text())["mcpServers"]["kmx-agent-builder"]
            command = [str(cache / server["command"]), *server["args"]]
            home = base / "private kmx home"
            process = subprocess.Popen(command, cwd=base, stdin=subprocess.PIPE,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                       text=True, encoding="utf-8",
                                       env={**os.environ, "KMX_HOME": str(home),
                                            "UNRELATED_SECRET": "fixture-canary"})
            timer = threading.Timer(30, process.kill)
            timer.start()
            try:
                def send(message):
                    process.stdin.write(json.dumps(message) + "\n")
                    process.stdin.flush()

                def request(number, method, params):
                    send({"jsonrpc": "2.0", "id": number, "method": method, "params": params})
                    reply = json.loads(process.stdout.readline())
                    self.assertEqual(reply["id"], number)
                    self.assertNotIn("fixture-canary", json.dumps(reply))
                    return reply

                initialized = request(1, "initialize", {
                    "protocolVersion": "2025-06-18", "capabilities": {},
                    "clientInfo": {"name": "plugin-integration", "version": "1.0.0"}})
                self.assertEqual(initialized["result"]["serverInfo"]["name"], "kmx")
                send({"jsonrpc": "2.0", "method": "notifications/initialized"})
                tools = request(2, "tools/list", {})["result"]["tools"]
                self.assertEqual({t["name"] for t in tools}, {"create", "edit", "run", "test"})
                for tool in tools:
                    self.assertIn("path", tool["inputSchema"]["required"])
                    self.assertIn("schemaVersion", tool["inputSchema"]["required"])

                counter = 2
                def call(name, **arguments):
                    nonlocal counter
                    counter += 1
                    reply = request(counter, "tools/call", {
                        "name": name, "arguments": {"schemaVersion": 1, **arguments}})
                    result = reply["result"]
                    payload = result["structuredContent"]
                    text = result["content"][0]["text"]
                    if set(payload) == {"schemaVersion", "reasonCode", "reason"}:
                        self.assertEqual(text, payload["reason"])
                    else:
                        self.assertEqual(json.loads(text), payload)
                    return result, payload

                skill = (cache / "skills/kmx-agent-builder/SKILL.md").read_text()
                self.assertIn("````markdown\n", skill, "orientation needs a runnable canonical charter")
                source = skill.split("````markdown\n", 1)[1].split("\n````", 1)[0] + "\n"
                wrong = source.replace("Hello, ", "Goodbye, ", 1)
                self.assertNotEqual(source, wrong)
                path = ".squad/agents/greeter/charter.md"
                result, created = call("create", path=path, content=wrong)
                self.assertFalse(result.get("isError", False))
                revision = created["file"]["revision"]
                self.assertEqual(revision, "sha256:" + hashlib.sha256(wrong.encode()).hexdigest())
                self.assertEqual((root / path).read_bytes(), wrong.encode())

                result, failed = call("run", path=path, input="Grace")
                self.assertTrue(result["isError"])
                self.assertEqual((failed["state"], failed["verdict"], failed["fullPass"]),
                                 ("complete", "fail", False))
                self.assertEqual(failed["file"]["revision"], revision)
                self.assertEqual(failed["examples"][0]["assertions"][0]["id"], "greeting-text")
                self.assertNotIn("answerExcerpt", json.dumps(failed))

                result, fixed = call("edit", path=path, content=source, expectedRevision=revision)
                self.assertFalse(result.get("isError", False))
                new_revision = fixed["file"]["revision"]
                self.assertNotEqual(new_revision, revision)
                result, partial = call("test", path=path, caseId="greeting")
                self.assertFalse(result.get("isError", False))
                self.assertEqual((partial["verdict"], partial["fullPass"], partial["selection"]["scope"]),
                                 ("pass", False, "subset"))
                result, passed = call("test", path=path)
                self.assertFalse(result.get("isError", False))
                self.assertEqual((passed["state"], passed["verdict"], passed["fullPass"]),
                                 ("complete", "pass", True))
                self.assertEqual(passed["file"]["revision"], new_revision)
                self.assertEqual(passed["modelRoute"], {"provider": "fake", "model": "fake"})
                self.assertEqual(passed["runner"], "preview runner (no journal)")
                self.assertEqual(passed["retention"], {"status": "not_requested"})

                # Mutation refusal must not change the bound charter or escape into the cache/cwd.
                for name, args, reason in [
                    ("edit", dict(path=path, content=wrong, expectedRevision=revision), "stale_revision"),
                    ("create", dict(path=path, content=source), "already_exists"),
                    ("create", dict(path="../escape.agent.md", content=source), "unsafe_path"),
                ]:
                    result, refused = call(name, **args)
                    self.assertTrue(result["isError"])
                    self.assertEqual(refused["reasonCode"], reason)
                self.assertEqual((root / path).read_bytes(), source.encode())
                self.assertFalse((base / "escape.agent.md").exists())
                self.assertFalse((cache / ".squad").exists())
                self.assertFalse((root / ".squad/team.md").exists())
                self.assertFalse((base / ".squad").exists())
                unknown = request(counter + 1, "tools/call", {"name": "explain", "arguments": {}})
                self.assertEqual(unknown["error"]["code"], -32602)
                process.stdin.close()
                process.wait(timeout=5)
                self.assertEqual(process.returncode, 0)
                self.assertEqual(process.stdout.read(), "")
                self.assertEqual(process.stderr.read(), "")
            finally:
                timer.cancel()
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=5)
                for stream in (process.stdin, process.stdout, process.stderr):
                    stream.close()


if __name__ == "__main__":
    unittest.main()
