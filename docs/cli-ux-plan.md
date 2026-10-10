# CLI presentation contracts

Static TTY hierarchy, responsive status/agent reports, and chat
presentation are implemented. This reference describes the existing CLI; it
does not decide the Orka authoring surface or claim complete CLI coverage or a
new live-cluster verification run.

## Goal

Make interactive `kmx` output easier to scan without making test output,
redirected logs, or automation harder to parse. Lip Gloss is a static styling
tool here, not a reason to turn commands into terminal applications.

The invariant is:

```text
interactive terminal: semantic emphasis
redirected text:       existing format, except documented safety fixes
JSON/YAML/artifacts:    structured data only
progress/diagnostics:   stderr
requested data:         stdout
```

## Why output needs classification first

Much of `kmx`'s readable output is also an interface consumed by the project:

- release jobs parse `kmx version` exactly;
- quickstart JSON, agent list/status JSON, manifests, completion and Task answers
  are machine formats.

Styling and richer layouts are destination-specific. Existing redirected
formats must remain intentional interfaces. Incorrect safety claims are not
frozen by this rule: the audit exceptions below apply in plain and rich modes.

## Output compatibility matrix

| Surface | Default human format | Machine/raw format | Compatibility rule |
|---|---|---|---|
| `quickstart` | phases, answer, governance warning, action tree | `-o json` | JSON stdout is one document; rich output is text mode only. |
| root `status` / `orka status` | Orka runtime fields and tables | table only | Running controller identity, Deployments, CRDs and Providers; no plane suffix or legacy object counts. Bundle-aware `agent status` separately supports JSON. |
| `agent list` | heading and table | `-o json\|yaml` | Structured modes remain kubectl-native and are never decoded or restyled. |
| `agent create` | phases, capabilities, next actions | `--out -` YAML | Manifest stdout is an artifact and permanently bypasses presentation. |
| `completion` | none | shell source / Cobra protocol | Permanently raw and executable. |
| agent chat | interactive Orka session | none | One-shot chat and raw A2A `--json` output are retired. Enhanced input requires capable input/output terminals; scanner fallback is supported. |
| context | fields | plain redirected text | Rich fields on a TTY; exact existing alignment when redirected. |
| progress and guard | phases and decision callout | plain stderr transcript | Progress delimiters and plain guard geometry remain; corrected action/confirmation commands apply in both modes. |
| version | fixed prose | release parser input | Keep exact plain format; only TTY token styling is safe. |
| lift record | none | JSON recovery state | Permanently raw internal artifact. |

Structured selection belongs to each command. There is no global output hook:
the command branches to JSON, YAML, manifest, completion, or Task answer
before constructing a human renderer.

## Foundation

`internal/kmx/cliui` owns presentation for one destination writer:

- the writer itself must expose a file descriptor and be a terminal;
- `TERM=dumb` disables rich rendering;
- any non-empty `NO_COLOR` disables ANSI color but retains terminal grouping;
- buffers, files, pipes, and injected test writers remain plain;
- semantic words remain present, so color never carries meaning alone.

The shared vocabulary is data-oriented rather than printf-oriented:

- `Fields` aligns key/value facts by terminal display width;
- `Table` uses Lip Gloss tables for modeled rows on a TTY;
- `Actions` renders commands and consequences as a tree;
- `Callout` reserves a compact border for an interrupting decision;
- semantic inline styles cover headings, phases, success, failure, warning,
  accent, and secondary context.

Migrated tables keep their former formatter as the explicit plain branch.
State reports and recovery advice also carry the intentional safety fixes below.

Lip Gloss v2 adds a meaningful dependency graph to a small CLI. Shared styling
and static layouts live here; chat also uses terminal-width helpers directly
for its stateful editor. Avoid spreading styling through command logic.

## Patterns validated against Lip Gloss v2 examples

The v2.0.6 examples were reviewed from the tagged upstream source. The useful
patterns for `kmx` are narrower than the full demonstration application.

### Semantic inline styles — use now

The color and layout examples compose small styled fragments into ordinary
text. That matches commands whose native subprocess output must keep streaming:
style `PHASE`, `FAILED`, `WARNING`, or an operation heading, then write the rest
of the existing line unchanged.

This is the implemented default. It adds hierarchy without clearing lines,
capturing subprocess output, or turning a command into an event loop.

### Compact bordered callouts — use selectively

The standalone example combines `RoundedBorder`, padding, and
`JoinVertical` into one self-contained decision. That pattern fits only moments
where the operator must stop and decide:

- a remote-context confirmation;
- a setup or deployment action awaiting explicit consent;
- a destructive teardown summary.

Keep these blocks compact and left-aligned. The command a user copies must stay
plain inside the block, and the non-TTY path must keep today's line-oriented
text rather than render border characters.

### Trees and lists — use for relationships

The tree and list examples make parent/child relationships legible without a
grid. Strong candidates are:

- agent → model route and raw tool inventory;
- next action → command → consequence;
- quickstart/up plan → pending, active, and completed phases.

These should be static snapshots at meaningful boundaries, not continuously
redrawn progress. A tree is especially useful where prose currently repeats
indentation to imply ownership.

### Width-aware composition — use after measuring the destination

The layout example uses `Width`, `lipgloss.Width`, `JoinHorizontal`, and
`JoinVertical` to compose columns, then clamps the result to the physical
terminal width. This can improve wide reports such as `kmx agent list`:

- related summaries side by side when there is room;
- one vertical flow on narrow terminals;
- a compact next-actions panel below both.

The presenter now measures the destination and supports narrow-width fields and
tables. Side-by-side status panels are not implemented. Redirected output does
not wrap according to a guessed terminal size.

For tables, terminal width is a maximum, not a target. A table that fits keeps
its natural content width and stays left-aligned; assigning the whole terminal
width makes Lip Gloss distribute spare space across columns and breaks visual
row tracking. A table that does not fit becomes labeled records instead of a
squeezed or horizontally stretched grid.

### Styled tables — implemented for modeled reports

Agent list uses a titled report with explicit state and numeric column roles.
Narrow modeled tables become labeled records. Root and Orka status retain their
fixed-width runtime fields. Numbers are not abbreviated; state styling never
guesses from an identifier's spelling. The plain table remains the redirected
compatibility format. Structured modes belong to each command.

### Patterns rejected for this CLI

- Full-screen placement and centered documents waste space in static command
  reports. Interactive chat and console have separate full-screen interfaces.
- Modal compositing obscures static reports.
- Gradients, animation, and decorative backgrounds add noise to operational
  state and have unreliable contrast across terminal themes.
- Rounded borders around every section flatten hierarchy rather than improve
  it; reserve a border for a decision that genuinely interrupts the flow.
- Background detection enters a more complex terminal interaction than named
  ANSI colors require. The current palette uses standard terminal colors.

## Interactive chat

On capable input/output terminals, Orka chat uses a full-screen transcript
viewport with a pinned header and message editor. It reflows on resize without
submitting typed input. Users can browse scrollback and draft the next message
while a response runs, but cannot submit another turn until it finishes.
See [interactive chat](interactive-chat.md) for keys and fallback behavior.

The renderer owns terminal mode, prompt redraw, progress, assistant grouping
and control-sequence sanitization. Trusted actor/action labels and indented
payloads keep tool/model prose separate from local controls. Lip Gloss styling
is applied only after dynamic text is sanitized. `/help` lists the active
backend's commands; unknown slash commands are refused.

Each native Orka turn is a fresh Task; its answer is displayed after result
retrieval. Chat has no resumable server-side session, server history replay or
native approval submission. Local scrollback and sent-message recall are UI
features, not persistent runtime history. Tools requiring approval are unavailable
in this client; use the runtime's supported operator path instead.

Retired `--json` and `--session` flags are refused before application loading.
An optional message after the Agent name is the first interactive turn, not a
one-shot invocation; use `kmx agent run` for one Task.

`NO_COLOR`, `TERM=dumb` and non-terminal input/output select the line-oriented
path. Scanner input remains available when enhanced input is unavailable.
Terminal state is restored on exit or cancellation. Full-screen resize preserves
the draft; the non-full-screen raw-input fallback retains its conservative
resize-abort behavior. Neither resize nor cancellation submits pending input.

## Delivery slices

### 1. Durable progress labels — implemented

Implemented first because `PHASE`, `DONE`, `FAILED`, and `COMPLETE` are
centralized on stderr. Only those tokens gain semantic color and weight. Their
spaces, brackets, names, elapsed times, newlines, and native subprocess output
stay unchanged. Buffer-based tests continue to assert the exact old transcript.

This improves `quickstart`, `up`, `agent create`, and `lift` together.

### 2. Safety prompts and guided input — guard implemented

Implemented:

- context-guard action and confirmation emphasis.

The `agent create` wizard now uses Bubble Tea for eligible terminals; its
input and cancellation boundaries are in [charm-ux-followup-plan.md](charm-ux-followup-plan.md).

Keep refusal wording and copyable remediation commands plain and atomic.
Guard vocabulary is safety behavior and remains asserted without ANSI.

### 3. Notes, warnings, and next actions — primary journeys implemented

Semantic styles now cover selected stderr messages:

- warnings, runtime boundaries and next actions in agent creation and quickstart.

Less common surviving operator journeys remain candidates.

Do not mechanically style every `notef` call. Many include multiline native
output, errors, or commands that users copy.

### 4. Status and agent list — modeled reports implemented

Status has grouped fields, counted tables, and explicit state/number styling;
agent list uses the same report renderer. Retained reports keep their redirected
table geometry; managed-tool governance fields are explicitly retired. Unknown
conditions and readiness verdicts are corrected in both human modes, as detailed
below.

## Audit fixes and limits

These are safety-semantic and format fixes, not merely color changes:

- Runtime status distinguishes unreadable from absent, and the running controller
  version from the compiled pin. Provider readiness is separate from Deployment
  readiness; neither proves a completed Task.
- Quickstart requires a completed task with a readable answer before marking its
  question phase done. `governed: false` retains the unchanged JSON key set and
  means this invocation did not enable governance, not that the cluster has none.
  Quickstart/up do not claim existing routing is absent on a rerun.
- Quickstart no longer discovers, installs, or reconciles the legacy Helm
  release. It reconciles the pinned Orka runtime, then reuses only an exact
  match of its fixed Provider and Agent; a differing live spec is refused
  rather than overwritten. Every run creates a fresh Task. Orka setup uses the
  verified v0.2.0 Helm chart; retiring the legacy chart did not remove Helm.
- Guard and recovery commands preserve the relevant target, options, and shell
  argument boundaries. Kind creation/image loading refuses mismatched cluster
  and context names. Native Provider Secret references are validated without
  printing their values.
- Lift confirms before any deletion, retains records for incomplete cleanup, and
  scopes telemetry, ownership, and billing claims to what was actually checked.
- Chat names the selected agent/context and distinguishes tool activity from
  local controls. Model prose is not authorization or enforcement evidence.
- Full-screen chat preserves typed input and reflows on resize. It retains local
  scrollback, not resumable server-side history.

Still unimplemented: a comprehensive presentation pass over uncommon surviving
operator paths, side-by-side status panels, and positive per-call governance
receipts in chat. Unit/fake-service and Linux PTY tests cover current behavior;
they are not evidence of a new live kind/AKS deployment or every terminal/platform
combination.

The former one-shot chat transport retries and question-only resampling are
removed. `kmx quickstart` creates one Orka Task and polls its result without
resubmitting. Interactive `/retry` explicitly resends a message and does not
promise exactly-once execution. Cancellation restores terminal state but does
not undo completed external actions.

## Test policy

Every styled surface needs both modes asserted:

1. A buffer or redirected file receives the compatibility format and no styling
   `ESC`; intentional safety-semantic changes have explicit regression tests.
2. A styled presenter retains the semantic words and emits ANSI.
3. `TERM=dumb` forces plain presentation. Non-empty `NO_COLOR` removes ANSI but
   retains static rich grouping on a capable terminal; chat input falls back.
4. Structured stdout remains untouched while stderr may be interactive.
5. Native subprocess output remains between durable phase boundaries.

Tests should not strip ANSI before comparing redirected output. If ANSI reaches
a non-terminal writer, that is a product bug rather than a test-normalization
problem.
