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
- a native runtime decision awaiting explicit consent;
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

- Full-screen placement and centered documents waste space in command output.
- Modal compositing obscures the durable transcript.
- Gradients, animation, and decorative backgrounds add noise to operational
  state and have unreliable contrast across terminal themes.
- Rounded borders around every section flatten hierarchy rather than improve
  it; reserve a border for a decision that genuinely interrupts the flow.
- Background detection enters a more complex terminal interaction than named
  ANSI colors require. The current palette uses standard terminal colors.

## Interactive chat

Interactive chat remains a durable human stream rather than a full-screen TUI.
It can also read lines through a scanner when enhanced input is unavailable.
Lip Gloss is used for bounded presentation jobs:

- trusted semantic labels (`YOU`, `AGENT`, tool activity, governance routes,
  approvals, failures, and working state) use the shared palette;
- slash hints and prompt cursor positions use terminal-cell width rather than
  rune counts, so wide and combining characters do not corrupt redraws.
- rich startup leads with agent identity, subdued context, scoped model posture,
  and compact tool information rather than a command dump;
- `/help` renders the grouped command reference on demand;
- static native approval/question details use a callout above the prompt,
  without taking over input handling or truncating consent information.

Rich startup includes a short message/help/exit hint. Plain/scanner output keeps
its line-oriented status report. Rich exit notices distinguish explicit exit,
EOF, and cancellation; plain exits retain their existing status record. Working
feedback covers connection, posture checks, waiting tasks, and decision
continuation. The spinner resumes after tool activity only where a transient
row can be cleared safely, and stops for final states and pending approvals.

The existing chat state machine remains authoritative for:

- raw mode and terminal restoration;
- cursor clearing and prompt redraw;
- serialized spinner, prompt, and stream writes;
- assistant grouping and the `  | ` response rail;
- four-space trusted operation labels and six-space fields;
- control-sequence sanitization before styling;
- tool correlation, governance provenance, and HITL prompts.

Those indentation levels are not DIY decoration to replace with a snapshot
tree: they stop model-authored text from occupying renderer-owned provenance
positions. Lip Gloss styling is applied only after dynamic text is sanitized.
Orka chat is always a session, including with scanner input. Retired `--json`
is refused before application loading; there is no raw A2A JSON equivalent for
an existing Agent.

`NO_COLOR` keeps static rich layout on a capable terminal, but chat separately
disables cursor effects and enhanced input under it. `TERM=dumb`, non-terminal
input/output, or unavailable raw mode also use scanner input. Enhanced editing
tracks physical rows and grapheme widths, retains native approval/question
prompts, bounds escape-sequence waits, and restores raw mode on exit. Spinner
clearing is restricted to transient output, not durable response lines.
Resize during enhanced input aborts chat without submitting the current message
or approval and restores terminal settings. It avoids stale-coordinate erasure;
live resize/reflow is not implemented.

Long input uses a marked editing viewport, then emits the complete sanitized
submission into the durable transcript. Cancelling or resizing never submits
the hidden portion. Native confirmation batches reject missing or duplicate
identifiers and refuse arguments larger than the inspectable limit before
asking for a decision; no truncated preview can authorize an unseen call.

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
- Chat keeps received session IDs on stream failures and clears retry history
  after a validated resume. Native HITL refuses malformed, incomplete, duplicate,
  mixed question/approval, or oversized argument requests before sending a
  decision. Each batch call requires a decision; arguments within the 16 KiB
  per-call inspection limit are shown without truncation. Questions preserve
  free text and validate single/multiple choices rather than splitting every
  answer on commas.
- Session lists now decode `agent_id`, distinguish empty/null lists from invalid
  shapes, and share the active renderer with history. Actor/prompt boundaries
  close correctly around replay. Ordinary tool events with IDs are deduplicated
  by event content for display in streams/history; changed payloads and different
  IDs remain visible. The uncolored startup header includes the selected context
  and groups commands on capable terminals.

Still unimplemented: a comprehensive presentation pass over uncommon surviving
operator paths, side-by-side status panels, and
positive per-call governance receipts in chat. Existing chat route labels attest startup configuration, not enforcement
receipts. Unit/fake-service and Linux PTY tests cover these changes; they are not
evidence of a new live kind/AKS deployment or every terminal/platform combination.

Residual policies: broad one-shot **legacy-runtime** chat transport retries are unchanged
(up to three retries for matching connection refusal, EOF, or reset). Ambiguous
disconnects can repeat effects, including with an explicit one-shot session.
`kmx quickstart` no longer shares that policy: the Orka path creates one Task and
polls its result without resubmitting.
Question-only resampling remains at most twice under its existing exclusions.
Interactive `/retry` resends a message explicitly, not exactly once. History still
skips malformed event data and limits verbose payload display; replay deduplication
is not execution deduplication or a complete audit trail. Resize handling is a
safe input abort, not live reflow. These are scoped fixes, not an all-clear audit.

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
