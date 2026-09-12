#!/usr/bin/env python3
"""AP fixtures approve through admin only; retired human mode fails closed."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent
CALLERS = ("ap-demo.sh", "ap-injection.sh")


class APApprovalTest(unittest.TestCase):
    def run_ap(self, caller, *, human=None, uses="1", entry=False, make=False,
               admin_exit=0):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            log = work / "commands.jsonl"
            kmx = work / "kmx"
            # Replace only external commands. For entry tests, intercept even
            # mktemp: refusal must precede setup, agent turns, and tool calls.
            kmx.write_text(f"#!{sys.executable}\n" + '''import json
import os
from pathlib import Path
import sys
with open(os.environ["AP_TEST_LOG"], "a") as log:
    log.write(json.dumps([Path(sys.argv[0]).name, *sys.argv[1:]]) + "\\n")
sys.exit(int(os.environ["AP_TEST_ADMIN_EXIT"]) if Path(sys.argv[0]).name == "kmx" else 99)
''')
            kmx.chmod(0o700)
            if entry or make:
                (work / "mktemp").symlink_to(kmx)
            env = {**os.environ, "KMX": str(kmx), "KUBECTL": str(kmx),
                   "PATH": str(work) + os.pathsep + os.environ["PATH"],
                   "AP_TEST_LOG": str(log), "AP_TEST_ADMIN_EXIT": str(admin_exit)}
            for name in ("AP_HUMAN", "MAKEFLAGS", "MAKEOVERRIDES"):
                env.pop(name, None)
            if human is not None and not make:
                env["AP_HUMAN"] = human
            source = SCRIPTS / caller
            if make:
                # Skip unrelated cluster guarding/building, but execute the
                # real recipe. A makefile variable (unlike a command-line one)
                # is not auto-exported, so this exercises explicit forwarding.
                command = ["make", "--no-print-directory", "-o", "guard", "-o", str(kmx),
                           caller.removesuffix(".sh"), f"KMX={kmx}",
                           "--eval", f"AP_HUMAN := {human}"]
            elif entry:
                command = ["bash", str(source)]
            else:
                # Execute the real setup and approve function, not the ERP
                # scenario (whose gateway assertions run in cluster CI).
                definitions, separator, _ = source.read_text().partition("# --- 0.")
                self.assertTrue(separator, "AP scenario boundary is missing")
                command = ["bash", "-c", definitions + '\napprove "$@"\n',
                           str(source), "request-exact-42"]
                if uses is not None:
                    command.append(uses)
            result = subprocess.run(command, cwd=SCRIPTS.parent, env=env,
                                    capture_output=True, text=True, timeout=10)
            calls = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
            return result, calls

    def test_human_mode_is_refused_before_any_setup_or_external_command(self):
        for caller in CALLERS:
            for human in ("1", "2", "-1", "true"):
                with self.subTest(caller=caller, human=human):
                    result, calls = self.run_ap(caller, human=human, entry=True)
                    self.assertEqual(calls, [], result.stderr)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertIn("AP_HUMAN", result.stderr)
                    self.assertIn("retired", result.stderr)

    def test_make_forwards_legacy_flag_for_refusal_not_autoapproval(self):
        for caller in CALLERS:
            with self.subTest(caller=caller):
                result, calls = self.run_ap(caller, human="1", make=True)
                self.assertEqual(calls, [], result.stderr)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("AP_HUMAN", result.stderr)
                self.assertIn("retired", result.stderr)

    def test_default_and_zero_approve_exact_request_with_bounded_grant(self):
        for caller in CALLERS:
            for human in (None, "0"):
                for uses in ("1", "2"):
                    with self.subTest(caller=caller, human=human, uses=uses):
                        result, calls = self.run_ap(caller, human=human, uses=uses)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertEqual(calls, [["kmx", "approve", "request-exact-42",
                                                  "--ttl", "10m", "--uses", uses]])

    def test_demo_defaults_to_one_use(self):
        result, calls = self.run_ap("ap-demo.sh", uses=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, [["kmx", "approve", "request-exact-42",
                                  "--ttl", "10m", "--uses", "1"]])

    def test_admin_failure_is_not_reported_as_approval(self):
        for caller in CALLERS:
            with self.subTest(caller=caller):
                result, _ = self.run_ap(caller, admin_exit=23)
                self.assertEqual(result.returncode, 23, result.stderr)


if __name__ == "__main__":
    unittest.main()
