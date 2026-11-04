# Machine-readable output

`kmx version -o json` reports the binary's supported machine interfaces without
loading configuration, inspecting a cluster, accessing credentials or using the
network. Text remains the default. The native Windows version command supports
the same JSON interface without enabling deferred Windows operations.

```sh
kmx version -o json
```

A successful invocation emits one UTF-8 JSON object and a newline:

```json
{"schemaVersion":1,"kind":"kmx.version","kmxVersion":"v0.9.0-rc.6","orkaVersion":"v0.3.0","capabilities":[{"operation":"version","output":"json","schemaVersion":1},{"operation":"cluster.up","output":"jsonl","schemaVersion":1},{"operation":"cluster.orka.install","output":"jsonl","schemaVersion":1},{"operation":"file.test","output":"jsonl","schemaVersion":1,"runner":"embedded"},{"operation":"file.test","output":"jsonl","schemaVersion":1,"runner":"copilotd"}]}
```

The version values above are examples. `kmxVersion` uses the build's resolved
version, without the text report's provenance suffix. `orkaVersion` is the Orka
version this binary targets, not a running controller's observed version.
Capabilities describe implemented binary interfaces, not dependency availability,
cluster readiness, permission, model access or qualified execution.

Unix builds advertise version JSON, two setup JSONL adapters and `file.test`
JSONL with separate `runner: embedded` and `runner: copilotd` selectors. Native
Windows advertises version JSON and only `file.test`/`runner: embedded`/`model: fake`;
its existing package-free/tool-free fake intake gate still applies. Setup and
native copilotd remain deferred there. Existing Run, Test, list, show, deployment and other JSON
formats keep their current shapes and exit meanings. This contract does not
wrap or replace those documents.

## Schema compatibility

`schemaVersion` is the integer wire major, independent of the kmx release, agent
source formats and evidence receipts. Readers accept major 1, ignore unknown
optional object fields and reject unsupported majors or record kinds. Additive
optional fields and capability entries are compatible; removing fields or
changing their types or meanings requires a new major.

Match the capability's exact operation, output and schema version before using
it. Match any operation-specific selectors as well: `file.test` entries specify
`runner`, and native Windows additionally restricts `model` to `fake`. Matching
only the three common fields does not establish support for another runner or
model. An unspecified runner is not a wildcard. Do not infer a capability by
parsing human version text or interpreting an Orka pin as runtime readiness.

## Version failures

An explicitly selected version JSON invocation emits one safe `kmx.result`
object for argument, flag or context-preprocessing errors, with no preceding
progress lines. For example, `kmx version -o json unexpected` exits 1 and emits:

```json
{"schemaVersion":1,"kind":"kmx.result","operation":"version","operationId":"00000000-0000-4000-8000-000000000001","seq":1,"elapsedMs":0,"outcome":"failed","verdict":"not_applicable","exitCode":1,"error":{"code":"invalid_arguments","message":"The command arguments are invalid.","recovery":"check_inputs"}}
```

The UUID is a synthetic example; real invocation IDs and elapsed times vary.
`operationId` is invocation correlation only,
not a resumable handle, an idempotency key or permission to replay. `seq` is 1
for this single terminal result. `elapsedMs` is a nonnegative integer measured
by kmx's monotonic clock since invocation start. `exitCode` preserves the CLI's
existing error mapping; consumers must compare it with the actual process exit.
There are no resource effects or remote-execution claims on a version result.

Early error framing recognizes exactly one output option spelled `-o json`,
`-ojson`, `--output json` or `--output=json` after the version command, with the
existing context and container-engine flag operands respected. Nothing after
`--` selects machine output. Repeated output flags, combined short-option
clusters and ambiguous forms retain ordinary Cobra handling without this early
error-framing guarantee. Unknown commands are not a root-wide JSON protocol.
Help and completion remain human/inert output, even with an output flag.

Machine errors contain fixed code/message/recovery fields and an optional
allowlisted stage; they never serialize the original cause or an arbitrary
provider details object. Error text and stderr omit reflected arguments,
credentials, headers, URLs, payloads, tool inputs/results and provider bodies.
Clients should use codes, not parse the message prose.

The shared safe-error vocabulary is:

| Code | Recovery |
|---|---|
| `invalid_arguments` | `check_inputs` |
| `configuration_invalid` | `check_configuration` |
| `unsupported` | `check_inputs` |
| `confirmation_required` | `confirm_target` |
| `credential_unavailable` | `supply_credentials` |
| `permission_denied` | `inspect_target` |
| `dependency_unavailable` | `check_configuration` |
| `operation_failed` | `inspect_target` |
| `execution_unknown` | `inspect_execution` |
| `cancelled` | `inspect_execution` |
| `deadline_exceeded` | `inspect_execution` |
| `output_failed` | `none` |

Only `invalid_arguments` and `output_failed` are used by version. Other codes
are shared vocabulary, not claims that additional command adapters exist.
Recovery is guidance, never automatic retry authorization. Unknown internal
classifications fall back to `operation_failed`; untyped causes are not
classified by substring matching their text. The narrow discovery exception below
classifies only fixed, version-pinned kubectl diagnostic-line prefixes; it never
reflects diagnostic bodies or grants API evidence.

## Provisioning event streams

Use `kmx cluster up -o jsonl` or `kmx cluster orka install -o jsonl` to receive
newline-delimited events and one terminal result. Their `--output` / `-o` accepts
`text|jsonl`, default `text`; `json` is not a setup format. Existing confirmation
and execution rules still apply. The adapter detaches stdin and suppresses raw
App/child stdout, stderr and command echoes. It never drives a hidden prompt.
Supply existing scoped consent through `KAIMAHI_CONFIRM` and existing environment
configuration, not credential flags or prompt input. Keyed Provider creation is
not added. Missing consent uses the existing nonterminal refusal.

Every record has `schemaVersion`, `kind`, `operation`, fresh UUID `operationId`,
contiguous one-based `seq` and monotonic nonnegative `elapsedMs`. Operations are
`cluster.up` and `cluster.orka.install`; kinds are `kmx.event` and `kmx.result`.
Events are `stage.started` and `stage.finished`; finished status is `complete`,
`failed`, `cancelled` or an actually observed `skipped`. A result may close an
interrupted stage without a finish event. Preflight covers argument/configuration
and adapter preparation only. Early parse failures emit its pair on unwind;
successful help/completion stays human output with no event prefix. This early
framing guarantee accepts one explicit `-o jsonl`, `-ojsonl`, `--output jsonl` or
`--output=jsonl` after the full command route, respecting known flag operands and
`--`. Other ambiguous or combined forms keep ordinary Cobra error behavior;
a valid selection can still start a stream at handler entry. Handler-owned
validation is unchanged: an invalid step can return `operation_failed` after
adapter preflight completes, without any stage or mutation.

`cluster.up` observes the selected existing `cluster`, `ollama`, `model`, `orka`
phases in that order. A single `--step` emits only that phase; offline node-only
up retains its existing `cluster` callback. Inter-phase Orka checks have no
invented stage. With stdin detached, interactive model reuse is not selected.

Online Orka install maps installer fetch to `installer`, chart apply or dry-run
validation to `runtime`, reader provisioning to `result_reader`, and keyless
Provider wiring to `provider`. `--provider -` omits provider. Unknown observed
phase labels become `other`, never raw labels. Dry-run has installer/runtime
validation phases, not readiness. Online `--no-apply` has no internal phases:
only preflight and result, with `effects: []`; complete means its fetch and
verification branch completed, not an installed runtime.

Offline Orka install has no internal phase callbacks in any mode, so it emits
only preflight and result. Its one coarse `runtime` effect covers the existing
whole handler, including reader work, not invented per-resource observations.
Unlike online no-apply's empty effects, offline no-apply records that coarse
entry as `completed` / `none` on success (`started` / `none` on failure).

Setup results include `mode: apply|no_apply|dry_run`, `outcome: complete|failed|cancelled`,
`verdict: not_applicable`, `exitCode`, ordered `effects`, and optional safe `error`.
An effect has `stage`, `attempt: not_started|started|completed`, and
`mutation: none|possible|acknowledged`. Mutating stages become possible before
entry; even successful completion remains possible without granular write
acknowledgment. No-apply/dry-run cluster mutation is none; tool/cache downloads
are outside this cluster-mutation statement. Effects are not an exhaustive
inventory, proof of creation, ownership or authority to delete, adopt or replay.
Failures before accepted options use default apply mode and no selected effects.

SIGINT/SIGTERM cooperatively cancel the invocation context before config/work;
writer errors cancel that same context and stop later phases/sends. Direct
children are killed and waited by existing CommandContext, not a remote workload
or guaranteed descendant tree. Handled setup cancellation retains exit 1, not
130. Untyped consent/dependency failures use fixed `operation_failed`; config
load failures use `configuration_invalid`, Windows refusal `unsupported`, and
context errors `cancelled` or `deadline_exceeded`. Writer failure takes precedence
over consequent cancellation. No rollback, resubmission or new cancel endpoint.

Consumers ignore unknown progress names/fields, display new stages generically,
and reject unknown majors, operations, record kinds or terminal outcomes. Require
exactly one final result with matching operation/UUID, contiguous seq and actual
process exit. Truncated/missing result, sequence gaps, malformed bytes or bytes
after the result mean incomplete delivery, never success or permission to retry.
Progress alone cannot establish readiness; retain possible effects for read-only
inspection. Legacy command JSON documents are unchanged.

## File Test JSONL

```sh
kmx test reader.agent.md -o jsonl
kmx test reader.agent.md --runner copilotd --host ws://127.0.0.1:8080 -o jsonl
```

This opt-in format keeps the existing runner choice, intake, model/tool/judge
validation and timeout budgets. Embedded remains default; format selection does
not require copilotd. Run and folder Test still support their existing formats,
not JSONL. `--github-summary` still requires JSON and refuses JSONL before work.
Text and the existing single-document `-o json` remain unchanged.

The operation is `file.test`. After the full `test FILE` route, the four explicit
selectors `-o jsonl`, `-ojsonl`, `--output jsonl` and `--output=jsonl` guarantee safe
early-error framing when exactly one output selector occurs. Known flag operands
and `--` are respected. FILE detection is syntactic, without reading its content
or guessing an extension; a directory operand is an attempted file intake and
gets a framed refusal. Other Cobra-valid forms may stream at handler entry but
do not acquire the early-error guarantee. Successful help/completion stays inert.

The shared envelope and checked-write limits apply. Progress names include
`stage.started`, `stage.finished`, `case.started`, `session.observed`,
`tool.started`, `tool.finished` and `case.finished`. Stages are `preflight`,
`execution`, `assertions` and `finalize`; execution includes existing interleaved
judging, while assertions covers existing App acceptance/projection, not another
judge call. Case/runtime progress is provisional. No raw route or Session notice,
answer, assertion operand/reason, path, host, tool payload or expanded verbose
content is copied into JSONL. Provider/judge credentials remain in their existing
channels; dynamic identities are checked against both frozen credential owners.
Unsafe or over-1024-byte identities are omitted/unavailable, never clipped.

Ordered `case.summary` events immediately precede the terminal result, one per
selected case including not-started cases (maximum 128). `caseIndex` is one-based;
`caseId` is optional. Each summary has `state`, `verdict`, nullable `durationMs`,
`session`, `chat`, `turn`, `task`, `calls`, `usage` and separate `judgeUsage`.
Identity fields have `availability` and an optional checked `id`; Task remains
unavailable because native file execution uses AHP Session/chat, not Orka Tasks.
Tool names are observed names, and call IDs are local opaque ordinals within the
case, never raw provider IDs. Session requires create acknowledgment; turn
requires a matching accepted server echo, not just local nonce assignment.

Usage counters are nullable optional nonnegative `inputTokens`/`outputTokens`.
Unavailable is not zero: explicit reported zero is available. Model-only reports
are `not_reported`; more than one matching usage report is `ambiguous`, never
summed or replaced by the last report. Tool-using hosts may report once per model
call, so ambiguity can be common. Usage is correlated at the receive-side
channel/turn/sequence/model boundary, not read from an overwriting final snapshot.
Negative counters are withheld without changing execution acceptance. Judge
usage is separately unavailable; no aggregate token total or cost is invented.

Tool progress may wait until matching actual-model evidence arrives, possibly
at turn end. At most 512 payload-free observations are buffered per case; overflow
withholds live tool observations without refusing execution or changing final
trace acceptance. Native `calls` requested/completed counts are available only
from an accepted final complete trace; failed/truncated traces say
`incomplete_trace` with null counts. Embedded exposes only its checked final
counts; private Session/journal internals and usage are not inspected.

The result includes runner (omitted before option admission), selection,
`evidence: diagnostic_preview`, checked
revisions, selected/started/completed case counts, `summaryCount` and `remoteState`.
Counts are zero before selection. No setup `effects` array is added. Existing
exit 0/1/2/130 meanings remain authoritative; assertion failure differs from an
unknown execution/verdict. Source/judge/journal/required-close vetoes remain
final; prior case progress or a passing summary cannot override an unknown result.
Name-based native selection is diagnostic preview, never package identity proof.

`remoteState` is `not_started`, `stopped`, `may_continue` or `unknown`. A send
attempt is recorded before Dispatch: failed/lost acknowledgment is `may_continue`,
never proof of not-started. Stopped requires all started turns' accepted terminal
proof. Cooperative cancellation/disposal acknowledgment alone does not establish
remote stop, and redaction cannot erase the attempted-send fact. Signals retain
the existing post-intake timing; writer failure cancels the existing file context,
wins over resulting cancellation, and exits 1 when the write error returns.

## Reading the complete output

Read stdout separately from stderr, parse the entire document through EOF and
require a final newline. Each machine record is at most 64 KiB including that
newline. A valid JSON prefix followed by another object, malformed bytes or a
truncated document is not a successful response.

A write error returned to the CLI exits 1 with a fixed safe diagnostic. A
partial write is not retried and cannot promise a second error record on the
broken output channel. Broken stdout may instead terminate the process with
SIGPIPE before a write error returns; no diagnostic or result is guaranteed in
that case. Hard kill, crash and transport loss may likewise prevent a terminal
document.
Treat missing output or disagreement between a result and process exit as an
incomplete operation, not success. Never infer absence of effects or blindly
repeat another mutating operation from a disconnected process.

See the [CLI output contracts](kmx.md#output-contracts) for existing formats.

## Destination discovery

`kmx cluster discover [-o text|jsonl]` defaults to text. Without context it projects
only local labels; with explicit `--context CTX` it lists namespaces using GETs.
Version capability entries have operation `cluster.discover`, output `jsonl`,
schemaVersion `1`, and exact scope `contexts` or `namespaces`; `target` is unavailable.

JSONL emits one terminal `kmx.result` with operation `cluster.discover`, normal
UUID/sequence/elapsed fields, verdict `not_applicable`, and a `discovery` object:

```json
{"scope":"namespaces","complete":true,"checks":[],"rows":[{"context":"dev","namespace":"demo","uid":"observed-namespace-uid"}]}
```

Context rows instead contain `context` and boolean `current` (including false).
`checks` and `rows` are arrays even when empty. Complete means bounded reads
finished, not runtime readiness or model proof; complete results exit 0. Incomplete
results retain fitting safe rows, `complete:false`, and fixed checks of shape
`{scope,state:"unknown",reason}`, with failed outcome/exit 1. Cancellation uses
outcome/code `cancelled`; deadlines use failed outcome/code `deadline_exceeded`.
Output failure uses `output_failed` and never retries a partial terminal write.
Read through EOF and match the actual process exit. Abrupt termination may leave
no terminal record and never proves a complete observation or no effects.

Diagnostic hints recognize only case-sensitive line-start markers with a following
colon, whitespace or end boundary: `Error from server (Forbidden)` → `read_denied`
(`permission_denied`); `error: You must be logged in to the server` → `unauthorized`
(`credential_unavailable`); `Unable to connect` → `network_unavailable`
(`dependency_unavailable`). NotFound on these lists is `read_unknown`, never resource
absence. Unknown tokens, conflicts, truncation and lookalikes stay unknown. Raw
bodies never leave capture. Hints are not authenticated API status or retry
permission; connection-refused or credential-helper wording may stay unknown or
be a coarse network hint. Real formatting fixtures are version-labelled.

Kubernetes `get --raw` is used only for fixed, context-pinned GET paths with encoded
pagination tokens, never kubeconfig `config view --raw`. Local projection contains
no server/user/credential fields. Rows are safe identities, not full API documents.
Malformed data uses `malformed`; bounds and ambiguous lists use `read_unknown`,
with existing safe `operation_failed`. Unsafe explicit context uses
`invalid_arguments` before reads. Unsafe/secret-shaped labels are omitted with
explicit incompleteness, never redacted into a different selectable name.

The adapter begins inside RunE: valid JSONL target-scope refusals are in-band;
Cobra operand/flag/preprocessing errors retain their ordinary contract. Successful
help/completion is inert and emits no stream or dynamic discovery reads.
