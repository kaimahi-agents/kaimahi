"""Synthetic contract tests; no requests, transcripts, or model output."""
import json
import sqlite3
from contextlib import closing
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from activity import ActivityLog, Journal, JournalUnavailable, ProjectionUnavailable, Projector


DIGESTS = {"coordinator": "sha256:" + "a" * 64, "purchasing": "sha256:" + "b" * 64,
           "receiving": "sha256:" + "c" * 64}
ROOT = "ses_root12345678"
FIRST = "ses_child12345678"
SECOND = "ses_child87654321"
MARKER = "UNTRUSTED_SYNTHETIC_MARKER"


def created(child, parent=ROOT):
    return ("session.created.1", {"info": {"id": child, "parentID": parent}})


def errored(child):
    return ("message.updated.1", {"sessionID": child, "info": {"error": {"name": MARKER}}})


def tool(call, agent, child=None, status="completed", parent=ROOT):
    return {"type": "tool", "sessionID": ROOT,
            "part": {"type": "tool", "tool": "task", "callID": call,
                     "state": {"status": status,
                               "input": {"subagent_type": agent, "description": MARKER},
                               "metadata": {"sessionId": child, "parentSessionId": parent},
                               "output": MARKER}}}


class ActivityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.db = self.base / "opencode.db"
        with closing(sqlite3.connect(self.db)) as conn, conn:
            conn.execute("CREATE TABLE event (type TEXT, data TEXT)")
        self.path = self.base / "activity.jsonl"
        self.log = ActivityLog(self.path, "rev-123", "try-1", DIGESTS)
        self.addCleanup(self.log.close)
        self.projector = Projector(self.log, Journal(self.db), ("purchasing", "receiving"))

    def record(self, *items):
        with closing(sqlite3.connect(self.db)) as conn, conn:
            conn.executemany("INSERT INTO event (type, data) VALUES (?, ?)",
                             [(kind, json.dumps(data)) for kind, data in items])

    def rows(self):
        payload = self.path.read_text()
        self.assertNotIn(MARKER, payload)
        return [json.loads(line) for line in payload.splitlines()]

    def test_two_verified_children_and_parent_terminal_remain_distinct(self):
        self.log.start()
        self.record(created(FIRST), created(SECOND))
        self.projector.project(tool("call_first", "purchasing", FIRST))
        self.projector.project(tool("call_second", "receiving", SECOND))
        self.projector.finish()
        self.log.terminal(0)
        rows = self.rows()
        self.assertEqual([r["seq"] for r in rows], list(range(1, len(rows) + 1)))
        self.assertEqual([(r["agent"], r["status"], r["handoff"]) for r in rows],
                         [("coordinator", "running", None),
                          ("purchasing", "running", "task"),
                          ("purchasing", "succeeded", "task"),
                          ("receiving", "running", "task"),
                          ("receiving", "succeeded", "task"),
                          ("coordinator", "succeeded", None)])
        self.assertEqual(rows[1]["task"], FIRST)
        self.assertEqual(rows[1]["parentTask"], "try-1")
        self.assertEqual(rows[1]["bundleDigest"], DIGESTS["purchasing"])
        self.assertEqual(rows[-1]["run"], "rev-123/try-1")
        self.assertTrue(rows[-1]["terminal"])
        self.assertTrue(all(r["v"] == 1 and isinstance(r["at"], int) for r in rows))

    def test_digest_in_safe_row_uses_portable_sha256_identity(self):
        self.log.start()
        rows = self.rows()
        self.assertEqual(rows[0]["bundleDigest"], DIGESTS["coordinator"])

    def test_empty_native_child_error_does_not_become_helper_success(self):
        self.record(created(FIRST), ("message.updated.1", {"sessionID": FIRST,
                                                          "info": {"error": {}}}))
        self.projector.project(tool("call_first", "purchasing", FIRST))
        with self.assertRaises(JournalUnavailable):
            self.projector.finish()
        self.assertEqual(self.rows(), [])

    def test_child_error_after_parent_completed_overrides_helper_success(self):
        self.record(created(FIRST))
        self.projector.project(tool("call_first", "purchasing", FIRST))
        self.record(errored(FIRST))
        self.projector.finish()
        self.log.terminal(0)
        rows = self.rows()
        self.assertEqual([(r["agent"], r["status"]) for r in rows],
                         [("purchasing", "running"), ("purchasing", "failed"),
                          ("coordinator", "succeeded")])
        self.assertEqual(rows[1]["summary"], "Native child error event")

    def test_native_error_stays_failed_even_if_tool_phase_is_running(self):
        self.record(created(FIRST), errored(FIRST))
        self.projector.project(tool("call_first", "purchasing", FIRST, status="running"))
        self.projector.finish()
        self.assertEqual([r["status"] for r in self.rows()], ["running", "failed"])

    def test_rejected_tool_without_child_is_separate_coordinator_work_not_handoff(self):
        self.projector.project(tool("call_bad", "receiving", status="error"))
        self.projector.finish()
        rows = self.rows()
        self.assertEqual([(r["agent"], r["status"], r["handoff"]) for r in rows],
                         [("coordinator", "failed", None)])
        self.assertTrue(rows[0]["task"].startswith("try-1/tool/"))
        self.assertEqual(rows[0]["parentTask"], "try-1")
        self.assertNotIn("call_bad", rows[0]["task"])

    def test_errored_tool_without_child_and_wrong_parent_is_missing_evidence(self):
        self.projector.project(tool("call_wrong", "purchasing", status="error",
                                    parent="ses_other12345678"))
        self.projector.finish()
        rows = self.rows()
        self.assertEqual([(r["agent"], r["status"], r.get("evidenceMissing")) for r in rows],
                         [("coordinator", "running", "child-session")])
        self.assertEqual(rows[0]["handoff"], None)

    def test_running_part_can_acquire_native_child_metadata_at_completion(self):
        self.record(created(FIRST))
        running = tool("call_first", "purchasing", status="running")
        self.projector.project(running)
        self.projector.project(tool("call_first", "purchasing", FIRST))
        self.projector.finish()
        self.assertEqual([r["status"] for r in self.rows()], ["running", "succeeded"])

    def test_mixed_root_sessions_are_refused(self):
        self.record(created(FIRST), created(SECOND, "ses_other12345678"))
        self.projector.project(tool("call_first", "purchasing", FIRST))
        other = tool("call_second", "receiving", SECOND, parent="ses_other12345678")
        other["sessionID"] = "ses_other12345678"
        with self.assertRaises(ProjectionUnavailable):
            self.projector.project(other)
        self.assertEqual(self.rows(), [])

    def test_duplicate_tool_part_does_not_create_duplicate_child(self):
        self.record(created(FIRST))
        event = tool("call_first", "purchasing", FIRST)
        self.projector.project(event)
        self.projector.project(event)
        self.projector.finish()
        self.assertEqual([r["status"] for r in self.rows()], ["running", "succeeded"])

    def test_running_tool_without_child_creation_is_missing_evidence(self):
        self.projector.project(tool("call_first", "purchasing", FIRST, status="running"))
        self.projector.finish()
        self.assertEqual([(r["agent"], r["status"], r.get("evidenceMissing")) for r in self.rows()],
                         [("coordinator", "running", "child-session")])

    def test_journal_is_scanned_once_for_two_child_calls(self):
        self.record(created(FIRST), created(SECOND))
        self.projector.project(tool("call_first", "purchasing", FIRST))
        self.projector.project(tool("call_second", "receiving", SECOND))
        original = sqlite3.connect
        with patch("activity.sqlite3.connect", wraps=original) as connect:
            self.projector.finish()
        self.assertEqual(connect.call_count, 1)
        self.assertEqual(sum(r["status"] == "succeeded" for r in self.rows()), 2)

    def test_missing_journal_child_is_missing_evidence_not_coordinator_failure(self):
        self.projector.project(tool("call_first", "purchasing", FIRST))
        self.projector.finish()
        rows = self.rows()
        self.assertEqual([(r["agent"], r["status"], r["handoff"]) for r in rows],
                         [("coordinator", "running", None)])
        self.assertEqual(rows[0]["task"].split("/tool/")[0], "try-1")
        self.assertEqual(rows[0]["parentTask"], "try-1")
        self.assertEqual(rows[0]["evidenceMissing"], "child-session")

    def test_errored_tool_with_unverified_child_is_missing_not_rejected(self):
        self.record(created(FIRST, "ses_other12345678"))
        self.projector.project(tool("call_wrong", "purchasing", FIRST,
                                    status="error", parent="ses_other12345678"))
        self.projector.finish()
        self.assertEqual([(r["status"], r.get("evidenceMissing")) for r in self.rows()],
                         [("running", "child-session")])

    def test_mismatched_root_or_child_parent_never_corroborates_handoff(self):
        self.record(created(FIRST, "ses_other12345678"))
        self.projector.project(tool("call_wrong", "purchasing", FIRST,
                                    parent="ses_other12345678"))
        self.projector.finish()
        self.assertEqual([(r["agent"], r["handoff"], r.get("evidenceMissing")) for r in self.rows()],
                         [("coordinator", None, "child-session")])

    def test_standalone_coordinator_allows_no_helpers(self):
        standalone = Projector(self.log, Journal(self.db), ())
        standalone.finish()
        self.log.terminal(0)
        self.assertEqual([(r["agent"], r["status"]) for r in self.rows()],
                         [("coordinator", "succeeded")])

    def test_unlisted_agent_does_not_become_helper(self):
        self.record(created(FIRST))
        self.projector.project(tool("call_unknown", MARKER, FIRST))
        self.projector.finish()
        self.assertEqual([(r["agent"], r["status"], r.get("evidenceMissing")) for r in self.rows()],
                         [("coordinator", "running", "child-session")])

    def test_missing_journal_file_fails_closed(self):
        self.db.unlink()
        self.projector.project(tool("call_first", "purchasing", FIRST))
        with self.assertRaises(JournalUnavailable):
            self.projector.finish()
        self.assertEqual(self.rows(), [])

    def test_oversized_journal_record_fails_closed_without_leak(self):
        self.record(("session.created.1", {"info": {"id": FIRST, "parentID": ROOT,
                                                 "payload": MARKER * 2000}}))
        self.projector.project(tool("call_first", "purchasing", FIRST))
        with self.assertRaises(JournalUnavailable):
            self.projector.finish()
        self.assertEqual(self.rows(), [])

    def test_cap_emits_one_marker_reserves_terminal_and_bounds_lines(self):
        with patch.object(ActivityLog, "MAX_BYTES", 1800):
            for _ in range(60):
                self.log.start()
            self.log.terminal(1)
        raw = self.path.read_bytes()
        rows = self.rows()
        self.assertLessEqual(len(raw), 1800)
        self.assertTrue(all(len(line) <= ActivityLog.MAX_LINE_BYTES for line in raw.splitlines()))
        self.assertEqual(sum(r.get("historyTruncated", False) for r in rows), 1)
        self.assertEqual(rows[-1]["status"], "failed")
        self.assertTrue(rows[-1]["terminal"])
        self.assertEqual([r["seq"] for r in rows], list(range(1, len(rows) + 1)))

    def test_log_rejects_arbitrary_summary_and_unsafe_identity(self):
        with self.assertRaises(ValueError):
            self.log.append("coordinator", "try-1", None, None, "running", MARKER)
        with self.assertRaises(ValueError):
            self.log.append("purchasing", MARKER, "try-1", "task", "running",
                            "Native child session observed")
        with self.assertRaises(ValueError):
            self.log.append("coordinator", "try-1", None, None, {"status": MARKER},
                            "Coordinator started")
        self.assertEqual(self.rows(), [])

    def test_ordinary_append_cannot_forge_root_success_with_safe_summary(self):
        with self.assertRaises(ValueError):
            self.log.append("coordinator", "try-1", None, None, "succeeded",
                            "Native Task tool completed")
        self.assertEqual(self.rows(), [])

    def test_ordinary_append_cannot_forge_terminal_summary(self):
        with self.assertRaises(ValueError):
            self.log.append("coordinator", "try-1", None, None, "succeeded", "Harness finished")
        self.assertEqual(self.rows(), [])

    def test_existing_log_cannot_be_reopened_for_accidental_second_start(self):
        with self.assertRaises(FileExistsError):
            ActivityLog(self.path, "rev-123", "try-1", DIGESTS)


if __name__ == "__main__":
    unittest.main()
