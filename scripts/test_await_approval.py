#!/usr/bin/env python3
"""The human wait must never mistake a denial or an old grant for approval."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("await-approval.sh")
CALL = "payment_schedule: invoice_id INV-88134, amount_cents 3255000, payee_id MER-4471"
PENDING = f"request-1 now ap-agent tool payment_schedule {CALL}\n"
AUDIT = f"now ap-agent tool payment_schedule approved admin expires=later uses=1 {CALL}\n"
GRANT = "grant-new ap-agent tool payment_schedule yes later 0/1 - now admin call abcdef012345\n"


class AwaitApprovalTest(unittest.TestCase):
    def run_wait(self, *, pending=PENDING, audit=AUDIT, before="", after=GRANT,
                 transient=False, timeout=False, uses="1", caller=None):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            for name, text in (("pending", pending), ("audit", audit),
                               ("before", before), ("after", after)):
                (work / name).write_text(text)
            # Only the external admin CLI is replaced. The real shell helper
            # selects the call, polls, and validates the audit and grants.
            kmx = work / "kmx"
            kmx.write_text('''#!/usr/bin/env python3
import os
from pathlib import Path
import sys
p = Path(__file__).parent
cmd = sys.argv[1:]
state = p / (cmd[0] + "-count")
n = int(state.read_text()) if state.exists() else 0
state.write_text(str(n + 1))
if cmd == ["approvals"]:
    if n == 1 and os.environ["TRANSIENT"] == "1":
        sys.exit(1)
    if n == 0 or os.environ["TIMEOUT"] == "1":
        print((p / "pending").read_text(), end="")
elif cmd == ["grants", "ap-agent"]:
    print((p / ("before" if n == 0 else "after")).read_text(), end="")
elif cmd == ["audit", "approval", "ap-agent"]:
    print((p / "audit").read_text(), end="")
else:
    sys.exit("unexpected admin command: " + repr(cmd))
''')
            kmx.chmod(0o700)
            env = {**os.environ, "KMX": str(kmx), "CRED": "ap-agent",
                   "HUMAN_TIMEOUT": "0" if timeout else "3", "HUMAN_POLL": "0",
                   "TRANSIENT": str(int(transient)), "TIMEOUT": str(int(timeout))}
            command = ["bash", str(SCRIPT), "request-1", uses]
            if caller:
                source = SCRIPT.with_name(caller)
                # Execute the actual approval function without starting the
                # ERP scenario. Its external dependency is the same fake CLI.
                definitions = source.read_text().split("# --- 0.", 1)[0]
                command = ["bash", "-c", definitions + "\napprove request-1 1\n", str(source)]
                env.update(CRED="hello-world", CRED_AP="ap-agent", AP_HUMAN="1")
            return subprocess.run(command, env=env, capture_output=True, text=True, timeout=10)

    def test_admin_approval_for_exact_call_with_new_live_grant(self):
        result = self.run_wait()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("kmx approve request-1 --uses 1 --ttl 10m", result.stderr)
        self.assertNotIn("Slack", result.stderr)

    def test_denial_does_not_count_as_approval(self):
        result = self.run_wait(audit=AUDIT.replace("approved", "denied"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no 'approved' row", result.stderr)

    def test_approval_of_different_call_does_not_count(self):
        result = self.run_wait(audit=AUDIT.replace("MER-4471", "MER-9911"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("this exact call", result.stderr)

    def test_existing_grant_does_not_count_as_new(self):
        result = self.run_wait(before=GRANT)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no NEW live grant", result.stderr)

    def test_expired_grant_does_not_count(self):
        result = self.run_wait(after=GRANT.replace("yes", "no"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no NEW live grant", result.stderr)

    def test_missing_request_fails_before_waiting(self):
        result = self.run_wait(pending="")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not pending", result.stderr)

    def test_missing_call_summary_is_refused(self):
        result = self.run_wait(pending="request-1 now ap-agent tool payment_schedule\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("states no call", result.stderr)

    def test_ap_human_wait_uses_ap_credential_not_default_model_credential(self):
        for caller in ("ap-demo.sh", "ap-injection.sh"):
            with self.subTest(caller=caller):
                result = self.run_wait(caller=caller)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_timeout_never_claims_approval(self):
        result = self.run_wait(timeout=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("still pending", result.stderr)

    def test_failed_read_is_retried_not_treated_as_decision(self):
        result = self.run_wait(transient=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_uses_must_be_a_positive_integer(self):
        for uses in ("0", "-1", "person", "1x"):
            with self.subTest(uses=uses):
                result = self.run_wait(uses=uses)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("uses must be a positive integer", result.stderr)


if __name__ == "__main__":
    unittest.main()
