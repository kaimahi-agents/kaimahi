# Single-file agent charter

`internal/kmx/agentfile` parses a small, tool-free Markdown charter offline.
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
- Include exactly one of each H2 shown above, with those exact unindented
  spellings. Examples must be last; the other sections can be reordered.
  Unknown or duplicate H2 sections refuse, including Tools or Hooks. No
  frontmatter, legacy aliases, inferred fields or arbitrary charter importer.
  Any `<!--` marker outside code fences refuses, even inside inline code, to
  avoid host-dependent heading/fence scanning.
- Identity contains only the two shown bullet fields. ID is 1–63 lowercase
  ASCII letters, digits or hyphens, starting with a letter. Purpose is nonblank
  single-line text. Model contains only the Preferred bullet. Empty lines are
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
  preserve the complete prefix before Examples, including identity/model metadata,
  with CRLF normalized to LF. Examples are never part of those instructions.
  The revision is `sha256:<hex>` of the **original bytes**, including line endings.

Preferred is one exact printable ASCII model identifier (1–256 bytes; letters,
digits, `.`, `_`, `:`, `/`, `-`, starting with a letter or digit). `auto`, URLs,
spaces and wildcards refuse. `fake` names only kmx's deterministic orientation
route, not a general-purpose model or a claim that Squad runs that model.
The trusted provider route, endpoint and credentials must come from outside
this file; the parser neither checks availability nor chooses/falls back to a
model. Prose cannot grant tools or hooks: downstream execution must remain
tool-free regardless of what instructions request. Parsing is not a sandbox or
a guarantee that arbitrary prose is safe or secret-free. The complete charter,
including raw YAML comments, is checked with the existing credential-shape scanner
and refuses known shapes with a fixed redacted error. Decoded example inputs and
assertion operands are checked too, so YAML escapes cannot hide known shapes;
arbitrary secrets can still evade shape detection.

## Examples and assertions

The outer mapping has only `examples`. Each example has exactly `id`, `input`
and `assertions`. Order is preserved. Example IDs are unique across the file,
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
`Model.Preferred` and the missing responsibility/Examples sections, without
inferring identity from its folder or rewriting the file.

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

`Parse`/`Load` and current file Run/Test/MCP remain canonical-only. Explicit
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

`mcp.OpenSquad` and `SquadCreator.Create` add a separate internal authoring core,
not new CLI flags or MCP bindings. Current four-tool/v1 behavior is unchanged.
The host must approve composition, native side effects and the owned charter
changes before invoking it. The operator separately initializes and reviews the
local project with Squad; this core never runs init, downloads a dependency or
selects/upgrades a runtime.

Trusted startup supplies the installed absolute executable, declared qualified
release `1.1.0` and matching platform. Those labels and file identity checks do
not authenticate the program or its runtime bundle. There is no version-probe
child or shell fallback: the only action is `cast --name ID --role ROLE` at the
selected root. Batch/PowerShell-associated launcher extensions refuse, and
Windows requires a qualified `.exe` rather than implicitly introducing `cmd.exe`. Its installed launcher may itself use its native interpreter.
Stdin is closed, stdout/stderr are drained with 32 KiB retention each, private
native output never enters results/errors/MCP stdout, and child execution is
bounded to 30 seconds plus bounded pipe cleanup. Child environment permits only
basic OS/home/temp/PATH/locale settings and disables personal lookup; root
redirects, Node/shell injection variables and unrelated credentials are omitted.

The approved request has a Lead and up to seven specialists, at most 1 MiB of
logical authored fields. Each member must fit the existing 256 KiB charter
budget after enrichment. CLI role and expected native display Role are separate.
Stable IDs must be portable, nonreserved and unchanged by native slugification.
Role text excludes option prefixes, control characters and known Markdown/TS
interpolation delimiters. Authored-field syntax and known cross-member identity
collisions validate before the first native effect. The combined charter size
is checked after observing the native starter: an unexpectedly large starter
can leave a registered-but-unenriched member requiring human inspection.

Only explicit canonical local roots with `.squad/{config.json,team.md,routing.md}`
are accepted. Config is exactly `version: 1`, optionally `teamRoot: "."`;
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
footprint, not every transient write. Failure/cancellation stops later members;
unknown observations remain explicit errors. An acknowledged publication is not
relabelled unenriched by later cancellation. Nothing is rolled back or deleted,
and no operation recovery/adoption API is exposed: interrupted members need human
inspection. Registration, valid enrichment, member execution, exact package-bound
results and actual coordinator dispatch are separate observations.

Native Go tests cover parser/file/process contracts on Linux/macOS/Windows.
Windows test-child execution is not Windows Squad/CLI qualification. An opt-in
installed-Squad test separately performs native init and actual catalog/fallback
cast/enrich in an isolated project/HOME; it runs no model or coordinator dispatch.

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
