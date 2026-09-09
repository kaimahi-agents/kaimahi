# Review — PR #158, "Feat/cli ux and interactive chat"

Read 2026-09-09 against main at `a4bcb07`. PR #158, 79 files,
+6637/−731, two commits plus a merge of main.

This is a read-only review. Nothing was pushed to the author's branch and
no comment was posted on the pull request.

## Recommendation

**Merge after three fixes.** The work itself is sound and most of it is a
straightforward improvement. What stands between it and main is a merge
cadence problem, not a quality problem: the branch was cut before #157
("The tool seam works for a client we did not write") landed, and two of
the three fixes are single lines. The third is a real design question
about `kmx tools govern` that the author should answer rather than have
chosen for them.

None of this is the author's fault. #157 landed on 2026-09-08 and this
branch's merge of main (`a6ecb7a`) picked up only as far as #156.

## Does it still apply?

Yes. The branch's own merge of main was done carefully — spot-checked on
the two files where #155 and this branch both edited the same functions,
and it kept main's content on both sides:

- `lift_verify.go` retained #155's PodMonitor guidance and the sentence
  saying the run does not touch the cluster-wide
  `ama-metrics-prometheus-config` ConfigMap.
- `lift_down.go` retained #155's ownership comment while adopting this
  branch's error-returning `removeInClusterObservability`, which is a
  genuine improvement: teardown now fails loudly instead of dropping the
  error.

A local `git rebase origin/main` is the wrong instrument here and its
result should be ignored. Rebase discards the merge commit and replays
the two original commits from before it, so it re-raises the #155
conflicts the author already resolved by hand. Merging main into the
branch — which is what GitHub will do — applies cleanly with no
conflicts.

## Verification performed

Done in a throwaway worktree with `origin/main` merged into the PR
branch; the worktree has been removed and nothing was pushed anywhere.

| Check | Result |
| --- | --- |
| `git merge origin/main` into the branch | clean, no conflicts |
| `go build ./...` | passes |
| `go vet ./...` | passes |
| `go test ./...` | **2 failures**, both in `internal/kmx/app` |
| `scripts/check-repository-map.py` (the map itself) | passes, 27 claims |
| `scripts/check-repository-map.py --selftest` | **fails**, 2 cases |
| `scripts/check-mutations.py` | **fails**, consequence of the above |
| the other ten checkers | pass |
| PR checks as GitHub ran them | `hygiene` and `e2e-quickstart` red |

`check-board.py` also fails, on both this branch and plain main — three
board rows for lanes that have merged. That is coordinator debt, not
anything to do with this PR.

`e2e-hello-world` shows as failing only because it is the aggregator; the
one shard that actually failed is `e2e-quickstart`.

Not evaluated: the interactive chat experience itself. The PTY tests are
substantial and they pass, but nobody drove the TUI by hand in this
review, so the rendering, the resize handling and the `/help` grouping
are unverified beyond what the tests assert.

## The three things that need fixing

### 1. A stale mutation string in the map checker's self-test — one line

Both the red `hygiene` job and the `check-mutations` failure come from a
single line. The branch adds a file to `cmd/kmx` (17 files now, was 16)
and a package to `internal/kmx` (`cliui`). `docs/repository-map.md` was
correctly updated for both, and the self-test's mutation string was
updated for the package count — but not for the file count:

```python
("| `cmd/kmx` (16 files)", "| `cmd/kmx` (15 files)",
 "gets a binary's file count wrong"),
```

That edit no longer matches anything in the map, so the case tests
nothing, and the checker's own "every claim must be broken by some case"
rule then reports that nothing exercises `the_cmd_table_covers_cmd`. Two
FAILs, one cause. Changing `16`/`15` to `17`/`16` was verified locally to
turn the self-test green:

> `27 claims, each broken by at least one of 28 map edits and 5 tree
> changes, every one caught`

Worth saying plainly: this is the map checker doing exactly the job it
was built for. It noticed a count going stale in the same change that
made it stale.

### 2. `kmx quickstart` is no longer safe to run twice — one line

This is the `e2e-quickstart` failure, and it is a real behaviour
regression rather than a test artifact. In `up.go`:

```go
if len(extra) > 0 {
	args = append([]string{"install"}, args[2:]...)
}
```

`installKagent`'s doc comment says quickstart "checks for absence first
and uses install, not upgrade, so a concurrently created release is
preserved". The check does not hold. `stepQuickstartKagent` calls
`installKagent` when the release is absent **or** when it is present and
already carries the minimal first-answer profile:

```go
if !present || minimal {
	...
	return a.installKagent(quickstartValues...)
}
```

Quickstart is the only caller that passes extras, so the second run of
quickstart on the same cluster reaches `helm install` against a release
that exists. CI caught it exactly there:

```
Error: INSTALLATION FAILED: cannot re-use a name that is still in use
FAILED [4/6] Install or verify kagent (2.9s)
```

The rest of that change is good and should stay — querying each Helm
status independently is a correct fix for Helm 3 intersecting combined
status flags, and refusing an undecodable release list rather than
reading it as absence is the right instinct. Only the verb switch is
wrong. Either keep `upgrade --install` for the present-and-minimal path,
or narrow the `install` verb to the `!present` case.

The general shape is worth naming, because the comment is the part that
misleads: it asserts a precondition ("checks for absence first") that the
caller does not enforce. The code and the sentence describing it drifted
apart inside one change.

### 3. `kmx tools govern` collides with #157 — a decision, not a fix

This is the substantive one, and it is where the author's judgement is
needed rather than a reviewer's.

The two test failures on the merged tree are:

```
--- FAIL: TestGovernToolsCompletesOnAClusterWithNoKagent
    governing a foreign runtime must complete: kmx tools govern: the applied
    RemoteMCPServer references Secret kagent/kaimahi-tools-token; unsupported
    --secret or --secret-namespace would issue an unusable token
--- FAIL: TestGovernToolsStillDrivesKagentWhereItIsInstalled
    the kagent path did not run "patch agent"
```

They have two different causes.

**The second is mechanical.** The branch adds a case to the shared fake
`kubectl` in `govern_test.go`:

```sh
*"get remotemcpserver"*) printf '%s' "$KMX_TEST_TOOL_SERVER"; exit 0 ;;
```

#157's tests rely on an existing `*"get remotemcpserver"*` case further
down that serves `$KMX_TEST_SEAM`. Shell `case` takes the first match, so
the new arm shadows the old one and the seam verdict read comes back
empty — the wait then times out and the agent is never patched. Folding
the two arms into one (prefer `$KMX_TEST_TOOL_SERVER`, fall back to
`$KMX_TEST_SEAM`) was verified locally to fix this test with nothing else
changed.

This deserves a moment of attention beyond the fix. Both branches were
honestly green, git merged the file with no conflict marker, and the
result is broken — the same shape as the D44 incident, arriving this time
through a shared test double rather than an import block. A fake that
dispatches on first match is a place where two correct additions compose
into a wrong one, silently, and it will not announce itself.

**The first is a design conflict.** The branch adds, at the top of
`GovernTools`:

```go
if opt.SecretNamespace != config.DefaultNamespace ||
	(opt.Server == config.DefaultToolServer && opt.Secret != config.DefaultToolsSecret) {
	return fmt.Errorf("kmx tools govern: the applied RemoteMCPServer references Secret %s/%s; ...")
}
```

That refusal is correct for the world the branch was written in, where
the seam is the committed `kaimahi-tools.yaml` and its Secret is fixed.
#157 deliberately made both variable: `kmx tools add` scaffolds a seam
whose Secret and namespace the operator chooses, and `kmx tools govern`
must complete on a cluster with **no kagent at all**, writing the
credential and the CA into the runtime's own namespace. The refusal above
forbids precisely that.

Narrowing the guard to the committed-seam case gets one step further and
then hits the same wall again:

```
cannot inspect RemoteMCPServer kaimahi-sundae credential reference:
unexpected end of JSON input
```

The branch's new "verify the seam's `Authorization` header references the
expected Secret" block runs before the no-kagent check and reads
`-n kagent get remotemcpserver`. On a cluster with no kagent there is no
such object and no such CRD, so the read cannot succeed. The block needs
to move inside the `seamInstalled` branch, and its namespace needs to
follow the seam rather than being hard-coded.

I stopped there rather than pushing further, because the right resolution
is a question about intent: the branch's premise is "the seam is ours and
its Secret is fixed"; #157's premise is "the seam may be an application we
did not write, in a namespace we do not own". Both are defensible
readings of what `kmx tools govern` is for; they cannot both be enforced.
#157 is on main and has a live CI shard behind it, so the practical
answer is almost certainly to keep the header verification as a check
that runs *when there is a seam to check*, and to drop the unconditional
namespace refusal. But that is the author's call, and naming it is more
useful than deciding it for them.

Note that #157's `requireNamespace` — the pre-mint check that refuses
before a token is shown once and lost — survived the merge intact. That
invariant is not at risk.

## What the e2e-foreign shard will say

Checked because it is the shard most likely to be affected and it has
never run against this branch: #157's `e2e-foreign` job survives the
merge unchanged, and stays in the required aggregator's `needs` list. No
CI coverage is lost.

It will, however, fail for the same reason as finding 3. The shard
asserts that `kmx tools govern --secret-namespace acme --server
kaimahi-warehouse` completes and prints "This cluster has no kagent" —
exactly what the new refusal blocks. The unit failure and the live shard
are one finding, not two.

## Invariants

All held.

- **No new credential material acceptance.** Nothing in the diff reads a
  secret from anywhere; the terminal-only prompt from the credential
  capture lane is untouched.
- **Every mutation still goes through the context guard.** The guard
  itself gained a rich rendering and, more usefully, shell-quoting of the
  context name in the `KAIMAHI_CONFIRM=` retry line. The `isTerminal`
  change from `Stat`/`ModeCharDevice` to `term.IsTerminal` makes the
  interactive prompt strictly harder to reach, which is the safe
  direction.
- **No lane or decision identifiers in the new code.** Grepped the whole
  added diff for `P<n>`/`W<n>`/`D<n>` — zero hits. The comments say what
  the thing does. That is a marked contrast with parts of the existing
  tree and is worth acknowledging.

## Would a checker catch a regression this introduces?

Mostly yes, and one of them already did — the map checker caught its own
stale count, and `check-mutations` caught the map checker being unable to
prove itself.

The area where new UX code could start lying quietly is governance state,
and the branch handles it correctly. `status.go` keeps `unknown` distinct
from `none` throughout (`"unknown — " + reason`), which preserves the
"never invent a zero" rule, and it adds an explicit line separating two
claims a reader could otherwise conflate:

> Governed means the cluster object points at the plane; the plane field
> says whether enforcement is available.

The `quickstart` change is in the same spirit: `governed: false` is now
documented as invocation-scoped rather than a cluster assertion, and the
prose moved from `UNGOVERNED` to "This command does not enable
governance", with the CI grep updated in the same commit so the assertion
follows the words. The JSON key set is unchanged, so automation does not
break.

## Two smaller things, neither blocking

**The dependency footprint is not justified in the PR body.** The change
adds four direct and twelve indirect modules — the `lipgloss` v2 stack,
`ultraviolet`, `cancelreader`, `uniseg`, `go-colorful` and friends — to a
repository that until now built on `cobra`, `yaml` and `x/term`. One of
them (`ultraviolet`) is pinned to an untagged pseudo-version. The prime
directive asks for net-new to be justified in writing in the PR, and
dependencies are the clearest case of that. This is a request for two
sentences, not an objection: a rich terminal UI plausibly needs a
rendering library, and `lipgloss` is the obvious one.

**`InvocationCommand` overrides the per-command retry strings.**
`guardWith` does:

```go
if a.InvocationCommand != "" {
	command = a.InvocationCommand
}
```

and `appRun` sets `InvocationCommand` for every command except `chat`.
The many new `a.operationCommand("tools", "govern", ...)` strings added
across `app` are therefore only reachable from interactive chat slash
commands. That appears to be the intent — the comment says as much — but
it means those strings are unexercised on the path most users take, and
any test asserting on them is asserting something the CLI does not print.
Worth a glance to confirm the coverage is where it is meant to be.

A cosmetic consequence of the same code: `state.argv` is appended after
an injected `--context <resolved>`, so when the user typed `--context` it
appears twice in the printed retry line. Harmless — the last flag wins
and both name the same context — but it reads as a bug to whoever copies
it.

## What is good here, and should not get lost

Enumerated because a review that lists only findings misrepresents the
change:

- The Helm status handling is a genuine correctness fix, independent of
  the verb regression sitting next to it.
- `removeInClusterObservability` returning its error, and teardown
  failing on it, closes a real silent-failure path in the AKS lift.
- Refusing `--interactive` with `--json` before the application loads is
  the right place for that check.
- The shell-quoting work — in the guard's confirm line, in the retry
  advice, and in the quickstart `next` commands — is exactly the class of
  thing that bites later, and the CI assertion was rewritten to
  `shlex.split` the commands rather than string-match them, which is a
  better test than the one it replaced.
- The PTY and session test files are substantial and were written
  alongside the feature rather than after it.

## Suggested path

1. Author merges current main into the branch (not a rebase — it would
   re-raise the #155 conflicts already resolved).
2. Fix the `cmd/kmx` mutation string: `16`/`15` → `17`/`16`.
3. Restore `upgrade --install` for the present-and-minimal quickstart
   path, and correct the `installKagent` comment to match.
4. Fold the duplicated `get remotemcpserver` arm in the fake `kubectl`.
5. Decide the `kmx tools govern` question: keep the header verification
   where there is a seam to verify, drop the unconditional
   `--secret-namespace` refusal, and move the seam read inside the
   `seamInstalled` branch.
6. Re-run, expecting `e2e-foreign` to be the shard that confirms 5.

Steps 2–4 were each verified locally to do what is claimed. Step 5 was
deliberately left open.
