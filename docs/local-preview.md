# Local preview

`internal/kmx/localpreview` runs one tool-free [single-file agent](agent-file.md)
in the kmx process. Every result is labeled **local preview**. It is not
deployment qualification, suite evidence, a passing receipt, or authority to
publish or deploy. It is not wired into the CLI yet: `kmx run FILE` and
`kmx test FILE` use a temporary
[preview runner (no journal)](kmx.md#agent-file-local-preview-unreleased-integration)
until this runner replaces it under
[#598](https://github.com/kaimahi-agents/kaimahi/issues/598).

Status: the hermetic tests (fake model, an in-process HTTP endpoint and the
judge) run with the root Go tests. Native Windows execution has not been run:
the package only cross-compiles (`GOOS=windows`). No run against a real
compatible model is recorded yet. To record one, a human runs:

```sh
KMX_LOCAL_PREVIEW_LIVE_BASE_URL=http://127.0.0.1:11434/v1 \
KMX_LOCAL_PREVIEW_LIVE_MODEL=<installed model> \
go test -count=1 -v -run TestLiveCompatibleModel ./internal/kmx/localpreview
```

For an HTTPS endpoint, add `KMX_LOCAL_PREVIEW_LIVE_KEY_ENV=<variable name>`.
Name the variable; never put the credential value on the command line.

## How it runs

- Each example gets one fresh Session on embedded
  [Sessions](https://github.com/agent-primitives/sessions) (`v0.2.0`): its
  Sessions service, Placer and `sqlitelog` journal, with the built-in `chat`
  harness. That is the same harness contract a deployed AgentSessions host
  serves. The Sessions API and the harness use in-memory gRPC listeners, so
  there is no daemon, socket file, port, subprocess, Docker, WSL or cluster.
- kmx's strict Sessions client checks every turn. A turn must produce the
  complete five-record chat journal and must replay to the same answer with
  zero model calls. If it doesn't, the example is not a pass.
- Instructions are the agent file's prefix before Examples, sent as the system
  prompt. The example input is sent as the user message. Each example gets one
  model request. There are no retries, no fallback model and no tool execution.
  Cancellation and the per-example deadline (default 2 minutes, which also
  covers judging) stop the request in flight.

## Model routes

`Model.Preferred` selects the model exactly. The route comes from outside the
agent file:

| Preferred | Route | Network |
|---|---|---|
| `fake` | kmx's deterministic fake model, served in memory | none |
| any other model | an explicit OpenAI-compatible base URL, plus an optional environment variable name for a bearer credential | that endpoint only |

The fake model answers only these instruction directives (first match wins).
Editing the instructions really changes its answer, so it can't pass every
check:

| Directive in instructions | Fake answer |
|---|---|
| ``Reply with exactly `TEXT`.`` | `TEXT` |
| ``Reply with exactly `TEXT` followed by the input`` | `TEXT` followed by the input |
| ``Call the tool `NAME` `` | a tool-call reply, which is refused |
| no directive | fixed guidance text naming the directive |

Compatible endpoints follow the existing judge URL rules: HTTPS, or keyless
HTTP at literal `127.0.0.1` or `::1` (for example a local Ollama at
`http://127.0.0.1:11434/v1`). Credentials need HTTPS. The credential is read
from the named environment variable into memory. It is never written to the
agent file, the journal, results or error text. A successful reply whose
answer contains the credential (for example an endpoint that echoes the
`Authorization` header) is refused before Sessions journals it; this catches
the exact value only, not encoded or partial copies. The check applies at any
length, so a placeholder key such as `EMPTY` refuses every answer containing
that word; leave the key variable unset for an endpoint that needs no
credential. Redirects are not followed. Request and response bodies are each
limited to 1 MiB.

## Refusals

The following refuse before any journal file is created or any request is sent.
Each refusal says what to change:

- a non-fake model with no configured endpoint, or `fake` combined with an
  endpoint;
- an invalid, insecure or credential-bearing endpoint, or an empty or invalid
  credential variable;
- tool assertions or other unsupported assertion types;
- `llmJudge` examples without the shared judge, or a judge override with no
  `llmJudge` examples;
- instructions, example IDs or inputs, ad-hoc `Run` input, or the agent ID,
  revision or model name containing the resolved agent or judge credential
  (exact value, any length). Sessions records this content when a turn starts,
  before any reply check, so it is checked before the journal is created.

The [agent file parser](agent-file.md) already refuses credential-shaped
content anywhere in the file, including YAML comments, and in decoded example
inputs and assertion operands. Ad-hoc `Run` input gets the same 16 KiB bound and
credential-shape check. Shape detection cannot catch arbitrary secrets. YAML
comments in Examples are discarded when parsed: local preview receives only
example IDs, inputs and assertions, so comment text never reaches a model or
judge request, the journal or a result.

A model reply that contains a tool call (in any key spelling), a refusal, a
truncated or malformed body, a key repeated at any depth (including a
case-insensitive alias), an oversized body, an answer containing the endpoint
credential or a non-200 status is refused.
Nothing is executed. The example is `unknown`, never a pass. Default error
text gives only the status code or the class of problem. It never includes
response bodies, prompts, answers or credentials.

## Judging

Assertions reuse the existing `contains`, `notContains`, `regex` and
`llmJudge` semantics. `llmJudge` uses the native two-sample judge through
`app.LocalPreviewJudge`, so malformed, unavailable, split or inconclusive
judgments are `unknown`, never a pass. The judge uses the agent's route by
default, and the request names each assertion's authored `judgeModel`. With
the fake model, the fake judge answers in memory. It scores 100 when every
backtick-quoted phrase in the rubric appears in the answer and 0 when one is
missing. A rubric with no quoted phrase gets `unknown`. A different judge
endpoint must be set explicitly (`JudgeEndpoint`). Judge reasoning is
discarded.

## Journal retention and clearing

- Location: `config.LocalPreviewJournalDir()`, which is
  `<kmx state>/local-preview/journals`. Set `KMX_HOME` to override the kmx
  state directory. Without it, kmx uses `os.UserConfigDir()/kmx`: XDG config on
  Linux, `Library/Application Support` on macOS, `%AppData%` on Windows.
  The journal is never stored in a checkout.
- **Windows:** `%AppData%` is the roaming profile. On a domain account with
  roaming profiles, the cleartext journal can be copied to the profile server
  at sign-out. Where all Windows kmx state lives is decided by native Windows
  support, tracked in [#578](https://github.com/kaimahi-agents/kaimahi/issues/578);
  until then, set `KMX_HOME` to a local directory or clear journals after use.
- Each preview run is one SQLite file named `<UTC time>-<random>.db` (the time
  has nanosecond precision, so names sort in creation order), with its
  `-wal`/`-shm` companions. While the run executes it holds an OS lock on a
  `<run>.lock` file, removed when the run finishes. The directory is created
  owner-only (`0700`). A directory with group or other permission bits is
  refused, not changed. On Windows, privacy relies on the user profile's
  access control lists.
- **Retention:** the journal stores the instructions, each input, the
  model identity, each answer and the terminal or error records **in
  cleartext**. It never stores credentials. kmx keeps the newest 20 runs and
  deletes the oldest finished runs when a new run starts. A run that is still
  executing, in this or another process, is never deleted. Executing runs, and
  overlapping starts that each count the same runs, can leave more than 20
  runs until the next run starts or the journals are cleared.
- **Clearing:** `localpreview.Clear(dir, runID)` deletes one run, and
  `Clear(dir, "")` deletes all finished runs plus unlocked `<run>.lock` files
  whose journal is missing. A run that is still executing is kept and
  reported as a refusal. Only files named like preview journals and run locks
  are removed. Deleting the directory also clears everything.

## Replay and inspection

`localpreview.Inspect(ctx, dir, runID)` reopens a retained journal and rebuilds
each Session. Without a `ctx` deadline it applies a 2-minute bound. Errors
are fixed-text refusals or context errors. Complete turns are replayed through
the `chat` harness against their recorded completions. Any live model call is
refused and counted. An inspection
is labeled "local preview journal replay; not a test result". It has no
verdicts, and incomplete, failed or canceled turns stay `incomplete`.
Inspection never sends to a model, never re-evaluates assertions and never
creates a journal: the journal opens read-write only if it already exists.

## Executor seam

`localpreview.Executor` sits between the example loop and execution. The
default is the journaled embedded Sessions executor. Any other executor gets
the same `Turn` (exact model and route) and must not retry, run tools, fall back
to another model or report an incomplete turn as complete. Results report
`Journaled` so that a non-journaled runner cannot pass as journaled.
