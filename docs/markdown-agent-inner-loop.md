# Markdown agents: paired tests and the editing loop

Design update for [#604](https://github.com/kaimahi-agents/kaimahi/issues/604),
2026-10-21. **Implemented spike with remaining extensions noted here.** The
[usage guide](copilot-inference.md#markdown-agent-spike-unreleased) describes the
current commands. Native paired files, offline validation, remembered provider,
SDK-selected agents, mocked tools, metadata history and the reused chat TUI are
implemented, including a wide-terminal test pane and orphan-test diagnostics.
Persisted answer/Explain content and runtime-version revalidation of historical
badges are extensions; reloaded records are labeled historical. The design below
records the intended contracts; wording such as
“proposed” identifies the original design rationale, not a released API promise.

**Interface correction:** agents use shared resource operations, like Docker's
resource model. Markdown is type `md`, alongside Orka, not a command namespace.
Use `list --type md`, `inspect NAME --type md`, `create NAME --type md`,
`chat NAME --type md`, `run NAME --type md`, and `test NAME`. Bare root and
`console` use the shared inventory/UI. This supersedes references below to the
earlier dedicated browser and proposed command grouping.

**Lifecycle correction:** `status` and the default local console inventory show
only tracked instances, never files found by scanning. Run/Chat create path-keyed
instances with process-held leases and immutable SHA-256 definition snapshots.
Edited source selects a new cached version on the next run; a live owner must exit
before another process replaces it. `run FILE.md` starts a background worker and
prints Chat/Stop commands; `-i` selects foreground chat, and `--prompt` runs once
and records stopped. `info` shows setup; `list/console --definitions` discover source.

## Decision

Use a native Copilot agent definition and a separate Markdown test document.
Keep source history in Git, identify exact tested snapshots with hashes, and
reuse the existing chat timeline for a local edit → test → chat loop.
KMX owns tests and presentation; Copilot owns agent execution and tool calling.

```text
.github/
  agents/
    hello.agent.md
    weather.agent.md
  agent-tests/
    hello.agent.test.md
    weather.agent.test.md
```

Tests live outside Copilot's agent-discovery directory. The filename stem, not
the front-matter display name, determines pairing. Under an explicitly selected
project root, `.github/agents/<stem>.agent.md` pairs with exactly
`.github/agent-tests/<stem>.agent.test.md`. Start with flat directories; do not
guess between recursive matches. Duplicate display names are fine; rows show paths.
Show orphan test files as diagnostics rather than assigning them to another agent.

For files elsewhere, require an explicit test path with proposed
`test FILE --tests TESTFILE`; an unpaired agent remains runnable. Do not search
parent repositories for a matching stem. Discovery explicitly includes these
two hidden `.github` directories, while retaining exclusions for Git internals
and dependencies. The present generic scan skips hidden directories and must change.

## First-run Copilot preference

On first interactive local-agent execution, detect `copilot` on PATH and ask:

```text
Copilot CLI found.
Use it by default for local agents? [Y/n]
```

Enter means **Yes**. Record an explicit accepted/declined choice so neither
answer is asked again on each launch. After acceptance, show the selected route
and continue the requested agent operation. When a file was supplied, validate it
first. Bare `kmx` always displays help; `kmx console` explicitly opens the browser.
`kmx --help`, `help`, version, completion, offline list/show/validate and redirected
invocations stay inert: no onboarding prompt, provider process or preference write.
An unrelated cluster operation need not wait for local-agent setup.

Use `config.StateDir()` for a proposed `preferences.json`, keeping this separate
from cluster context and existing destination/workspace settings:

```json
{
  "schemaVersion": 1,
  "localAgents": {
    "copilotChoice": "accepted",
    "defaultProvider": "copilot"
  }
}
```

StateDir is `$KMX_HOME` when set; otherwise it is the OS user config directory
plus `kmx` (normally `~/.config/kmx` on Linux and
`~/Library/Application Support/kmx` on macOS). This is per-user, per-machine
configuration, not a tracked project `.kmx` directory. Native Windows remains
an independently tracked implementation requirement.

For No, write `copilotChoice: "declined"` and leave the default unset. Missing
CLI is a distinct discovery outcome, not an implicit decline: show an install
hint and leave onboarding eligible if it is installed later. EOF or cancellation
makes no choice and writes nothing. Acceptance does not mean authenticated: check
login when running the agent, and direct the user to `copilot login` if needed.
Do not perform inference merely to accept a default. If saving fails, say the
choice is session-only rather than claiming it will be remembered.

Store no token, username or absolute executable path; resolve the installed CLI
on each execution. Use bounded strict JSON, atomic writes and private local file
permissions, preserve other supported preferences, and serialize concurrent
updates. A corrupt preferences file produces a repair diagnostic without silently
overwriting it. Add a Settings action in the browser to change/reset the choice;
explicit provider selection on a single run does not overwrite it.

Selection precedence:

1. Explicit per-invocation provider/model override (when the CLI exposes it).
2. An explicit model in the native agent definition for model selection.
3. Saved default provider for the local run route; Copilot's default model when
   the definition/command supplies none.
4. Interactive setup if no provider was selected; a clear setup error in scripts.

The native Copilot file does not carry KMX provider configuration. Provider and
model selection are separate: selecting an explicit model must not silently switch
providers or fall back after failure. A saved accepted default works noninteractively
without prompts, subject to normal CLI authentication/access. A remembered decline
does not prevent an explicit Copilot override. Record the effective route/model in
each run key so a settings change invalidates current test status where relevant.

## Paired file examples

### `.github/agents/hello.agent.md`

```markdown
---
name: hello
description: Greets people by name
tools: []
---

When the user gives their name, reply with exactly Hello, NAME!
Replace NAME with their name, preserving spelling and capitalization.
```

Use Copilot's string-valued `model` when explicitly selecting a model; omit it
to inherit Copilot's default. KMX shows the selected/default route separately.
No KMX provider object or test metadata belongs in the native definition.

### `.github/agent-tests/hello.agent.test.md`

````markdown
---
schemaVersion: 1
---

# Hello tests

Greetings must preserve the supplied name.

## Greeting Sam

```yaml agent-test
id: greet-sam
input: My name is Sam
assertions:
  - id: exact-greeting
    type: regex
    pattern: '^Hello, Sam!$'
```

## Greeting Aria

```yaml agent-test
id: greet-aria
input: My name is Aria
assertions:
  - id: exact-greeting
    type: regex
    pattern: '^Hello, Aria!$'
```
````

Headings/prose document intent. Only exact `yaml agent-test` fences define cases;
all other fenced blocks are rejected in v1 to catch misspelled executable fences.
Case order is document order. Preserve existing assertion IDs, types and operands;
reuse `runtime.ParseEvaluationCase` for the ordinary case projection. A test-file
decoder owns the version and optional mock fields; native case parsing is unchanged.
Tests, assertions and expected answers never become agent instructions.

## Fast validation comes first

Every explicit agent-file entry point (show, chat, run, test, browser selection)
uses the same offline `LoadAgentPair` boundary before invoking Copilot, checking
login, creating a session or creating state. Use `validate FILE`
for an offline check that editors/CI can invoke. It emits file/line/column,
a fixed error code and an actionable repair; JSON diagnostics can be added using
the same typed errors. Never dump invalid front matter or credential-like values.

Validation has two levels: **format valid** and **supported by this local runner**.
An unsupported valid Copilot field must not be silently dropped or mislabeled
as invalid Copilot syntax. Version the supported subset and name the field in
the diagnostic. Initial scope: name, required description, optional string model,
explicit tools; tool-free Run requires `tools: []`. The later mocked-tool mode
accepts only the tool declarations it can bind to fixtures. Hooks, inherited
agents, arbitrary MCP startup and other executable declarations remain unsupported
until their inputs/execution semantics are deliberately integrated.

Checks, in order:

1. Literal `.agent.md` path and explicit pair resolution; confined bounded
   regular-file reads, no symlinks/FIFOs; UTF-8 and CRLF support. Agent <=64 KiB,
   tests <=128 KiB, at most 32 cases. Also enforce Copilot's documented 30,000
   character prompt ceiling on the instruction body.
2. Well-formed single front-matter document, matching delimiters, nonblank
   description/instructions, correct field types, duplicate/unknown-field and
   alias/tag handling. Name, when present, must be safe to display/select.
3. Test schema version, closed case/mock structures, stable unique case IDs,
   per-case unique assertion IDs, nonempty inputs/checks, valid regex syntax and
   supported assertion types. Source spans survive parsing into UI diagnostics.
4. Mock schemas and expected argument/result types, tool references, bounded
   call expectations and complete fixture coverage before any send.
5. Capture immutable source bytes and identities; only then perform bounded
   Copilot availability/model-capability checks. Catalog availability is an
   execution check, not something the offline validator claims to establish.

Missing companion: show **NO TESTS**, permit tool-free chat/run, and refuse Test
as an empty suite. Existing malformed companion: show its error and block the
paired workflow until repaired; never silently run a different/older suite.
An invalid agent is visible in the browser but cannot be sent to the model.

Run validation on browser load, before each operation, after editor return and
after debounced save detection. Watch only the selected pair; bounded background
discovery may validate other rows lazily. Target <100 ms for a normal pair on a
local disk, measured in a benchmark rather than promised for remote filesystems.
Do not add a model/network probe to this fast path.

## Revision identities and test status

Hash original file bytes, before YAML/Markdown parsing or newline normalization:

```text
agentDigest = sha256(agent file bytes)
testsDigest = sha256(test file bytes)  # absent is a distinct state
pairDigest  = sha256(JCS({schema: "kmx.agent-pair/v1",
                         agentDigest, testsDigest}))
runKey      = sha256(JCS({schema: "kmx.agent-test-run/v1", pairDigest,
                         runnerVersion, adapterVersion, copilotVersion,
                         modelSelection, executionMode, fixtureDigest,
                         contextDigest}))
```

Use the existing canonical JSON helper, not ambiguous string concatenation.
`fixtureDigest` covers mock contracts/results (initially embedded in the test
file, with a separate field for clarity). `contextDigest` identifies any other
admitted runtime instructions/settings; initial isolated mode admits none.
Do not hash secrets into public metadata. If effective context cannot be closed,
label the run contextual and do not present the file hash as its complete identity.

Whitespace-only, comment and CRLF edits count as source changes. Short prefixes
are display-only; comparison uses full digests. Git commit/dirty state is optional
navigation context, not the revision key. A reverted file can match an older
tested snapshot; show the timestamp and label it historical rather than a new run.
With `auto` or inherited defaults, label the model unpinned and retain any observed
model ID separately. A matching hash means the source is unchanged, not that
the hosted model will behave identically today.

Track three things separately:

- **Disk pair:** last validated file bytes currently saved by the editor.
- **Loaded chat snapshot:** instructions/model used by the active conversation.
- **Test attempt:** frozen pair, configuration, selected IDs, generation/run ID,
  per-case results and completion status.

Test freezes both files, validates, and uses that snapshot for every case.
Reread both before the first send and after completion; mismatch marks the result
stale against disk. Save events also mark it stale immediately while it runs.
Never reconstruct later cases from a newly edited file. Old asynchronous events
carry run ID and snapshot identity; they cannot overwrite a newer run's badge.
Read/hash both files again to detect changes during initial capture; reject an
unstable read rather than mixing a detected pair of revisions. This does not claim
an atomic transaction across two independently saved files.

Represent status along three independent axes: source validity/freshness,
attempt progress/verdict, and coverage/mode. Example presentation:

| Display | Condition |
|---|---|
| INVALID | Current definition or paired tests fail validation |
| NO TESTS / UNTESTED | Missing cases / no completed attempt for this key |
| RUNNING 1/2 | Attempt is active, with its frozen revision visible |
| PASSED 2/2 | Latest complete full attempt for the current key passed |
| FAILED 1/2 | Latest complete attempt includes a failed check |
| PARTIAL 1/1 | Only selected cases ran; never a full-suite green badge |
| CANCELLED / ERROR / UNKNOWN | Incomplete execution or inconclusive observation |
| CHANGED · last pass a13c… | Current source differs from the retained attempt |
| RUNNING OLD REVISION | Files changed while the captured attempt is running |

Always show `mocked tools` on mock-mode results. A previous full pass can appear
as history beside a later subset/failure, but must not replace that latest outcome.
Malformed/deleted files immediately remove the current-pass indication.

Start with in-memory status. For restart continuity, add a small separate private
store under `KMX_HOME/md-tests/<canonical-root-and-agent-path-hash>/`, with versioned
bounded records, atomic replacement and last-10 attempt retention. Store hashes,
selected IDs, verdicts, safe reasons, model route and times; no credentials or raw
answers by default. Missing/corrupt state means unavailable history, not a pass.
Keep it separate from native passing/deployment receipt stores. If retained answer
details/Explain are added, extend #588's diagnostics through explicit agent-file
scoping rather than inventing another raw-content store.

## Reuse the chat TUI

Code inspection favors the chat timeline over the cluster console:

| Existing seam | Reuse / required change |
|---|---|
| `chat_interactive.go`: `interactiveChatBackend` (`Agent`, `Connect`, `Send`) | Add a markdown backend over a loaded pair and the Copilot adapter; construct a local App with I/O only, no operational config |
| `chat_timeline.go`: textarea, viewport, history, worker/events, cancellation | Reuse editor/scrolling/resize behavior; add typed revision/test-progress events and a local header with source/test status |
| `chat_picker.go`: search, selection, item details | Reuse for path-based agent selection with model, validation and test badges |
| `chat_slash.go`: completion/help | Supply markdown commands; extend dispatch to parse arguments such as `/test greet-sam` |
| `chat_orka_controls.go`: `Configure` | Suitable for between-turn settings; current timeline exits/re-enters for controls, so background testing needs a first-class job, not a blocking Configure call |
| `agent_tui.go`: two cluster environments and inventory/actions | Keep its cluster lifecycle out of local file discovery; optionally add a later explicit link to the local browser |
| `markdown_agent_session.go`: direct printing and string prompt transcript | Extract structured Run/Test results/events shared by CLI and TUI; never parse rendered PASS/FAIL text to update badges |

Proposed screen:

```text
hello   .github/agents/hello.agent.md   Copilot / default (unpinned)
Definition a13c…   Tests 81bd…   CHANGED · last pass 2/2 on 901e…
──────────────────────────────────────────────────────────────────
CHAT                                         TESTS
you: My name is Sam                           greet-sam   FAILED
hello: Goodbye, Sam!                          expected: ^Hello, Sam!$
                                             actual: Goodbye, Sam!
──────────────────────────────────────────────────────────────────
Message or /command
/edit  /edit-tests  /test [id]  /reload  /diff  /reset
```

On narrow terminals, switch between Chat and Tests instead of squeezing columns.
Case detail opens expected/actual output deliberately; retain bounded sanitization.
Show an expected-text/rubric failure beside its source location and an Edit action.

### Editing workflow

1. Choose an agent; validate the pair and render any diagnostics before connecting.
2. `/edit` or `/edit-tests` opens the appropriate file in the configured editor.
   Use Bubble Tea's `tea.ExecProcess` terminal handoff for terminal editors, with
   executable/argv parsing and the filename as an argument, not `sh -c` string
   interpolation. Support a wait-enabled GUI command such as `code --wait`.
3. On return, reread and hash both files. Also use a low-frequency bounded hash
   check for the active pair (e.g. every 500 ms plus debounce) so editing in a
   separate window works. Hash bytes rather than trusting mtime; parent-directory
   notification can replace polling later to handle atomic editor saves.
4. Valid edits update the disk status immediately. Agent/model changes require
   `/reload` before the next chat send and start a fresh conversation; keep the
   previous transcript visibly labeled with its old revision. Test-only changes
   invalidate test status but need not reset chat.
5. `/test` validates and tests the latest saved pair, irrespective of the older
   chat snapshot. Show both identities if different. `/test ID` gives fast partial
   feedback; full Test is the final green check.
6. `/diff` compares disk with the last tested snapshot while those bytes are held
   in memory; after restart use Git or say the previous bytes are unavailable.
   Never present a fabricated diff from hashes alone.

Run one inference/test job at a time initially. Keep scrolling and file-change
events responsive while busy; cancellation marks incomplete cases, never passes.
Auto-test-on-save is optional and off by default: debounce, cancel queued obsolete
work and run only a clearly selected scope. Editing does not silently send model
requests. A later “suggest instruction edit” action may propose a diff based on a
chosen failure; applying it must recheck the base hash and show a separate test
file diff if expectations change. Start with the user's editor, not a new editor.

## Quick mocked-tool test

This fixture syntax is implemented in the current spike. It
tests a real model tool invocation against a deterministic replacement handler,
not a prompt saying “pretend the weather tool returned …”.

Agent `.github/agents/weather.agent.md`:

```markdown
---
name: weather
description: Answers weather questions using lookup_weather
tools: [lookup_weather]
---

For a weather question, call lookup_weather with the requested city.
Report the returned condition and temperature in Celsius; do not invent weather.
```

Companion `.github/agent-tests/weather.agent.test.md`:

````markdown
---
schemaVersion: 1
---

# Weather tests

## Wellington uses the tool result

```yaml agent-test
id: wellington-weather
input: What is the weather in Wellington?
mocks:
  tools:
    - name: lookup_weather
      description: Look up current weather for a city
      parameters:
        type: object
        properties:
          city: {type: string}
        required: [city]
        additionalProperties: false
      calls:
        - arguments: {city: Wellington}
          result: {condition: rainy, temperatureC: 12}
assertions:
  - id: consulted-weather
    type: toolCalled
    tool: lookup_weather
  - id: used-condition
    type: contains
    value: rainy
  - id: used-temperature
    type: contains
    value: '12'
```
````

`calls` is an ordered exact-call expectation for that tool, consumed once per
entry; the example therefore requires exactly one matching call. Arguments compare
as canonical JSON objects, not YAML spelling/key order. Missing/extra calls or
wrong arguments fail the fixture expectation independently of answer assertions.
Malformed/incomplete tool events are unknown/error. Each case gets fresh handlers
and call counts. Unmatched calls never fall through to a real implementation.
Return fixture results only when the runtime actually calls the mock; do not
preload expected arguments/results into the model prompt. Surface tool name,
matched expectation and mock mode in the test details. Model inference remains real.

The weather agent is native-format compatible, but direct Copilot execution needs
a real tool provider exposing `lookup_weather`; its `tools` entry only selects a
tool, it does not define one. The companion supplies a mock binding only for Test.
Use explicit mock-preview mode if chat should also use these fixtures; ordinary
chat must not silently reuse a test's mock.

### Adapter feasibility

The current `copilotPromptModel` subprocess path rejects native tool requests and
does not offer callback registration. It cannot implement this fixture by itself.
The official Copilot SDK documents session tools with handlers, custom-agent
selection and lifecycle events. Prefer a narrowly pinned SDK adapter that starts
the installed CLI and registers in-process mock handlers. Do not use a post-tool
hook to replace a result: the real tool would already have executed.

For both ordinary chat and Test, freeze the same supported native definition and
select it explicitly through the SDK's custom-agent/session selection seam, or
qualify staged-file `--agent` discovery. Do not retain the current prompt-only
approximation while claiming native-agent parity. Admit no ambient repository
instructions in isolated mode; disclose this when comparing with direct CLI runs.
Record additional admitted context if repository-context mode is later offered.

Before claiming support, qualify one pinned CLI/SDK combination: explicit selected
agent active on the first turn, exact instructions/model/tool schema supplied,
exactly one real tool callback receiving Wellington, fixture return seen in the
answer, no real tool/MCP invocation, and terminal/cancel/error attribution. Current
SDK docs demonstrate the API direction, not compatibility with installed CLI 1.0.98.
The implementation pins SDK v1.0.24. A live run with CLI 1.0.98 selected the weather
agent, invoked its mock with Wellington, and passed all three assertions.

## Proposed: infer and review scoped local tools

The implemented import default is now a general, tool-free inference pass returning
zero to three scoped CLI binding proposals. It asks no GitHub repository question
up front; unknown scope blocks the affected proposal pending author clarification.
Per-tool allow/deny adds declarations, not execution grants. Standalone tools suggest
uses the same flow; explicit --repo retains the fixed GitHub template proposal mode.
This supersedes the catalog-only default described in the original design below.

Same-file explicit CLI bindings and compatibility probes are now implemented;
see [the concrete format and activation flow](copilot-inference.md#same-file-cli-tools-and-compatibility-checks).
These are user-authored reviewed commands; inference suggestions remain limited
to the maintained GitHub read templates and cannot invent executable bindings.

**First slice implemented.** Suggest/review supports the two fixed GitHub PR read
templates, private definition-bound overlays, manager/session binding identity and
mock/live separation. The [usage guide](copilot-inference.md#suggest-and-approve-pr-monitor-tools)
records the concrete CLI and limits. The broader template catalog, project-owned
binding files, direct TUI acceptance and generated mock cases remain future work.
Unlike the original argv sketch below, live reads use fixed HTTPS GET routes with
redirects disabled; gh supplies authentication through memory-only token capture.
Acceptance leaves source unchanged and activates an explicitly reviewed overlay.

### User journey

Suppose an author writes a PR-monitor definition:

```markdown
---
name: pr-monitor
description: Summarizes open PRs and failing checks in kaimahi-agents/kaimahi
tools: []
---

Review the open pull requests in kaimahi-agents/kaimahi.
Summarize their review state and failing checks. Do not change any PRs.
```

Proposed commands and TUI action:

```sh
kmx tools suggest .github/agents/pr-monitor.agent.md
kmx tools review PROPOSAL_ID
```

The editor exposes **Suggest tools** / `/suggest-tools`. This is an explicit model
call, not part of ordinary validation, completion or every Run. After import or a
relevant instruction edit, offer the action without starting it automatically.
Use the selected inference route; disclose that the definition and a small tool
catalog are sent to it. No provider fallback on failure.

Example review:

```text
Suggested tools for pr-monitor
Reason: the instructions require live pull-request and check data.

github_pr_list   List open PRs
github_pr_read   Read PR metadata, review state and check conclusions

Host        github.com
Repository  kaimahi-agents/kaimahi
Effects     read-only
Arguments   PR number and bounded pagination only
Execution   gh, using its existing authentication
Excluded    comments, edits, merge, workflow dispatch, arbitrary API/shell

[Accept selected] [Adjust scope] [Reject]
```

The example does not need arbitrary repository-file access, check logs, or issue
content. Add such capabilities only if the author actually needs them. If the
repository is absent/ambiguous, ask the author to select it. An optional read-only
local Git-remote hint can be displayed, but inference must not silently make that
repository the approved scope. Multiple requested repositories require an explicit
list, never automatic wildcard access.

### Separate suggestion from execution authority

1. **Snapshot and validate offline.** Capture the agent definition hash and existing
   binding hash before any inference. Invalid input stops here.
2. **Build a bounded capability catalog.** Start with maintained tool templates
   (`github.pr.list`, `github.pr.read`), including argument schemas, effects and
   required scope. Optional PATH detection says only whether `gh` is installed;
   do not enumerate home files, tokens, repositories or account data.
3. **Ask inference for structured proposals.** Its result selects catalog template
   IDs, suggested literal scopes, source-section rationale, and unresolved needs.
   It cannot supply executable paths, shell snippets, arbitrary flags, auth values,
   permission decisions or auto-install instructions.
4. **Validate the proposal deterministically.** Reject unknown templates, excessive
   scopes, malformed arguments and incompatible effects. Model confidence and
   prose are explanations, not permission checks. Agent text is analyzed as data;
   instructions claiming approval do not establish approval.
5. **Review one concrete diff.** Display operations, host/repository scope, data
   exposed, executable choice, bounds and credential source. Let the user select,
   narrow or reject proposals. Wider scope requires another explicit review.
6. **Apply only on acceptance.** Recheck the definition and binding base hashes.
   Persist accepted non-secret bindings and propose/update the agent's tool-name
   list as a visible source change. No silent edits or grants during Suggest.
7. **Test and activate explicitly.** Generate proposed companion mock cases, review
   them, run tests, then update the agent. Existing sessions keep their old binding
   snapshot until the explicit version/update transition.

Suggestion runs use no agent tools. The only external request is inference through
the selected model route. An unknown capability is reported as a gap, not filled
with a generic shell tool. Installing a binary or signing in is a separate action.
Automation may emit a proposal with `-o json|yaml`, but cannot silently accept it.

### Repository-scoped GitHub implementation

The reviewed proposal compiles into fixed operation templates. For example,
`github_pr_read` accepts only `{number: positive integer}`. The model never supplies
`repo`, host, URL, method, query, executable or extra arguments at invocation time.

The executor builds argv directly using a verified `gh` executable, the approved
literal host/repository and a fixed JSON field projection. It never invokes a
shell or forwards model-authored `gh api` expressions. A template might implement
list/read using `gh pr list --repo OWNER/REPO --json ...` and
`gh pr view NUMBER --repo OWNER/REPO --json ...`; pin/qualify the supported CLI
behavior before claiming those projections provide every required check field.
Check-status retrieval can use an additional fixed read-only template if needed.

Repository isolation is enforced by construction: all operation targets are formed
from the reviewed binding; PR numbers are validated as numbers; returned links do
not authorize subsequent requests. Avoid shell aliases and repository hooks, use
an isolated working directory, and normalize routing-related environment variables
such as GH_HOST/GH_REPO so ambient settings cannot redirect the request. Bound
pagination, duration, output and response size; request only the fields used by the
agent. Return structured data and never credential-bearing command diagnostics.

This is **application-enforced tool scope**, not a reduction in the underlying
`gh` token's permissions. The local CLI may authenticate as a user with access to
many repositories. To require credential-level repository isolation, bind a GitHub
App installation or fine-grained credential limited to the approved repository
and required read permissions. Keep credentials in an approved credential facility,
not Markdown, binding files, proposals, argv or test reports. The review explicitly
distinguishes application enforcement from credential-enforced restrictions.

Read-only here means no requested remote mutations. It does not mean offline:
repository data reaches the agent and selected inference provider. Do not expose
other generic shell, HTTP, file-read or Copilot built-in tools that could bypass
the reviewed binding. Shared-manager sessions receive only their own approved
handlers; no process-wide union of all agents' tools. Capability execution itself
belongs in a small deterministic adapter, not in the LLM's policy interpretation.

### Binding and identity sketch

Illustrative accepted local binding (proposed schema, not yet loadable):

```yaml
schemaVersion: 1
agent: pr-monitor
tools:
  - name: github_pr_list
    template: github.pr.list/v1
    scope:
      host: github.com
      repository: kaimahi-agents/kaimahi
    approval: granted-scope
    limits: {timeoutSeconds: 15, maxItems: 50, maxOutputBytes: 65536}
  - name: github_pr_read
    template: github.pr.read/v1
    scope:
      host: github.com
      repository: kaimahi-agents/kaimahi
    approval: granted-scope
    limits: {timeoutSeconds: 15, maxOutputBytes: 65536}
```

`granted-scope` means the author approved these exact read-only operations for
unattended execution; it is not a universal approval. The review records that
choice. Other local tools can require allow-once/session approval, surfaced in the
chat UI; a background session without an approval client refuses those calls.
Selecting only read operations must not unexpectedly enable write operations
because the binary or token also supports them.

Keep proposals under private KMX state and persist accepted binding configuration
separately from the definition; choose the user/project storage location explicitly
in review. Machine-specific executable paths remain local. A shared project binding
is a declaration to review on another machine, not a transferable permission grant.
Bind the approval to canonical scope/template/limits, template implementation version
and selected executable identity. Binary/template changes require revalidation;
expanded capabilities require review.

The proposed deployment identity becomes definition digest + binding digest +
adapter identity. Test keys also include mocked/live mode and fixture identity.
Permission-affecting binding edits invalidate old test status and require an explicit
agent update even if the Markdown bytes are unchanged. Suggestion model identity,
source hash and rationale belong in the proposal audit, not the deployed instructions.
Applying a suggestion never claims that the agent passed or is deployable.
The current AgentSuite mapper continues refusing tools until genuine native tool
bindings are implemented; accepting a local gh binding must not bypass that gate.

### Tests and useful first slice

Start with one read-only PR-monitor vertical slice:

1. Implement reviewed fixed GitHub list/read templates and schema/argv validation.
2. Register per-session SDK handlers and surface tool/result events in chat.
3. Add schema-constrained Suggest and review, using those templates only.
4. Add definition/binding digest checks, explicit activation and status tool summary.
5. Demonstrate one real repository-scoped read separately from mocked tests.

Required checks: suggestion performs no tools or file edits; rejected/stale proposals
make no grants; omitted/wildcard/foreign repositories refuse; auth/host environment
cannot override the binding; model strings cannot inject argv/options; pagination
and outputs stay bounded; no automatic retry after ambiguous execution; two agents
cannot see each other's handlers. Verify the actual requests against a hermetic
endpoint, not just the intended argv. A mock-only pass remains clearly labeled.

Example companion test intent: PR 42 has one failing check; the mock supplies only
that repository's PR/check response. Require the agent to call `github_pr_read`
with `{number: 42}` and summarize the failing check. Separately test the binding
executor rejects a forged `repository: other/repo` field before invoking gh.
Repository content asking the agent to merge, post a comment or fetch another repo
must never gain an undeclared operation. The author can later request a narrowly
scoped write tool through another reviewed proposal.

## Delivery slices and checks

1. **Pair + validation:** native supported agent subset, test Markdown parser,
   conventional/explicit pairing, source locations, offline validate and samples.
   Cover CRLF/spaces, malformed fields/fences, missing/duplicate IDs, regex errors,
   invalid mocks, missing/orphan tests and special files. Assert no Copilot probe,
   request or state write occurs on invalid input.
   Add first-run `[Y/n]` onboarding and the local provider preference: cover Enter,
   explicit No, cancellation, missing CLI, later installation, persistence across
   invocations, explicit overrides, corrupt/write-failing state and prompt-free
   help/completion/scripts. Saved choice must not load cluster configuration.
2. **Snapshot + status:** immutable pair/config keys, structured result events,
   latest-attempt/coverage semantics and in-memory history. Cover same-size edits,
   preserved mtime, test-only edits, invalid intermediate saves, deletion, revert,
   edits during runs and late results from superseded generations.
3. **Chat TUI reuse:** local backend, picker, source/test header, case details,
   editor handoff, reload/diff and responsive progress. PTY tests cover narrow
   resizing, Ctrl-C, external editor return and no cluster/config calls.
4. **Copilot native selection + mocks:** pin/qualify the official adapter path,
   schema binding and fixture dispatch. Test wrong/missing/extra calls,
   isolation across cases, unavailable model and cancellation. Add a live mock
   call demonstration separately from hermetic protocol tests.
5. **Persistence and convenience:** bounded metadata history and opt-in auto-test;
   qualify invalidation across restart and selected/full/mock-mode distinctions.

Migration replaces `hello.agent.md` with the native `.github/agents/hello.agent.md`
and moves its examples to the paired test file. Explain the old nested model and
inline-example syntax change with a repair hint. Do not silently treat old tests
as native prompt text or discard them. Update CLI help, completion, samples,
tests and current usage docs together when slice 1 lands.

## References reviewed

- [Copilot custom-agent format](https://docs.github.com/en/copilot/reference/custom-agents-configuration):
  description, model string, tools and Markdown instruction body.
- [Copilot SDK custom agents](https://github.com/github/copilot-sdk/blob/main/docs/features/custom-agents.md):
  session agent selection, custom tools/handlers and runtime events.
- [Copilot SDK hooks](https://github.com/github/copilot-sdk/blob/main/docs/features/hooks.md):
  lifecycle and pre/post tool behavior; post-tool substitution is too late for mocks.
- Local code inspection: `internal/kmx/app/chat_interactive.go`,
  `chat_timeline.go`, `chat_picker.go`, `chat_slash.go`, `chat_orka_controls.go`,
  `agent_tui.go`, `markdown_agent.go`, `markdown_agent_session.go`, and
  `copilot_prompt.go`. No existing external-editor launch integration was found.
