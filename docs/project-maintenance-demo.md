# Demo: monitor projects and rebase worktrees

This offline demo gives resident agents a useful fixed command tool: read mock PR
and issue activity, inspect three disposable Git worktrees, and write a Markdown
report. A second agent also rebases worktrees whose base has advanced.

**All project activity is mocked.** The generator creates local repositories from
scratch, with no remotes. No GitHub account, credentials, real repository or hosted
model is used. Git worktree inspection and rebases are real operations on those
fixtures. This is a demo adapter, not a live GitHub integration or background
scheduler. Each Chat invocation runs one monitoring or maintenance cycle.

## Set up

Use Linux, macOS or WSL, Python 3.9+, Git 2.31+ and this branch's built CLI. From the
repository root:

```bash
go build -o /tmp/kmx-project-demo ./cmd/kmx
python3 scripts/demo-project-maintenance.py init /tmp/kmx-project-demo-data
cd -P /tmp/kmx-project-demo-data
export KMX_HOME="$PWD/state"
```

Choose a fresh destination if that directory exists; the generator refuses to
overwrite it. The generated tree contains:

```text
monitor.agent.md / monitor.agent.test.md
maintain.agent.md / maintain.agent.test.md
kmx.yaml             # two enabled fixed command tools, no model arguments
project-tool         # executable adapter copied from the checkout
activity.json        # editable mock PRs/issues/checks/reviews
repos/               # atlas, beacon, courier; each has a local main branch
worktrees/           # feature/demo branches with outstanding work
reports/             # Markdown and JSON reports
state/               # isolated KMX resident state
```

The tools have fixed `monitor` and `maintain` argv, with an empty-object input
schema. Readiness runs `project-tool check`, which verifies the demo marker and
runs no rebase. The agent cannot supply repository paths, shell commands or refs.

## 1. Monitor the projects

```bash
/tmp/kmx-project-demo run monitor.agent.md --check-tools
/tmp/kmx-project-demo run monitor.agent.md
/tmp/kmx-project-demo chat monitor --runtime local "Report on the projects."
cat reports/monitor.md
```

The report includes PR titles, check/review status, open issues and priorities,
changes since the previous observation, base drift and actionable follow-up:

| Project | Mock activity | Initial worktree result |
|---|---|---|
| atlas | PR #41 passing/approved; high-priority search issue | `needs-rebase` |
| beacon | PR #22 failing/changes requested; two open issues | `skipped-dirty` |
| courier | PR #8 pending/review required; retry-delay issue | `needs-rebase` |

Monitoring writes reports but leaves Git refs and worktree contents unchanged.
It cannot predict Courier's conflict merely from commit counts.

## 2. Maintain the worktrees

```bash
/tmp/kmx-project-demo run maintain.agent.md
/tmp/kmx-project-demo chat maintain --runtime local "Update the worktrees and report."
cat reports/maintain.md
```

Expected outcomes:

- **Atlas — `rebased`:** local feature commits are replayed onto the advanced main
  commit. `feature.txt` survives and `settings.txt` now contains `timeout=30`.
- **Beacon — `skipped-dirty`:** its HEAD and uncommitted `notes.txt` remain intact.
- **Courier — `conflict-aborted`:** conflicting timeout edits trigger a real Git
  conflict. The adapter aborts that attempt and verifies the original clean HEAD
  was restored; `settings.txt` remains `timeout=20`.

The JSON report retains before/after/base commit IDs and backup ref names. Rebase
attempts first create `refs/demo-backup/<original-head>`. No stash, force-push or
automatic conflict resolution occurs. Unknown/failed restoration is reported as
`unknown`, never as successful maintenance.

Run the maintenance chat again: Atlas reports `current`; Beacon remains dirty;
Courier still needs its conflict resolved. A completed report can have blocked
worktrees. The fake model's reply only says the report was written.

## 3. Simulate new activity

Edit `activity.json`, for example changing Beacon's PR `checks` from `failing` to
`passing` and `review` to `approved`. Invoke the monitor again. Its report identifies
Beacon as changed and updates the attention items. Mock activity text never chooses
commands; only bounded counts and worktree statuses are returned to the model.
Full PR/issue text stays in local reports.

To demonstrate a newly eligible rebase, commit the **fixture's** notes, then invoke
maintenance again:

```bash
git -C worktrees/beacon add notes.txt
git -C worktrees/beacon -c user.name=Demo -c user.email=demo@example.invalid \
  -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -m "Save demo notes"
/tmp/kmx-project-demo chat maintain --runtime local "Update the worktrees and report."
```

Beacon now rebases while preserving the newly committed notes.

## Test and stop

```bash
/tmp/kmx-project-demo test monitor.agent.md --model fake
/tmp/kmx-project-demo stop monitor
/tmp/kmx-project-demo stop maintain
/tmp/kmx-project-demo list --all
```

The monitor's companion Test asserts its tool call and answer. Testing the
maintenance companion also performs real maintenance on the fixtures. The focused
checkout tests (`python3 scripts/test_demo_project_maintenance.py`) verify actual
Git effects, conflict restoration, dirty-file preservation and repeat behavior.

The helper serializes its cycles with a local lock, ignores host Git configuration
and limits each Git subprocess to five seconds. The resident broker retains its
30-second call budget. Interrupted or unknown outcomes require inspecting fixture
state before another attempt. The generated adapter is a fixed fixture program;
editing its bytes requires a resident upgrade. It is not a template for safely
rebasing arbitrary live repositories.

See [resident tool boundaries and Agent Primitives mapping](resident-tools.md) for
the underlying host-user execution boundary. No sandbox, durable Sessions recovery,
public AgentSuite artifact or real-model qualification is established by this demo.
