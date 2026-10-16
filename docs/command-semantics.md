# Command alignment

Tracking issue: [#301](https://github.com/kaimahi-agents/kaimahi/issues/301).
The [CLI reference](kmx.md) documents the implemented interface and the
[command conventions](command-conventions.md) govern future changes. This page
distinguishes implemented alignment from proposed follow-ups. The implemented
alignment is available in v0.7.0; historical section names remain for stable links.

## Implemented entry-point alignment (unreleased)

| Command | Contract |
|---|---|
| `kmx ctx [--check]` | List named workspace destinations only; optional bounded local-context/anonymous registry checks. No selection or switching. `targets` remains runtime capabilities. |
| `kmx context show` | Read the effective context, selection source and posture. |
| `kmx context use <context>` | Remember KMX's access selection after the existing context guard; never change kubectl current-context. |
| `kmx local up` | Provision the local kind environment, model and Orka; no agent. The existing `--step` choices remain. |
| `kmx local down` | Delete the named local kind cluster, including its data, under the existing context/container-engine guard. |
| `kmx aks up/down` | Retain the guarded AKS provisioning/teardown operations and historical ownership records; hidden from root help and completion. |

Bare `context` and `local` display help. Canonical commands use the existing
operations, settings, saved-selection file and guards. Root `up`, `down`,
the AKS `lift` alias and `lift down` are removed outright, with no compatibility aliases or
retirement stubs. The old context-selecting `ctx` alias remains removed; the new
`ctx` is a list-only named-destination view and refuses legacy switching syntax. Use `context show/use`, `local up/down` and `aks up/down`
respectively. Tagged v0.4.1 used the old context/local spellings; the grouped
commands require a newer build.

The redirected context report retains its `source: kmx ctx` provenance label
for saved selections as a compatibility format. That label is not a next-step
command. Generated selection and local teardown instructions use the canonical
spellings. Invocation-specific confirmation preserves the caller's selectors
and exact arguments.

Generated local commands preserve `KIND_CLUSTER`, `CONTAINER_ENGINE` and
`--context`. AKS retry/teardown instructions use the common effective-option
renderer, including `--byo` only for the existing ownership mode; Azure setup
and cleanup diagnostics say AKS rather than agent Lift. The carried scripts
use these native instructions when invoked by KMX. Standalone script use has
no KMX ownership receipt, so its fallback names the actual script with quoted
arguments instead of a deleted Make target or an invented BYO recovery route.

## Implemented workflow command mapping (unreleased)

| Previous route | Current route | Contract |
|---|---|---|
| `agent create [name]` / `create --live` | Removed | No live resource/bundle creation, previews or wizard. Root `create NAME` authors native workspace source offline. |
| `agent chat <name> [message...]` | `chat <name> [message...]` | Interactive Orka session, not workspace execution. |
| `agent run [<bundle-dir>]` | `run --agent NAME` | Bundle mode removed; independent live Orka Task/answer/result contracts retained. Suite-owned Agents refuse legacy invocation. |
| `agent show <name>` / `agent list` | `orka show <name>` / `orka list` | Live Orka detail/inventory, not workspace inspection. Show still requires namespace. |
| `agent evaluate/lift/retire/status` / `bundle ...` | Removed | Old commands/readers/writers/receipts removed outright, no stubs or migration. No legacy-status reader or retirement replacement. |
| `agent verify` / `bundle verify` | Removed | Legacy replay/judges and RecordedExecution are removed without a native replacement. |
| `agent eval diff <before.json> <after.json>` | `eval diff <before.json> <after.json>` | Offline receipt comparison, not evaluation execution. |
| `agent eval from-sessions <session-uid>...` | `eval from-sessions <session-uid>...` | Private reference-case preparation, not execution. |
| `suite push --workspace DIR REF` / `suite push REF` | `push [--workspace DIR] [REF]` | Exact prepared workspace graph publication; discovers the nearest ancestor `workspace.yaml`, or selects an exact root without fallback with `--workspace`. |

The entire `kmx agent` group is removed, including its hidden edit stub, with no
aliases or retirement stubs. Use kubectl to edit live Orka resources. Root
`status --to NAME` separately inventories deployed Orka/kagent Agents and per-object
Lift provenance with GET-only table/JSON reports; `orka status` retains the table-only
runtime report. There is no legacy-status reader or retirement replacement. Root `eval` executes a full native workspace inventory with explicit
target selection, optional private output and exactly one runtime-derived model binding: Orka
`--inference` or packaged kagent `--bindings`. Standalone packaged-reference
evaluation remains unimplemented. Root digest `lift` deploys a packaged native
suite with explicit private evidence, GET-only `--plan`, strict guarded apply and
explicit source-only `--promote`; no model/runtime override, update or adoption. `task result` and
`suite push DIRECTORY REFERENCE`, `suite build/diff/pull/validate` are unchanged.
`suite policy FILE [--administrative-policy FILE]` resolves portable policy
offline and prints canonical effective-policy bytes plus their digest; it does
not authenticate, authorize, select or invoke an enforcement provider. See the
[portable policy contract](portable-policy.md).
These routes and workspace authoring/publication are available in v0.7.0;
historical v0.5.1 retains the `agent` routes. Existing operation handlers, arguments, applicable flags,
stdout and exit contracts are preserved; this is not a receipt or bundle migration.

## Independent evaluation helpers

`eval export [--workspace DIR] --member ID --out FILE` writes detached public
AgentSuite evidence from the latest completed exact-suite managed evaluation.
It runs offline with explicit member matching, strict public-library verification,
new-file publication and no cluster/model/registry calls. Fresh captured Go build
provenance requires a tagged module version plus module checksum, or full VCS
revision plus explicit unmodified state. Modified producers, unstamped development
builds, pseudo-versions without a clean revision and legacy metadata refuse.
Private receipts and push/Lift authority remain unchanged.
See [public evidence](suite-workspace.md#export-portable-evaluation-evidence).

`eval verify --suite LAYOUT --evidence FILE --expect FILE [-o text|json]` is the
state-independent offline public-evidence CI gate. Digest-form `--suite sha256:…`
requires `--from-layout DIR`; no registry/cache search or workspace preparation.
The complete ExpectedEvaluation file is independently reviewed and committed
beside the suite, never populated from evidence. Success-only stdout summary
exits0; stable safe refusal codes on stderr exit1. No private receipt authority,
configuration, model, cluster or KMX state access. See [CI verification](suite-workspace.md#verify-portable-evaluation-evidence-in-ci).

Offline `eval diff` compares strict full-passing native Orka/kagent suite evidence
only; old normalized Orka/kagent/AgentSessions bundle receipts are unreadable.
Zero is comparison, not a new pass or promotion gate.

`eval from-sessions UID... --sessions SOURCE --assertions CASE.yaml --out FILE`
remains private read-only reference extraction/export, not execution. Existing
output appends atomically; duplicates refuse. No recorded payloads are persisted.
RecordedExecution, legacy bundle Sessions evaluation/replay/judges and their
consumers are removed. There is no native reference-manifest consumer; author
native workspace cases. See [reference extraction](kmx.md#reference-extraction).

`eval cleanup --to NAME` lists candidates read-only. `eval cleanup RUN --to NAME`
previews one full run's frozen identities, confirms even on local kind and performs
UID/resourceVersion-safe cleanup. Explicit context/namespace needs no workspace or
inference. Missing Task/Job anchors remain uncertain, never guessed from labels;
there is no automatic sweep, lease, local journal or later-publication blockade.

## Named workspace destinations

Optional workspace-root `kmx.yaml` supplies selectors to existing `eval/lift --to NAME`.
Individual eval may use its explicitly saved `evalTarget`; digest Lift has no
implicit destination. Resolve once to concrete values, print their source, then
pass those copies through the operation. Editing config during confirmation
cannot redirect an invocation. Source, runtime, binding-byte and remote-object
checks remain independent. Explicit invocation flags override corresponding saved
fields, including an explicit false transport flag. Config is not packaged or
included in source identity. Explicit digest Lift without `--to` never discovers a
workspace. See [configuration and checks](suite-workspace.md#named-destinations).

No-reference `kmx lift --to NAME` composes missing Orka workspace eval, push and
Lift. It requires saved publishRepository and destination evaluationTarget, freezes
one source/selector plan, automatically discloses promotion, and confirms that plan
once. Four default stage lines retain skips; verbose/JSON carries details. Private
exact-version completion observations permit only verified GET-only skips, never
Apply, adoption, replacement or repair. Explicit-reference Lift remains digest/private
evidence based; eval --out and the push reference remain optional. Kagent stays
standalone until candidate-scoped evaluation/cleanup is qualified. See
[composed Lift](suite-workspace.md#composed-workspace-lift).

## Offline suite workspace

Single-member [explicit runtime profiles](suite-workspace.md#explicit-runtime-profiles-offline-only)
use source v3. Root push accepts `--runtime-profile ID [--platform OS/ARCH]`
only for offline v3 `--unevaluated` transfer; all profiles remain in S. Suite build
uses the same selectors for exact planning, but AgentKit refuses profiled plans.
Eval/Lift expose neither flag. Profiled execution, private/public native export
and evaluated publication remain unsupported; external portable eval verify is
an independent consistency check, not private proof authority.

`create <name> --model <model> --instructions <file> [--workspace <directory>]`
authors one tool-free native-Orka member offline by default. Model and an
instructions file are required; description is optional. Create and push share
nearest-ancestor workspace discovery. An exact `--workspace` override initializes
a missing workspace without a prompt. Without a workspace, terminal create asks
`Start one here? [Y/n]`; nonterminal or redirected use refuses with an explicit
`--workspace` hint. Invalid nearest markers refuse without fallback; an existing
one-member workspace refuses a second member without changing source.

Live create/previews/wizard and live-only flags are removed outright, unknown
before config. No runtime/model bootstrap belongs in workspace create.
Help/completion is inert; authoring needs no cluster, builder or model call.

`push [reference]` discovers the nearest ancestor `workspace.yaml`;
`--workspace <directory>` instead selects that exact directory, without fallback.
Missing, invalid or unreadable workspace selection refuses. The command prepares
that member and its shareable authored cases as an ordinary AgentSuite, then
copies the exact OCI manifest/blob graph. A single stderr line names the absolute
workspace and prepared OCI manifest digest; result stdout is unchanged. Native
member image requirements are empty. Registry auth, plain-HTTP and non-atomic tag
replacement remain as documented. Old `suite push <directory> <reference>` retains
extracted-artifact transfer behavior without workspace discovery; suite push no
longer accepts `--workspace` or one positional reference. Root push refuses two
positional arguments.
Root `eval --to-context CTX [--out FILE]` performs offline workspace/inventory/
managed-proof and optional output preparation before configuration, then one guarded runtime-specific
placement and one fresh Task per declared run of each ordered case (default one).
Optional KMX YAML `runs`/`minPasses` counts completed attributable assertion
verdicts; omitted threshold requires all runs. Each run has its own deadline;
repeat preflight states the total run-time budget, excluding placement. There is
no early success; any unknown or execution error aborts. AgentSessions development
evaluation uses fresh Session/config/Exec per run under the same policy. Field names/defaults follow
[AgentSuite §8.2.2](https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions):
`minPasses` requires `runs`, and both are positive integers. KMX's private YAML
and receipts remain separate from the spec's embedded JSON format.
Public Orka uses fresh S/run candidates and exports only after verified root/child
cleanup and final fences. Startup reports without deleting or blocking a fresh
eval. Private Quickstart/console stay canonical. Orka requires
`--inference provider:NAME`; packaged exact-v0.10.3 Go kagent requires
author-owned `--bindings FILE`, unsecure auth and no `--result-port`. Only full
passing metadata is registered in private KMX state; --out optionally also exports
a standalone private file outside workspace/cache. Failure, unknown, drift,
cancellation or output error stops later sends. No
package reference, subset or content output is accepted. Authored `llmJudge`
requires explicit `--judge-base-url`, optional `--judge-key-env` and authored
model/version/threshold. Two independent samples must agree on the threshold
side; errors/splits stay unknown. Judges use the fixed modern Chat Completions
request shape: a 1024
`max_completion_tokens` budget, no temperature override and no error fallback.
Evidence binds that style; old judge receipts require fresh evaluation.
Judgment shares the current run's deadline and is
followed by attribution rechecks. Passing rows bind judge identity, endpoint
configuration hash and both scores without reasoning or raw judge URLs; recovery
commands omit the endpoint. Text-only receipt encoding is unchanged. Development
AgentSessions evaluation shares this judge transport, evidence and per-run deadline.
`push --evaluation FILE` checks runtime-specific private receipt coherence and
exact manifest S before publication, overriding managed lookup without fallback.
Root push otherwise requires the latest completed exact-S pass from private managed
state; --unevaluated explicitly opts out. Root eval's --out is optional. Explicit
push tags stay unchanged; only bare push uses saved `publishRepository:s-<full-S-hex>`. Receipts are
unsigned observations, not policy gates. Dedicated shards qualify bounded Orka
and kagent workspace scenarios; the native Orka inventory includes two text cases
and one open-ended rubric case sharing the pinned AIKit workload, with no timeout
increase. Kagent retains its two text cases. Hermetic runner tests are not live
judge proof; this is not general runtime support.
Root `lift REF@sha256:DIGEST` consumes that explicit private full-pass evidence
for source-free, deploy-only packaged Orka or exact-kagent placement. Bounded
qualification covers explicit Orka candidate-source promotion and kagent fresh-target creation, not every
promotion, creation or partial-outcome path. See [suite workspace](suite-workspace.md).

## Vocabulary

**Decided: keep Create → Prove → Lift.** Root digest Lift deploys an exact
packaged native suite using private full-passing evidence, not old bundle state.
Chat `/lift` and console Lift only print
`Lift moved: kmx lift <ref>@<digest> --to-context … --evidence …`.
No release GUI or live-copy fallback remains. Standalone AKS up/down and internal
App.Lift cloud setup remain independent. Removal does not change AKS ownership,
recovery or teardown behavior. Ready/answered/evaluated are distinct evidence.

- **Context:** a kubeconfig access selection; not a durable cluster identity.
- **Target:** selected runtime, cluster identity, namespace and deployment
  destination, following [#238](https://github.com/kaimahi-agents/kaimahi/issues/238).
- **Runtime adapter:** KMX's integration with an execution platform.
- **Inference provider/backend:** an independent model-access route.
- **Provision** infrastructure; **install** runtime software; **create** source;
  **eval** full cases; **push** exact graph; **lift** an evaluated digest.
  Live Orka **show/list** and independent **run/chat** remain separate.
- **Ready**, **answered** and **evaluated** are different evidence.
  **Unknown** is not **absent**; **retry** resubmits; **result** only retrieves.
- Destination status is GET-only inspection; retirement remains unavailable.
  Lift does not transfer live state,
  sessions or memory, delete source, perform cutover or grant teardown authority.

## Audit and existing work

The command inventory is pinned by the command-tree and entry-point tests in
[`cmd/kmx`](../cmd/kmx). Context show/use split read and selection operations;
local up/down scope the existing kind operations. Root discovery hides the AKS
compatibility group, which remains directly callable. Removed root aliases are
neither registered nor suggested by help or completion. Help and internal
completion are not operational leaves.

Other implemented alignment in development builds includes
`quickstart --interactive` (`-i`) and intrinsically interactive root `chat`.
`quickstart-wizard` is a retirement stub, and chat no longer accepts
`--interactive`; tagged v0.4.1 still uses those older forms. `kmx targets` is the
read-only runtime capability view. Plane commands, administration, migration,
model overlays and the hello-to-governed demo are removed, not candidates for
regrouping. Current native setup does not deploy a model plane.

Sources: command constructors and completion in [`cmd/kmx`](../cmd/kmx), chat
registry/dispatch in [`chat_slash.go`](../internal/kmx/app/chat_slash.go) and
[`runtime_session.go`](../internal/kmx/app/runtime_session.go), console actions
in [`agent_tui.go`](../internal/kmx/app/agent_tui.go), current guides, Make and
embedded script recovery messages. This is a source/help audit, not a new
live-cluster proof.

The review identified mixed singular/plural groups, noun-only mutations,
inconsistent inspection verbs, `local` meaning cluster-configured inference,
different prompt/file flags, different bundle/live target flags, and preview
modes with different file/network/cluster effects. It also found stale status
formats, namespace claims, governance wording and deleted Make recovery targets.
These are tracked in #301 rather than silently treated as part of this slice.

| Related work | Ownership retained there |
|---|---|
| [#194](https://github.com/kaimahi-agents/kaimahi/issues/194), [#248](https://github.com/kaimahi-agents/kaimahi/issues/248) | Product story and lifecycle vocabulary. |
| [#223](https://github.com/kaimahi-agents/kaimahi/issues/223) | Cloud extraction, infrastructure ownership, recovery and teardown. |
| [#224](https://github.com/kaimahi-agents/kaimahi/issues/224), [draft #225](https://github.com/kaimahi-agents/kaimahi/pull/225) | Target identity, runtime selection, lifecycle contracts and extensions. |
| [#238](https://github.com/kaimahi-agents/kaimahi/issues/238), [#294](https://github.com/kaimahi-agents/kaimahi/issues/294) | Runtime qualification and supported operations. |
| [#289](https://github.com/kaimahi-agents/kaimahi/issues/289) | Read-only capability reporting, implemented as `kmx targets`. |
| [#276](https://github.com/kaimahi-agents/kaimahi/issues/276), [#286](https://github.com/kaimahi-agents/kaimahi/issues/286) | Shared operations and authoring/execution/adapter/UI extraction. |
| [#284](https://github.com/kaimahi-agents/kaimahi/issues/284) | Historical model-plane work; the plane is now removed. |
| [#292](https://github.com/kaimahi-agents/kaimahi/issues/292) | Verbosity levels and redaction. |
| [#299](https://github.com/kaimahi-agents/kaimahi/issues/299), [#300](https://github.com/kaimahi-agents/kaimahi/pull/300) | AgentSuite artifact and validation contracts; `suite validate` remains. |

Merged [#203](https://github.com/kaimahi-agents/kaimahi/pull/203) is the precedent
for separating AKS provisioning from agent lift. #301 owns command consistency
and compatibility over these operations, not parallel implementations.

## Proposed follow-ups — not current syntax

Interactive controls should share resource/action names: `/agent show`,
`/agent chat`, `/agent use`, `/agent tools configure`,
`/agent inference configure`, and console `/context use local|remote <context>`.
Interactive `/lift` and console Lift are retired move notices, not release
workflows. Do not add a grouped release GUI or live-copy fallback.
Use `/inference use cluster|host-copilot|host-foundry` to distinguish execution
routes, `/exit` with `/quit` compatibility, and `/verbose on|off` coordinated
with #292. Retain keyboard shortcuts and advertise only supported controls.

Proposed flag alignment: `--prompt`/`--prompt-file` instead of `--task`,
`--instructions-file` instead of file-valued `--instructions`. Keep report `--output` separate from artifact `--out`;
preserve raw and answer-only stdout.

## Remaining decisions

1. **Infrastructure hierarchy:** #223 owns the long-term Azure interface;
   `target aks` is not the chosen final shape. `aks up/down` remain callable
   compatibility routes, hidden from root help and completion.
2. **Runtime setup:** retain `orka install/status` pending agreement on a
   generic runtime setup family; a name cannot imply installer support.
3. **Capability view:** `kmx targets` is implemented for #289; retain that
   spelling and its read-only/offline-default contract rather than adding synonyms.
4. **Authoring/execution verbs:** default `create` now authors offline;
   live create is removed. Reconcile `evaluate` with `test`
   and root `run` with canvas-service `run start` through #276 and its
   authoring/execution work. Their scopes differ.
5. **Runtime selection:** settle operator/saved configuration versus public
   selection through #224/#238; retain current capabilities in rename slices.
6. **Target flags:** decide `--to-*` versus `--context/--namespace` with runtime-specific
   bindings, multi-target status, namespace ambiguity and alias conflicts.
7. **Preview terms:** distinguish artifact rendering, no-write planning and
   server validation, including downloads/cache/local files and which resources
   admission actually checks. Do not mechanically alias `--no-apply` modes.
8. **Remaining compatibility routes:** follow the announced breaking-release
   policy for future removals. The root context/local/AKS aliases are already
   removed outright; no retirement stubs remain. The hidden AKS group is retained.

## Keeping future work aligned

[`AGENTS.md`](../AGENTS.md) records the contributor rules alongside the
[command conventions](command-conventions.md). For each implemented
slice, update command registration, help, completion, interactive labels where
applicable, generated next/recovery commands, primary docs, compatibility tests
and CHANGELOG together. Use existing operation handlers. Keep historical records
and deliberate compatibility fixtures distinct from current examples.

Tests must cover more than names: read-only behavior, saved selection and refusal,
context/namespace/engine preservation, mode conflicts before operation loading,
stdout versus stderr, and operation result/exit compatibility. Help/completion
must not perform operational mutations. Doc-link/map and boundary checks should
include newly added files.
