# Command alignment

Tracking issue: [#301](https://github.com/kaimahi-agents/kaimahi/issues/301).
The [CLI reference](kmx.md) documents the implemented interface and the
[command conventions](command-conventions.md) govern future changes. This page
distinguishes implemented alignment from proposed follow-ups.

## Implemented entry-point alignment (unreleased)

| Canonical command | Compatibility input | Contract |
|---|---|---|
| `kmx context show` | `kmx ctx` | Read the effective context, selection source and posture. |
| `kmx context use <context>` | `kmx ctx <context>` | Remember KMX's access selection after the existing context guard; never change kubectl current-context. |
| `kmx local up` | `kmx up` | Provision the local kind environment, model and Orka; no agent. The existing `--step` choices remain. |
| `kmx local down` | `kmx down` | Delete the named local kind cluster, including its data, under the existing context/container-engine guard. |

Bare `context` and `local` display help. Canonical commands use the existing
operations, settings, saved-selection file and guards. Old spellings remain
callable and emit deprecation notices only on stderr; help and completion lead
with the canonical names. No removal release has been scheduled. Tagged v0.4.1
uses the compatibility spellings; the canonical names require a newer build.

The redirected context report retains its `source: kmx ctx` provenance label
for saved selections as a compatibility format. That label is not a next-step
command. Generated selection and local teardown instructions use the canonical
spellings. Invocation-specific confirmation may repeat the compatibility
command the caller actually supplied to preserve its exact arguments.

Generated local commands preserve `KIND_CLUSTER`, `CONTAINER_ENGINE` and
`--context`. AKS retry/teardown instructions use the common effective-option
renderer, including `--byo` only for the existing ownership mode; Azure setup
and cleanup diagnostics say AKS rather than agent Lift. The carried scripts
use these native instructions when invoked by KMX. Standalone script use has
no KMX ownership receipt, so its fallback names the actual script with quoted
arguments instead of a deleted Make target or an invented BYO recovery route.

## Vocabulary

**Decided: keep Create → Prove → Lift.** Lift is the chosen lifecycle verb for
the local-to-remote journey: create an agent locally, prove it with a real
answer, then lift it to a remote environment. Retain `kmx agent lift` and
interactive `/lift`; use **Lift** consistently in help, menu labels and the
product narrative. Do not rename the agent command to `deploy`. “Deployment”
remains useful technical language for the resulting resources and receipts.

The current bare `kmx lift` spelling is the deprecated AKS provisioning route,
not a shorthand for `kmx agent lift`. This vocabulary decision does not change
that route's arguments or behavior; any reassignment needs an explicit
compatibility transition coordinated with #223.

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
compatibility group and the old context/local spellings, while direct invocation
remains available. Help and internal completion are not operational leaves.

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
8. **Removal timing:** announce a breaking minor release before removing
   compatibility routes. No date/version is selected by this slice.

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
