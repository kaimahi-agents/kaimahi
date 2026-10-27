"""Pinned catalog contracts; opt-in installer checks use private host profiles."""
from __future__ import annotations

import copy
import json
import os
import posixpath
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
CATALOG = ".github/plugin/marketplace.json"


def pinned_payload(root: Path, entry: dict) -> dict[str, bytes]:
    """Inspect the pinned inventory, including Git links, never the working tree."""
    sha, directory = entry["source"]["sha"], entry["source"]["path"]
    def blob(path):
        return subprocess.run(["git", "show", f"{sha}:{path}"], cwd=root, check=True,
                              capture_output=True, timeout=15).stdout
    tree = subprocess.run(["git", "ls-tree", "-r", "-z", sha, directory], cwd=root,
                          check=True, capture_output=True, timeout=15).stdout
    files = {}
    for row in tree.split(b"\0"):
        if not row:
            continue
        metadata, raw_path = row.split(b"\t", 1)
        path = raw_path.decode("utf-8")
        relative = path[len(directory) + 1:]
        if relative not in ("plugin.json", "mcp.json") and not relative.startswith(("skills/", "com.github.copilot/")):
            continue
        content = blob(path)
        if metadata.split()[0] == b"120000":
            target = posixpath.normpath(posixpath.join(posixpath.dirname(path), content.decode("utf-8")))
            content = blob(target)
        files[relative] = content
    return files


class MarketplaceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.git("init", "-q")
        for directory, name in (("copilot-wsl", "kmx-agent-builder"), ("copilot", "kmx-agent-builder-path")):
            path = self.root / "plugins" / directory / "plugin.json"
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps({"name": name, "version": "0.9.0-rc.3"}))
        self.git("add", "plugins")
        self.git("-c", "user.name=Catalog Test", "-c", "user.email=catalog@example.invalid",
                 "commit", "-qm", "fixture")
        self.sha = self.git("rev-parse", "HEAD").strip()
        self.catalog: dict = {"name": "kmx-plugins", "owner": {"name": "kaimahi-agents"}, "plugins": [
            {"name": name, "version": "0.9.0-rc.3", "source": {"source": "github",
             "repo": "kaimahi-agents/kaimahi", "sha": self.sha, "path": "plugins/" + directory}}
            for directory, name in (("copilot-wsl", "kmx-agent-builder"), ("copilot", "kmx-agent-builder-path"))]}
        self.write_catalog()

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, capture_output=True,
                              text=True, check=True, timeout=15).stdout

    def write_catalog(self):
        path = self.root / CATALOG
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(self.catalog))

    def check(self, *args):
        return subprocess.run([sys.executable, str(HERE / "marketplace.py"), *args], cwd=self.root,
                              capture_output=True, text=True, check=False, timeout=15)

    def test_pinned_manifests_not_working_tree_define_version_and_name(self):
        # Main may advance while the catalog still advertises the previous release.
        path = self.root / "plugins/copilot/plugin.json"
        path.write_text(json.dumps({"name": "not-the-release", "version": "1.0.0"}))
        before = path.read_bytes()
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(path.read_bytes(), before)

    def test_frozen_json_is_utf8_even_with_non_utf8_process_locale(self):
        path = self.root / "plugins/copilot-wsl/plugin.json"
        value = json.loads(path.read_text())
        value["description"] = "Māori agent examples"
        path.write_text(json.dumps(value, ensure_ascii=False), encoding="utf-8")
        self.git("add", "plugins")
        self.git("-c", "user.name=Catalog Test", "-c", "user.email=catalog@example.invalid",
                 "commit", "-qm", "UTF-8 manifest")
        for entry in self.catalog["plugins"]:
            entry["source"]["sha"] = self.git("rev-parse", "HEAD").strip()
        self.write_catalog()
        result = subprocess.run([sys.executable, str(HERE / "marketplace.py")], cwd=self.root,
                                env={**os.environ, "LC_ALL": "C", "PYTHONUTF8": "0", "PYTHONCOERCECLOCALE": "0"},
                                check=False, capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_invalid_entries_refuse_specific_pin_contracts(self):
        original = copy.deepcopy(self.catalog)
        cases = [("sha", "main", "full commit SHA"), ("repo", "other/repo", "source repository"),
                 ("path", "plugins/copilot", "source path"), ("source", "url", "GitHub source")]
        for key, value, reason in cases:
            with self.subTest(key=key):
                self.catalog = copy.deepcopy(original)
                self.catalog["plugins"][0]["source"][key] = value
                self.write_catalog()
                result = self.check()
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(reason, result.stderr)
        self.catalog = copy.deepcopy(original)
        self.catalog["plugins"][1] = self.catalog["plugins"][0]
        self.write_catalog()
        self.assertIn("two variant identities", self.check().stderr)

    def test_catalog_version_must_equal_frozen_manifest(self):
        self.catalog["plugins"][0]["version"] = "0.9.0"
        self.write_catalog()
        result = self.check()
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("pinned manifest", result.stderr)

    def test_different_pins_and_noncommit_objects_refuse(self):
        original = copy.deepcopy(self.catalog)
        self.catalog["plugins"][1]["source"]["sha"] = "f" * 40
        self.write_catalog()
        self.assertIn("same release commit", self.check().stderr)
        for sha in ("f" * 40, self.git("rev-parse", "HEAD^{tree}").strip()):
            self.catalog = copy.deepcopy(original)
            for entry in self.catalog["plugins"]:
                entry["source"]["sha"] = sha
            self.write_catalog()
            result = self.check()
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertIn("commit object", result.stderr)

    def test_payload_inventory_comes_from_pin_not_newer_main(self):
        path = self.root / "plugins/copilot/com.github.copilot/extensions/agent-view/later.mjs"
        path.parent.mkdir(parents=True)
        path.write_text("new asset on main")
        self.git("add", "plugins")
        self.git("-c", "user.name=Catalog Test", "-c", "user.email=catalog@example.invalid",
                 "commit", "-qm", "newer asset")
        entry = self.catalog["plugins"][1]
        self.assertEqual(set(pinned_payload(self.root, entry)), {"plugin.json"})
        entry["source"]["sha"] = self.git("rev-parse", "HEAD").strip()
        self.assertEqual(pinned_payload(self.root, entry)[
            "com.github.copilot/extensions/agent-view/later.mjs"], b"new asset on main")

    def test_payload_resolves_git_links_without_host_symlinks(self):
        relative = "com.github.copilot/extensions/agent-view/shared.mjs"
        source = self.root / "plugins/copilot-wsl" / relative
        source.parent.mkdir(parents=True)
        source.write_bytes(b"canonical source bytes")
        self.git("add", "plugins")
        link = subprocess.run(["git", "hash-object", "-w", "--stdin"], cwd=self.root,
                              input=("../../../../copilot-wsl/" + relative).encode(),
                              check=True, capture_output=True, timeout=15).stdout.decode().strip()
        self.git("update-index", "--add", "--cacheinfo", "120000," + link + ",plugins/copilot/" + relative)
        self.git("-c", "user.name=Catalog Test", "-c", "user.email=catalog@example.invalid",
                 "commit", "-qm", "Git link fixture")
        entry = self.catalog["plugins"][1]
        entry["source"]["sha"] = self.git("rev-parse", "HEAD").strip()
        self.assertEqual(pinned_payload(self.root, entry)[relative], b"canonical source bytes")

    def test_release_gate_refuses_absent_or_different_tag_before_network(self):
        result = self.check("--release")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("release tag", result.stderr)
        self.git("tag", "v0.9.0-rc.3", self.sha)
        path = self.root / "extra"
        path.write_text("later commit")
        self.git("add", "extra")
        self.git("-c", "user.name=Catalog Test", "-c", "user.email=catalog@example.invalid",
                 "commit", "-qm", "later")
        later = self.git("rev-parse", "HEAD").strip()
        for entry in self.catalog["plugins"]:
            entry["source"]["sha"] = later
        self.write_catalog()
        result = self.check("--release")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("tag commit", result.stderr)

    def test_published_metadata_requires_tag_and_all_binary_assets(self):
        import marketplace
        tag = "v0.9.0-rc.3"
        info = {"tag_name": tag, "draft": False, "prerelease": True, "assets": [
            {"name": name} for name in ("checksums.txt", "kmx-linux-amd64", "kmx-linux-arm64",
                                       "kmx-darwin-amd64", "kmx-darwin-arm64")]}
        marketplace.release_metadata(info, tag)
        for change in ({"draft": True}, {"tag_name": "v0.9.0-rc.2"}, {"assets": info["assets"][1:]}):
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, "published release|release assets"):
                marketplace.release_metadata({**info, **change}, tag)


@unittest.skipUnless(os.environ.get("KMX_MARKETPLACE_COPILOT") and os.environ.get("KMX_MARKETPLACE_SOURCE"),
                     "opt-in remote Git catalog check; local directory catalogs load live")
class InstalledMarketplaceTests(unittest.TestCase):
    def test_variants_migrate_refresh_and_keep_pinned_installed_bytes(self):
        import marketplace
        root = HERE.parents[1]
        entries = marketplace.check(root)
        for entry in entries:
            with self.subTest(variant=entry["name"]), tempfile.TemporaryDirectory(prefix="kmx marketplace ") as temp:
                home = Path(temp)
                env = {**os.environ, "COPILOT_HOME": str(home / "profile"), "COPILOT_CACHE_HOME": str(home / "cache")}
                def host(*args, env=env):
                    result = subprocess.run([os.environ["KMX_MARKETPLACE_COPILOT"], "--no-auto-update", *args],
                                            env=env, check=False, capture_output=True, text=True, timeout=120)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    return result.stdout

                def installed(home=home):
                    return list((home / "profile/installed-plugins").rglob("plugin.json"))

                self.assertIn("1.0.102", host("--version"))
                host("plugin", "install", str(root / entry["source"]["path"]))
                old = installed()
                self.assertEqual(len(old), 1)
                old_name = json.loads(old[0].read_text())["name"]
                host("plugin", "uninstall", old_name)
                self.assertFalse(installed(), "direct copy must be gone before marketplace install")
                host("plugin", "marketplace", "add", os.environ["KMX_MARKETPLACE_SOURCE"])
                catalogs = list((home / "cache/marketplaces").glob("*/.github/plugin/marketplace.json"))
                self.assertEqual(len(catalogs), 1, "expected one remote catalog checkout")
                expected_commit = os.environ.get("KMX_MARKETPLACE_COMMIT") or marketplace.git(root, "rev-parse", "HEAD")
                actual_commit = marketplace.git(catalogs[0].parents[2], "rev-parse", "HEAD")
                self.assertEqual(actual_commit, expected_commit, "catalog ref moved away from this run's head")
                host("plugin", "install", entry["name"] + "@kmx-plugins")

                def check_cache(entry=entry):
                    host("plugin", "list")
                    manifests = installed()
                    self.assertEqual(len(manifests), 1, "installed copy must survive a fresh list command")
                    cache = manifests[0].parent
                    self.assertEqual(cache.parent.name, "kmx-plugins")
                    manifest = json.loads(manifests[0].read_text())
                    self.assertEqual((manifest["name"], manifest["version"]), (entry["name"], entry["version"]))
                    payload = pinned_payload(root, entry)
                    self.assertIn("mcp.json", payload)
                    self.assertTrue(any(name.startswith("skills/") and name.endswith("/SKILL.md") for name in payload))
                    for relative, expected in payload.items():
                        self.assertEqual((cache / relative).read_bytes(), expected, relative)
                    self.assertFalse(any(p.is_symlink() for p in cache.rglob("*")))
                    self.assertFalse((cache / "bin").exists())

                check_cache()
                host("plugin", "marketplace", "update", "kmx-plugins")
                host("plugin", "update", entry["name"])
                check_cache()
                host("plugin", "uninstall", entry["name"])
                host("plugin", "list")
                self.assertFalse(installed())


if __name__ == "__main__":
    unittest.main()
