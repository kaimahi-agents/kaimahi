#!/usr/bin/env python3
"""Cluster-free safety checks; live behavior is verified by the two recordings."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("demo-hello-to-governed.sh")


class DemoSafetyTest(unittest.TestCase):
    def run_demo(self, *args, **env):
        # No cluster tools are needed for argument/precondition failures.
        host_env = os.environ.copy()
        for key in ("KIND_CLUSTER", "DEMO_RECORD_PROFILE", "DEMO_APP_PORT", "DEMO_WATCH_PORT"):
            host_env.pop(key, None)
        return subprocess.run(
            ["/bin/bash", str(SCRIPT), *args],
            env={**host_env, **env}, text=True, capture_output=True,
            timeout=10,
        )

    def test_host_demo_configuration_does_not_override_test_inputs(self):
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {
            "KIND_CLUSTER": "production", "DEMO_RECORD_PROFILE": "compressed",
            "DEMO_APP_PORT": "bad-port", "DEMO_WATCH_PORT": "bad-port",
        }):
            result = self.run_demo("record", directory)
            self.assertIn("prepared", result.stderr)

    def test_help_has_no_cluster_side_effect(self):
        result = self.run_demo("--help")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prepare", result.stdout)
        self.assertIn("record", result.stdout)
        self.assertIn("teardown", result.stdout)

    def test_unknown_action_refused(self):
        result = self.run_demo("install")
        self.assertEqual(result.returncode, 64)
        self.assertIn("Usage:", result.stderr)

    def test_prepare_refuses_existing_output_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            sentinel = Path(directory, "keep")
            sentinel.write_text("previous evidence")
            result = self.run_demo("prepare", directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("already exists", result.stderr)
            self.assertEqual(sentinel.read_text(), "previous evidence")

    def test_unowned_cluster_name_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("prepare", directory + "/run", KIND_CLUSTER="production")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("kmx-hello-governed", result.stderr)
            self.assertFalse(Path(directory, "run").exists())

    def test_record_refuses_unprepared_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("record", directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("prepared", result.stderr)

    def test_teardown_refuses_unowned_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("teardown", directory)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("prepared", result.stderr)

    def test_verify_requires_successful_rows_with_actual_tokens(self):
        # The real ledger timestamp is one field, not a date and time pair.
        # Wrong column offsets, accepting failed calls, or trusting only an
        # application's plausible answer must all fail this test.
        row = '2026-09-14T18:58:12 concierge orka local/qwen2.5:3b 186 59 0 unpriced 200 caller 10.0.0.1 none\n'
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            (p / "cluster").write_text("kmx-hello-governed\n")
            (p / "kubeconfig").touch()
            (p / "app-answer.json").write_text(json.dumps({"reply": "Hello!"}))
            for ledger, success in [
                (row, True),
                (row.replace("200 caller", "500 caller"), False),
                (row.replace("186 59", "0 0"), False),
                ("-- month to date: 0 cents, 0 tokens\n", False),
            ]:
                with self.subTest(ledger=ledger):
                    (p / "ledger.txt").write_text(ledger)
                    for optimize in ("", "1"):
                        result = self.run_demo("verify", directory,
                                               KIND_CLUSTER="kmx-hello-governed",
                                               PYTHONOPTIMIZE=optimize)
                        self.assertEqual(result.returncode == 0, success, result.stderr)

    def test_watch_failure_is_preserved_for_the_presenter(self):
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            (p / "cluster").write_text("kmx-hello-governed\n")
            (p / "kubeconfig").touch()
            (p / "watch-ready").touch()
            (p / "bin").mkdir()
            kmx = p / "bin/kmx"
            kmx.write_text('#!/bin/sh\nprintf "%s" "$ADMIN_PORT" > "$TEST_PORT_FILE"\n'
                           "echo 'model ledger unavailable' >&2\nexit 23\n")
            kmx.chmod(0o700)
            for settings, expected_port in [({}, "19093"), ({"DEMO_WATCH_PORT": "19593"}, "19593")]:
                with self.subTest(settings=settings):
                    result = self.run_demo("_watch", directory, KIND_CLUSTER="kmx-hello-governed",
                                           TEST_PORT_FILE=str(p / "used-port"), **settings)
                    self.assertEqual(result.returncode, 23, result.stderr)
                    self.assertTrue((p / "watch-exit").exists(), "watch lost its exit status")
                    self.assertEqual((p / "watch-exit").read_text().strip(), "23")
                    self.assertEqual((p / "used-port").read_text(), expected_port)

    def test_prepare_requires_agg_before_running_setup(self):
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            tools = p / "tools"
            tools.mkdir()
            (tools / "dirname").symlink_to("/usr/bin/dirname")
            for name in ("go", "docker", "kind", "kubectl", "helm", "python3",
                         "curl", "gh", "tmux", "asciinema"):
                tool = tools / name
                tool.write_text("#!/bin/sh\necho 'unexpected tool execution' >&2\nexit 97\n")
                tool.chmod(0o700)
            result = self.run_demo("prepare", str(p / "run"), PATH=str(tools))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("missing prerequisite: agg", result.stderr)
            self.assertFalse((p / "run").exists())

    def prepared_directory(self, p):
        (p / "cluster").write_text("kmx-hello-governed\n")
        (p / "kubeconfig").touch()
        (p / "prepared").touch()

    def test_record_rejects_invalid_or_identical_ports(self):
        with tempfile.TemporaryDirectory() as directory:
            self.prepared_directory(Path(directory))
            for key in ("DEMO_APP_PORT", "DEMO_WATCH_PORT"):
                for value in ("", "0", "65536", "-1", "01234", "1234:80", "port", "1+2", "19091"):
                    with self.subTest(key=key, value=value):
                        result = self.run_demo("record", directory, ADMIN_PORT="19091", **{key: value})
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn(key, result.stderr)
            result = self.run_demo("record", directory,
                                   DEMO_APP_PORT="19301", DEMO_WATCH_PORT="19301")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("distinct", result.stderr)
            self.assertFalse(Path(directory, "demo.cast").exists())

    def test_record_rejects_an_occupied_port_before_recording(self):
        with tempfile.TemporaryDirectory() as directory, socket.socket() as listener:
            self.prepared_directory(Path(directory))
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            port = str(listener.getsockname()[1])
            for key in ("DEMO_APP_PORT", "DEMO_WATCH_PORT"):
                with self.subTest(key=key):
                    with socket.socket() as companion:
                        companion.bind(("127.0.0.1", 0))
                        free_port = str(companion.getsockname()[1])
                    ports = {"DEMO_APP_PORT": free_port, "DEMO_WATCH_PORT": free_port}
                    ports[key] = port
                    result = self.run_demo("record", directory, ADMIN_PORT="19091", **ports)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(key, result.stderr)
                    self.assertIn("available", result.stderr)
            self.assertFalse(Path(directory, "demo.cast").exists())

    def test_hello_checks_only_stdout_and_stops_before_show_on_mismatch(self):
        # Stub only external command boundaries; run the real beat dispatcher,
        # transcript/capture path, fail-fast behavior and cleanup trap.
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory)
            self.prepared_directory(p)
            (p / "setup-time.txt").write_text("test setup\n")
            tools = p / "bin"
            tools.mkdir()
            for name in ("sleep", "tmux"):
                tool = tools / name
                tool.write_text("#!/bin/sh\nexit 0\n")
                tool.chmod(0o700)
            kmx = tools / "kmx"
            kmx.write_text('''#!/bin/sh
shift 2 # --context and its value
case "$1 $2" in
  'orka install') exit 0 ;;
  'agent create')
    shift 2
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --out ]; then printf 'kind: Agent\\n' > "$2"; fi
      shift
    done
    printf 'Task succeeded; diagnostics are not the answer\\n' >&2
    cat "$TEST_ANSWER_FILE"
    exit "${TEST_CREATE_STATUS:-0}"
    ;;
  'agent show') touch "$TEST_SHOW_MARKER"; exit 23 ;;
  *) exit 97 ;;
esac
''')
            kmx.chmod(0o700)
            answer = p / "test-answer.txt"
            shown = p / "show-called"
            for text, status, expect_show in [
                ("Hello world.\n", "0", True),
                ("Hello world!\n", "0", False),
                (" Hello world.\n", "0", False),
                ("Hello world.\n\n", "0", False),
                ("Hello world.\nExtra text\n", "0", False),
                ("", "0", False),
                ("Hello world.\n", "19", False),
            ]:
                with self.subTest(text=text, status=status):
                    answer.write_text(text)
                    shown.unlink(missing_ok=True)
                    result = self.run_demo("_beats", directory,
                                           TEST_ANSWER_FILE=str(answer),
                                           TEST_SHOW_MARKER=str(shown),
                                           TEST_CREATE_STATUS=status)
                    self.assertEqual(shown.exists(), expect_show, result.stdout + result.stderr)
                    self.assertEqual((p / "hello.yaml").read_text(), "kind: Agent\n")
                    self.assertTrue((p / "hello-answer.txt").exists(), "answer was not captured separately")
                    self.assertEqual((p / "hello-answer.txt").read_text(), text)
                    self.assertIn("diagnostics are not the answer", result.stderr)
                    self.assertEqual(result.returncode, 23 if expect_show else int(status) or 1)

    def test_unknown_profile_refused_before_preparation(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_demo("prepare", directory + "/run", DEMO_RECORD_PROFILE="compressed")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("presenter", result.stderr)
            self.assertFalse(Path(directory, "run").exists())


if __name__ == "__main__":
    unittest.main()
