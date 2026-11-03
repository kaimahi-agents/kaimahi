"""Source plugin release checks run without installing a host or changing the checkout."""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
DERIVED = ("skills/kmx/SKILL.md", "skills/kmx-agent-builder/SKILL.md",
           "com.github.copilot/extensions/agent-view/extension.mjs")
VIEW = "com.github.copilot/extensions/agent-view/"
WORDING = '{"contains":"Missing \\"{value}\\""}'


class SourceReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for directory, name in (("copilot-wsl", "kmx-agent-builder"),
                                ("copilot", "kmx-agent-builder-path")):
            plugin = self.root / "plugins" / directory
            plugin.mkdir(parents=True)
            (plugin / "plugin.json").write_text(json.dumps({
                "name": name, "version": "0.9.0-rc.3"}))
            for relative in DERIVED:
                path = plugin / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                identity = "kmx-agent-builder" if directory == "copilot-wsl" else "kmx-agent-builder-path"
                entry = "kmx-agent-builder" if directory == "copilot-wsl" else "kmx-agent-builder-path"
                if relative == "skills/kmx/SKILL.md":
                    content = (f"---\r\nname: kmx\r\ndescription: Fixture opener.\r\n---\r\n"
                               f"```json\r\n"
                               f'{{"extensionId":"plugin:{identity}:agent-view"}}\r\n'
                               f"```\r\n"
                               f"`Use /{entry}:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`\r\n")
                elif relative == "skills/kmx-agent-builder/SKILL.md":
                    content = (f"---\r\nname: kmx-agent-builder\r\ndescription: Fixture builder.\r\n---\r\n"
                               f"Use provider `plugin:{identity}:agent-view` and canvas `kmx-agent-view`.\r\n")
                else:
                    content = ('import { OPEN_TOOL_NAME, OPEN_TOOL_PARAMETERS, createOpenHandler, createOpenCommand, readOpenView } from "./open.mjs";\r\n'
                               f'handler: createOpenHandler({{ getSession: () => session, extensionId: "plugin:{identity}:agent-view" }}),\r\n'
                               f'handler: createOpenCommand({{ getSession: () => session, extensionId: "plugin:{identity}:agent-view" }}),\r\n')
                path.write_bytes(content.encode())

        wsl = self.root / "plugins/copilot-wsl" / VIEW
        (wsl / "failure-wording.json").write_text(WORDING)
        self.run_check("--write")

    def run_check(self, *args):
        return subprocess.run([sys.executable, str(HERE / "source_release.py"), *args],
                              cwd=self.root, capture_output=True, text=True, check=False, timeout=10)

    def manifest(self, directory, **fields):
        path = self.root / "plugins" / directory / "plugin.json"
        value = json.loads(path.read_text())
        value.update(fields)
        path.write_text(json.dumps(value))

    def snapshot(self):
        return {str(p.relative_to(self.root)): p.read_bytes()
                for p in self.root.rglob("*") if p.is_file()}

    def test_exact_rc_check_and_ordinary_check_are_read_only(self):
        before = self.snapshot()
        for args in ((), ("--version", "v0.9.0-rc.3")):
            result = self.run_check(*args)
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.snapshot(), before)

    def test_version_and_identity_mismatches_refuse_without_writes(self):
        for directory, fields, reason in (
                ("copilot", {"version": "0.9.0"}, "source plugin versions differ"),
                ("copilot", {"name": "kmx-agent-builder"}, "unexpected source plugin identity"),
                ("copilot-wsl", {"version": "0.1.0-demo"}, "non-demo semantic release versions"),
                ("copilot-wsl", {"version": "invalid"}, "non-demo semantic release versions")):
            with self.subTest(directory=directory, fields=fields):
                before = self.snapshot()
                self.manifest(directory, **fields)
                mutated = self.snapshot()
                result = self.run_check()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(reason, result.stderr)
                self.assertEqual(self.snapshot(), mutated)
                for relative, data in before.items():
                    (self.root / relative).write_bytes(data)
        self.assertEqual(self.run_check("--version", "v0.9.0").returncode, 1)
        self.assertEqual(self.run_check("--version", "0.9.0-rc.3").returncode, 1)

    def test_write_stamps_both_manifests_and_substitutes_variant_identity(self):
        for version in ("v0.9.0-rc.4", "v0.9.0"):
            result = self.run_check("--write", "--version", version)
            self.assertEqual(result.returncode, 0, result.stderr)
            for directory in ("copilot", "copilot-wsl"):
                manifest = json.loads((self.root / "plugins" / directory / "plugin.json").read_text())
                self.assertEqual(manifest["version"], version[1:])
            self.assertEqual(self.run_check("--version", version).returncode, 0)
        for relative in DERIVED:
            derived = (self.root / "plugins/copilot" / relative).read_bytes()
            if relative == "skills/kmx/SKILL.md":
                expected = (b"---\r\nname: kmx\r\ndescription: Fixture opener.\r\n---\r\n"
                            b"```json\r\n"
                            b'{"extensionId":"plugin:kmx-agent-builder-path:agent-view"}\r\n'
                            b"```\r\n"
                            b"`Use /kmx-agent-builder-path:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`\r\n")
            elif relative == "skills/kmx-agent-builder/SKILL.md":
                expected = (b"---\r\nname: kmx-agent-builder\r\ndescription: Fixture builder.\r\n---\r\n"
                            b"Use provider `plugin:kmx-agent-builder-path:agent-view` and canvas `kmx-agent-view`.\r\n")
            else:
                expected = (b'import { OPEN_TOOL_NAME, OPEN_TOOL_PARAMETERS, createOpenHandler, createOpenCommand, readOpenView } from "./open.mjs";\r\n'
                            b'handler: createOpenHandler({ getSession: () => session, extensionId: "plugin:kmx-agent-builder-path:agent-view" }),\r\n'
                            b'handler: createOpenCommand({ getSession: () => session, extensionId: "plugin:kmx-agent-builder-path:agent-view" }),\r\n')
            self.assertEqual(derived, expected)
        before = self.snapshot()
        self.assertEqual(self.run_check("--write").returncode, 0)
        self.assertEqual(self.snapshot(), before)

    def test_missing_or_mixed_canonical_skill_entry_refuses_without_writes(self):
        path = self.root / "plugins/copilot-wsl/skills/kmx/SKILL.md"
        canonical = path.read_bytes()
        mutations = {
            "missing": canonical.replace(b"/kmx-agent-builder:", b"/different-entry:"),
            "mixed": canonical + b"/kmx-agent-builder-path:kmx\r\n",
        }
        for name, content in mutations.items():
            with self.subTest(name=name):
                try:
                    path.write_bytes(content)
                    before = self.snapshot()
                    for args in ((), ("--write", "--version", "v0.9.0")):
                        result = self.run_check(*args)
                        self.assertEqual(result.returncode, 1, result.stderr)
                        self.assertIn("canonical skill entry identity is missing or mixed", result.stderr)
                        self.assertEqual(self.snapshot(), before)
                finally:
                    path.write_bytes(canonical)

    def test_wrong_or_mixed_canonical_identities_refuse_without_writes(self):
        opener = self.root / "plugins/copilot-wsl/skills/kmx/SKILL.md"
        builder = self.root / "plugins/copilot-wsl/skills/kmx-agent-builder/SKILL.md"
        extension = self.root / "plugins/copilot-wsl/com.github.copilot/extensions/agent-view/extension.mjs"
        originals = {path: path.read_bytes() for path in (opener, builder, extension)}
        mutations = {
            "opener-name": (opener, originals[opener].replace(b"name: kmx", b"name: wrong"), "canonical skill name"),
            "builder-name": (builder, originals[builder].replace(b"name: kmx-agent-builder", b"name: wrong"), "canonical skill name"),
            "wrong-entry-suffix": (opener, originals[opener].replace(b"/kmx-agent-builder:kmx", b"/kmx-agent-builder:kmx-evil"), "canonical skill entry"),
            "duplicate-name": (opener, originals[opener].replace(b"name: kmx\r\n", b"name: kmx\r\nname: wrong\r\n"), "canonical skill name"),
            "quoted-duplicate-name": (opener, originals[opener].replace(b"name: kmx\r\n", b"name: kmx\r\n\"name\": wrong\r\n"), "canonical skill name"),
            "mixed-provider": (builder, originals[builder] + b"plugin:kmx-agent-builder-path:agent-view\r\n", "canonical provider identity"),
            "duplicate-provider": (extension, originals[extension] + b'handler: createOpenHandler({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n', "canonical provider identity"),
            "inert-provider": (builder, originals[builder].replace(
                b"Use provider `plugin:kmx-agent-builder:agent-view` and canvas `kmx-agent-view`.",
                b"Use provider `plugin:wrong:agent-view` and canvas `kmx-agent-view`.\r\n"
                b"<!--\r\nUse provider `plugin:kmx-agent-builder:agent-view` and canvas `kmx-agent-view`.\r\n-->"), "canonical provider identity"),
            "fenced-provider": (builder, originals[builder].replace(
                b"Use provider `plugin:kmx-agent-builder:agent-view` and canvas `kmx-agent-view`.",
                b"Use provider `plugin:wrong:agent-view` and canvas `kmx-agent-view`.\r\n"
                b"```text\r\nUse provider `plugin:kmx-agent-builder:agent-view` and canvas `kmx-agent-view`.\r\n```"), "canonical provider identity"),
            "indented-provider": (builder, originals[builder].replace(
                b"Use provider `plugin:kmx-agent-builder:agent-view` and canvas `kmx-agent-view`.",
                b"Use provider `plugin:wrong:agent-view` and canvas `kmx-agent-view`.\r\n"
                b"    Use provider `plugin:kmx-agent-builder:agent-view` and canvas `kmx-agent-view`."), "canonical provider identity"),
            "indented-entry": (opener, originals[opener].replace(
                b"`Use /kmx-agent-builder:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`",
                b"`Use /different-entry:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`\r\n"
                b"    `Use /kmx-agent-builder:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`"), "canonical skill entry"),
            "commented-extension-provider": (extension, originals[extension].replace(
                b"plugin:kmx-agent-builder:agent-view", b"plugin:wrong:agent-view") +
                b"/*\r\n"
                b'handler: createOpenHandler({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n'
                b'handler: createOpenCommand({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n'
                b"*/\r\n", "canonical provider identity"),
            "template-extension-provider": (extension, originals[extension].replace(
                b"plugin:kmx-agent-builder:agent-view", b"plugin:wrong:agent-view") +
                b"const inert = `\r\n"
                b'handler: createOpenHandler({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n'
                b'handler: createOpenCommand({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n'
                b"`;\r\n", "unsupported JavaScript syntax"),
            "dead-extension-provider": (extension,
                originals[extension].replace(b"createOpenHandler({", b"(createOpenHandler)({", 1)
                .replace(b"createOpenCommand({", b"(createOpenCommand)({", 1)
                .replace(b"plugin:kmx-agent-builder:agent-view", b"plugin:wrong:agent-view") +
                b"if (false) {\r\n"
                b'handler: createOpenHandler({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n'
                b'handler: createOpenCommand({ getSession: () => session, extensionId: "plugin:kmx-agent-builder:agent-view" }),\r\n'
                b"}\r\n", "canonical provider identity"),
        }
        for name, (path, content, reason) in mutations.items():
            with self.subTest(name=name):
                try:
                    path.write_bytes(content)
                    before = self.snapshot()
                    for args in ((), ("--write", "--version", "v0.9.0")):
                        result = self.run_check(*args)
                        self.assertEqual(result.returncode, 1, result.stderr)
                        self.assertIn(reason, result.stderr)
                        self.assertEqual(self.snapshot(), before)
                finally:
                    path.write_bytes(originals[path])

    def test_invalid_release_stamp_refuses_before_any_writes(self):
        before = self.snapshot()
        for version in ("v0.9.0-01", "v00.9.0", "v0.9.0-", "0.9.0", "v0.1.0-demo"):
            with self.subTest(version=version):
                result = self.run_check("--write", "--version", version)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual(self.snapshot(), before)

    def test_stale_missing_or_linked_derived_file_requires_regeneration(self):
        path = self.root / "plugins/copilot" / DERIVED[0]
        source = self.root / "plugins/copilot-wsl" / DERIVED[0]
        canonical = source.read_bytes()
        path.write_bytes(b"stale")
        self.assertEqual(self.run_check().returncode, 1)
        path.unlink()
        self.assertEqual(self.run_check().returncode, 1)
        if os.name != "nt":
            path.symlink_to(source)
            self.assertEqual(self.run_check().returncode, 1)
        result = self.run_check("--write")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(path.is_symlink())
        self.assertEqual(source.read_bytes(), canonical)
        self.assertEqual(self.run_check().returncode, 0)

    @unittest.skipIf(os.name == "nt", "release workflow shell runs on Ubuntu; Windows checks source parity")
    def test_release_step_checks_tags_named_rehearsals_and_synthetic_parity(self):
        workflow = (HERE.parents[1] / ".github/workflows/release.yml").read_text()
        marker = "      - name: Check source plugin release and PATH copies\n"
        self.assertIn(marker, workflow, "release workflow does not check source manifests")
        step = workflow.split(marker, 1)[1].split("\n      - ", 1)[0]
        script = textwrap.dedent(step.split("        run: |\n", 1)[1])
        shutil.copyfile(HERE / "source_release.py", self.root / "plugins/copilot/source_release.py")
        for synthetic, version, expected in (("false", "v0.9.0-rc.3", 0),
                                              ("false", "v0.9.0", 1),
                                              ("false", "v0.9.0-rc.2", 1),
                                              ("true", "v0.0.0-dryrun.fixture.1.1", 0)):
            with self.subTest(synthetic=synthetic, version=version):
                result = subprocess.run(["bash", "-c", script], cwd=self.root,
                                        env={**os.environ, "SYNTHETIC": synthetic, "VERSION": version},
                                        capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode, expected, result.stderr)
        stale = self.root / "plugins/copilot" / DERIVED[0]
        stale.write_bytes(b"stale")
        result = subprocess.run(["bash", "-c", script], cwd=self.root,
                                env={**os.environ, "SYNTHETIC": "true", "VERSION": "synthetic"},
                                capture_output=True, text=True, check=False, timeout=10)
        self.assertEqual(result.returncode, 1, result.stderr)

    def test_release_requires_semantic_provider_registration_contract(self):
        workflow = (HERE.parents[1] / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertIn("run: node --test plugins/tests/copilot/*.test.mjs", workflow)
        self.assertIn("needs: [release, plugin-install, release-contracts, release-windows-amd64]", workflow)
        opener = (HERE.parents[1] / "plugins/tests/copilot/open.test.mjs").read_text(encoding="utf-8")
        self.assertIn("actual ${variant} extension opens its own provider", opener)

    def test_generated_failure_wording_module_is_checked_and_regenerated(self):
        module = self.root / "plugins/copilot-wsl" / VIEW / "failure-wording.mjs"
        self.assertIn("Generated from failure-wording.json", module.read_text())
        self.assertEqual(self.run_check().returncode, 0)
        module.write_text("stale")
        result = self.run_check()
        self.assertEqual(result.returncode, 1)
        self.assertIn("failure-wording.mjs", result.stderr)
        self.assertEqual(self.run_check("--write").returncode, 0)
        self.assertEqual(self.run_check().returncode, 0)

    def test_generated_module_allows_checkout_line_endings_without_rewriting(self):
        module = self.root / "plugins/copilot-wsl" / VIEW / "failure-wording.mjs"
        crlf = module.read_bytes().replace(b"\n", b"\r\n")
        module.write_bytes(crlf)
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(module.read_bytes(), crlf)

    def test_real_source_tree_is_synchronized(self):
        result = subprocess.run([sys.executable, str(HERE / "source_release.py")],
                                cwd=HERE.parents[1], capture_output=True, text=True, check=False, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
