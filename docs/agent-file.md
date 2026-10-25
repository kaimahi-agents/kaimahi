# Single-file agent charter

`internal/kmx/agentfile` parses a small Markdown charter offline, with an optional host-tool name declaration.
This is a **format and parser**, not an execution command. Existing native suite
loaders, `create`, `run --agent`, and workspace `test` are unchanged. There is no
model call, workspace discovery, source write or implicit conversion.

The [complete greeter sample](../internal/kmx/agentfile/testdata/greeter.agent.md)
is the canonical shape:

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
- Include exactly one of each H2 shown above except Model, which may be omitted,
  with those exact unindented spellings. Examples must be last; the other sections can be reordered.
  An optional `## Tools` section may appear directly before Examples, containing
  1–32 unique names as `- tool_name` bullets and otherwise only blank lines.
  Names are 1–64 ASCII letters, digits, `_` or `-`, starting with a letter or `_`.
  Dotted names such as `server.read` refuse; names must fit the Chat/Responses
  function-name contract.
  The host supplies each tool's schema and description. No endpoints,
  schemas, credentials or other names are accepted. Unknown or duplicate H2 sections
  refuse, including Hooks. No
  frontmatter, legacy aliases, inferred fields or arbitrary charter importer.
  Any `<!--` marker outside code fences refuses, even inside inline code, to
  avoid host-dependent heading/fence scanning.
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
bindings/credentials, unsupported routes or invalid provider results refuse.
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
shipping evidence. Each nonempty example has exactly `id`, `input` and
`assertions`. Order is preserved. Example IDs are unique across the file,
1–63 ASCII letters, digits, `.`, `_` or `-`, starting with a letter or digit.
Input is a nonblank string, validated with the existing runtime case validator
(including known credential-shape refusal). Assertions are nonempty and reuse existing
[runtime assertion semantics](../internal/kmx/runtime/evaluation_assertions.go).

Supported types and operands:

| Type | Required operand fields |
|---|---|
| `contains`, `notContains` | `value`: nonblank single-line string |
| `regex` | `pattern`: nonblank valid Go RE2, at most 1024 bytes |
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

No tool assertions, `tool` fields, legacy `expectContains`, repeat policy,
executable declarations, unknown/duplicate fields, anchors, aliases, merge keys
or custom YAML tags are accepted. String fields must be YAML strings: quote a
numeric-looking input such as `"123"`; null entries cannot disappear silently.

## Bounds, loading and errors

| Resource | Maximum |
|---|---:|
| Original file bytes | 256 KiB |
| Examples | 128 |
| Assertions per example | 64 |
| Input or any YAML scalar | 16 KiB UTF-8 |
| YAML nesting / nodes | 12 levels / 32768 nodes |

Existing stricter assertion limits still apply. These are parsing limits, not
execution/output/token budgets. A runner must separately bound outputs and cost.

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
For example, `# Legacy` refuses with `Identity.ID`, `Identity.Purpose`,
and the missing responsibility/Examples sections, without
inferring identity from its folder or rewriting the file.

## Frozen run-profile export (unreleased)

**Temporary demo bridge—not the deploy contract.** The deploy contract is the
tested AgentSuite artifact handed off by digest (kmx pushes; the host pulls).
This export does not implement that deployment path or define a host-side API.

`kmx run-profile export FILE` uses `agentfile.ExportRunProfile`, which calls
`Load`/`Parse` once, without configuration, state, provider selection, credentials
or execution. It accepts canonical runnable charters only, not `Inspect`'s minimal
display files or the distinct native Squad profile. Existing tools/settings and
safe parser refusals apply unchanged. Export still requires an explicit authored
Model.Preferred; preview defaults and picker overrides are not export provenance.
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

The experimental v1 envelope has exactly these fields:

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
digests or AgentSuite JCS digests. Paths, source locations, timestamps, routes,
credentials, local results and build state are omitted. Comments in the Examples YAML affect
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

`Parse`/`Load`, file Run/Test and v1 MCP remain canonical-only. Explicit
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
Windows test-child execution is not Windows Squad/CLI qualification. An opt-in
installed-Squad test separately performs native init and actual catalog/fallback
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
