# Local preview

`internal/kmx/localpreview` runs one [single-file agent](agent-file.md), tool-free
by default,
in the kmx process. Every result is labeled **local preview**. It is not
deployment qualification, suite evidence, a passing receipt, or authority to
publish or deploy. `kmx run FILE`, `kmx test FILE` and MCP Run/Test use it for
fake/local routes through the `agentpreview.Runner` interface.
Selected examples and optional ad-hoc input share one invocation-owned journal;
each gets a fresh Session. The adapter aborts later sends on execution or deadline
failure, and only exposes a private journal handle after successful finalization.

**Tool-free Copilot remains direct and unjournaled.** If either the agent or judge uses
Copilot, the whole invocation uses the existing
[preview runner (no journal)](kmx.md#agent-file-local-preview-unreleased-integration)
and reports `NoJournal`. This preserves Copilot's credential-owning transport:
it does not yet offer the content preflight needed before Sessions retains a
turn. A declared tool with either a Copilot agent or a Copilot judge refuses
before authentication or journal creation. Enabled Copilot models may use Chat
Completions or Responses; the credential-owning transport selects the protocol
from the catalog without changing the preview runner or adding a login step.
Copilot tool support depends on
[#662](https://github.com/kaimahi-agents/kaimahi/issues/662), not this slice.

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

## Host-tool foundation

The optional [Tools declaration](agent-file.md#markdown-contract) names
`github_list_merged_prs`. File Run/Test accepts explicit `--tool-bindings FILE`
for one real read-only GitHub MCP operation, without discovery or workspace
configuration. Missing bindings or credentials refuse `tools_unavailable`;
neither CLI nor inbound MCP silently executes declarations as tool-free agents.
Inbound MCP still has no operator binding selector and cannot grant tools.

The strict local JSON binding is at most 16 KiB and contains references, never
credential values:

```json
{
  "schemaVersion": "kmx.local-tools/v1",
  "bindings": [{"name": "github_list_merged_prs", "endpointRef": "github-mcp", "repository": "owner/repository"}],
  "endpoints": {"github-mcp": {
    "url": "https://api.githubcopilot.com/mcp/readonly",
    "credentialEnv": "GITHUB_MCP_TOKEN"
  }}
}
```

Only that fixed HTTPS endpoint, one repository and one environment-variable
reference are accepted. Use the repository's canonical GitHub letter case: its
returned repository URL must match the binding exactly. Select a clean path (for example `tools.json`, not
`./tools.json`) with no parent traversal or symlink/reparse components. The
bounded regular-file read checks opened-file identity; it is not a hostile
concurrent filesystem sandbox. No token flags, implicit gh/Copilot credential reuse,
keychain login, arbitrary origin, command, headers or dynamic tool schema.
The format is kmx-local, not portable AgentSuite remote execution. AgentSuite's
remote-MCP catalog uses unresolved endpoint/credential references; it does not
currently select a remote declaration for agent execution.

The production runner installs the host factory; trusted embedding code may
still supply `RunnerOptions.Tools`. Runner construction and parsing perform no
credential or network access. A tool-bearing invocation reads only the named
environment variable once, holds that snapshot in memory, and checks supplied
content before journal creation. No token value belongs in the binding file.

Only a model-selected call opens a fresh Streamable-HTTP MCP session. It checks
the remote search schema, calls `search_pull_requests` once with a fixed
repository/merged query and page-one limit of 20, and closes the session before
returning. No PR-by-PR requests or hidden data prefetch. Exact `repository_url`
and explicit `pull_request.merged_at` timestamps establish attribution;
`state: closed` or the search query alone does not. Incomplete searches refuse.
This bounded first page is not an exhaustive PR inventory; `total_count` does
not force a full page or authorize fetching additional items. Only PR numbers and
titles reach the model under fixed repository/merged metadata; no bodies,
authors, comments, arbitrary links or remote descriptions are forwarded.

The protocol is pinned to `2025-11-25`, without discovery/fallback, reconnect,
automatic input-required retries, OAuth or sampling/roots/elicitation callbacks.
To move to a newer protocol, requalify the hosted schema/output, then update the
pin and exact-handshake tests rather than enabling automatic negotiation.
Each read has a 30-second cap within the existing case deadline, a 1 MiB reply
cap and bounded SSE response events; standalone SSE, redirects and ambient
proxy credentials are disabled. Auth/schema/transport/result/close failure
stops the operation without retry or partial success.

The host validates the fixed no-argument call, exact repository, explicit merged
attribution, result count and bounded number/title projection.
Inputs, decoded model responses and projected tool results are checked before
journaling. Exact configured-credential echoes are also refused throughout
host-only protocol data; opaque server icons/session metadata never enter the
model or journal, and SDK logs are discarded. Exact credentials and known credential shapes refuse; encoded or
partial secret copies are not a general data-loss-prevention guarantee.

The opt-in `TestHostedMCPWithScriptedModelJournal` test exercises a real hosted
read with a scripted loopback model, then offline replay without credentials.
It prints no PR content and removes its private temporary journal afterward;
it is not real-LLM tool-selection qualification. Ordinary tests use owned TLS
MCP fixtures and a test-only bridge excluded from shipped builds.

A distinct `kmx-local-tools-v1` harness permits zero or one model-selected read
and at most two agent-model calls. No forced call, parallel calls, pagination,
fanout, writes, automatic resend or fallback. A text Test can pass without a
tool call; actual-call qualification must separately prove one correlated read
from journal/count metadata. `toolCalls` is a safe completion count (zero is
omitted in JSON), not a tool assertion or suite receipt. The unchanged tool-free
judge sees only the final answer; its two samples are separate from the agent's
call limit but share the existing case deadline.

The tool trace has its own complete-invocation validator. The existing five-record
chat and suite proof stay strict. Offline inspection replays recorded calls and
results with live model and executor dispatch forbidden; it needs no binding or
credential and never reruns assertions or judges. Incomplete traces stay incomplete.
Reads are repeat-safe, not an exactly-once network guarantee after a crash.
Projected PR titles and drafts are cleartext journal content, subject to the
same retention and clear operations below. Native Squad parsing stays tool-free.

## Model routes

Preview chooses the exact runtime picker/tool override, then authored
`Model.Preferred`, then `gpt-5.6-terra`. An omitted Model remains omitted in the
source and revision; both runners receive a per-invocation execution copy. The
route still comes from outside the agent file:

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

The standalone library's compatible endpoints follow the existing judge URL
rules: HTTPS, or keyless HTTP at literal `127.0.0.1` or `::1` (for example a local Ollama at
`http://127.0.0.1:11434/v1`). Credentials need HTTPS. The credential is read
from the named environment variable into memory. It is never written to the
agent file, the journal, results or error text. A successful reply whose
answer contains the credential (for example an endpoint that echoes the
`Authorization` header) is refused before Sessions journals it; this catches
the exact value only, not encoded or partial copies. The check applies at any
length, so a placeholder key such as `EMPTY` refuses every answer containing
that word; leave the key variable unset for an endpoint that needs no
credential. Redirects are not followed. Request and response bodies are each
limited to 1 MiB. Replies must name the exact requested model: missing, empty
or different model identities refuse rather than being attributed to the request.

CLI/MCP routes retain the existing model-access policy: fake, or an explicit
keyless local compatible/Ollama/Foundry endpoint. Validated `localhost` and
loopback addresses and the selected base path are preserved; the adapter does
not discover runtimes or silently rewrite the route. Copilot uses the direct
exception described above; unbound MCP selects that route for the effective
non-fake model when no startup provider is configured. Selecting `fake` there
remains offline. Runtime model choice does not change a trusted startup provider,
endpoint or credential source. This does not change the local
adapter's endpoint policy.

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
- instructions, example IDs or inputs, ad-hoc `Run` input, the agent ID,
  revision or model name, or assertion IDs/types and authored judge model/version
  metadata containing the resolved agent or judge credential
  (exact value, any length). Sessions records this content when a turn starts,
  before any reply check, so it is checked before the journal is created.

The [agent file parser](agent-file.md) already refuses credential-shaped
content anywhere in the file, including YAML comments, and in decoded example
inputs and assertion operands. Ad-hoc `Run` input gets the same 16 KiB bound and
credential-shape check. Shape detection cannot catch arbitrary secrets. YAML
comments in Examples are discarded when parsed: local preview receives only
example IDs, inputs and assertions, so comment text never reaches a model or
judge request, the journal or a result.

On the tool-free path, a model reply that contains a tool call (in any key spelling), a refusal, a
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
discarded. For CLI/MCP fake/local execution this replaces the direct runner's
single sample with two samples per judge assertion, within unchanged deadlines.
The Copilot direct exception retains its single-sample policy, including when
only the judge uses Copilot; it creates no journal.

## Journal retention and clearing

- Location: `config.LocalPreviewJournalDir()`, which is
  `<kmx state>/local-preview/journals`. Set `KMX_HOME` to override the kmx
  state directory. Without it, kmx uses XDG config on Linux,
  `Library/Application Support` on macOS, and machine-local
  `%LOCALAPPDATA%\kmx` on Windows.
  The journal is never stored in a checkout.
- **Windows:** `%LOCALAPPDATA%` is machine-local and does not roam with a domain
  profile. Only private state-root creation and lookup are qualified in the
  current Windows foundation; local-preview storage remains behind the guarded
  CLI until its later integration phase under
  [#578](https://github.com/kaimahi-agents/kaimahi/issues/578).
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
