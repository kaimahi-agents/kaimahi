# Single-file agent files

`internal/kmx/agentfile` parses Copilot custom agents, minimal Markdown prompts and existing kmx charters offline, with optional model, host-tool and test declarations.
This is a **format and parser**, not an execution command. Existing native suite
loaders, `create`, `run --agent`, and workspace `test` are unchanged. There is no
model call, workspace discovery, source write or implicit conversion.

## Copilot custom agents and minimal prompts

Open an existing `.agent.md` with YAML frontmatter and a free-form Markdown prompt.
No title or charter headings are required. For example:

```markdown
---
name: Reviewer
description: Review changes without editing files
model: gpt-5.6-terra
tools: []
---
Review the supplied changes for correctness and missing tests.
Report actionable findings with file references. Do not edit files.
```

This follows [GitHub's custom-agent format](https://docs.github.com/en/copilot/reference/custom-agents-configuration)
and [VS Code's custom-agent format](https://code.visualstudio.com/docs/agents/reference/custom-agent-file-reference).
A plain prompt without frontmatter is also accepted. Existing complete kmx charters
keep their original instruction projection and source revisions.

Exact model IDs and exact supported tool names use the existing preview path.
Copilot tool sets, wildcards, namespaced tool references, model preference lists
and other unmapped settings are shown as preserved frontmatter in the canvas;
kmx does not execute imported MCP server configuration or invent tool aliases.
The canvas shows the model and tools actually selected for the preview.
A frontmatter declaration with more than 32 mapped tool names stays wholly
unmapped rather than silently activating only its first 32 names; its original
header remains visible and unchanged.
Unknown fields, comments and formatting are written back untouched when editing
the prompt or tests. Runtime model selection is not written into the file.

When a Copilot file omits `tools`, it opens and runs tool-free. The canvas and CLI say:

> Copilot gives this agent all tools; these tests run without tools. Add tools to choose which.

Use **Add tools** in the canvas to choose explicit tool names. `tools: []` is
already explicitly tool-free and does not show that notice.

A final `## Examples` with a fenced YAML `examples` mapping adds kmx tests.
It is optional; the original prompt stays unchanged until you edit it. A prose
Examples heading is still prompt text, not a test declaration.

**Copilot includes everything below frontmatter in its prompt, including inline tests.**
Before the canvas or MCP adds or changes tests in a Copilot file, it says:

> Tests added here become part of this agent's prompt in Copilot.

Choose **Go ahead** or **Cancel**. Cancel leaves the file unchanged. MCP Create/Edit
uses `acknowledgeCopilotTests: true` only after the user agrees to that warning;
MCP Run/Test also requires this acknowledgment before capturing pending tool
recordings into a Copilot file's tests (replay-only runs do not write);
without acknowledgment it reports `confirmation_required` and does not write.
Prompt-only edits retaining identical tests do not ask again. Existing MCP/canvas
inline authoring keeps this rule; paired CLI recording writes only the tests file.

<a id="tests-in-a-separate-file-reader-only"></a>

## Tests in a separate file (CLI)

`kmx run FILE` and `kmx test FILE` read the selected tests file with the shared
reader. Run returns answers without checks; Test evaluates the checks. Add
`--tests FILE` to select an override, or use `run FILE "input"` for only that
input. No source migration is automatic. A missing or empty test source is not
a passing Test.

MCP has separate read-only source/history resources and paired offline export.
The existing Run/Test tools also accept version 3 with a checked `sourceVersion`
from the source resource: Run answers without checks; Test evaluates selected
checks. Version-3 Create/Edit write the selected pair with per-source
acknowledgements, source-only history and current-binding restore. Pending tool
capture saves only after journal close; external capture leaves agent bytes exact.
Partial/uncertain saves require refresh, never automatic retry or Test. The canvas
keeps separate prompt/tests drafts, saves them before Enter/Test, and shows checked
source history without treating it as saved verdicts. New chat Create writes both
sources; existing inline tests stay in place. Frozen recordings replay without
another capture. Explicit suite selection is also available to the canvas opener.
The legacy version-1 agent-view resource, Run/Test and Edit refuse an agent-only view when its
default tests file exists; they do not ignore it, report an old agent-only pass
as current, or fall back to inline writes. Native Squad and single-source files
remain supported.

The tests file is `NAME.agent.test.md` beside `NAME.agent.md`. An agent directly
inside `.github/agents/` instead pairs with `.github/agent-tests/NAME.agent.test.md`.
The resolver does not search for a Git root or other candidates. A caller may
select one explicit tests file instead; it replaces, rather than combines with,
the default selection. Native Squad `charter.md` files have no default tests file.

The tests source starts with version frontmatter, followed by Markdown prose and
one `yaml agent-test` fence per test, opening at column 1 (not inside a list or
blockquote). For example:

````markdown
---
schemaVersion: 1
---

# greeter tests

```yaml agent-test
id: greeting
input: Ada
assertions:
  - id: greeting-text
    type: contains
    value: Ada
```
````

Only `schemaVersion: 1` is supported in this frontmatter. Each fence contains one
mapping using the existing `id`, inline `input`, `assertions` and optional
`mocks.tools` fields. The [assertion semantics](#examples-and-assertions),
[explicit tool modes](#per-test-recorded-tools), limits and credential checks are
unchanged. Tool checks and mock names must match the agent's declared tools.
IDs are unique across the tests file, and node/case limits apply across fences,
not separately to each fence. A valid header without test fences means zero tests,
distinct from a missing tests file; neither is a passing test result.
Unknown/duplicate fields, YAML aliases/anchors, reserved fence-label lookalikes,
unclosed fences and HTML comments outside code fences refuse. Other fenced prose
is not a test. This is a source-format scanner, not a CommonMark/HTML renderer;
HTML display and collapsing do not define which cases are selected. Original bytes
and source locations are retained, not rewritten.

`LoadPair` applies the existing CLI-style checked read boundary to both sources.
A missing default file or parent records absence; an invalid existing path is an
error. A missing explicit selection is an error, never a fallback. A structured
inline Examples section (even `examples: []`) and a default tests file
conflict; an explicit tests-file selection deliberately overrides inline tests
without changing the original agent bytes.

The combined revision binds both selected file paths, source state and exact
byte revisions; the agent's own revision remains its raw agent-byte hash.
Changing only the tests, their presence or their path changes the combined
revision. Default and explicit selection of the same path have the same combined
revision; another path with identical bytes does not.

`SameSourceVersion` also checks whether either file or its parent directory
changed, even when the combined revision is unchanged. Directory timestamp
changes can conservatively invalidate this check without changing either file.
Pure `ComposePair` values cannot claim a checked file version. These cooperative
checks are not an atomic two-file transaction, a hostile-filesystem sandbox or
write permission. The reader keeps separate tests out of the agent's prompt.

CLI JSON keeps `file.revision` as the raw agent hash and adds `snapshotRevision`
and selected `tests` metadata. Capturing a pending Recorded tool patches only its
actual source after journal close: separate tests for paired storage, the agent
for legacy inline storage. The result remains bound to the executed snapshot;
`recordingTarget`, raw `savedRevision` and `savedSnapshotRevision` acknowledge the
save separately. Cancellation, journal failure or source change saves nothing;
a refused tests save never falls back to the agent. Comments, prose, line endings
and unrelated numeric lexemes remain unchanged. CLI adds no source history.

`kmx test explain --file FILE [--tests FILE]` compares retained failures with the
current selection. Tests-only edits and a different identical-content override
make the old result historical without changing the raw agent revision. After a
downgrade, older binaries may report the retained store unavailable on encountering
new pair metadata; use this version or newer to read it. No records are migrated
or removed automatically. Put journal/summary files outside the checked source parent when possible: creating
entries there can conservatively stale source-version checks.

## Test a folder

Bare `kmx test` selects direct `*.agent.md` entries in the current folder and
`.github/agents/`, sorted by path, without recursion or Git-root discovery.
Each agent uses the same companion rule above. Tests files and agents in other
subfolders are not selected. Missing optional folders are fine; unreadable or
invalid folders are reported rather than silently treated as empty.
Matching unsafe files are refused by the existing checked reader.

```sh
kmx test
kmx test -o json --no-content --github-summary
kmx test greeter.agent.md --tests selected-tests.md --case greeting
```

`--tests` and `--case` require an explicit agent FILE, even if the folder contains
exactly one agent. Route/model, tool-server selection, `--no-content` and verbose
options apply to each selected file. Native workspace evaluation stays at
`kmx package test`; bare Test never falls back to it. Native Windows still
requires an explicit file through its guarded fake/tool-free path.

Each file gets its own checked pair, journal and five-minute execution budget.
This is not one atomic folder snapshot. Assertion failures, completed inconclusive
checks and invalid/missing/empty tests do not prevent the next file from being
checked. Execution, route/auth, journal, source and other operational failures
abort later execution; remaining selected files are explicitly `not_run`, not
passing. This also includes invalid tool settings, unavailable tools/models,
recording mismatches or missing recordings, and runner content refusals
(`unsafe_tool_content` or `credential_content`). Load-time parser refusals,
including credential-shaped source rejected as `invalid_file`, remain
file-attributable and may continue to later agents. An inconclusive judge is `unknown`, including an unavailable judge; no
automatic retries occur.

Text has per-file details and one final summary. Folder JSON is one
`schemaVersion: 1`, `kind: kmx.agent-folder-test` document. Its ordered `files`
rows include an ordinal, safe display directory/name, status and the existing
safe per-file `result` when available. Root and repository agents with the same
basename stay distinct for completed results. Unrun rows use `[agent file]`
rather than an unchecked filename, and have no fabricated result or revisions.
Non-complete results also withhold discovered agent/tests/recording path names:
intake refusals may precede resolved-credential checks. Their known revisions
and selection keys remain, not a claim that the test executed.
Final `summary` counts are `total`, `passed`, `failed`, `unknown`, `couldNotRun`
and `notRun`, followed by `exitCode` and `reasonCode`.

Exit 2 takes precedence over assertion-failure exit 1 if any file is unknown,
could not run or was not run; an empty/invalid discovery also exits 2.
Otherwise all pass is 0; interruption is 130. Output/diagnostic/summary I/O errors
are exceptional exit 1 and stop later sends. Output streams one file at a time:
a failed write can leave a partial document without its final counts.
Do not interpret partial output as a completed folder result.

The optional GitHub summary opens once before execution, including for an empty
folder or discovery error. It contains ordinal-only per-file sections and final
counts, never source paths or authored content. The whole summary is bounded to
1 MiB; sections that do not fit are omitted with an exact omitted-section count
and a pointer to the JSON report. Final counts still cover all selected files.
The summary sink must finish successfully before JSON's final counts are written;
neither output is an atomic two-sink publication.

## Existing kmx charters

The [complete greeter sample](../internal/kmx/agentfile/testdata/greeter.agent.md)
remains supported unchanged:

````markdown
# Greeter — Greeting specialist

## Identity
- **ID:** greeter
- **Purpose:** Return a short greeting for the supplied name.

## Accountable Responsibilities
Reply with exactly `Hello, ` followed by the input name.

## Non-Responsibilities
Do not call tools, read files or contact other agents.

## Collaboration and Review Authority
Return the greeting to the caller. No delegated work.

## Model
- **Preferred:** fake

## Examples
```yaml
examples:
  - id: greeting
    input: Ada
    assertions:
      - id: greeting-text
        type: contains
        value: Hello, Ada
```
````

## Markdown contract

- Start with one nonblank H1 name (`# Name`). The ` — Role` suffix is optional;
  `# Name`, `# Name —` and `# Name — Role` are accepted display titles, not identity.
  Put all prose and fenced blocks after the first canonical H2 section.
- Charter sections are optional for minimal files. In a complete charter, keep
  the exact unindented spellings shown above. A structured Examples section must
  be last; the other charter sections can be reordered.
  An optional `## Tools` section may appear directly before Examples, containing
  1–32 unique names as `- tool_name` bullets and otherwise only blank lines.
  Names are 1–64 ASCII letters, digits, `_` or `-`, starting with a letter or `_`.
  Dotted names such as `server.read` refuse; names must fit the Chat/Responses
  function-name contract.
  The host supplies each tool's schema and description. No endpoints,
  schemas, credentials or other names are accepted. Unknown or duplicate H2 sections
  refuse in the complete charter profile. Frontmatter files instead treat their
  Markdown body as a free-form prompt, not charter metadata. The complete charter
  scanner refuses `<!--` outside fences to avoid ambiguous heading boundaries;
  free-form prompts preserve HTML comments.
- Identity contains only the two shown bullet fields. ID is 1–63 lowercase
  ASCII letters, digits or hyphens, starting with a letter. Purpose is nonblank
  single-line text. When present, Model contains only a nonblank Preferred bullet. Empty lines are
  allowed; duplicate fields and extra metadata refuse.
- Each responsibility section contains nonblank Markdown. Fenced code is prose,
  not executable configuration. Headings inside backtick or tilde fences do not
  create sections. Fences use at least three matching markers, with zero to
  three leading spaces; the closing fence has no info string and at least as
  many markers as the opener. Unclosed fences refuse.
- Examples contains exactly one `yaml` fence and otherwise only blank lines.
  That fence contains exactly one YAML document, as below. Additional text,
  fences or sections refuse rather than entering instructions.
- Files are UTF-8, LF or CRLF; remove a leading byte-order mark. Controls other
  than newline/tab and Unicode line/paragraph separators refuse, including in
  decoded YAML strings. Instructions
  preserve the complete prefix before Tools (or Examples when Tools is absent),
  including identity/model metadata, with CRLF normalized to LF. Tools and Examples
  are never part of those instructions. Tool-free instruction bytes are unchanged.
  The revision is `sha256:<hex>` of the **original bytes**, including line endings.

Canonical MCP edits retain bounded, private cleartext copies of the saved source
being replaced. Retention is best-effort: a valid save still succeeds when
history is unavailable, with an explicit warning in its result. Restore reads
old source and uses the same revision-checked Edit; it never rewrites history or
restores test evidence. See [saved source versions and clear](kmx.md#saved-source-versions-and-restore)
for the resource API, bounds, privacy and operator cleanup command.

An omitted Model stays absent in the parsed source and revision. Preview execution
uses the canvas/tool runtime override, then authored Preferred, then `gpt-5.6-terra`;
no default or picker choice is written into the file. A present blank Model is invalid.

Preferred is one exact printable ASCII model identifier (1–256 bytes; letters,
digits, `.`, `_`, `:`, `/`, `-`, starting with a letter or digit). `auto`, URLs,
spaces and wildcards refuse. `fake` names only kmx's deterministic orientation
route, not a general-purpose model or a claim that Squad runs that model.
The trusted provider route, endpoint and credentials must come from outside
this file; the parser neither checks availability nor chooses/falls back to a
model. Prose cannot grant tools or hooks. Tool names declare a requirement,
not authority: execution needs an explicit host binding and a qualified provider.
The CLI supports one [host-bound read-only GitHub MCP operation](local-preview.md#host-tool-foundation)
with an explicit local binding and environment credential reference. Missing
bindings leave a declared tool available as a stub answering "not available in this test".
Malformed supplied bindings, missing credentials for an actual live binding, unsupported routes and invalid provider results still refuse.
Bound tools also work on Copilot Chat Completions and Responses routes; judges
remain tool-free. Trusted embedding can bind multiple declared names to copied
host schemas/executors. The Copilot extension runtime can bind those declarations
to its currently offered MCP tools, using their input schemas without a binding
file. Raw-name collisions and missing/deferred schemas refuse; no default server
is selected. See [host tools](local-preview.md#copilot-host-tools).
Parsing is not a sandbox or
a guarantee that arbitrary prose is safe or secret-free. The complete charter,
including raw YAML comments, is checked with the existing credential-shape scanner
and refuses known shapes with a fixed redacted error. Decoded example inputs and
assertion operands are checked too, so YAML escapes cannot hide known shapes;
arbitrary secrets can still evade shape detection.

## Examples and assertions

The outer mapping has only `examples`. An explicit empty sequence, `examples: []`,
is valid while authoring; a missing sequence, YAML null or a mapping still refuses.
Create, Edit and Load preserve the same canonical source/revision at zero tests.
Ad-hoc Run with an input remains available. Test refuses with
“No tests yet — add or suggest one”; an empty test set never passes or supplies
shipping evidence. Each nonempty example requires `id`, `input` and
`assertions`, with optional per-test `mocks.tools` data described below. Order is preserved. Example IDs are unique across the file,
1–63 ASCII letters, digits, `.`, `_` or `-`, starting with a letter or digit.
Input is a nonblank string, validated with the existing runtime case validator
(including known credential-shape refusal). Assertions are nonempty and reuse existing
[runtime assertion semantics](../internal/kmx/runtime/evaluation_assertions.go).

Supported types and operands:

| Type | Required operand fields |
|---|---|
| `contains`, `notContains` | `value`: nonblank single-line string |
| `regex` | `pattern`: nonblank valid Go RE2, at most 1024 bytes |
| `toolCalled`, `toolNotCalled` | `tool`: a name declared in `## Tools` |
| `llmJudge` | `rubric`: nonblank, at most 4096 bytes; `minScore`: integer 0–100; `judgeModel`, `judgeVersion`: nonblank safe printable ASCII identity strings, at most 256 bytes each |

Every assertion also requires `id` and `type`. Assertion IDs use the example-ID
syntax, are unique **within each example**, and cannot use reserved
`expectContains.*` IDs. Other-type operands refuse even when null or empty.
`llmJudge` is a definition only; this parser does not evaluate or route judges.
The existing runner/judge owns judging, not another evaluator in the parser.
CLI/MCP [local previews](local-preview.md#judging) journal each of the two judge
samples as a separate Session, including explicit Copilot routes. Judge payloads
and responses are retained in cleartext under the same retention/clearing policy
as agent turns; never author secrets in a rubric.

No legacy `expectContains`, repeat policy,
executable declarations, unknown/duplicate fields, anchors, aliases, merge keys
or custom YAML tags are accepted. String fields must be YAML strings: quote a
numeric-looking input such as `"123"`; null entries cannot disappear silently.

## Per-test recorded tools

File Run/Test accepts hand-authored recordings and saves a successful first live capture for a pending Recorded tool.
Each `mocks.tools` entry names a declared tool and an explicit `mode: live` or `mode: recorded`:

```yaml
mocks:
  tools:
    - name: ping
      mode: recorded
      description: Read a greeting.
      parameters: {type: object, additionalProperties: false}
      calls:
        - arguments: {}
          result: {greeting: "Hello"}
```

A populated recording requires a nonblank description, an offline JSON parameter schema and exactly one saved call with
object arguments/result. A requested pending capture with a blank live description refuses before executing that tool;
uncalled tools stay pending, and ordinary Live calls do not require a recording description. The result is model-visible
JSON output, not an MCP envelope. Live entries have only name/mode.
Recorded entries with `calls: []` use a live binding if present, otherwise an unavailable stub. A successful live
call fills that entry after the invocation's journal closes successfully, even if an ordinary answer check fails.
Uncalled or unavailable entries stay pending. Ordinary Live and ad-hoc calls are never saved. Omitted settings keep
live/unavailable behavior. Saved data never grants live execution authority.
A recording runs without a tool binding or tool sign-in, but the model still needs its normal access.

Schema-valid calls match canonical JSON arguments, independent of key order or model call ID. Different arguments stop the run
with a recording-mismatch message and no live fallback. Schema-invalid model requests are refused before dispatch,
like invalid live requests. The same saved response can answer repeated identical
requests within the existing five-call first-response batch. Saved-but-unused data is not an implicit Calls check.
Both call checks count validated Enabled requests, including recorded and unavailable requests. Disabled answers
consume a request slot but do not count as calls and never become recordings; incomplete execution cannot pass a Doesn't call check. Results report configured mode, selected source and actual count; zero means
**Not called**, not proof that a live tool worked. A first capture says **recorded just now**, with source `live`,
not replayed. A recorded pass does not qualify today's server.

A pending tool executes live once per test: further identical requests in that batch use its new in-memory snapshot;
different arguments stop with a mismatch and nothing is saved. Tests never share these snapshots. Capture requires
both a successful trusted live observation and the matching complete, verified journal; replay and unavailable
answers cannot become new recordings. Cancellation, execution errors or failed journal finalization save nothing.

Saving uses one revision- and physical-file/parent-checked edit for the invocation. Source comments, line endings and
unrelated tests are preserved; strings are JSON-quoted and the whole candidate is parsed again. A stale file, a
competing save, an unsupported layout or data that cannot fit the existing bounds fails visibly without retrying
the tool or claiming a full pass. Existing recordings are not replaced automatically. CLI saving is bound to the
one file explicitly selected; MCP retains its normal path restrictions. Snapshot embedding must provide a trusted
checked-save capability for pending entries, never write using a display label alone.

The result's `file.revision` remains the revision that executed. A successful capture also returns `savedRevision`
and marks only captured tools `recordingSaved: true`. Those answers are historical against the new bytes; they are
not relabelled as a run on the saved revision. Saved output is plain file content and can enter Git history.

In a matching current-source canvas, each test offers **⚡ Live** or **◉ Recorded** per tool. These choices and
**Record again** are drafts until Enter saves and runs; changing to Live removes that tool's saved fixture.
Recorded details show the saved call and result. Live details report actual source and counts, not saved payload.
Model comparison captures pending Recorded tools at most once, then every compared model (including the capture
model) replays the acknowledged saved source. A still-uncaptured Recorded tool stops comparison; Live tools remain
live for each model. Modes never change silently. Source or effective-settings changes stop subsequent execution.

The existing MCP Run/Test operations accept an optional `expectedRevision` precondition. Test additionally accepts
`expectedToolSettings` (the complete named Enabled/Disabled vector) and `recordedReplayOnly`; the latter prevents
new Recorded captures, not Live calls. Edit accepts bounded `exampleEdits` alongside `content` and its expected
revision. With sparse edits the supplied content retains the original Examples bytes; only named test fields and
mode/reset intents change, never caller-supplied fixture payload. Older backends keep fixture-bearing files
inspection-only in the canvas. These source capabilities are not supplied by the pinned older release.

Recorded call `arguments` and `result` each have their own 32-level nesting budget, measured from that
payload's root at depth zero. Each nested object value or array element adds one level. Other Examples structure,
including tool `parameters` schemas, keeps the 12-level budget measured from the YAML document root at depth zero.
Payload nodes still count toward the same 32,768-node total for the whole Examples document; byte caps are unchanged.
Fixtures reject external schema references, duplicate keys, inexact JSON numbers and known credential shapes. Arbitrary private data
can evade those checks: saved results are plain file content that the author may commit. Native Squad stays tool-free.
Run-profile export refuses fixtures and tool checks rather than dropping them. Older backends without sparse
`exampleEdits` keep fixture/tool-check files read-only in the canvas; CLI/MCP Test remains available. No automatic migration occurs.

## Bounds, loading and errors

| Resource | Maximum |
|---|---:|
| Original file bytes | 256 KiB |
| Examples | 128 |
| Assertions per example | 64 |
| Input or any YAML scalar | 16 KiB UTF-8 |
| Examples structure nesting (outside recorded payloads) | 12 levels from document root (depth 0) |
| Each recorded call's arguments or result nesting | 32 levels from payload root (depth 0) |
| YAML nodes, including all recorded payloads | 32768 total |

Existing stricter assertion limits still apply. Recorded payload safety checks also inspect JSON encoded inside
strings; that decoded nesting counts toward their 32-level safety budget, and encoded duplicate keys refuse.
These are parsing limits, not execution/output/token budgets. A runner must separately bound outputs and cost.

`Parse(filename, bytes)` returns an Agent or a typed Error and nil Agent.
`Load(filename)` first checks a bounded regular file, then calls Parse. Paths
containing a `..` component refuse before cleaning, so normalization cannot erase
an unchecked symlink traversal or redirect the read. It also refuses static symlinks and irregular/reparse entries (including Windows junctions) in
**any path component**, directories, devices, FIFOs, unreadable
and oversized files, then rechecks opened identity/type/size and bounds the read.
Resolve a trusted symlinked path explicitly before loading if appropriate (for
example macOS `/var` paths). This is the existing workspace reader's static-file
boundary, **not protection against hostile concurrent filesystem replacement**.
Selected-root confinement and safe source editing belong to the calling operation.

Errors carry a filename and 1-based line/UTF-8 byte column, canonical field name
(or joined list of missing fields), and repair guidance. Missing sections point
to 1:1; malformed YAML points to its document start. Authored values and underlying
YAML/OS errors are not echoed; control characters in filenames are quoted and
known credential-shaped filenames are withheld. Parsed structures themselves
contain source content and must not be used as default logs or result JSON.
For example, a complete charter with missing Identity values still reports
`Identity.ID` and `Identity.Purpose`; a plain `# Legacy` prompt is accepted
without inventing charter sections or rewriting the file.

## Frozen run-profile export (unreleased)

**Temporary demo bridge—not the deploy contract.** The deploy contract is the
tested AgentSuite artifact handed off by digest (kmx pushes; the host pulls).
This export does not implement that deployment path or define a host-side API.

`kmx package run-profile export FILE [--tests FILE]` uses the shared checked
`LoadPair` reader without configuration, state, provider selection, credentials
or execution. Default tests use the fixed companion rule; an explicit override
uses the same C3 selection as file Run/Test. It accepts ordinary agent-file
shapes, not the distinct native Squad profile. Both selected sources must be
valid; there is no fallback to inline tests after a companion failure.
Export still requires an explicit authored model; preview defaults and picker overrides are not export provenance.
Zero-test authoring charters refuse export
before any profile bytes are written. Packaging and deployment still require test
evidence; saving a prompt or passing an ad-hoc Run grants neither. The original
source is not modified.

The explicit export emits one deterministic, pretty-printed JSON document with
a final LF, at most 2 MiB. It includes authored content, not the payload-free
preview result. Keep it private; known-secret scanning cannot detect arbitrary
secrets in prose. No output-file writer is provided. Shell redirection is caller
owned and can truncate a file before validation; it does not inherit kmx privacy
or exclusive-create guarantees. Refusal writes no profile bytes; an output failure
exits nonzero but may leave partial bytes at the caller's destination.

Inline/single-source profiles retain the experimental v1 envelope below. External
tests use v2 with the same fields plus `snapshotRevision` and `testsSource`
(`state`, physical `path`, raw `revision`, `selectionKey`). The agent's raw hash
stays `sourceRevision`. V2 includes local physical-path metadata and is deliberately
machine/selection-bound: copying identical sources to another folder changes its
snapshot/profile bytes, but not semantic example hashes. The profile is a
normalized projection, not an editable raw-source bundle or a save acknowledgement.

The v1 envelope has exactly these fields:

| Field | Meaning |
|---|---|
| `schemaVersion` | `1` |
| `kind` | `kmx.agent-run-profile` |
| `sourceRevision` | `sha256:<lowercase hex>` of original source bytes, including CRLF and YAML comments |
| `agentId` | Canonical authored identity |
| `instructions` | Object with `text` (complete canonical LF-normalized prefix before Examples) and `sha256` of that text's decoded UTF-8 bytes |
| `model` | Object with `id` (exact authored Preferred string) and `sha256` of that string's decoded UTF-8 bytes |
| `examples` | Ordered objects with `id`, `input`, complete ordered `assertions`, `sha256` and `targetStatus: not_run` |

Each example hash is SHA-256 of **compact JSON** of a fixed object with fields
in this order: `id`, `input`, `assertions`. Assertion objects retain authored
`id`, `type` and applicable operands in this order: `value`, `pattern`, `tool`,
`rubric`, `minScore`, `judgeModel`, `judgeVersion`; empty inapplicable operands
are omitted, while an authored zero `minScore` remains present. Tool assertions
remain unsupported. Encoding is Go `encoding/json.Marshal`'s default: HTML
characters `<`, `>` and `&` use lowercase `\u003c`, `\u003e`, `\u0026` escapes;
standard JSON quoting applies, UTF-8 is otherwise preserved, and there is no
whitespace or trailing newline in the hash preimage. Neither `sha256` nor
`targetStatus` is part of that preimage. For example:

```json
{"id":"greeting","input":"Ada","assertions":[{"id":"greeting-text","type":"contains","value":"Hello, Ada"}]}
```

These hashes include assertion IDs and are **not** runtime evaluation-definition
digests or AgentSuite JCS digests. Source locations, timestamps, routes,
credentials, local results and build state are omitted; v1 also omits paths.
V2's selected-source identity is separate from semantic hashes. Comments in the Examples YAML affect
`sourceRevision` but never become example inputs/rubrics or instructions.
Callers may separately hash the complete emitted artifact. An unsigned profile
and recomputable hashes establish consistency, not authenticated parser/source
provenance or model weights/provider identity.

No model or judge identity is substituted or resolved. A consuming runtime must
separately establish compatible serving identities and run target examples;
export cannot claim a target pass. A run profile is not an AgentSuite runtime
profile (`--runtime-profile`), evaluation receipt, tested package, signature or
deployment authority. Source-file projection is not exact-package continuity.

## Placement and Squad boundary

The same bytes work as a standalone `<id>.agent.md` or
`.squad/agents/<id>/charter.md`. At the latter path Identity.ID must equal the
directory ID; relative names and dot segments are resolved against the current
directory for this check, and placement components are recognized case-insensitively.
Other standalone filenames do not depend on cwd; relative `charter.md` depends
on successful current-directory resolution. This is a spelling check, not filesystem
canonicalization: Windows 8.3 names and trailing-dot/space aliases are not expanded.
Use canonical path spellings for this check; standalone filenames never supply identity.
The parser does not discover or initialize Squad, register a member, or change
rosters, history, routing or casting. Placement is not team onboarding.

This is kmx's canonical output contract, not arbitrary legacy Squad-charter
support or an editor host's custom-agent format. Against Squad's pinned
[working-draft profile](https://github.com/bradygaster/squad/blob/56549a6e465ca8e8da10969cfe44cadd89b05274/docs/specification/charter-v0.1.md),
plain Examples is an accepted extra section with warning SQC108, **not** warning-free
canonical conformance. Squad treats Preferred as a preference, not kmx's exact
model requirement. Its raw-charter prompt may include Examples, and structured
editing need not preserve extra sections. Do not claim hidden checks or identical
prompts across hosts.

## Native Squad profile — internal foundation

`Parse`/`Load`, file Run/Test and v1 MCP accept ordinary agent files, not the
native Squad profile. Explicit
`ParseSquad(filename, bytes, SquadProfile)` separately accepts the qualified
**Squad 1.1.0** cast shapes; it does not auto-detect or import arbitrary charters.
The [captured catalog and fallback fixtures](../internal/kmx/agentfile/testdata/squad-v1.1.0/README.md)
come from real non-TTY init/cast, not a hand-written template approximation.

The caller supplies release `1.1.0`, an approved lowercase `MemberID` and the
approved native **display Role** (not its catalog lookup ID). The title and
native Role must match this mapping; optional native Name must match when
present. Catalog output has no Name field: none is invented. The filename must
end in `.squad/agents/<approved-id>/charter.md`, checked lexically without cwd
lookup and with slash/backslash support. Structural placement components ignore
case; the approved ID matches exactly. Reserved built-ins and IDs changed by
Squad's slug rules refuse. This is not confinement or proof that cast ran;
the caller must establish actual release, provenance and reviewed role mapping.

The distinct `SquadCharter` retains copied original source, exact-byte revision,
ordered half-open **original-byte** section/body spans and a Preferred-value-only
span. `Source()` returns a copy. CRLF, Unicode, native attribution, placeholders,
Model Rationale/Fallback and a missing final newline are preserved, not rendered
through the canonical format. Native section order/metadata and the qualified
attribution preamble are checked; unknown/duplicate/conflicting structures and
unqualified releases refuse with a repair hint. Present Fallback stays inert
text: it never authorizes a fallback model or tool access.

A bare starter is valid parser input but has `Enriched=false`; it is not an
executable agent. Valid enrichment has nonblank `## Task Instructions`, concrete
`Model.Preferred` and valid `## Examples`, with Task Instructions then Examples
last. Existing native Model stays before Collaboration. If it was absent, a new
Model can be appended after Voice immediately before Task Instructions; absent
Rationale/Fallback is not synthesized. Partial or malformed additions refuse.
Effective instructions retain all native text and task instructions before
Examples (LF-normalized only in that projection), including any native Model
prose. Task-only instructions are separate for the later fake-route integration.

The existing canonical loader does not accept these native documents. Native
preview/execution, preservation-aware public Edit, versioned team Create guidance,
exact package digest/results and real coordinator dispatch remain separate work;
a source revision or `Enriched=true` is not a package-bound pass.

### Internal native cast and enrich

`mcp.OpenSquad` remains the initialized-only cast/enrich core. Explicit trusted
startup may enable `OpenSquadSetup` and native v2 branches of the same four tools;
see [native MCP](kmx.md#optional-native-squad-chat-flow). With no native dependency
configured, four-tool/v1 discovery and behavior are unchanged. The host must
approve composition, native setup effects and owned charter changes before Create.
Startup/discovery is read-only. Conditional setup runs fixed
`init --no-workflows --no-vscode-default` only when `.squad` is absent; existing
supported setup is validated/reused, never reinitialized. There is no dependency
download, implicit runtime choice, upgrade, recovery or rollback.

Trusted startup supplies the installed absolute executable, declared qualified
release `1.1.0` and matching platform. Those labels and file identity checks do
not authenticate the program or its runtime bundle. There is no version-probe
child or shell fallback: apart from the guarded conditional init, the only action
is `cast --name ID --role ROLE` at the selected root. Batch/PowerShell-associated launcher extensions refuse, and
Windows requires a qualified `.exe` rather than implicitly introducing `cmd.exe`. Its installed launcher may itself use its native interpreter.
Stdin is closed, stdout/stderr are drained with 32 KiB retention each, private
native output never enters results/errors/MCP stdout, and child execution is
bounded to 30 seconds plus bounded pipe cleanup. Child environment permits only
basic OS/home/temp/PATH/locale settings and disables personal lookup; root
redirects, Node/shell injection variables and unrelated credentials are omitted.
Setup-enabled creators derive `SQUAD_STANDALONE_HOME` from the selected launcher,
set root-bound Git ceiling/work-tree overrides, disable system/global Git config,
and override the origin URL at command scope to avoid platform introspection.
The operator's HOME remains inherited, not isolated containment. The qualified
standalone launcher prevents native npx-spec fallback; no additional runtime is
bundled or installed by kmx.

The approved request has a Lead and up to seven specialists, at most 1 MiB of
logical authored fields. Each member must fit the existing 256 KiB charter
budget after enrichment. CLI role and expected native display Role are separate.
Stable IDs must be portable, nonreserved and unchanged by native slugification.
Role text excludes option prefixes, control characters and known Markdown/TS
interpolation delimiters. Authored-field syntax and known cross-member identity
collisions validate before the first native effect. The combined charter size
is checked after observing the native starter: an unexpectedly large starter
can leave a registered-but-unenriched member requiring human inspection.

After setup, only explicit canonical local roots with
`.squad/{config.json,team.md,routing.md}` are accepted. For new setup, prior
`.mcp.json`, `.copilot`, `.github/skills` or native capability files refuse;
linked targets, partial native state and `.git` worktree pointers also refuse.
Native init owns new built-ins, state/templates/skills, local MCP configuration
and `.gitignore`/`.gitattributes` changes. Existing regular singly-linked bounded
ignore/attributes files may be modified by that approved native setup. Config is exactly `version: 1`, optionally `teamRoot: "."`;
unknown keys, redirect/external state and `.ai-team` refuse rather than being
rewritten. Existing personal/home/global exclusions are resolved before comparison,
including aliases. Root TypeScript configuration is unsupported: identities in
arbitrary expressions/template literals are not guessed or rewritten.
The bounded observed footprint is `.squad/` and the native capability file
`.github/agents/squad.agent.md`: 4096 entries/8 MiB, regular singly-linked files,
no static links or special-file aliases. Native registry locks/unfinished
transaction artifacts require human review. Casting registry/history/commit
state must be entirely absent or a managed pair with matching version, revision,
transaction and raw-byte SHA-256 commit hashes. Orphan/mismatched/legacy state
refuses before cast rather than triggering implicit native recovery/migration. Existing
member directories (including history-only ones), roster/routing references and
registry identities refuse; no prior member is adopted or silently re-cast.

After successful native cast, matching active roster/registry and a new regular
bare charter establish an operation-owned observation. The full native profile
must match. Enrichment retains original bytes, replaces only Model.Preferred,
and appends Model if absent, Task Instructions and fenced plain Examples last.
The full candidate is reparsed, then published through same-directory staging
and existing OS locks with exact revision, inode and parent/root identity checks.
Mode bits are preserved, not ownership/ACLs/xattrs. Native source is never
regenerated through a canonical renderer; no other native metadata is patched.

Calls through one creator are serialized. Arrange a quiet project: native cast
is not a transaction, other processes do not share this creator's gate, outside
editors can ignore publication locks, and a working directory is not a sandbox.
A trusted child still has ambient authority. These are cooperative filesystem
checks, not hostile-tree confinement or crash-durable completion.

Results distinguish not-attempted, failed cast, successful-but-unverified cast,
registered-but-unenriched and enriched members. Changed paths describe retained differences in the observed
footprint, not every transient write. Setup-enabled observations additionally
cover native skills, `.copilot`, root MCP and ignore/attributes files; the same
4096-entry/8-MiB total bounds apply. `setup` distinguishes initialized/reused/failed/unknown;
partial/unknown setup stops before cast. Failure/cancellation stops later members;
unknown observations remain explicit errors. An acknowledged publication is not
relabelled unenriched by later cancellation. Nothing is rolled back or deleted,
and no operation recovery/adoption API is exposed: interrupted members need human
inspection. Registration, valid enrichment, member execution, exact package-bound
results and actual coordinator dispatch are separate observations.

Native Go tests cover parser/file/process contracts on Linux/macOS/Windows.
Windows test-child execution is not Windows native-Squad qualification; the
separate day-zero CLI supports canonical package-free fake file Run/Test only.
An opt-in installed-Squad test separately performs native init and actual catalog/fallback
cast/enrich in an isolated project/HOME; it runs no model or coordinator dispatch.

### Reopened native member operations

Native member resources and Edit/Run/Test reconstruct the explicit profile from
validated native casting state on each operation and after restart: managed pair
and raw-byte commit hashes, exact active ID/display name/role, one matching roster
row/path/status, rooted charter, supported release and enriched native parse.
The same bounded read-only inventory used by Create also rejects pending casting
transactions, registry locks and temporary recovery files. No recovery runs.
Invalid or missing/inconsistent state shows **not set up by kmx** and disables
native mutation/preview. No filename/H1 guessing, v1 fallback, repair, recast or
origin/pass claim. Valid state does not restore a lost proposal, previous preview
or same-operation initial-enrichment proof. No kmx durable mapping is written.

Owned Edit replaces only Preferred's value and Task Instructions/Examples bodies,
reparses the candidate and uses the same source/parent CAS. Preview reparses an
explicit native snapshot and shares the existing tool-free App/runner/judge path
in memory, without writing a canonical file or inventing Identity.Purpose. Real
routes receive preserved native plus task instructions excluding Examples; fake
orientation reads task-only directives. Results bind exact native source revision,
not an AgentSuite/package digest or team execution. Native flow is chat-only;
there are no new Squad canvas actions.

## Future AgentSuite mapping — not implemented

Identity.ID could identify one member; the prefix instructions could become its
instruction blob; Preferred could supply a model requirement **after explicit
trusted-route/runtime selection**. Ordered examples could map to public
`evals/<member>/` index/case documents with existing `id`, `input`, assertions and
public digests. These source bytes/revisions are not an OCI suite identity or
private passing evidence. Packaging still needs explicit build/runtime profiles,
implementation/composition declarations and public validation. No tools,
publication, deployment or evaluation authority follows from parsing or local
checks; native suite loaders do not accept this charter implicitly.
