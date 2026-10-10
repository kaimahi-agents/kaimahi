#!/usr/bin/env python3
"""Exercise the real Linux shell runner with bounded Go/Docker/provider doubles."""
from __future__ import annotations

import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[1]
RUNNER = ROOT / "scripts/ci/live-eval-loop.sh"
SECRET = "runner-test-" + "selected-secret"
INHERITED_MODEL = "inherited-" + "model-secret"
INHERITED_OPENAI = "inherited-" + "openai-secret"
RAW = "RAW-PROVIDER-PAYLOAD-DO-NOT-PRINT"

DAEMON = '''#!/usr/bin/env python3
import json, os, signal, socket, sys, time
from pathlib import Path
root = Path(os.environ['TEST_STATE'])
args = sys.argv[1:]
(root / 'daemon.json').write_text(json.dumps({'argv': args, 'key': os.getenv('MODEL_API_KEY'), 'openai': os.getenv('OPENAI_API_KEY'), 'selected': os.getenv('TEST_PROVIDER_KEY')}))
(root / 'daemon.pid').write_text(str(os.getpid()))
def stop(*args):
    (root / 'daemon.stopped').touch()
    sys.exit(0)
signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
print('RAW-PROVIDER-PAYLOAD-DO-NOT-PRINT', flush=True)
print(os.getenv('MODEL_API_KEY', ''), file=sys.stderr, flush=True)
if os.getenv('TEST_DAEMON_FAIL') == '1':
    sys.exit(1)
addr = args[args.index('-addr') + 1]
host, port = addr.rsplit(':', 1)
s = socket.socket()
s.bind((host, int(port)))
s.listen()
while True:
    conn, _ = s.accept()
    conn.close()
'''

GO = '''#!/usr/bin/env python3
import json, os, shutil, sys
from pathlib import Path
root = Path(os.environ['TEST_STATE'])
with (root / 'go.jsonl').open('a') as f:
    f.write(json.dumps({'argv': sys.argv[1:], 'cwd': os.getcwd(), 'key': os.getenv('MODEL_API_KEY'), 'selected': os.getenv('TEST_PROVIDER_KEY'), 'gowork': os.getenv('GOWORK')}) + '\\n')
print('RAW-PROVIDER-PAYLOAD-DO-NOT-PRINT', file=sys.stderr)
if sys.argv[1] == 'list':
    print(os.environ['TEST_MODULE'])
elif sys.argv[1] == 'build':
    out = Path(sys.argv[sys.argv.index('-o') + 1])
    shutil.copy2(os.environ['TEST_DAEMON'] if 'agentsessionsd' in str(out) else os.environ['TEST_KMX'], out)
else:
    sys.exit(2)
'''

DOCKER = '''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
root = Path(os.environ['TEST_STATE'])
args = sys.argv[1:]
with (root / 'docker.jsonl').open('a') as f:
    f.write(json.dumps(args) + '\\n')
print('RAW-PROVIDER-PAYLOAD-DO-NOT-PRINT', file=sys.stderr)
if args[0] == 'info':
    print('8')
elif args[0] == 'inspect':
    print('true')
elif args[0] == 'run':
    (root / 'container').write_text(args[args.index('--name') + 1])
    print('container-id')
elif args[0] == 'stop':
    (root / 'model.stopped').touch()
elif args[0] == 'rm':
    if os.getenv('TEST_DOCKER_RM_FAIL') == '1':
        sys.exit(1)
    (root / 'container.removed').touch()
elif args[0] == 'logs':
    print('RAW-PROVIDER-PAYLOAD-DO-NOT-PRINT')
'''

CURL = '''#!/usr/bin/env python3
import os, shutil, sys
args = sys.argv[1:]
print('RAW-PROVIDER-PAYLOAD-DO-NOT-PRINT', file=sys.stderr)
if '-o' in args or '--output' in args:
    flag = '-o' if '-o' in args else '--output'
    output = args[args.index(flag) + 1]
    if 'chat/completions' in args[-1]:
        from pathlib import Path
        Path(output).write_text('{"model":"qwen-3.5-2b","created":1,"choices":[{"message":{"content":"warmup"}}]}')
    else:
        shutil.copy2(os.environ['TEST_ARCHIVE'], output)
'''

GATE = '''#!/usr/bin/env python3
import argparse, json, os, sys, time
from pathlib import Path
p = argparse.ArgumentParser()
p.add_argument('mode', choices=['prepare', 'evaluate', 'verify'])
for name in ['kmx', 'bundle', 'sessions', 'artifacts', 'case-timeout', 'verify-timeout', 'command-timeout']:
    p.add_argument('--' + name)
a = p.parse_args()
root = Path(os.environ['TEST_STATE'])
with (root / 'gate.jsonl').open('a') as f:
    f.write(json.dumps({'argv': sys.argv[1:], 'mode': a.mode, 'bundle': a.bundle, 'home': os.getenv('KMX_HOME'), 'no_color': os.getenv('NO_COLOR'), 'selected': os.getenv('TEST_PROVIDER_KEY'), 'key': os.getenv('MODEL_API_KEY'), 'model_stopped': (root / 'model.stopped').exists()}) + '\\n')
if a.mode == 'prepare':
    if os.getenv('TEST_FAIL') == 'prepare':
        print('error: tracked bundle preflight failed', file=sys.stderr)
        sys.exit(1)
    sys.exit(0)
artifacts = Path(a.artifacts)
(artifacts / ('eval-test.json' if a.mode == 'evaluate' else 'verify-eval-test.json.json')).write_text('{}')
if os.getenv('TEST_HANG') == a.mode:
    (root / 'gate.waiting').touch()
    time.sleep(60)
if os.getenv('TEST_FAIL') == a.mode:
    print('error: evaluation gate did not pass' if a.mode == 'evaluate' else 'error: verification gate did not pass', file=sys.stderr)
    sys.exit(1)
print(a.mode + ': concise success')
'''


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="kmx-runner-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "action-repo"
        (self.repo / "scripts/ci").mkdir(parents=True)
        self.script = self.repo / "scripts/ci/live-eval-loop.sh"
        shutil.copy2(RUNNER, self.script)
        (self.repo / "scripts/ci/eval-loop.py").write_text(GATE)
        for name in ("eval-loop-model.yaml", "eval-loop-warmup.json"):
            shutil.copy2(ROOT / "scripts/ci" / name, self.repo / "scripts/ci" / name)
        sample = self.repo / "internal/kmx/app/testdata/live-eval-loop"
        sample.mkdir(parents=True)
        self.bundle = self.root / "caller" / "bundle with spaces"
        self.bundle.mkdir(parents=True)
        self.state = self.root / "state"
        self.state.mkdir()
        self.work = self.root / "work"
        self.work.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.module = self.root / "pinned-module"
        self.module.mkdir()
        self.daemon = self.executable("agentsessionsd", DAEMON)
        self.kmx = self.executable("kmx", "#!/usr/bin/env python3\n")
        for name, content in (("go", GO), ("docker", DOCKER), ("curl", CURL), ("jq", "#!/usr/bin/env python3\n")):
            self.executable(name, content)
        self.artifacts = self.root / "evidence"
        self.output = self.root / "github-output"
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ["PATH"],
                        RUNNER_TEMP=str(self.work), KMX_BIN=str(self.kmx),
                        BUNDLE_DIR=str(self.bundle), ARTIFACT_DIR=str(self.artifacts),
                        MODEL_MODE="endpoint", MODEL_NAME="caller-model",
                        MODEL_BASE_URL="https://provider.example/v1", MODEL_KEY_ENV="TEST_PROVIDER_KEY",
                        TEST_PROVIDER_KEY=SECRET, MODEL_API_KEY=INHERITED_MODEL,
                        OPENAI_API_KEY=INHERITED_OPENAI, SESSIONS_PORT=str(port),
                        GITHUB_OUTPUT=str(self.output), TEST_STATE=str(self.state),
                        TEST_MODULE=str(self.module), TEST_DAEMON=str(self.daemon), TEST_KMX=str(self.kmx))
        for name in ("SESSIONS_ARCHIVE_URL", "SESSIONS_ARCHIVE_SHA256", "AIKIT_IMAGE", "AIKIT_CONFIG", "VERIFY", "TEST_FAIL", "TEST_HANG", "TEST_DAEMON_FAIL", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS"):
            self.env.pop(name, None)

    def executable(self, name, content):
        path = self.bin / name
        path.write_text(content)
        path.chmod(0o755)
        return path

    def run_runner(self, shell_flags=(), **overrides):
        result = subprocess.run(["bash", *shell_flags, str(self.script)], cwd=self.bundle.parent,
                                env=dict(self.env, **overrides), capture_output=True,
                                text=True, timeout=15)
        self.assert_private(result.stdout + result.stderr)
        return result

    def assert_private(self, text):
        for forbidden in (SECRET, RAW, INHERITED_MODEL, INHERITED_OPENAI):
            self.assertNotIn(forbidden, text)

    def records(self, name):
        path = self.state / (name + ".jsonl")
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def outputs(self):
        return dict(line.split("=", 1) for line in self.output.read_text().splitlines())

    def assert_cleaned(self):
        self.assertTrue((self.state / "daemon.stopped").exists())
        self.assertEqual(list(self.work.iterdir()), [])
        self.assertTrue(self.artifacts.is_dir(), "never delete artifact directory during cleanup")

    def test_endpoint_keeps_caller_bundle_and_secret_only_in_daemon_environment(self):
        result = self.run_runner()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.records("gate")
        self.assertEqual([c["mode"] for c in calls], ["prepare", "evaluate", "verify"])
        self.assertTrue(all(c["bundle"] == str(self.bundle) for c in calls))
        self.assertTrue(all(c["selected"] == SECRET for c in calls), "gate needs selected key for redaction")
        self.assertTrue(all(c["home"].endswith("/.kmx") and c["no_color"] == "1" for c in calls))
        daemon = json.loads((self.state / "daemon.json").read_text())
        self.assertEqual(daemon["key"], SECRET)
        self.assertEqual(daemon["openai"], "")
        self.assertFalse(daemon["selected"])
        self.assert_private(json.dumps(daemon["argv"]))
        self.assertIn("caller-model", daemon["argv"])
        self.assertEqual(self.records("docker"), [], "endpoint mode must not own caller model")
        self.assertTrue(all(not c["model_stopped"] for c in calls))
        go = self.records("go")
        self.assertEqual(go[0]["cwd"], str(self.repo))
        self.assertTrue(all(not c["key"] and not c["selected"] for c in go))
        self.assertTrue(all(c["gowork"] == "off" for c in go))
        outputs = self.outputs()
        self.assertEqual(outputs["artifact-dir"], str(self.artifacts))
        self.assertEqual(outputs["receipt"], str(self.artifacts / "eval-test.json"))
        self.assertEqual(outputs["verify-report"], str(self.artifacts / "verify-eval-test.json.json"))
        self.assertEqual(outputs["daemon-install"], "source-fallback")
        self.assertGreaterEqual(int(outputs["elapsed-seconds"]), 0)
        self.assertEqual(self.artifacts.stat().st_mode & 0o777, 0o700)
        self.assert_cleaned()

    def test_inherited_shell_tracing_cannot_expose_provider_payloads(self):
        result = self.run_runner(shell_flags=("-xv",))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_private(result.stdout + result.stderr)
        self.assert_cleaned()

    def test_propagates_bounded_gate_timeouts(self):
        result = self.run_runner(CASE_TIMEOUT="17s", VERIFY_TIMEOUT="23s", COMMAND_TIMEOUT="42")
        self.assertEqual(result.returncode, 0, result.stderr)
        evaluate, verify = self.records("gate")[1:]
        self.assertEqual(evaluate["argv"][1:5], ["--case-timeout", "17s", "--command-timeout", "42"])
        self.assertEqual(verify["argv"][1:5], ["--verify-timeout", "23s", "--command-timeout", "42"])
        self.assert_cleaned()

    def test_verify_false_skips_replay(self):
        result = self.run_runner(VERIFY="false")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([c["mode"] for c in self.records("gate")], ["prepare", "evaluate"])
        self.assertNotIn("verify-report", self.outputs())
        self.assert_cleaned()

    def test_evaluation_failure_keeps_evidence_and_never_dumps_logs(self):
        result = self.run_runner(TEST_FAIL="evaluate")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual([c["mode"] for c in self.records("gate")], ["prepare", "evaluate"])
        self.assertIn("receipt", self.outputs())
        self.assert_cleaned()

    def test_verification_failure_preserves_report_and_exit_status(self):
        result = self.run_runner(TEST_FAIL="verify")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("verification gate did not pass", result.stderr)
        self.assertIn("verify-report", self.outputs())
        self.assert_cleaned()

    def test_aikit_stops_before_replay_and_removes_only_owned_container(self):
        result = self.run_runner(MODEL_MODE="aikit", MODEL_KEY_ENV="", MODEL_NAME="", MODEL_BASE_URL="")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.records("gate")[-1]["model_stopped"])
        docker = self.records("docker")
        self.assertEqual([c[0] for c in docker], ["info", "run", "inspect", "stop", "rm"])
        run = next(c for c in docker if c[0] == "run")
        self.assertIn("4", run)
        self.assertRegex(run[-2], r"@sha256:[a-f0-9]{64}$")
        owned = run[run.index("--name") + 1]
        self.assertEqual(docker[-2][-1], owned)
        self.assertEqual(docker[-1][-1], owned)
        self.assertTrue((self.state / "container.removed").exists())
        daemon = json.loads((self.state / "daemon.json").read_text())
        self.assertEqual(daemon["key"], "")
        self.assertEqual(daemon["openai"], "")
        self.assert_cleaned()

    def test_builds_kmx_from_action_repository_when_not_provided(self):
        result = self.run_runner(KMX_BIN="", VERIFY="false")
        self.assertEqual(result.returncode, 0, result.stderr)
        builds = [c for c in self.records("go") if c["argv"][0] == "build"]
        self.assertEqual(len(builds), 2)
        kmx_build = next(c for c in builds if "./cmd/kmx" in c["argv"])
        self.assertEqual(kmx_build["cwd"], str(self.repo))
        self.assert_cleaned()

    def archive(self, member="nested/agentsessionsd", symlink=False):
        path = self.root / "release.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            entry = tarfile.TarInfo(member)
            if symlink:
                entry.type = tarfile.SYMTYPE
                entry.linkname = "/tmp/untrusted"
                archive.addfile(entry)
            else:
                content = DAEMON.encode()
                entry.size = len(content)
                entry.mode = 0o755
                archive.addfile(entry, io.BytesIO(content))
        self.env["TEST_ARCHIVE"] = str(path)
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def test_release_archive_is_checksum_pinned(self):
        digest = self.archive()
        result = self.run_runner(SESSIONS_ARCHIVE_URL="https://releases.example/daemon.tar.gz", SESSIONS_ARCHIVE_SHA256=digest)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.outputs()["daemon-install"], "release-sha256")
        self.assertEqual(self.records("go"), [])
        self.assert_cleaned()

    def test_optimized_python_cannot_bypass_checksum_or_tls_checks(self):
        self.archive()
        result = self.run_runner(PYTHONOPTIMIZE="1", SESSIONS_ARCHIVE_URL="https://releases.example/daemon.tar.gz", SESSIONS_ARCHIVE_SHA256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "daemon.json").exists())
        self.artifacts.rmdir()
        result = self.run_runner(PYTHONOPTIMIZE="1", MODEL_BASE_URL="http://provider.example/v1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "daemon.json").exists())

    def test_failed_container_cleanup_cannot_report_success(self):
        result = self.run_runner(MODEL_MODE="aikit", MODEL_KEY_ENV="", MODEL_NAME="", TEST_DOCKER_RM_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cleanup", result.stderr)
        self.assertNotIn("Live eval loop passed", result.stdout)
        self.assertIn("receipt", self.outputs())
        self.assert_cleaned()

    def test_failed_private_directory_cleanup_cannot_report_success(self):
        self.executable("rm", "#!/bin/bash\nexit 1\n")
        result = self.run_runner(VERIFY="false")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("cleanup", result.stderr)
        self.assertNotIn("Live eval loop passed", result.stdout)
        self.assertTrue(list(self.work.iterdir()))
        self.assertTrue(self.artifacts.is_dir())
        self.assertTrue((self.state / "daemon.stopped").exists())

    def test_release_checksum_mismatch_rejects_before_daemon_start(self):
        self.archive()
        result = self.run_runner(SESSIONS_ARCHIVE_URL="https://releases.example/daemon.tar.gz", SESSIONS_ARCHIVE_SHA256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "daemon.json").exists())
        self.assertEqual([c["mode"] for c in self.records("gate")], ["prepare"])
        self.assertEqual(list(self.work.iterdir()), [])
        self.assertEqual(self.outputs()["artifact-dir"], str(self.artifacts))

    def test_unsafe_release_member_is_not_extracted(self):
        for name, symlink in (("../agentsessionsd", False), ("agentsessionsd", True)):
            with self.subTest(member=name, symlink=symlink):
                digest = self.archive(name, symlink)
                result = self.run_runner(SESSIONS_ARCHIVE_URL="https://releases.example/daemon.tar.gz", SESSIONS_ARCHIVE_SHA256=digest)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.state / "daemon.json").exists())
                if self.artifacts.exists():
                    self.artifacts.rmdir()

    def test_rejects_credential_urls_and_keyed_plaintext_loopback(self):
        for url, key_name in (("https://user:password@provider.example/v1", ""),
                              ("https://provider.example/v1?api_key=secret", ""),
                              ("http://provider.example/v1", ""),
                              ("http://127.0.0.1:9000/v1", "TEST_PROVIDER_KEY"),
                              ("http://localhost:9000/v1", "")):
            with self.subTest(url=url):
                result = self.run_runner(MODEL_BASE_URL=url, MODEL_KEY_ENV=key_name)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((self.state / "daemon.json").exists())
                if self.artifacts.exists():
                    self.artifacts.rmdir()

    def test_rejects_nominated_secret_in_url_path(self):
        result = self.run_runner(MODEL_BASE_URL="https://provider.example/" + SECRET + "/v1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "daemon.json").exists())

    def test_rejects_percent_encoded_nominated_secret_in_url_path(self):
        encoded = "".join(f"%{ord(c):02X}" for c in SECRET)
        result = self.run_runner(MODEL_BASE_URL="https://provider.example/" + encoded + "/v1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "daemon.json").exists())

    def test_invalid_release_url_key_never_enters_helper_argv(self):
        self.executable("python3", '#!/bin/bash\nprintf "%s\\n" "$@" >> "$TEST_STATE/python.args"\nexec ' + sys.executable + ' "$@"\n')
        result = self.run_runner(SESSIONS_ARCHIVE_URL="https://releases.example/" + SECRET + "/daemon.tar.gz", SESSIONS_ARCHIVE_SHA256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(SECRET, (self.state / "python.args").read_text())
        self.assertFalse((self.state / "daemon.json").exists())

    def test_keyless_literal_loopback_is_allowed(self):
        result = self.run_runner(MODEL_BASE_URL="http://127.0.0.1:9000/v1", MODEL_KEY_ENV="")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads((self.state / "daemon.json").read_text())["key"], "")
        self.assert_cleaned()

    def test_prepare_preflight_fails_before_any_build_or_model_start(self):
        result = self.run_runner(TEST_FAIL="prepare", KMX_BIN="")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("tracked bundle preflight failed", result.stderr)
        self.assertEqual(self.records("go"), [])
        self.assertEqual(self.records("docker"), [])
        self.assertFalse((self.state / "daemon.json").exists())
        self.assertEqual(list(self.work.iterdir()), [])

    def test_daemon_startup_failure_suppresses_private_logs(self):
        result = self.run_runner(TEST_DAEMON_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("failed during sessions startup", result.stderr)
        self.assertEqual([c["mode"] for c in self.records("gate")], ["prepare"])
        self.assertEqual(list(self.work.iterdir()), [])
        self.assertTrue(self.artifacts.is_dir())

    def test_nominating_standard_provider_variable_retains_redaction_key(self):
        result = self.run_runner(MODEL_KEY_ENV="MODEL_API_KEY", MODEL_API_KEY=SECRET)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(all(c["key"] == SECRET for c in self.records("gate")))
        self.assertEqual(json.loads((self.state / "daemon.json").read_text())["key"], SECRET)
        self.assert_cleaned()

    def test_nominated_key_must_be_a_variable_name(self):
        result = self.run_runner(MODEL_KEY_ENV=SECRET)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.state / "daemon.json").exists())

    def test_aikit_requires_digest_and_regular_yaml_config(self):
        for overrides in ({"AIKIT_IMAGE": "example:latest"}, {"AIKIT_CONFIG": str(self.root / "missing.yaml")}):
            with self.subTest(overrides=overrides):
                result = self.run_runner(MODEL_MODE="aikit", MODEL_KEY_ENV="", **overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.records("docker"), [])
                if self.artifacts.exists():
                    self.artifacts.rmdir()

    def test_existing_artifact_directory_is_never_removed(self):
        self.artifacts.mkdir()
        (self.artifacts / "keep").write_text("existing")
        result = self.run_runner()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.artifacts / "keep").read_text(), "existing")
        self.assertFalse((self.state / "daemon.json").exists())

    def test_term_cleans_owned_daemon_container_and_work_but_keeps_evidence(self):
        proc = subprocess.Popen(["bash", str(self.script)], cwd=self.bundle.parent,
                                env=dict(self.env, MODEL_MODE="aikit", MODEL_KEY_ENV="", MODEL_NAME="", TEST_HANG="evaluate"),
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        def reap():
            if proc.poll() is None:
                proc.kill()
            proc.communicate(timeout=5)
        self.addCleanup(reap)
        deadline = time.monotonic() + 10
        while not (self.state / "gate.waiting").exists() and time.monotonic() < deadline and proc.poll() is None:
            time.sleep(0.05)
        self.assertTrue((self.state / "gate.waiting").exists(), "runner must reach evaluation")
        proc.send_signal(signal.SIGTERM)
        stdout, stderr = proc.communicate(timeout=5)
        self.assertEqual(proc.returncode, 143)
        self.assert_private(stdout + stderr)
        self.assertTrue((self.state / "container.removed").exists())
        self.assert_cleaned()


if __name__ == "__main__":
    unittest.main()
