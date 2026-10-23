"""Installer boundary tests; downloads/host commands are explicit local fixtures."""
import hashlib
import io
import json
import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent


@unittest.skipIf(os.name == "nt", "bootstrap runs on macOS/Linux/WSL, not native Windows")
class TryItTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="try it tests ")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.home = self.base / "home"
        self.home.mkdir()
        self.assets = self.base / "assets"
        self.assets.mkdir()
        self.bin = self.base / "commands"
        self.bin.mkdir()
        self.root = self.home / "my project"
        self.env = {**os.environ, "HOME": str(self.home), "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
                    "FIXTURE_ASSETS": str(self.assets), "FIXTURE_CALLS": str(self.base / "calls")}
        self.stub("curl", '''#!/bin/sh
if [ "$1" = "-fsSLI" ]; then printf '%s' 'https://github.com/kaimahi-agents/kaimahi/releases/tag/v0.9.0-rc.1'; exit; fi
while [ "$#" -gt 0 ]; do case "$1" in -o) out=$2; shift 2;; https:*) url=$1; shift;; *) shift;; esac; done
cp "$FIXTURE_ASSETS/${url##*/}" "$out"
''')
        self.stub("copilot", '''#!/bin/sh
printf '%s\\n' "$*" >> "$FIXTURE_CALLS"
case "$*" in *'plugin list --json'*) printf '[]';; *'--version'*) printf 'GitHub Copilot CLI test';; esac
''')
        self.stub("github", '#!/bin/sh\nprintf \'app %s\\n\' "$*" >> "$FIXTURE_CALLS"\n')
        self.bundle()

    def stub(self, name, source):
        path = self.bin / name
        path.write_text(source)
        path.chmod(0o755)

    def bundle(self, **changes):
        target = subprocess.check_output(["uname", "-s"], text=True).strip()
        arch = subprocess.check_output(["uname", "-m"], text=True).strip()
        self.platform = ("darwin" if target == "Darwin" else "linux") + "-" + ("arm64" if arch in ("arm64", "aarch64") else "amd64")
        self.name = "kmx-agent-builder-" + self.platform + ".tar.gz"
        engine = b'''#!/bin/sh
case "$*" in 'model list --provider copilot -o json') printf '[{"model":"claude-haiku-5.5"}]';; *) exit 0;; esac
'''
        files = {"bin/kmx": engine, "plugin.json": b'{"name":"kmx-agent-builder","version":"0.9.0-rc.1"}',
                 "mcp.json": b'{"mcpServers":{"kmx-agent-builder":{"type":"stdio","command":"./bin/kmx","args":["mcp","--root","__SELECTED_ROOT__"]}}}',
                 "selected-root.json": b'{"root":"__SELECTED_ROOT__"}',
                 "engine.json": json.dumps({"schemaVersion": 1, "version": "v0.9.0-rc.1", "platform": self.platform.replace("-", "/"), "sha256": hashlib.sha256(engine).hexdigest()}).encode(),
                 "try-it.json": b'{"schemaVersion":1}',
                 "samples/release-writer.agent.md": b'# Release writer\n- **Preferred:** claude-haiku-5.5\njudgeModel: claude-haiku-5.5\n',
                 "skills/kmx-agent-builder/SKILL.md": b'fixture skill', "SETUP.md": b'fixture setup'}
        for name in ("extension.mjs", "view.mjs", "index.html", "app.js", "style.css", "authoring.mjs"):
            files["com.github.copilot/extensions/agent-view/" + name] = b'fixture asset'
        files.update(changes)
        with tarfile.open(self.assets / self.name, "w:gz") as archive:
            for name, data in files.items():
                if data is None:
                    continue
                entry = tarfile.TarInfo("kmx-agent-builder/" + name)
                entry.size = len(data)
                entry.mode = 0o755 if name == "bin/kmx" else 0o644
                archive.addfile(entry, io.BytesIO(data))
        digest = hashlib.sha256((self.assets / self.name).read_bytes()).hexdigest()
        (self.assets / "plugin-checksums.txt").write_text(digest + "  " + self.name + "\n")

    def run_setup(self, *args, answers=None):
        return subprocess.run(["sh", str(HERE / "try-it.sh"), "--version", "v0.9.0-rc.1", *args],
                              input=answers if answers is not None else str(self.root) + "\ny\n\n", env=self.env,
                              text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30, check=False)

    def test_dry_run_binds_confirmed_root_and_copies_sample(self):
        result = self.run_setup("--dry-run")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue((self.root / "release-writer.agent.md").is_file())
        configs = list(self.home.glob(".local/share/kmx-try-it/run.*/kmx-agent-builder/mcp.json"))
        self.assertEqual(len(configs), 1, result.stdout)
        server = json.loads(configs[0].read_text())["mcpServers"]["kmx-agent-builder"]
        self.assertEqual(server["args"], ["mcp", "--root", str(self.root.resolve()), "--provider", "copilot"])
        self.assertFalse((self.base / "calls").exists())

    def test_refused_root_creates_no_workspace(self):
        result = self.run_setup("--dry-run", answers=str(self.root) + "\nn\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.root.exists())

    def test_bad_checksum_never_extracts_or_runs(self):
        (self.assets / "plugin-checksums.txt").write_text("0" * 64 + "  " + self.name + "\n")
        result = self.run_setup("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("checksum", result.stdout.lower())
        self.assertFalse(self.root.exists())
        self.assertFalse((self.base / "calls").exists())
        self.assertFalse(list(self.home.glob(".local/share/kmx-try-it/run.*/kmx-agent-builder")))

    def test_missing_authoring_asset_refuses_incomplete_bundle(self):
        self.bundle(**{"com.github.copilot/extensions/agent-view/authoring.mjs": None})
        result = self.run_setup("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no compatible try-it bundle", result.stdout)
        self.assertFalse(self.root.exists())

    def test_missing_try_it_marker_refuses_old_bundle(self):
        self.bundle(**{"try-it.json": None})
        result = self.run_setup("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("try-it", result.stdout.lower())
        self.assertFalse(self.root.exists())

    def test_duplicate_digest_refuses(self):
        path = self.assets / "plugin-checksums.txt"
        path.write_text(path.read_text() * 2)
        result = self.run_setup("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.root.exists())

    def test_existing_sample_is_never_overwritten(self):
        self.root.mkdir()
        sample = self.root / "release-writer.agent.md"
        sample.write_text("my existing file")
        result = self.run_setup("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(sample.read_text(), "my existing file")

    def test_unsafe_root_and_version_refuse(self):
        for root in ("/", str(self.home / '${PLUGIN_ROOT}'), str(self.home / 'a"b'), str(self.home / 'a\\b')):
            with self.subTest(root=root):
                result = self.run_setup("--dry-run", answers=root + "\ny\n")
                self.assertNotEqual(result.returncode, 0)
        result = subprocess.run(["sh", str(HERE / "try-it.sh"), "--version", "../bad"], env=self.env, capture_output=True, check=False)
        self.assertNotEqual(result.returncode, 0)

    def test_install_and_launch_are_separate_argv_operations(self):
        result = self.run_setup(answers=str(self.root) + "\ny\n\ny\n")
        self.assertEqual(result.returncode, 0, result.stdout)
        calls = (self.base / "calls").read_text()
        self.assertIn("plugin install ", calls)
        self.assertIn("app open " + str(self.root.resolve()), calls)

    def test_archive_traversal_is_rejected_before_extraction(self):
        self.bundle(**{"../escape": b'bad'})
        result = self.run_setup("--dry-run")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.root.exists())
        self.assertFalse(list(self.home.rglob("escape")))

    def test_windows_binding_uses_explicit_distro_exec_and_checked_wsl_engine(self):
        if not self.platform.startswith("linux"):
            self.skipTest("WSL host setup is Linux-only")
        self.env["WSL_DISTRO_NAME"] = "Test Ubuntu"
        self.stub("wsl.exe", '#!/bin/sh\n[ "$1" = -d ] && [ "$2" = "Test Ubuntu" ] && [ "$3" = -e ] || exit 9\nshift 3\ncat >/dev/null\nexec "$@"\n')
        self.stub("wslpath", '#!/bin/sh\nif [ "$1" = -u ]; then printf "%s/win-profile" "$HOME"; else printf "C:/fixture%s" "$2"; fi\n')
        self.stub("cmd.exe", '#!/bin/sh\nprintf "C:/fixture-profile"\n')
        self.stub("copilot.exe", (self.bin / "copilot").read_text())
        self.stub("github.exe", (self.bin / "github").read_text())
        result = self.run_setup("--windows")
        self.assertEqual(result.returncode, 0, result.stdout)
        source = next(self.home.glob(".local/share/kmx-try-it/run.*/kmx-agent-builder"))
        config = json.loads((source / "mcp.json").read_text())["mcpServers"]["kmx-agent-builder"]
        self.assertEqual(config["command"], "wsl.exe")
        self.assertEqual(config["args"], ["-d", "Test Ubuntu", "-e", str(source / "bin/kmx"),
                                        "mcp", "--root", str(self.root.resolve()), "--provider", "copilot"])
        cache = next(self.home.glob("win-profile/.kmx-try-it/run.*/kmx-agent-builder"))
        self.assertEqual((cache / "mcp.json").read_bytes(), (source / "mcp.json").read_bytes())

    def test_declining_replacement_does_not_create_sample(self):
        self.stub("copilot", '#!/bin/sh\nprintf \'[{"name":"kmx-agent-builder"}]\'\n')
        result = self.run_setup(answers=str(self.root) + "\ny\n\nn\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Existing plugin left unchanged", result.stdout)
        self.assertFalse(self.root.exists())

    def test_failed_install_retains_sample_and_prints_recovery(self):
        self.stub("copilot", '#!/bin/sh\ncase "$*" in *"plugin list --json"*) printf "[]";; *) echo PRIVATE_HOST_CANARY >&2; exit 1;; esac\n')
        result = self.run_setup()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Copilot refused installation", result.stdout)
        self.assertIn("To retry or refresh installation", result.stdout)
        self.assertTrue((self.root / "release-writer.agent.md").is_file())
        self.assertNotIn("PRIVATE_HOST_CANARY", result.stdout)
        log = next(self.home.glob(".local/share/kmx-try-it/run.*/setup.log"))
        self.assertNotIn("PRIVATE_HOST_CANARY", log.read_text())

    def test_authentication_refusal_does_not_create_sample_or_log_tool_output(self):
        self.bundle(**{"bin/kmx": b'#!/bin/sh\necho PRIVATE_CREDENTIAL_CANARY >&2\nexit 1\n'})
        # Keep the engine metadata coherent so this tests auth, not hash refusal.
        # bundle's engine digest is deliberately unchanged; rewrite the fixture
        # through the same tar API with matching metadata.
        archive = self.assets / self.name
        with tarfile.open(archive) as source:
            rows = []
            for member in source:
                stream = source.extractfile(member)
                assert stream is not None
                rows.append((member, stream.read()))
        digest = hashlib.sha256(next(d for m, d in rows if m.name.endswith("bin/kmx"))).hexdigest()
        with tarfile.open(archive, "w:gz") as out:
            for member, data in rows:
                if member.name.endswith("engine.json"):
                    metadata = json.loads(data)
                    metadata["sha256"] = digest
                    data = json.dumps(metadata).encode()
                    member.size = len(data)
                out.addfile(member, io.BytesIO(data))
        (self.assets / "plugin-checksums.txt").write_text(hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + self.name + "\n")
        result = self.run_setup(answers=str(self.root) + "\ny\nn\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Sign-in declined", result.stdout)
        self.assertFalse(self.root.exists())
        log = next(self.home.glob(".local/share/kmx-try-it/run.*/setup.log"))
        self.assertNotIn("PRIVATE_CREDENTIAL_CANARY", log.read_text())
        self.assertNotIn("PRIVATE_CREDENTIAL_CANARY", result.stdout)


@unittest.skipUnless(os.environ.get("KMX_TRY_IT_COPILOT"), "opt-in real isolated CLI install")
class InstalledTryItTests(unittest.TestCase):
    # Reuse only fixture construction, not all the inherited boundary tests.
    setUp = TryItTests.setUp
    stub = TryItTests.stub
    bundle = TryItTests.bundle
    def test_real_dry_run_then_isolated_install_uninstall_reinstall(self):
        import check_install
        import release
        binary = os.environ.get("KMX_TRY_IT_BINARY")
        archive = os.environ.get("KMX_TRY_IT_ARCHIVE")
        version = os.environ.get("KMX_TRY_IT_VERSION", "v0.9.0-rc.1")
        if archive:
            source = Path(archive)
        else:
            assert binary, "set KMX_TRY_IT_BINARY or KMX_TRY_IT_ARCHIVE"
            source = self.base / "native.tar.gz"
            release.archive(binary=Path(binary), sha256=hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
                            target=self.platform.replace("-", "/"), version=version,
                            revision="a" * 40, output=source)
        import shutil
        shutil.copyfile(source, self.assets / self.name)
        (self.assets / "plugin-checksums.txt").write_text(
            hashlib.sha256(source.read_bytes()).hexdigest() + "  " + self.name + "\n")
        result = subprocess.run(["sh", str(HERE / "try-it.sh"), "--version", version, "--dry-run"],
                                input=str(self.root) + "\ny\n", env=self.env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60, check=False)
        self.assertEqual(result.returncode, 0, result.stdout)
        plugin = next(self.home.glob(".local/share/kmx-try-it/run.*/kmx-agent-builder"))
        digest = json.loads((plugin / "engine.json").read_text())["sha256"]
        check_install.check(os.environ["KMX_TRY_IT_COPILOT"], plugin, digest,
                            self.base / "isolated-copilot-profile")


if __name__ == "__main__":
    unittest.main()
