# Local preview

## CLI storage, privacy and evidence caveats

This is the shared caveats page linked by file Run, Test and Explain help.
File previews on fake, local and Copilot models use embedded Agent Sessions.
They retain instructions, inputs, answers, judge payloads and reasoning in
cleartext SQLite journals under the kmx state directory (`KMX_HOME` when set),
in `local-preview/journals`. The journal is never suite evidence or permission
to publish or deploy. Copilot content checks stay provider-owned and run before
journaling. Replay makes zero model calls; it is inspection, not a new test.

Journal retention keeps the newest 20 runs at each start; active runs are kept
and concurrent starts can leave more. Stop previews before removing journal
files there to clear retained content. See [retention and clearing](#journal-retention-and-clearing)
for private-file, active-run and platform boundaries; no new cleanup CLI is implied.

Completed non-pass Test examples also retain bounded diagnostics under
`eval-failures` (last 10 per resolved file subject across revisions).
Use `kmx test explain --file FILE` to inspect them offline; `--show-content`
opts into bounded excerpts, and `--clear` clears one ID or that file subject.
The stores are independent: deleting journals does not clear failure diagnostics,
and clearing failure diagnostics does not delete journals, source history or
passing proof. See [retained Test failures](#retained-test-failures) for scope,
revision attribution, bounds and retention-unavailable behavior.

## Copilot sign-in in CI

The app's sign-in is not the runner's sign-in. Supply your own Copilot-enabled
GitHub credential as the runner's `COPILOT_GITHUB_TOKEN` environment variable;
do not put a token in the agent file, command arguments or workflow source.
In GitHub Actions, store it as a repository or environment secret (named
`COPILOT_GITHUB_TOKEN` in this example), then expose it only to the test step:

```yaml
- name: Test with Copilot
  env:
    COPILOT_GITHUB_TOKEN: ${{ secrets.COPILOT_GITHUB_TOKEN }}
  run: kmx test ./release-notes.agent.md --provider copilot -o json --github-summary
```

Run after checkout and installation of a current-main binary, with the agent's
model set to an exact enabled ID or an explicit `--model ID`. The default
non-fake route is Copilot; the explicit provider above makes the CI choice clear.
The workflow-provided `GITHUB_TOKEN` is not a substitute for a credential with
Copilot model access. Do not print or echo the secret. No interactive login or
keychain setup is needed when a valid explicit token is supplied. Missing,
invalid or refused credentials stop execution; kmx never switches credentials
after an invalid explicit token. See [credential precedence](kmx.md#copilot-model-access).

Use `kmx test FILE -o json --github-summary` to gate on all saved tests.
Exit 0 means pass, 1 means assertion failure, 2 means it could not run or decide,
and 130 means cancelled; flag-parsing and output/summary-write failures also exit 1.
A case filter is always partial. `--no-content` omits answers and failure operands
from JSON; the step summary is content-free regardless. The
[CI report and artifact example](copilot-app.md#run-the-same-tests-in-ci) preserves
the test's status while retaining JSON. Keep content-bearing reports private as
needed. Neither a report nor a green preview is package deployment evidence,
and the journal and failure-retention rules above still apply on a runner.
The fake model checks wiring offline; it does not qualify hosted Copilot access.

## Preview execution

`internal/kmx/localpreview` runs one [single-file agent](agent-file.md), tool-free
by default,
in the kmx process. Every result is labeled **local preview**. It is not
deployment qualification, suite evidence, a passing receipt, or authority to
publish or deploy. `kmx run FILE`, `kmx test FILE` and MCP Run/Test use it for
fake/local and default or explicit Copilot routes through the `agentpreview.Runner` interface.
CLI `kmx run FILE` executes the saved inputs and returns answers without evaluating
assertions or setting up a judge. Explicit positional input runs that input; saved
inputs explicitly selected alongside it keep their own fixtures. Completed Run
rows are **ANSWERED**, not passing tests; JSON keeps an unknown verdict and
`fullPass: false`. No judge is selected or authenticated for CLI Run. Test and the
existing single-source MCP Run/Test behavior continue to evaluate checks. MCP
Run/Test also accept schema version 3 with a checked `sourceVersion` from the
source resource, running the selected agent/tests pairs: Run answers without
checks, and Test evaluates the selected checks. Version-3 Create/Edit and
post-journal tool capture share checked sequential source publication, complete
pair-preimage history and current-binding restore. Inspect per-source partial or
uncertain acknowledgements; do not retry or Test automatically. Frozen recordings
replay read-only. Canvas activation is still deferred. Legacy/native versions stay separate. File
CLI selects a [separate tests file](agent-file.md#tests-in-a-separate-file-reader-only)
by the shared default rule or `--tests FILE`; its examples never enter the agent's
prompt. Original/unselected agent content and raw tests prose still undergo the
same credential preflight. Executed combined revision and saved target/revision
are separate acknowledgements; tests-only edits stale retained results. Legacy
MCP/canvas capabilities refuse a present default tests file until their paired
reader/writer slice lands rather than silently reporting an agent-only result;
the canvas paired loop remains deferred.

Selected examples and optional ad-hoc input share one invocation-owned journal;
each gets a fresh Session, as does each of the two samples when judging is requested. The adapter aborts later sends on execution or deadline
failure, and only exposes a private journal handle after successful finalization.

**Tool-free Copilot is journaled too.** Its transport performs credential checks
before any Session starts; token values never leave the provider. Copilot agent
and judge routes share one invocation-local credential snapshot, established
before the journal opens. There is no mid-run credential refresh, inference retry
or fallback. Expiry, logout through that provider, authentication refusal or
cancellation stops later samples and examples; start a new preview to establish
fresh credentials. Logging out in another kmx process does not revoke this
in-memory snapshot or stop a running preview; cancel that preview explicitly.
It ends within the five-minute invocation bound. Ordinary model listing and
non-preview access keep their existing refresh behavior. Unbound Copilot agent
tool names use stubs answering "not available in this test"; bound tools execute
through their trusted host. Ordinary model access is still required, and judges
remain tool-free.
Enabled Copilot models may use Chat Completions or Responses; the provider
captures the catalog-selected protocol with the credential snapshot. The same
path is retained while the catalog still advertises it; disappearance aborts,
never switches protocols. Responses requests remain nonstreaming with
`store:false`. The provider checks the raw envelope (including discarded fields)
and translated completion, so joined output fragments cannot bypass the check.

Status: the hermetic tests (fake model, an in-process HTTP endpoint and the
judge) run with the root Go tests. Hosted Windows amd64 natively executes the CLI
against one real Markdown file through package-free fake Run, an instruction-
regressed failing Test, retained diagnostics and offline Explain. It checks that
Explain sends nothing and neither private state family keeps an ambient
credential canary. That qualifies only the narrow
[native Windows day-zero path](windows.md), not providers, tools, clusters,
suite evidence or native arm64 execution. Windows arm64 remains cross-build/vet
and checksum evidence only. No run against a real compatible model is recorded
yet. To record one on a supported non-Windows route, a human runs:

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
  model request when tool-free. A bound tool case permits up to five tool calls
  from its first response, executed serially, and one follow-up model send. There are no retries or fallback models.
  Cancellation and the per-example deadline (default 2 minutes, which also
  covers judging) stop the request in flight.

## Host-tool foundation

The optional [Tools declaration](agent-file.md#markdown-contract) names
`github_list_merged_prs`. Explicit `--tool-bindings FILE` retains the fixed
read-only GitHub MCP operation described below. File Run/Test can also select
named stdio MCP servers from the agent folder's `kmx.yaml` (see below). An unbound name is offered as a bounded unavailable stub answering
"not available in this test"; it is never silently dropped. Malformed supplied bindings,
missing live credentials and broken calls still refuse. [Per-test recordings](agent-file.md#per-test-recorded-tools)
supply saved output without any tool binding. A pending Recorded tool (`calls: []`) captures its first successful
live result and saves it with its test after verified journal finalization. Ordinary Live, replay and unavailable
responses never become recordings; uncalled tools remain pending. Save errors are explicit and withhold full success.
Default inbound MCP remains unbound; tool input cannot supply a binding.
Bound tools support local, fake and Copilot Chat Completions/Responses routes;
judging remains tool-free. Copilot checks current tool capability before each
model send. Responses reasoning/continuation items stay invocation-private and
are never included in Sessions' journal. File previews do not stream.

The names-only declaration supports up to 32 unique tool names. Trusted embedding
may supply `RunnerOptions.ResolveTools` and `localtools.NewTrustedHost` with copied
schemas, descriptions, an executor and a content checker. Model arguments must
match the selected host schema and stay within 8 KiB; results stay within 32 KiB.
One case executes at most five tool calls and two agent model sends. The whole
first-response batch validates before its first call; duplicate IDs, a sixth
call or invalid later arguments refuse the batch. The final response must be
text, not another tool round. App tools still use the app's native permission
handling on every call.
Per-test definitions and versioned dispatch-source selection are journaled to reconstruct the exact request during
zero-call offline replay; endpoint/binding credentials are not. Source selection is not proof of capture or of a call.
Old fixed-read journals remain inspectable. Their source metadata is absent (`ToolSources` is nil), meaning
source not recorded, never assumed live. Older kmx
builds refuse the new source metadata, including the optional `capturing` flag, rather than reinterpret it.
That flag is only valid for Recorded/live selection and is not capture proof by itself. Publication also requires
the trusted live observation matched to the complete submitted journal, and a checked source-file edit. A captured
result keeps its executed revision and separately returns the saved revision; it does not become fresh evidence
for the new source bytes. No execution retry follows a stale or failed save. There is no general CLI binding-file format; the explicit
CLI binding below remains the fixed read. Copilot's runtime-owned binding is
separate and requires no hand-edited file.

### Recorded and unavailable tools in terminal tests

Populated per-test Recorded tools replay their saved results without resolving a
live host or tool credential. A different tool argument stops the run with a
recording mismatch; it never falls back to a live call. An empty Recorded entry
is pending capture, not a saved result. Without a selected live source or app host,
it stays unavailable and the source file is unchanged.

Text Run/Test reports `No recording; tool unavailable.` beneath each Enabled
unavailable tool, even if it was not requested. Disabled tools do not show this
note. JSON keeps the exact `mode`, `source`, `callCount` and `toolSettings`; the
Actions summary explains displayed unavailable rows without including arguments
or results.

Calls checks count Enabled requests, including requests to unavailable tools;
they do not prove successful live execution. A Test can therefore pass a Calls
check while reporting `source=unavailable`. Inspect the source as well as the
verdict. Recorded tool replay is offline, but a hosted model or judge still needs
network access. A fake-model test without judge assertions or live tool sources
is fully offline. Selecting `fake` does not disable an Enabled live binding;
`--tool-bindings` or a selected stdio server can still make a network request.

### Selected stdio servers in file Run/Test

Only the file CLI consumes `mcpServers` and `liveToolServers` beside the checked
agent file. Definitions alone do nothing. The Copilot app and `kmx mcp` keep
their host-provided tools; these launch keys do not replace that bridge.

Example shape (replace the placeholder command and tool names with your server):

```yaml
version: 1
mcpServers:
  notes:
    command: /absolute/path/to/your-mcp-server
    args: []
    env:
      AUTH_TOKEN: NOTES_TOKEN  # host environment variable NAME, not its value
    tools:
      lookup: search_notes    # declared agent name: exact server tool name
liveToolServers: [notes]
```

```sh
kmx test example.agent.md --mcp-server notes
kmx run example.agent.md --mcp-server notes --mcp-server another
kmx test example.agent.md --no-mcp-servers
```

Repeatable `--mcp-server NAME` **replaces** the saved list. `--no-mcp-servers`
clears both saved and explicit selections, even if supplied together; it never
prompts. The fixed `--tool-bindings` path takes precedence if also supplied.
A selected server starts only for mapped, declared **Enabled** tools needing a
live source. Disabled, unmapped and unselected tools do not resolve credentials
or executables. Populated test recordings replay without launch resolution;
argument mismatch never falls back to live. Ad-hoc input has no per-test fixture
and may need live tools even when saved tests have recordings.

The working directory is the agent folder. Commands may be absolute paths,
paths relative to that folder, or executable names resolved through the frozen
PATH. There is no shell. Only PATH is inherited; HOME, TMPDIR and other needs
must be explicit named refs in `env`. Configuration and each used env reference
are frozen before the first launch. Every explicit ref is conservatively
protected as potentially sensitive at any length; a short value such as `1`
can conflict with protocol metadata. Path-valued refs such as HOME are protected
too: arguments and results containing that path are withheld (use executable
names and folder-relative arguments where appropriate). Use non-secret command
arguments for ordinary settings rather than weakening credential checks. The
complete child environment is bounded to 64 entries including PATH: with an
inherited PATH, at most 63 additional refs fit unless PATH itself is overridden. Missing refs, bad selections,
startup/protocol/call failures or uncertain cleanup stop completion (exit 2);
cancellation remains 130. Pending Recorded tools use the existing checked
capture path only after owner cleanup; a cleanup failure cannot save a capture
or publish full success.

For fully offline Actions tests, use recorded tools, a fake model and no judge
assertions or fixed live bindings; fake alone does not disable live sources:

```sh
kmx test example.agent.md --provider fake --model fake --no-mcp-servers -o json --github-summary
```

Destination commands ignore only these two launch subtrees after checking YAML
structure, so args/lists do not break operational config. Aliases, anchors,
duplicate keys, known credential shapes and existing size bounds still reject;
only the tool consumer validates launch semantics.

### Stdio host lifecycle

The caller supplies a resolved absolute executable and directory, an explicit
frozen environment and exact local-to-server tool-name mappings. There is no
shell, ambient environment inheritance, server discovery, automatic retry or
credential sign-in. Only mapped tools become trusted definitions. Text results
project as `text`; structured object results take precedence over accompanying
text. Binary/resource results and input-required responses cannot complete a
call. Protocol frames are bounded and checked before SDK decoding; child stderr
and SDK logs are discarded. The trusted caller identifies credential env keys
separately from ordinary settings such as `DEBUG=1`; every identified credential
value is checked exactly, at any length, including decoded protocol metadata.
Known credential shapes are also rejected in model/journal-bound data. This is
not a general secret detector or a sandbox. Server-initiated requests and
notifications are unsupported; they stop the session rather than invoking a
callback.

Linux, macOS and WSL processes get invocation-owned process groups, pipes and
bounded kill/reap cleanup; any failed call or cancellation poisons the session.
A poisoned operation and an uncertain kill/reap have distinct fixed errors; both
withhold completion, including cleanup failures during startup. The invocation
must use its outer context, not the runner's shorter-lived resolver context,
and must check the owner's `Close` before accepting a result. Leader exit is
observed without reaping before cleanup sends its own kill, so a previously
killed server cannot pass merely because a descendant holds stdout open.
Cleanup failure withholds success. Descendants that deliberately escape their group with
`setsid` are outside this cleanup guarantee. Native Windows tool execution
remains unavailable. Adapter tests use real owned subprocesses and a scripted
fake model; they are not qualification of arbitrary servers or hosted models.

## Folder tool settings

File Run/Test reads `kmx.yaml` beside the agent's physical file, never from a
parent folder or the current workspace. Settings are committed with the folder,
not written into the agent file. Missing settings mean **Enabled**:

```yaml
tools:
  github_list_merged_prs: Disabled
  ping: Enabled
```

The canvas shows each declared tool's **Enabled** or **Disabled** state next to
Tests, without a switch. Edit `kmx.yaml` to change it. Other keys and comments
are not rewritten. Invalid or ambiguous YAML and unsupported states refuse the
preview before execution; the canvas also refuses to load that agent view until
the YAML is fixed. **Ask** is not supported yet.

Bindings and schemas still resolve as before. A Disabled request returns
"This tool is disabled for this test." to the model instead of calling the
native tool. It still consumes one of the five batch slots. Enabled app calls
continue through the app's unchanged permission handling.

Execution results' `toolSettings` record every declared tool's state frozen at run
start. Pre-execution refusals have no execution provenance. `toolCalls` counts
Enabled dispatches, not Disabled answers. Per-case tool sources distinguish live,
recorded and unavailable dispatch; a selected source alone is not proof of a live
call. Disabled also bypasses saved-recording matching and unavailable stubs. Changing
the effective settings makes an earlier result out of date even when the agent
file is unchanged. Offline replay uses the journaled settings and results,
never today's YAML or a live executor. Older journals without settings retain
Enabled semantics; older readers can refuse new settings-bearing journals.
The safe JSON report projects these settings and per-test source/call counts
without definitions, arguments or results. It also records monotonic total and
per-test `durationMs`; those timings are diagnostics, not model billing or
deployment evidence.

## Copilot host tools

With a capable Copilot SDK host, the extension runtime binds names to its actual
currently offered MCP tools and their input schemas. Match the server's raw tool
name exactly. Multiple offered servers with the same name, missing/deferred
schemas, tools from the original kmx server or this preview child, and non-MCP
command/builtin tools refuse; nothing is silently loaded or selected. These two
server identities are excluded, not every separately configured kmx alias. The binding is frozen per invocation and checked
again before native execution. The native `tools.execute` pipeline still owns
permission decisions; kmx adds no approval handler or retry.

The runtime starts a session-only kmx preview MCP process, preserving the
original server's checked root/provider/endpoint/cwd. Its Run/Test tools are
model-callable, not hidden authorizations; canvas UI is unchanged. Edit stays
on the original server; preview observations are read from the executing child.
Closing a canvas keeps the session-owned child, so reopening cannot revive the
original process's older result. Runtime/session teardown stops it and remains
unavailable rather than falling back. Windows-to-WSL uses the trusted kmx binary
to pipe one signed request/response; Windows localhost forwarding is not needed.
Immediately before native dispatch, a signed one-shot claim checks that the Go
call is still pending and its context is live. Cancellation or expiry completed
before this dispatch commitment refuses without starting the native operation.
After commitment, the delegated operation may finish after cancellation. A native
permission wait can also outlast the 30-second relay call; kmx cannot cancel that
already-committed host operation. Its journal stays incomplete, late results are discarded, and
nothing is retried. A waiting native call serializes the relay until it finishes.

Only ephemeral public signing keys appear in startup/discovery. Private keys
stay in memory, and signed requests/responses bind exact bytes and fresh nonces.
No app configuration or credential is copied, no new binding file is read, and
no endpoint/token can come from authored source or Run/Test arguments. The
original server/preferences are unchanged. Unsupported SDK/startup/platform
combinations retain the plain unavailable/unbound reason. These are runtime
contracts, not a claim of qualification for every installed host or tool.

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

A distinct `kmx-local-tools-v1` harness permits zero to five model-selected tool
requests in one batch and at most two agent-model calls. Calls execute in order;
failure or cancellation stops later dispatch and prevents a full pass. There is
no forced call, parallel execution, adaptive tool round, automatic resend or
fallback. Each fixed GitHub read is one unpaginated query and never writes. A text Test can pass without a
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

The following describes the standalone preview library. For file CLI use,
`--model` overrides the authored model, and a non-fake model defaults to Copilot
with an existing sign-in. Select a local route explicitly with `--provider`
and `--provider-url`; the CLI [route examples](kmx.md#agent-file-local-preview-unreleased-integration)
show both. No route is discovered or installed during Run/Test.

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
| ``Call the tool `NAME` `` | calls the named tool only when offered by a bound host; otherwise refused |
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
not discover runtimes or silently rewrite the route. Explicit Copilot selection
uses its fixed endpoint and provider-owned preflight described above. Before
journaling, it checks identities, instructions, inputs, assertion operands and
judge metadata against both the source credential and exchanged bearer. Checked
responses include decoded JSON strings and the exact terminal-sanitized and
control-compacted display views, so JSON escaping or display normalization
cannot assemble an unchecked exact credential. This is not detection of every encoded, partial or unrelated secret.
Unbound MCP selects Copilot for the effective non-fake model when no startup
provider is configured; selecting `fake` remains offline. Runtime model choice
does not change a trusted startup provider, endpoint or credential source.

## Refusals

The following refuse before any journal file is created or any request is sent.
Each refusal says what to change. Assertion/judge setup refusals apply when checks
are evaluated, not to answer-only CLI Run. Source validation and credential/content
checks still cover the authored assertions even when Run does not evaluate them:

- a non-fake model with no configured endpoint, or `fake` combined with an
  endpoint;
- an invalid, insecure or credential-bearing endpoint, or an empty or invalid
  credential variable;
- tool assertions in standalone tool-free Run/Test, or other unsupported assertion types;
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

Test and the default MCP/library preview path evaluate assertions using the
existing `contains`, `notContains`, `regex` and `llmJudge` semantics. CLI Run skips
assertion evaluation, judge selection/authentication and judge sessions, while
retaining completion attribution, tool evidence and journal-finalization checks. `llmJudge` uses the native two-sample judge through
`app.LocalPreviewJudge`, so malformed, unavailable, split or inconclusive
judgments are `unknown`, never a pass. The judge uses the agent's route by
default, and the request names each assertion's authored `judgeModel`. With
the fake model, the fake judge answers in memory. It scores 100 when every
backtick-quoted phrase in the rubric appears in the answer and 0 when one is
missing. A rubric with no quoted phrase gets `unknown`. A different judge
endpoint must be set explicitly (`JudgeEndpoint`). Judge reasoning is
not displayed by default. CLI/MCP execution uses two samples per judge assertion,
including Copilot, within unchanged deadlines. Each sample has a fresh tool-free
Session in the same journal as the agent turn, even with a different judge model.
The journal retains the judge policy, input/answer/rubric payload and response
(including reasoning) in cleartext. Malformed or unavailable replies abort later
sends; a valid split verdict stays unknown.

## Journal retention and clearing

- Location: `config.LocalPreviewJournalDir()`, which is
  `<kmx state>/local-preview/journals`. Set `KMX_HOME` to override the kmx
  state directory. Without it, kmx uses XDG config on Linux,
  `Library/Application Support` on macOS, and machine-local
  `%LOCALAPPDATA%\kmx` on Windows.
  The journal is never stored in a checkout.
- **Windows:** `%LOCALAPPDATA%` is machine-local and does not roam with a domain
  profile. The native day-zero CLI qualifies writable state only on local NTFS
  with persistent ACLs, current-user ownership and a protected owner-only DACL;
  unsafe existing ACLs are refused, never repaired. Non-NTFS, network/UNC,
  device, SUBST/remapped, stream and exposed symlink/junction/reparse state roots
  are refused. Synchronized folders are outside the qualified boundary even when
  their current NTFS representation is not exposed as a reparse point. Native
  and WSL writable state must be
  physically separate because their locks do not coordinate; WSL/Linux refuses
  recognized Windows-drive mounts for state. See
  [the complete Windows state boundary](windows.md#writable-state), including
  the read-only source distinction and lack of power-loss/backup certification.
- Each preview run is one SQLite file named `<UTC time>-<random>.db` (the time
  has nanosecond precision, so names sort in creation order), with its
  `-wal`/`-shm` companions. While the run executes it holds an OS lock on a
  `<run>.lock` file, removed when the run finishes. The directory and known
  SQLite family are private from first creation (`0700`/`0600` on Unix; protected
  current-user-only DACLs on Windows). Existing broader objects are refused,
  never repaired.
- **Retention:** the journal stores the instructions, each input, the
  model identity, each answer, judge sample payloads and responses, and the
  terminal or error records **in cleartext**. It never stores credentials. kmx keeps the newest 20 runs and
  deletes the oldest finished runs when a new run starts. A run that is still
  executing, in this or another process, is never deleted. Executing runs, and
  overlapping starts that each count the same runs, can leave more than 20
  runs until the next run starts or the journals are cleared.
- **Clearing:** `localpreview.Clear(dir, runID)` deletes one run, and
  `Clear(dir, "")` deletes all finished runs plus unlocked `<run>.lock` files
  whose journal is missing. A run that is still executing is kept and reported
  as a refusal. Removal rechecks each private file's physical identity and
  flushes the retained directory. A partial known journal family left by an
  interrupted clear remains discoverable and can be recovered explicitly;
  unrelated entries are never removed. A post-removal flush error reports
  uncertainty rather than claiming absence.

## Replay and inspection

`localpreview.Inspect(ctx, dir, runID)` reopens a retained journal and rebuilds
each Session. Without a `ctx` deadline it applies a 2-minute bound. Errors
are fixed-text refusals or context errors. Complete turns are replayed through
the `chat` harness against their recorded completions. Any live model call is
refused and counted. An inspection
is labeled "local preview journal replay; not a test result". It has no
verdicts, and incomplete, failed or canceled turns stay `incomplete`.
Inspection never sends to a model, never re-evaluates assertions and never
creates the main journal: it opens read-write only if that database already
exists. SQLite's known private companion files may be materialized before open.

## Retained Test failures

Completed non-pass `kmx test FILE` and canonical MCP Test outcomes also write a
separate bounded diagnostic projection under `<kmx state>/eval-failures`. This
reuses the owner-private checked failure store; it does not read verdicts from
the journal. The record binds the resolved file subject, exact source revision,
effective agent/judge provider and model, full/subset selection and assertion
identity/results. It retains no instructions, input, endpoint, credentials,
journal handle or arbitrary provider error. File judge scores/thresholds are
retained, but rationale is not retained in this first slice.
The checked file scope and matching source revision are frozen before any model
send, so a later path replacement cannot rebind retained content.

`kmx test explain --file FILE` reads this projection offline with zero model,
provider, credential, config or journal access. Records for edited source are
shown as `historical`; they are never attributed to current bytes. Content is
withheld unless `--show-content` is explicit. The last 10 records are retained
per checked physical parent plus normalized leaf across revisions. Explicit
clear is confined to that file subject and never deletes the Sessions journal,
source history, workspace diagnostics or passing proof. Run, passing Test,
cancellation, execution failure and journal-finalization failure produce no file
failure record. Retention failure leaves the original completed Test verdict
unchanged and reports `retention.status: unavailable`. If one invocation has more
than 10 non-pass examples, no partial set is written because every returned ID
must remain discoverable in that subject's bounded store.

## Executor seam

`localpreview.Executor` sits between the example loop and execution. The
default is the journaled embedded Sessions executor. Any other executor gets
the same `Turn` (exact model and route) and must not retry, run tools, fall back
to another model or report an incomplete turn as complete. Results report
`Journaled` so that a non-journaled runner cannot pass as journaled.
