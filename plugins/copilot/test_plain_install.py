"""PATH plugin and offline loop; pinned-host checks opt in without model credentials."""
import json
import os
import shutil
import subprocess
import tempfile
import threading
import unittest
from pathlib import Path

from test_wsl import ASSETS, SKILLS

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
SHARED = HERE.with_name("copilot-wsl")


@unittest.skipIf(os.name == "nt", "Windows uses the self-contained copilot-wsl plugin")
class PlainPluginTests(unittest.TestCase):
    def test_path_command_and_shared_assets_need_no_setup(self):
        config = json.loads((HERE / "mcp.json").read_text())
        self.assertEqual(config["mcpServers"], {"kmx-agent-builder": {
            "type": "stdio", "command": "kmx", "args": ["mcp"]}})
        self.assertFalse((HERE / "bin").exists())
        for name in ASSETS | SKILLS:
            with self.subTest(name=name):
                self.assertEqual((HERE / name).resolve(), (SHARED / name).resolve())
                self.assertEqual((HERE / name).read_bytes(), (SHARED / name).read_bytes())

    @unittest.skipUnless(os.environ.get("KMX_PLAIN_COPILOT"), "opt-in pinned Copilot install check")
    def test_installed_cache_is_self_contained(self):
        copilot = os.environ["KMX_PLAIN_COPILOT"]
        with tempfile.TemporaryDirectory(prefix="plain copilot ") as temp:
            home = Path(temp)
            env = {**os.environ, "COPILOT_HOME": str(home)}
            def host(*args):
                return subprocess.run([copilot, "--no-auto-update", *args], env=env,
                                      capture_output=True, text=True, check=True, timeout=120).stdout
            self.assertIn("1.0.102", host("--version"))
            host("plugin", "install", os.environ.get("KMX_PLAIN_SOURCE", str(HERE)))
            self.assertIn("kmx-agent-builder", host("plugin", "list"))
            manifests = list((home / "installed-plugins").rglob("plugin.json"))
            self.assertEqual(len(manifests), 1)
            cache = manifests[0].parent
            self.assertEqual(json.loads((cache / "mcp.json").read_text()),
                             json.loads((HERE / "mcp.json").read_text()))
            self.assertFalse((cache / "bin").exists())
            for name in ASSETS | SKILLS:
                self.assertEqual((cache / name).read_bytes(), (SHARED / name).read_bytes(), name)
            self.assertFalse(any(p.is_symlink() for p in cache.rglob("*")))
            self.assertFalse(list(cache.rglob("*.test.mjs")))
            host("plugin", "uninstall", "kmx-agent-builder")
            self.assertNotIn("kmx-agent-builder", host("plugin", "list"))

    def test_path_mcp_create_cli_test_edit_and_rerun_offline(self):
        with tempfile.TemporaryDirectory(prefix="plain kmx ") as temp:
            base = Path(temp).resolve()
            supplied = os.environ.get("KMX_PLAIN_BINARY")
            if supplied:
                binary = Path(supplied).resolve()
            else:
                binary = base / "bin/kmx"
                binary.parent.mkdir()
                subprocess.run(["go", "build", "-o", str(binary), "./cmd/kmx"], cwd=REPO,
                               env={**os.environ, "CGO_ENABLED": "0", "GOWORK": "off"},
                               check=True, capture_output=True, timeout=120)
            env = {**os.environ, "KMX_HOME": str(base / "state"),
                   "PATH": str(binary.parent) + os.pathsep + os.environ["PATH"]}
            resolved = shutil.which("kmx", path=env["PATH"])
            assert resolved is not None
            self.assertEqual(Path(resolved).resolve(), binary)
            config = json.loads((HERE / "mcp.json").read_text())["mcpServers"]["kmx-agent-builder"]
            process = subprocess.Popen([config["command"], *config["args"]], cwd=HERE,
                                       stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                       stderr=subprocess.PIPE, text=True, env=env)
            timer = threading.Timer(30, process.kill)
            timer.start()
            try:
                stdin, stdout, stderr = process.stdin, process.stdout, process.stderr
                assert stdin is not None and stdout is not None and stderr is not None
                counter = 0
                def request(method, params):
                    nonlocal counter
                    counter += 1
                    stdin.write(json.dumps({"jsonrpc": "2.0", "id": counter,
                                            "method": method, "params": params}) + "\n")
                    stdin.flush()
                    reply = json.loads(stdout.readline())
                    self.assertNotIn("error", reply)
                    return reply["result"]
                request("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                       "clientInfo": {"name": "plain-plugin-check", "version": "1"}})
                stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
                stdin.flush()
                def call(name, **args):
                    result = request("tools/call", {"name": name, "arguments": {
                        "schemaVersion": 1, **args}})
                    self.assertFalse(result.get("isError"), result)
                    return result["structuredContent"]
                source = (REPO / "internal/kmx/agentfile/testdata/greeter.agent.md").read_text()
                path = base / "unrelated repository/greeter.agent.md"
                path.parent.mkdir()
                created = call("create", path=str(path), content=source)
                self.assertEqual(path.read_text(), source)
                def cli_test(verdict, code):
                    result = subprocess.run([str(binary), "test", str(path), "-o", "json"],
                                            env=env, cwd=base, capture_output=True, text=True,
                                            check=False, timeout=30)
                    self.assertEqual(result.returncode, code, result.stderr)
                    payload = json.loads(result.stdout)
                    self.assertEqual(payload["verdict"], verdict)
                    self.assertEqual(payload["fullPass"], verdict == "pass")
                    self.assertEqual(payload["modelRoute"], {"provider": "fake", "model": "fake"})
                cli_test("pass", 0)
                wrong = source.replace("Reply with exactly `Hello, `", "Reply with exactly `Goodbye, `")
                self.assertNotEqual(wrong, source)
                edited = call("edit", path=str(path), content=wrong,
                              expectedRevision=created["file"]["revision"])
                self.assertEqual(path.read_text().split("## Examples")[1], source.split("## Examples")[1])
                cli_test("fail", 2)
                call("edit", path=str(path), content=source,
                     expectedRevision=edited["file"]["revision"])
                cli_test("pass", 0)
                stdin.close()
                self.assertEqual(process.wait(timeout=5), 0)
                self.assertEqual(stderr.read(), "")
            finally:
                timer.cancel()
                if process.poll() is None:
                    process.kill()
                process.wait(timeout=5)
                for stream in (process.stdin, process.stdout, process.stderr):
                    if stream is not None:
                        stream.close()


if __name__ == "__main__":
    unittest.main()
