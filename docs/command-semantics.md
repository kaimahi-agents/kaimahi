# Command alignment

Tracking issue: [#301](https://github.com/kaimahi-agents/kaimahi/issues/301).
The [CLI reference](kmx.md) documents the implemented interface and the
[command conventions](command-conventions.md) govern future changes. This page
distinguishes implemented alignment from proposed follow-ups.

## Implemented entry-point alignment (unreleased)

| Command | Contract |
|---|---|
| `kmx context show` | Read the effective context, selection source and posture. |
| `kmx context use <context>` | Remember KMX's access selection after the existing context guard; never change kubectl current-context. |
| `kmx local up` | Provision the local kind environment, model and Orka; no agent. The existing `--step` choices remain. |
| `kmx local down` | Delete the named local kind cluster, including its data, under the existing context/container-engine guard. |
| `kmx aks up/down` | Retain the guarded AKS provisioning/teardown operations and historical ownership records; hidden from root help and completion. |

Bare `context` and `local` display help. Canonical commands use the existing
operations, settings, saved-selection file and guards. Root `ctx`, `up`, `down`,
`lift` and `lift down` are removed outright, with no compatibility aliases or
retirement stubs. Use `context show/use`, `local up/down` and `aks up/down`
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

## Evaluation judge selectors (unreleased)

`kmx agent evaluate` accepts `--judge-base-url <url>` and optional
`--judge-key-env <env-name>` on both Orka and sessions paths. They select only
judge transport and the credential's environment-variable name, never a secret
value, judge model or version. Model/version, rubric and integer `minScore`
belong in explicit `llmJudge` assertions; there is no default endpoint/model or
agent-model fallback. Empty flags and unused judge configuration after `--case`
selection are refused before Task/session execution. Completion suggests no
endpoint/env names and suppresses file completion for both flags.

Existing destination selectors stay unchanged: `--to-context` or a remembered
Orka target, versus explicit `--sessions`; sessions still conflicts with
`--to-context` and Orka-only `--result-port`. Judging shares the case deadline
and never retries or reruns the agent. Safe reasoning goes to terminal stdout,
not receipts, and may contain answer text. Offline `agent eval diff` and
`agent verify` do not select a judge endpoint or call a judge; replay explicitly
reports judge assertions not verified. See [judge behavior and disclosure](agent-lift.md#explicit-llm-judge).

## Recorded-session selectors (unreleased)

`kmx agent eval from-sessions <session-uid>... --sessions <source>
--assertions <case.yaml> --out <manifest.json>` reads source journals without
inference. An existing output manifest means atomic append, never overwrite;
duplicates refuse the whole operation. Assertions are authored without `input`.
The `agent eval` group holds case preparation and offline diff, while execution
remains `agent evaluate`; no new top-level verb or workspace model is introduced.

`agent evaluate --from-sessions <manifest> --source-sessions <source>
--sessions <candidate>` requires both explicit source and candidate selection,
bypasses Kubernetes configuration, and refuses Orka-only selectors. Source CA
uses `--source-sessions-ca`; candidate CA retains `--sessions-ca`. `--show-content`
and source selectors require `--from-sessions`, never silently changing ordinary
evaluation. Existing authored-input behavior is unchanged.

Recorded inputs require named destination confirmation using
`KAIMAHI_CONFIRM=<normalized-candidate-host:port>` even on loopback. Judges require
separate `KAIMAHI_CONFIRM_JUDGE=<canonical-judge-base-url>`. Refusals name the
transfer destination plainly; choosing an endpoint alone is not consent.
Reference runs suppress answers and judge reasoning unless `--show-content` is
explicit. Help/completion performs no reads, writes or inference. See
[reference cases and row-local unknown comparisons](agent-lift.md#recorded-sessions-as-reference-cases).
Missing case identity produces an `unknown` diff row without hiding comparable
cases; unknown rows remain incomplete/nonzero. Observed identity disagreement
or no complete observed case identity in either receipt remains incompatible.

## Offline suite workspace

`agent create <name> --workspace <directory> --model <model> --instructions <file>`
authors one tool-free native-Orka member offline. It accepts optional description,
refuses live-create selectors and leaves default create/bundle output unchanged.
No Kubernetes configuration, builder or model call is needed.

`suite push --workspace <directory> <reference>` prepares that member and its
shareable authored cases as an ordinary AgentSuite, then copies the exact OCI
manifest/blob graph. Native member image requirements are empty. Registry auth,
plain-HTTP and non-atomic tag replacement remain as documented. Old
`suite push <directory> <reference>` retains extracted-artifact transfer behavior.
Workspace evaluation and reference lift are not implemented; these commands
create no evaluation or promotion evidence. See [suite workspace](suite-workspace.md).

## Vocabulary

**Decided: keep Create → Prove → Lift.** Lift is the chosen lifecycle verb for
the local-to-remote journey: create an agent locally, prove it with a real
answer, then lift it to a remote environment. Retain `kmx agent lift` and
interactive `/lift`; use **Lift** consistently in help, menu labels and the
product narrative. Do not rename the agent command to `deploy`. “Deployment”
remains useful technical language for the resulting resources and receipts.

Removing the root AKS aliases frees `lift` for a future suite lift workflow.
No new root verb or suite lift command is implemented; `kmx agent lift` remains
the agent operation, and `kmx aks up/down` remains the AKS route. Removal does
not change AKS arguments, ownership records, recovery or teardown behavior.

- **Context:** a kubeconfig access selection; not a durable cluster identity.
- **Target:** selected runtime, cluster identity, namespace and deployment
  destination, following [#238](https://github.com/kaimahi-agents/kaimahi/issues/238).
- **Runtime adapter:** KMX's integration with an execution platform.
- **Inference provider/backend:** an independent model-access route.
- **Provision** infrastructure; **install** runtime software; **create** an
  agent; **run/chat**; **evaluate** a revision; **lift** a bundle to a prepared
  destination; **show** live details; report deployment **status**; **retire**
  bundle-owned resources.
- **Ready**, **answered** and **evaluated** identify different evidence.
  **Unknown** is not **absent**. **Retry** resubmits; **result** retrieves the
  existing execution.

Bundle lift does not transfer sessions, memory or live state, delete its source,
or perform cutover. Workload retirement is separate from infrastructure teardown.
The guided live-copy fallback is explicitly untracked, not a bundle deployment.

## Audit and existing work

The command inventory is pinned by the command-tree and entry-point tests in
[`cmd/kmx`](../cmd/kmx). Context show/use split read and selection operations;
local up/down scope the existing kind operations. Root discovery hides the AKS
compatibility group, which remains directly callable. Removed root aliases are
neither registered nor suggested by help or completion. Help and internal
completion are not operational leaves.

Other implemented alignment in development builds includes
`quickstart --interactive` (`-i`) and intrinsically interactive `agent chat`.
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
Preserve `/lift` and Lift labels for the local-to-remote journey; any future
grouped `/agent lift` spelling must preserve that entry point.
Use `/inference use cluster|host-copilot|host-foundry` to distinguish execution
routes, `/exit` with `/quit` compatibility, and `/verbose on|off` coordinated
with #292. Retain keyboard shortcuts and advertise only supported controls.

Proposed flag alignment: `--prompt`/`--prompt-file` instead of `--task`,
`--instructions-file` instead of file-valued `--instructions`, `--bundle-dir`
and `--bundles-dir`. Keep report `--output` separate from artifact `--out`;
preserve raw and answer-only stdout.

## Remaining decisions

1. **Infrastructure hierarchy:** #223 owns the long-term Azure interface;
   `target aks` is not the chosen final shape. `aks up/down` remain callable
   compatibility routes, hidden from root help and completion.
2. **Runtime setup:** retain `orka install/status` pending agreement on a
   generic runtime setup family; a name cannot imply installer support.
3. **Capability view:** `kmx targets` is implemented for #289; retain that
   spelling and its read-only/offline-default contract rather than adding synonyms.
4. **Authoring/execution verbs:** reconcile applying `create` with offline
   `new`, `evaluate` with `test`, and `agent run` with canvas-service `run start`
   through #276 and its authoring/execution work. Their scopes differ.
5. **Runtime selection:** settle operator/saved configuration versus public
   selection through #224/#238; retain current capabilities in rename slices.
6. **Target flags:** decide `--to-*` versus `--context/--namespace` with bundle
   defaults, multi-target status, namespace ambiguity and alias conflicts.
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
