# AX preview activity protocol (source only)

`activity.py` defines the sanitized, bounded JSONL projection for a future
kmx-owned AX harness. It consumes OpenCode **1.17.20** native Task-tool parts
and its SQLite event journal; the journal itself contains transcripts and is
**never** a canvas artifact. This source has synthetic tests, but no command
wrapper, image, CI signature, AX adapter, guest read or live-cluster proof yet.
Do not use it as an AX Task image or infer AX completion from `Ready`.

## Caller's contract

The later command wrapper supplies `ActivityLog(path, revision, attempt,
digests)` with the fixed intended path `/workspace/kmx/activity.jsonl` and a
mapping from `coordinator` plus explicitly permitted helper names to exact
`sha256:<portable digest>` strings. It calls `start()` once, passes bounded
parsed OpenCode JSON events to `Projector(log, Journal(path), allowed_helpers)`,
and calls `finish()` only after the command exits. `finish()` correlates a
native Task tool's child/parent IDs with `session.created.1` and checks that
child's `message.updated.1.info.error`; an OpenCode parent tool reporting
`completed` does not override a child error. Only an observed command exit
may call `terminal(exit_code)`. Interrupted commands leave **no terminal**.

Projection does not orchestrate children. A Task-tool call without native
child creation is **not** a hand-off. A rejected tool call is distinct
coordinator tool work. A Task-tool call without verified child creation is
marked `evidenceMissing: "child-session"`, not invented as a failed helper
or successful hand-off. A verified child with a still-running tool part has
a `running` hand-off; without a terminal child observation its outcome remains
unknown. `JournalUnavailable` and `ProjectionUnavailable` fail
closed without returning source payloads. The future wrapper must not turn
these errors into a successful run or print their underlying SQLite data.

## Safe log ABI v1

Every newline-terminated record contains `v: 1`, per-file monotone `seq`,
observation time `at` (Unix nanoseconds), `run` as `<revision>/<attempt>`,
`revision`, `agent`, `task`, `parentTask`, `handoff`, `status`, a fixed allowlisted
`summary`, and `bundleDigest`. Root `task` is the attempt; verified helper work
uses a native OpenCode session ID and `handoff: "task"`. Tool work without
verified child creation gets a truncated hash of its tool ID and no
handoff. The hash avoids copying the raw ID but is **not** a secrecy boundary
for guessable IDs. `evidenceMissing: "child-session"` is intended to project as
`Missing(child-session, AX journal)` in the shared canvas model. Terminal
root records have `terminal: true`, independent of child outcomes; root
success does **not** imply application success. Projection order is observation
order, not proof of concurrent execution.

The only summaries are fixed constants. Tool input/output, native error bodies,
request text, model output and raw journal records never enter JSONL. Appends
flush and fsync. Each line is at most 1,024 bytes and the append-only file at
most 2 MiB, including a single `historyTruncated: true` marker and reserved
terminal space. Once truncated, ordinary events stop; the consumer must show
missing intervening history rather than infer status. Later readers can
bounded-reread and deduplicate `(cluster UID, atespace, Task, run, seq)` after
binding the outer cluster and Task identity from an ownership receipt.

Run the synthetic tests without a cluster or Python bytecode cache:

```sh
PYTHONDONTWRITEBYTECODE=1 PYTHONWARNINGS=error python3 -m unittest discover -s ax-harness -p 'test_activity.py' -v
```

The one-start command wrapper, fixed guest read, model and permission checks,
image packaging/signing and AX Task lifecycle are subsequent reviewable changes.
