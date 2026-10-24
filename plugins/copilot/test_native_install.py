"""Native release install gate; trusted workflow artifacts only, no downloads."""
import hashlib
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest

import check_install
import release

ROOT = Path(__file__).resolve().parents[2]


def stage_archive(source: Path, work: Path) -> Path:
    """Configure only the digest-pinned artifact from this trusted workflow run."""
    rows = [line.split() for line in (source.parent / "plugin-checksums.txt").read_text().splitlines()
            if line.split()[1:] == [source.name]]
    if len(rows) != 1 or rows[0][0] != hashlib.sha256(source.read_bytes()).hexdigest():
        raise ValueError("workflow archive digest mismatch")
    with tarfile.open(source, "r:gz") as archive:
        archive.extractall(work, filter="data")
    plugin = work / "kmx-agent-builder"
    root = work / "selected repository ✓"
    root.mkdir(mode=0o700)
    config = json.loads((plugin / "mcp.json").read_text())
    config["mcpServers"]["kmx-agent-builder"]["args"][2] = str(root.resolve())
    (plugin / "mcp.json").write_text(json.dumps(config), encoding="utf-8")
    (plugin / "selected-root.json").write_text(json.dumps({"root": str(root.resolve())}), encoding="utf-8")
    return plugin


class NativeArchiveTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="native archive ")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        engine = self.base / "kmx"
        engine.write_bytes(b"fixture engine; staging must not execute it\n")
        self.digest = hashlib.sha256(engine.read_bytes()).hexdigest()
        self.source = self.base / "native.tar.gz"
        release.archive(binary=engine, sha256=self.digest, target="linux/amd64", version="v0.1.0",
                        revision="a" * 40, output=self.source)
        self.checksum = hashlib.sha256(self.source.read_bytes()).hexdigest() + "  native.tar.gz\n"
        self.manifest = self.base / "plugin-checksums.txt"
        self.manifest.write_text(self.checksum)
        self.work = self.base / "extracted"
        self.work.mkdir()

    def test_stages_owned_root_and_leaves_engine_and_assets_unchanged(self):
        plugin = stage_archive(self.source, self.work)
        self.assertEqual(check_install.validate_package(plugin, self.digest),
                         (self.work / "selected repository ✓").resolve())
        for name in (*release.package.NATIVE_FILES, *release.package.SKILL_FILES,
                     *release.package.SAMPLE_FILES):
            self.assertEqual((plugin / name).read_bytes(), release.package.asset_source(name).read_bytes())
        self.assertEqual(list((self.work / "selected repository ✓").iterdir()), [])

    def test_missing_duplicate_or_wrong_digest_never_extracts(self):
        for text in ("", self.checksum * 2, "0" * 64 + "  native.tar.gz\n"):
            with self.subTest(text=text):
                self.manifest.write_text(text)
                with self.assertRaisesRegex(ValueError, "workflow archive digest"):
                    stage_archive(self.source, self.work)
                self.assertEqual(list(self.work.iterdir()), [])


@unittest.skipUnless(os.environ.get("KMX_NATIVE_COPILOT"), "opt-in real isolated CLI install")
class InstalledNativeTests(unittest.TestCase):
    def test_trusted_archive_isolated_install_cycle(self):
        with tempfile.TemporaryDirectory(prefix="native plugin install ") as temp:
            work = Path(temp)
            plugin = stage_archive(Path(os.environ["KMX_NATIVE_ARCHIVE"]), work)
            digest = json.loads((plugin / "engine.json").read_text())["sha256"]
            check_install.check(os.environ["KMX_NATIVE_COPILOT"], plugin, digest,
                                work / "isolated-copilot-profile")


class NativeWorkflowTests(unittest.TestCase):
    def test_release_install_gates_publication_without_installer(self):
        text = (ROOT / ".github/workflows/release.yml").read_text()
        install = text.split("  plugin-install:", 1)[1].split("  publish:", 1)[0]
        self.assertIn("needs: release", install)
        self.assertIn("os: [ubuntu-latest, macos-latest]", install)
        self.assertIn("actions/download-artifact@v4", install)
        self.assertIn("kmx-${{ needs.release.outputs.version }}", install)
        self.assertIn("export KMX_NATIVE_ARCHIVE=", install)
        self.assertIn("export KMX_NATIVE_COPILOT=", install)
        self.assertIn("-p test_native_install.py", install)
        self.assertIn("--ignore-scripts", install)
        self.assertIn("@github/copilot@1.0.102", install)
        self.assertNotIn("KMX_TRY_IT", install)
        publish = text.split("  publish:", 1)[1]
        self.assertIn("needs: [release, plugin-install, release-contracts]", publish)
        self.assertIn("contents: write", publish)
        self.assertIn("needs.release.outputs.dry_run == 'false'", publish)
        self.assertIn("gh release create", publish)

    def test_pr_checks_shared_sources_without_install_job(self):
        text = (ROOT / ".github/workflows/copilot-plugin.yml").read_text()
        self.assertEqual(text.count("- 'plugins/copilot-wsl/**'"), 2)
        self.assertNotIn("  try-it-install:", text)
        self.assertNotIn("KMX_TRY_IT", text)
        self.assertIn("os: [ubuntu-latest, macos-latest, windows-latest]", text)
        self.assertIn("-p 'test_*.py'", text)


if __name__ == "__main__":
    unittest.main()
