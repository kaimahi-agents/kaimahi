"""The repository-installable Windows plugin has assets, not an engine or setup template."""
import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "copilot-wsl"
VIEW = "com.github.copilot/extensions/agent-view/"
ASSETS = {VIEW + name for name in (
    "extension.mjs", "view.mjs", "open.mjs", "index.html", "app.js", "style.css", "authoring.mjs", "editor.mjs",
    "draft-controller.mjs", "action-controller.mjs", "observation-controller.mjs", "host-relay.mjs", "tool-catalog.mjs", "tools-view.mjs")}
SKILLS = {"skills/kmx/SKILL.md", "skills/kmx-agent-builder/SKILL.md"}


class WSLPluginTests(unittest.TestCase):
    def test_fixed_wsl_command_needs_no_configuration(self):
        self.assertTrue((ROOT / "plugin.json").is_file(), "WSL plugin manifest is missing")
        self.assertTrue((ROOT / "mcp.json").is_file(), "WSL MCP configuration is missing")
        manifest = json.loads((ROOT / "plugin.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["name"], "kmx-agent-builder")
        self.assertEqual(manifest["$schema"], "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json")
        expected = {
            "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
            "mcpServers": {"kmx-agent-builder": {
                "type": "stdio", "command": "wsl.exe", "args": ["-e", "/usr/local/bin/kmx", "mcp"]}}}
        source = (ROOT / "mcp.json").read_text(encoding="utf-8")
        for eol in ("\n", "\r\n"):
            self.assertEqual(json.loads(source.replace("\n", eol)), expected)
        self.assertNotIn("__SELECTED_ROOT__", source)
        self.assertNotIn("${PLUGIN_ROOT}", source)
        self.assertNotIn("--provider", source)

    def test_installed_tree_has_one_self_contained_asset_source(self):
        self.assertTrue(ROOT.is_dir(), "WSL plugin directory is missing")
        actual = {p.relative_to(ROOT).as_posix() for p in ROOT.rglob("*") if p.is_file()}
        self.assertEqual(actual, ASSETS | SKILLS | {"plugin.json", "mcp.json", "README.md"})
        for name in actual:
            path = ROOT / name
            self.assertFalse(path.is_symlink(), name)
            self.assertGreater(path.stat().st_size, 0, name)
        for name in ASSETS:
            if not name.endswith((".js", ".mjs")):
                continue
            path = ROOT / name
            for relative in re.findall(r'from\s+["\'](\.[^"\']+)["\']', path.read_text(encoding="utf-8")):
                target = (path.parent / relative).resolve()
                self.assertTrue(target.is_file(), relative)
                self.assertIn(ROOT.resolve(), target.parents)
        extension = (ROOT / VIEW / "extension.mjs").read_text(encoding="utf-8")
        self.assertIn('plugin:kmx-agent-builder:agent-view', extension)
        for name in SKILLS:
            self.assertIn('plugin:kmx-agent-builder:agent-view', (ROOT / name).read_text(encoding="utf-8"))

    def test_assets_fit_existing_native_bound_with_windows_line_endings(self):
        for name in ASSETS:
            source = (ROOT / name).read_bytes().replace(b"\r\n", b"\n")
            self.assertLessEqual(len(source), 65536, name)
            self.assertLessEqual(len(source.replace(b"\n", b"\r\n")), 65536, name)

    def test_install_instructions_are_the_two_plain_steps(self):
        self.assertTrue((ROOT / "README.md").is_file(), "WSL install instructions are missing")
        text = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn("/usr/local/bin/kmx", text)
        self.assertIn("copilot plugin install kaimahi-agents/kaimahi:plugins/copilot-wsl", text)
        self.assertNotIn("model login", text)
        self.assertNotIn("__SELECTED_ROOT__", text)
        self.assertNotIn("try-it.sh", text)
