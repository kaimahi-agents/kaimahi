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
                path.write_bytes(f"unchanged MCP kmx-agent-builder\r\nplugin:{identity}:agent-view\r\n".encode())

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

    def test_write_stamps_both_manifests_and_only_substitutes_provider(self):
        for version in ("v0.9.0-rc.4", "v0.9.0"):
            result = self.run_check("--write", "--version", version)
            self.assertEqual(result.returncode, 0, result.stderr)
            for directory in ("copilot", "copilot-wsl"):
                manifest = json.loads((self.root / "plugins" / directory / "plugin.json").read_text())
                self.assertEqual(manifest["version"], version[1:])
            self.assertEqual(self.run_check("--version", version).returncode, 0)
        for relative in DERIVED:
            derived = (self.root / "plugins/copilot" / relative).read_bytes()
            self.assertEqual(derived, b"unchanged MCP kmx-agent-builder\r\nplugin:kmx-agent-builder-path:agent-view\r\n")
        before = self.snapshot()
        self.assertEqual(self.run_check("--write").returncode, 0)
        self.assertEqual(self.snapshot(), before)

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

    def test_real_source_tree_is_synchronized(self):
        result = subprocess.run([sys.executable, str(HERE / "source_release.py")],
                                cwd=HERE.parents[1], capture_output=True, text=True, check=False, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
