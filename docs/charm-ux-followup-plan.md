# Charm UX follow-up

Status: implemented; native Orka field collection and local/PTY verification
are integrated. Runtime orchestration remains outside the wizard.

## Decision

Prototype Bubble Tea and Bubbles in `kmx agent create`, the smallest complete
interactive workflow in the CLI. The implementation uses
`charm.land/bubbletea/v2` v2.0.9 with the Bubbles v2 `textinput`, `help`, and
`key` packages. It is inline rather than alternate-screen, reads from the
application's stdin, writes only to its operator-facing stderr, and runs only
when both are real terminals under the command's existing eligibility rule.

The model collects missing description, name, namespace, Provider type, model
identifier and existing Secret name, then offers the explicit Apply/Cancel
choice. Namespace and model have no kagent defaults or preset discovery.
Supplied native flags remain authoritative. Executable `--task` also collects a
missing result-reader ServiceAccount and states its authority at confirmation;
offline/dry-run never asks for that account. Optional custom/local endpoints
stay in `--base-url`, explained visibly; no endpoint is inferred from a model.

Name/reference errors remain visible in the form. Both collectors share final
Orka option validation before confirmation or completion, including explicit
rate limits and refusal of legacy server:tool syntax. Validation can read a
supplied instructions file locally; there are no subprocesses or cluster calls.
Credential-shaped input is refused before a derived-name echo. The program
exits completely before `CreateAgent` writes a manifest, validates schemas,
resolves cluster state, runs a guard, or invokes kubectl.

Generated name defaults are capped at 32 characters rather than consuming the
full Kubernetes name allowance. Short descriptions keep their readable slug.
Long descriptions retain complete opening words plus a stable six-hex suffix,
so similar descriptions do not silently receive the same default. Explicit
names still retain the normal 63-character Kubernetes limit.

Enter selects Apply intentionally. Arrow keys or Tab expose and select Cancel;
Escape and Ctrl-C cancel without writing or applying. `--no-apply`, `--out -`,
and `--dry-run` do not show an unnecessary confirmation. The existing scanner
collector remains as a small fallback and unit-level contract for completion
semantics, but real eligible terminal use goes through Bubble Tea.

`TERM=dumb` retains the linear prompt instead of starting a terminal renderer.
Bubble Tea keeps its signal handler so OS-level interruption restores terminal
state; interruption is treated as cancellation, never partial completion. The
form uses the same cyan/magenta/red visual language as `cliui`. Flag-derived
values are stripped of terminal controls and flattened before confirmation,
while the underlying options remain unchanged. Descriptions are not silently
length-limited; explicit Agent names retain their 63-character bound. Native
Orka instructions preserve supplied text or file inputs. The removed BYO image,
run-as-user and isolation flags are not restored by this UI.

## Research boundary

The implementation was checked against the Bubble Tea v2.0.9 examples for
forms, help/key bindings, `tea.KeyPressMsg`, `tea.NewView`, and
`tea.WithInput`/`tea.WithOutput`. Bubbles v2.2.1 itself requires Bubble Tea
v2.0.9 and the repository's existing Lip Gloss v2.0.6, avoiding a split Charm
stack.

Do not extend this prototype to these surfaces:

- **Chat partial integration:** reject it. The chat path already has streaming,
  history, slash commands, native approval prompts, resize handling, and PTY
  restoration as one terminal protocol. Replacing only its input widget would
  create two renderers and two terminal owners.
- **Approvals:** keep the native, fail-closed confirmation whose exact context
  and default are security behavior, not presentation.
- **Model credentials:** never put secret material in a Tea model, message,
  view, or transcript. Preserve the existing model credential paths; tool
  capture is retired.
- **Guards:** context guards are mutation boundaries and must remain available
  to non-TTY and redirected callers with their current refusal semantics.
- **Progress, status, and reports:** these already have destination-aware plain,
  rich, and structured output. A Tea renderer would weaken pipelines and saved
  transcripts rather than improve them.
- **Runtime orchestration:** keep guards, progress, subprocesses and durable
  output outside the form. The retired blueprint runner is not a UI extension
  candidate.

Glamour may be useful later for trusted, local Markdown help or previews. Do
not render model output, remote content, instructions, or other untrusted text
as Markdown without a separate trust and terminal-escape decision.

## Verification

The model tests send messages directly and cover default derivation, required
description, inline name validation, authoritative supplied options, invalid
native references, explicit confirmation, Apply-by-default, and both cancellation
keys. They also cover long descriptions, invalid supplied names/options,
credential refusal before derived-name echo, hostile terminal sequences in
output paths, and offline completion without discovery or a result account.
Linux PTY tests run the real program through Ctrl-C, Escape and SIGTERM at
missing-field prompts, checking cancellation and exact terminal-mode restoration
without manifest output. Native stdout completion is exercised through both
Bubbles and the `TERM=dumb` fallback. This is UI/local verification, not a new
live-runtime claim.
