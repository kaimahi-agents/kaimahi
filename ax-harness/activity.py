"""Bounded, allowlisted AX preview activity projection from OpenCode 1.17.20.

The OpenCode journal is not a safe artifact: only these fixed schema-v1 records
may leave the guest. Observation time is not a native child start time.
"""

import hashlib
import json
import os
import re
import sqlite3
import stat
import time
from contextlib import closing
from pathlib import Path


_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,95}\Z", re.ASCII)
_AGENT = re.compile(r"[a-z][a-z0-9-]{0,63}\Z", re.ASCII)
_SESSION = re.compile(r"ses_[A-Za-z0-9]{8,64}\Z", re.ASCII)
_CALL = re.compile(r"[A-Za-z0-9_-]{1,80}\Z", re.ASCII)
_TOOL_TASK = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,95}/tool/[0-9a-f]{16}\Z", re.ASCII)
_DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z", re.ASCII)
_COORDINATOR_SUMMARIES = {
    "running": frozenset(("Coordinator started", "Task child evidence missing")),
    "failed": frozenset(("Task tool rejected; no child session observed",)),
}
_HELPER_SUMMARIES = {
    "running": frozenset(("Native child session observed",)),
    "succeeded": frozenset(("Native Task tool completed",)),
    "failed": frozenset(("Native child error event", "Native Task tool error")),
}


class JournalUnavailable(Exception):
    """Native evidence cannot be safely read; never include underlying DB data."""


class ProjectionUnavailable(Exception):
    """Native tool stream cannot be correlated within fixed safety limits."""


class ActivityLog:
    MAX_BYTES = 2 * 1024 * 1024
    MAX_LINE_BYTES = 1024

    def __init__(self, path, revision, attempt, digests):
        if not isinstance(revision, str) or not _NAME.fullmatch(revision):
            raise ValueError("invalid revision")
        if not isinstance(attempt, str) or not _NAME.fullmatch(attempt):
            raise ValueError("invalid attempt")
        if not isinstance(digests, dict) or "coordinator" not in digests or not all(
            isinstance(agent, str) and _AGENT.fullmatch(agent) and
            isinstance(digest, str) and _DIGEST.fullmatch(digest)
            for agent, digest in digests.items()
        ):
            raise ValueError("invalid bundle digests")
        self.revision = revision
        self.attempt = attempt
        self.run = f"{revision}/{attempt}"
        self.digests = dict(digests)
        self.seq = 0
        self.size = 0
        self.truncated = False
        self.ended = False
        # Compute reserve with this log's immutable identity; never depend on
        # the size of a caller-controlled event when reserving the terminal.
        self._terminal_reserve = max(len(self._encode(self._row("coordinator", attempt, None,
                            None, status, summary, terminal=True, seq=self.MAX_BYTES)))
            for status, summary in (("succeeded", "Harness finished"),
                                    ("failed", "Harness error")))
        self._truncation_reserve = len(self._encode(self._row("coordinator", attempt, None,
            None, "running", "Activity history truncated", historyTruncated=True, seq=self.MAX_BYTES)))
        if self._terminal_reserve + self._truncation_reserve > self.MAX_BYTES:
            raise ValueError("activity log capacity too small")
        flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        self._file = os.fdopen(os.open(path, flags, 0o600), "wb")

    def _row(self, agent, task, parent_task, handoff, status, summary, *, seq, **extra):
        return dict(v=1, seq=seq, at=time.time_ns(), run=self.run, revision=self.revision,
                    agent=agent, task=task, parentTask=parent_task, handoff=handoff,
                    status=status, summary=summary, bundleDigest=self.digests[agent], **extra)

    @staticmethod
    def _encode(row):
        return (json.dumps(row, separators=(",", ":"), ensure_ascii=True) + "\n").encode("ascii")

    def _write(self, row, *, remaining):
        data = self._encode(row)
        if len(data) > self.MAX_LINE_BYTES or self.size + len(data) + remaining > self.MAX_BYTES:
            raise ValueError("activity log bound exceeded")
        self._file.write(data)
        self._file.flush()
        os.fsync(self._file.fileno())
        self.size += len(data)
        self.seq += 1

    def append(self, agent, task, parent_task, handoff, status, summary, evidence_missing=False):
        """Append a safe ordinary record, or a single gap marker at capacity."""
        if self.ended:
            raise ValueError("activity log already terminal")
        root_record = task == self.attempt and parent_task is None
        tool_record = (isinstance(task, str) and _TOOL_TASK.fullmatch(task) and
                       task.startswith(self.attempt + "/tool/") and parent_task == self.attempt)
        if (not isinstance(agent, str) or agent not in self.digests or
            not isinstance(status, str) or not isinstance(summary, str) or
            summary not in (_COORDINATOR_SUMMARIES if agent == "coordinator" else
                            _HELPER_SUMMARIES).get(status, ()) or
            handoff not in (None, "task") or
            (agent == "coordinator" and (handoff is not None or
                not ((root_record and status == "running" and summary == "Coordinator started"
                      and not evidence_missing) or
                     (tool_record and status == "failed" and
                      summary == "Task tool rejected; no child session observed" and not evidence_missing) or
                     (tool_record and status == "running" and
                      summary == "Task child evidence missing" and evidence_missing is True)))) or
            (agent != "coordinator" and (not isinstance(task, str) or not _SESSION.fullmatch(task) or
                                         parent_task != self.attempt or handoff != "task" or evidence_missing))):
            raise ValueError("invalid activity fields")
        if self.truncated:
            return False
        extra = {"evidenceMissing": "child-session"} if evidence_missing else {}
        row = self._row(agent, task, parent_task, handoff, status, summary,
                        seq=self.seq + 1, **extra)
        data = self._encode(row)
        if len(data) > self.MAX_LINE_BYTES:
            raise ValueError("activity line too large")
        if self.size + len(data) + self._terminal_reserve + self._truncation_reserve > self.MAX_BYTES:
            marker = self._row("coordinator", self.attempt, None, None, "running",
                               "Activity history truncated", seq=self.seq + 1,
                               historyTruncated=True)
            self._write(marker, remaining=self._terminal_reserve)
            self.truncated = True
            return False
        self._write(row, remaining=self._terminal_reserve + self._truncation_reserve)
        return True

    def close(self):
        """Release the log descriptor after terminal or on an interrupted run."""
        self._file.close()

    def start(self):
        return self.append("coordinator", self.attempt, None, None, "running", "Coordinator started")

    def terminal(self, exit_code):
        """Call only after observing the command exit; AX Ready is not a verdict."""
        if type(exit_code) is not int or self.ended:
            raise ValueError("invalid terminal outcome")
        status = "succeeded" if exit_code == 0 else "failed"
        summary = "Harness finished" if exit_code == 0 else "Harness error"
        self._write(self._row("coordinator", self.attempt, None, None, status, summary,
                              seq=self.seq + 1, terminal=True), remaining=0)
        self.ended = True


class Journal:
    """Read-only, bounded inspection of OpenCode 1.17.20's native event table."""

    MAX_DB_BYTES = 128 * 1024 * 1024
    MAX_EVENTS = 4096
    MAX_EVENT_BYTES = 8192
    MAX_SCAN_BYTES = 8 * 1024 * 1024

    def __init__(self, path):
        self.path = Path(path)

    def children_status(self, pairs, deadline):
        """Correlate every child from one bounded, read-only native snapshot."""
        pairs = set(pairs)
        if not pairs:
            return {}
        if any(not (isinstance(child, str) and _SESSION.fullmatch(child) and
                    isinstance(parent, str) and _SESSION.fullmatch(parent))
               for child, parent in pairs):
            raise JournalUnavailable("invalid native session identity")
        try:
            file_stat = self.path.lstat()
            if not stat.S_ISREG(file_stat.st_mode) or file_stat.st_size > self.MAX_DB_BYTES:
                raise JournalUnavailable("native journal unavailable or oversized")
            wal = self.path.with_name(self.path.name + "-wal")
            if wal.exists() and wal.stat().st_size > self.MAX_DB_BYTES:
                raise JournalUnavailable("native journal unavailable or oversized")
            created, errored = set(), set()
            total = 0
            with closing(sqlite3.connect(self.path.resolve().as_uri() + "?mode=ro", uri=True,
                                 timeout=1)) as connection:
                connection.execute("PRAGMA query_only=ON")
                connection.set_progress_handler(
                    lambda: 1 if time.monotonic() >= deadline else 0, 1000)
                rows = connection.execute(
                    "SELECT type, length(CAST(data AS BLOB)), substr(CAST(data AS BLOB),1,?) "
                    "FROM event WHERE type IN ('session.created.1','message.updated.1') LIMIT ?",
                    (self.MAX_EVENT_BYTES + 1, self.MAX_EVENTS + 1))
                for count, (kind, length, payload) in enumerate(rows, start=1):
                    if time.monotonic() >= deadline or count > self.MAX_EVENTS or length is None or length > self.MAX_EVENT_BYTES:
                        raise JournalUnavailable("native journal limit exceeded")
                    total += length
                    if total > self.MAX_SCAN_BYTES:
                        raise JournalUnavailable("native journal limit exceeded")
                    entry = json.loads(payload)
                    if not isinstance(entry, dict):
                        raise JournalUnavailable("invalid native journal event")
                    info = entry.get("info")
                    if not isinstance(info, dict):
                        continue
                    if kind == "session.created.1":
                        child, parent = info.get("id"), info.get("parentID")
                        if isinstance(child, str) and isinstance(parent, str):
                            created.add((child, parent))
                    elif kind == "message.updated.1" and "error" in info and info["error"] is not None:
                        if not isinstance(info["error"], dict) or not info["error"]:
                            raise JournalUnavailable("native child error evidence malformed")
                        child = entry.get("sessionID")
                        if isinstance(child, str):
                            errored.add(child)
            return {pair: (pair in created, pair[0] in errored) for pair in pairs}
        except (OSError, sqlite3.Error, ValueError, TypeError, UnicodeError, RecursionError):
            raise JournalUnavailable("native journal unavailable or malformed") from None


class Projector:
    """Buffer Task parts; reconcile the final child journal after command exit."""

    MAX_TOOL_CALLS = 1024

    def __init__(self, log, journal, allowed_helpers):
        helpers = frozenset(allowed_helpers)
        if "coordinator" in helpers or not helpers.issubset(log.digests):
            raise ValueError("invalid helper allowlist")
        self.log = log
        self.journal = journal
        self.allowed_helpers = helpers
        self._parts = {}
        self._root = None
        self._finished = False

    def project(self, event):
        if self._finished:
            raise ProjectionUnavailable("projection already finalized")
        if not isinstance(event, dict) or event.get("type") != "tool":
            return
        part = event.get("part") or event
        if not isinstance(part, dict) or part.get("type") != "tool" or part.get("tool") != "task":
            return
        state = part.get("state")
        if not isinstance(state, dict) or state.get("status") not in ("running", "completed", "error"):
            return
        call = part.get("callID") or part.get("id")
        if not isinstance(call, str) or not _CALL.fullmatch(call):
            raise ProjectionUnavailable("invalid native tool identity")
        if call not in self._parts and len(self._parts) >= self.MAX_TOOL_CALLS:
            raise ProjectionUnavailable("native tool limit exceeded")
        metadata = state.get("metadata")
        metadata = metadata if isinstance(metadata, dict) else {}
        inputs = state.get("input")
        inputs = inputs if isinstance(inputs, dict) else {}
        root = event.get("sessionID")
        root = root if isinstance(root, str) and _SESSION.fullmatch(root) else None
        if root is not None:
            if self._root is not None and root != self._root:
                raise ProjectionUnavailable("mixed native root sessions")
            self._root = root
        child = metadata.get("sessionId")
        child = child if isinstance(child, str) and _SESSION.fullmatch(child) else None
        parent_claim = metadata.get("parentSessionId")
        valid = root is not None and parent_claim == root
        parent_mismatch = parent_claim is not None and parent_claim != root
        agent = inputs.get("subagent_type")
        agent = agent if isinstance(agent, str) and agent in self.allowed_helpers else None
        previous = self._parts.get(call)
        identity = (root, child, agent, valid)
        if previous and (previous[0] != root or
                         (previous[1] is not None and previous[1] != child) or
                         (previous[2] is not None and previous[2] != agent) or
                         (previous[3] and not valid) or
                         previous[5] != parent_mismatch or
                         (previous[1] is not None and not previous[3] and valid)):
            raise ProjectionUnavailable("conflicting native tool identity")
        phase = state["status"]
        if previous and previous[4] in ("completed", "error") and previous[4] != phase:
            raise ProjectionUnavailable("conflicting native tool outcome")
        if previous and previous[4] in ("completed", "error"):
            return
        self._parts[call] = (*identity, phase, parent_mismatch)

    def finish(self, deadline=None):
        """Call after OpenCode exits and before ActivityLog.terminal()."""
        if self._finished:
            raise ProjectionUnavailable("projection already finalized")
        deadline = min(deadline if deadline is not None else float("inf"), time.monotonic() + 5)
        pairs = {(child, root) for root, child, agent, valid, _, _ in self._parts.values()
                 if valid and child and agent}
        statuses = self.journal.children_status(pairs, deadline)
        observations = []
        children = set()
        for call, (root, child, agent, valid, phase, parent_mismatch) in self._parts.items():
            tool_task = f"{self.log.attempt}/tool/{hashlib.sha256(call.encode('ascii')).hexdigest()[:16]}"
            if not valid or not child or not agent:
                if phase == "error" and not child and root is not None and not parent_mismatch:
                    observations.append(("coordinator", tool_task, self.log.attempt, None,
                        "failed", "Task tool rejected; no child session observed"))
                else:
                    observations.append(("coordinator", tool_task, self.log.attempt, None,
                        "running", "Task child evidence missing", True))
                continue
            created, errored = statuses[(child, root)]
            if not created:
                observations.append(("coordinator", tool_task, self.log.attempt, None,
                                     "running", "Task child evidence missing", True))
                continue
            if child in children:
                raise ProjectionUnavailable("duplicate native child identity")
            children.add(child)
            observations.append((agent, child, self.log.attempt, "task", "running",
                                 "Native child session observed"))
            if phase in ("completed", "error") or errored:
                failed = phase == "error" or errored
                observations.append((agent, child, self.log.attempt, "task",
                    "failed" if failed else "succeeded",
                    "Native child error event" if errored else
                    "Native Task tool error" if failed else "Native Task tool completed"))
        for record in observations:
            self.log.append(*record)
        self._finished = True
