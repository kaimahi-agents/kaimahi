"""Release archive contracts; no signing or installed-host claim."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

import release
import check_install


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="plugin release ")
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.engine = self.base / "kmx"
        self.engine.write_bytes(b"pinned engine bytes for archive mechanics\n")
        self.digest = hashlib.sha256(self.engine.read_bytes()).hexdigest()
        self.revision = "a" * 40
        self.archive = self.base / "plugin.tar.gz"

    def archive_build(self, **changes):
        args = dict(binary=self.engine, sha256=self.digest, target="linux/amd64",
                    version="v0.1.0", revision=self.revision, output=self.archive)
        args.update(changes)
        return release.archive(**args)

    def test_unconfigured_archive_is_exact_and_reproducible(self):
        self.archive_build()
        other = self.base / "again.tar.gz"
        self.archive_build(output=other)
        self.assertEqual(self.archive.read_bytes(), other.read_bytes())
        release.verify_archive(self.archive, self.digest, "linux/amd64", "v0.1.0", self.revision)
        with tarfile.open(self.archive) as archive:
            members = archive.getmembers()
            self.assertEqual(len(members), 7)
            self.assertTrue(all(m.isfile() and m.uid == m.gid == m.mtime == 0 for m in members))
            files = {m.name: archive.extractfile(m).read() for m in members}
            config = json.loads(files["kmx-agent-builder/mcp.json"])
            self.assertEqual(config["mcpServers"]["kmx-agent-builder"]["args"],
                             ["mcp", "--root", "__SELECTED_ROOT__"])
            self.assertEqual(json.loads(files["kmx-agent-builder/selected-root.json"]),
                             {"root": "__SELECTED_ROOT__"})
            self.assertEqual(files["kmx-agent-builder/bin/kmx"], self.engine.read_bytes())
            self.assertEqual(next(m.mode for m in members if m.name.endswith("bin/kmx")), 0o755)
            self.assertNotIn(str(self.base).encode(), b"".join(files.values()))

    def test_archive_refuses_bad_hash_and_existing_output(self):
        with self.assertRaisesRegex(ValueError, "SHA256 mismatch"):
            self.archive_build(sha256="0" * 64)
        self.assertFalse(self.archive.exists())
        self.archive.write_bytes(b"keep")
        with self.assertRaisesRegex(FileExistsError, ""):
            self.archive_build()
        self.assertEqual(self.archive.read_bytes(), b"keep")

    def test_reopen_refuses_tamper_extra_link_and_unsafe_names(self):
        self.archive_build()
        with tarfile.open(self.archive) as original:
            rows = [(m, original.extractfile(m).read()) for m in original]
        for kind in ("engine", "extra", "link", "traversal"):
            with self.subTest(kind=kind):
                altered = self.base / (kind + ".tar.gz")
                with tarfile.open(altered, "w:gz") as out:
                    for member, data in rows:
                        if kind == "engine" and member.name.endswith("bin/kmx"):
                            data = b"changed" + data[7:]
                        out.addfile(member, io.BytesIO(data))
                    if kind != "engine":
                        extra = tarfile.TarInfo("../escape" if kind == "traversal" else "kmx-agent-builder/extra")
                        if kind == "link":
                            extra.type = tarfile.SYMTYPE
                            extra.linkname = "/outside"
                        out.addfile(extra)
                with self.assertRaisesRegex(ValueError, "archive"):
                    release.verify_archive(altered, self.digest, "linux/amd64", "v0.1.0", self.revision)

    def test_build_metadata_binds_path_platform_revision_and_clean_tree(self):
        info = {"Path": "github.com/kaimahi-agents/kaimahi/cmd/kmx", "Settings": [
            {"Key": "GOOS", "Value": "linux"}, {"Key": "GOARCH", "Value": "amd64"},
            {"Key": "vcs.revision", "Value": self.revision},
            {"Key": "vcs.modified", "Value": "false"}]}
        release.validate_buildinfo(info, "linux/amd64", self.revision)
        for key, value in [("GOARCH", "arm64"), ("GOOS", "darwin"),
                           ("vcs.revision", "b" * 40), ("vcs.modified", "true")]:
            wrong = json.loads(json.dumps(info))
            next(s for s in wrong["Settings"] if s["Key"] == key)["Value"] = value
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "engine metadata"):
                release.validate_buildinfo(wrong, "linux/amd64", self.revision)
        info["Settings"] = []
        with self.assertRaisesRegex(ValueError, "engine metadata"):
            release.validate_buildinfo(info, "linux/amd64", self.revision)

    def test_install_requires_matching_bound_root_and_pinned_engine(self):
        package_root = self.base / "plugin"
        release.package.build(binary=self.engine, sha256=self.digest, target="linux/amd64",
                              root=None, output=package_root, version="0.1.0")
        with self.assertRaisesRegex(ValueError, "root"):
            check_install.validate_package(package_root, self.digest)
        root = self.base / "selected root"
        root.mkdir()
        config = json.loads((package_root / "mcp.json").read_text())
        config["mcpServers"]["kmx-agent-builder"]["args"][-1] = str(root.resolve())
        (package_root / "mcp.json").write_text(json.dumps(config))
        with self.assertRaisesRegex(ValueError, "root"):
            check_install.validate_package(package_root, self.digest)
        (package_root / "selected-root.json").write_text(json.dumps({"root": str(root.resolve())}))
        self.assertEqual(check_install.validate_package(package_root, self.digest), root.resolve())
        with self.assertRaisesRegex(ValueError, "digest"):
            check_install.validate_package(package_root, "0" * 64)

    def test_install_refuses_an_unqualified_host_before_installing(self):
        plugin = self.base / "configured"
        root = self.base / "selected"
        root.mkdir()
        release.package.build(binary=self.engine, sha256=self.digest, target="linux/amd64",
                              root=root, output=plugin, version="0.1.0")
        home = self.base / "check-home"
        with mock.patch("check_install.subprocess.check_output", return_value="GitHub Copilot CLI 99.0.0.\n"):
            with self.assertRaisesRegex(ValueError, "qualified Copilot"):
                check_install.check("fixture-copilot", plugin, self.digest, home)
        self.assertEqual(list(home.iterdir()), [])

    def test_checksum_inventory_is_exact_before_packaging(self):
        for text in ("", "0" * 64 + "  ../engine\n", "0" * 64 + "  kmx-linux-amd64\n"):
            with self.subTest(text=text), self.assertRaisesRegex(ValueError, "inventory"):
                release.read_inventory(text)
        text = "".join(self.digest + "  kmx-" + p.replace("/", "-") + "\n" for p in release.TARGETS)
        self.assertEqual(len(release.read_inventory(text)), 4)
        with self.assertRaisesRegex(ValueError, "inventory"):
            release.read_inventory(text + text.splitlines()[0] + "\n")


if __name__ == "__main__":
    unittest.main()
