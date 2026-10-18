# Suite-first lifecycle: objects and everyday commands

**Status:** Direction, partly implemented; discussed in [#328](https://github.com/kaimahi-agents/kaimahi/issues/328). Current `main` implements one-member workspace authoring, packaged Orka/exact-v0.10.3 Go kagent evaluation and evaluated publication, suite diff, bounded native CI qualification, and runtime-dispatched deploy-only Lift by digest. Suite deployment status/retirement, workspace run/chat and general image-backed placement are not implemented. Open questions below are owned by the linked issues.

## Direction

The workspace is what people author; the suite is the portable release and lifecycle object. Everyday commands stay few, while their internals are split into small capabilities.

This develops the [walkthrough](https://github.com/kaimahi-agents/kaimahi/issues/328#issuecomment-6104051141) and Tatsinnit's reply, alongside the [lifecycle interface decision](kmx-lifecycle-interfaces.md), not as a replacement for it. Follow-ups belong to [#344](https://github.com/kaimahi-agents/kaimahi/issues/344). The merged [#355](https://github.com/kaimahi-agents/kaimahi/pull/355) makes the portable format public in `agentsuite/`; construction and transfer remain internal. Current supported packaged-native placement uses root `lift`; future image-backed placement should extend that verb rather than introduce `suite lift` as a second lifecycle.

## What the user holds

These are distinct objects, not interchangeable names for one directory. “Release artifact” and “published release” below describe proposed journey stages, not new API types or a claim of promotion: [#351](https://github.com/kaimahi-agents/kaimahi/issues/351) reserves release terminology until its semantics are agreed.

| What the user calls it | Identity | Producer / status | Mutable? | Where it lives |
|---|---|---|---|---|
| Workspace | Directory and authored revision; Git commit is provenance, not the resolved suite digest | `create` adds a member | Yes | Local directory, usually a Git repository |
| Resolved AgentSuite | Canonical digest of `agentsuite.json`, rooting the digest-bound agent, ToolProvider, composition and build-profile graph | Resolution inside current `eval` and `push` | No; edits produce a new graph | Validated local content, then the artifact payload |
| Member images (Agent Sandbox Images) | Runnable OCI manifest digest per member/platform, linked to the exact suite artifact, composition, build profile and sandbox binding; retain any index digest separately | Current `suite build` is an experimental one-composition builder; complete suite build remains future | No; rebuilding may produce new digests | Local image store/archive, then registry |
| Release artifact | AgentSuite OCI manifest digest plus an immutable association to every required member image digest; native profiles currently require no member images, while general release association remains open | Current workspace preparation produces the exact native OCI artifact; general release assembly remains future | No | KMX private local layout, portable layout or registry |
| Published release | The same artifact and image identities, with retrieval references; a tag is a mutable locator, not identity | Current root `push` | Content no; tags/locations yes | OCI registry or local OCI layout |
| Target-bound placement | Exact artifact, runtime, target and runtime-specific binding observations; durable aggregate deployment identity is open | Root `lift` places one-member native Orka packages by create/exact reuse or exact-v0.10.3 Go kagent packages by create-only placement | Live state yes; changed packaged identity requires new evidence, while explicit promotion requires an eligible destination with both suite-owned resources absent | Runtime resources and ownership markers; the opaque successful placement remains in process, with no aggregate deployment record |
| Evaluation evidence | Evaluation run ID and immutable subject/case-set identities, with per-case outcomes and execution references | Current root `eval` for packaged Orka or exact-v0.10.3 Go kagent; only a full pass exports runtime-specific private evidence | Completed evidence no; another run creates new evidence | Private evidence file; distributable attestations remain future |

The [AgentSuite specification](https://github.com/agent-primitives/suite/blob/main/spec.md) distinguishes canonical graph identity from OCI manifest identity. Neither is an image digest. Packaging and registry relocation must not be confused with source edits. A Kubernetes context supplies access; durable target identity is not its name. A deployment is placement or registration; a run is a Task, Session or equivalent invocation, not another deployment.

## Implemented workspace journey

This current-`main` recipe requires a prepared Orka target with a Ready Provider,
its Secret reference and result-reader access. It authors source, evaluates the
exact package, publishes that same identity, then reviews and applies an
exact-digest Lift. Public Orka eval uses disposable candidates and exports only
after verified cleanup. Its evidence requires `--promote` even in the same
environment, absent canonical destination resources and the same packaged
identity/runtime/model; evidence still proves only the source. Historical
canonical evidence retains its exact-subject reuse gate.

```sh
mkdir my-agents && cd my-agents && git init
printf '%s\n' 'Greet people briefly.' > instructions.md

kmx create greeter --workspace . \
  --model qwen2.5:3b --instructions instructions.md

mkdir -m 700 ../private-evidence
kmx eval --workspace . --to-context kind-demo \
  --inference provider:local --out ../private-evidence/passing.json

kmx push --workspace . --evaluation ../private-evidence/passing.json \
  ghcr.io/me/my-agents:v1

kmx lift ghcr.io/me/my-agents@sha256:<digest> --to-context kind-demo \
  --inference provider:local --evidence ../private-evidence/passing.json --promote --plan
kmx lift ghcr.io/me/my-agents@sha256:<digest> --to-context kind-demo \
  --inference provider:local --evidence ../private-evidence/passing.json --promote
```

Push prints the prepared OCI manifest digest; use that digest rather than the
mutable tag for Lift. Prompts and answers stay out of the receipt, but the
published suite includes authored case inputs. Lift never builds, repacks or
publishes. `--plan` is GET-only; apply creates absent resources or reuses exact
receipt-bound resources, without update, adoption, ambiguous retry or rollback.

## Lift and read-only status; execution remains separate

The command split is:

```text
kmx lift REF@sha256:DIGEST --to-context CTX --evidence FILE \
  (--inference provider:NAME | --bindings FILE)
                         # current: retrieve, verify, gate and place; never build
kmx status --to NAME     # GET-only destination Agent inventory and Lift provenance
kmx orka status          # table-only Orka runtime report
kmx run / kmx chat       # current: select an existing live Orka Agent
```

Destination status observes the whole namespace, not this workspace's suite.
It reads unsigned per-object Lift metadata, never infers missing provenance and
keeps candidates separate. Member selection for run/chat remains unavailable.
Root Lift's `--plan` performs GETs only; apply uses a guarded, single-use plan.
Runtime and model come from the package; Lift is not a runtime or model switch.
Evidence must agree with them but does not select them.
Created/recreated resources require fresh evaluation before later
receipt-bound reuse, and readiness is not destination execution evidence.

Commands find the workspace by walking up from the current directory to `workspace.yaml`, the way git finds `.git`; `--workspace` selects an exact root without fallback. Eval and push print the workspace and suite digest they used, because evidence binds to that suite. When create finds no workspace, it asks in a terminal whether to start one in the current directory; without a terminal it refuses with a hint to pass `--workspace`. Lift by digest needs no workspace.

Everyday create/chat/run/eval/push commands now sit at the root and `kmx agent`
is removed. Less frequent operations stay under nouns: `suite
build|diff|pull|push|validate`, `eval diff|from-sessions|cleanup`, `task`, hidden `aks`,
`local`, `orka` and `targets`. Removed root AKS aliases leave `lift` reserved for
the packaged-suite operation. Root `status --to NAME` is destination inventory;
`orka status` retains the runtime report. See [destination status](destination-status.md).

| Current path | Relationship to the direction |
|---|---|
| Root `create`, `eval`, `push` | Implemented suite workspace authoring, packaged Orka/kagent proof and exact ordinary or evaluated publication. |
| Root `lift REF@sha256:DIGEST` | Implemented deploy-only packaged Orka/kagent placement with mandatory runtime-matching private evidence; Orka exact reuse and kagent fresh-target creation have bounded live qualification. |
| Root `chat`, `run --agent NAME` | Live Orka Agents, including suite-owned Agents, selected by context/namespace/name; not workspace-local execution or fresh suite evaluation, and no adoption/copy/retirement authority. |
| Legacy user bundle lifecycle / `create --live` | Removed outright including readers/writers/receipts and previews/wizard; no migration or conversion. |
| `suite push DIRECTORY REFERENCE` | Existing extracted-artifact transfer without workspace discovery; root `push` is the workspace-aware operation. |
| Future image-backed path | Member authoring joins root create; publication joins root push; deployment uses root lift rather than `suite lift`. |

Keep **Create → Prove → Lift**, as [#302](https://github.com/kaimahi-agents/kaimahi/pull/302) and the [command alignment record](command-semantics.md) decided. Root `lift` now implements the bounded packaged Orka and exact-kagent paths. Follow the [command conventions](command-conventions.md), including aligned help, interactive labels, recovery commands and compatibility tests, when extending it.

## Explicit decisions

- `push` produces the suite and every member image the selected execution path needs (none for native-Orka declarative members), with an immutable link to each.
- Lift from a registry reference is deploy-only: no builder and no registry push credentials; missing images cause refusal, not an implicit build.
- Current Lift accepts only `REF@sha256:DIGEST`, optionally with `--from-layout DIR`; it does not discover a workspace or accept `lift .`.
- Runtime and model come from the package. Orka Provider/Secret or kagent bindings are part of the subject; an eligible changed destination requires explicit source-evidence `--promote`, not model substitution.
- Old Orka/kagent/AgentSessions bundle receipts are unreadable; native diff accepts only strict full-passing native suite evidence. No saved files/resources are deleted.
- Multi-agent evaluation needs a member-selection rule: every member, exported entrypoints, or a declared invocation closure remains open.
- AgentSuite covers local and remote runs; local commands compose suite operations rather than adding a separate lifecycle. A quick local preview may skip packaging, but only a run of the exact packaged suite counts as promotion evidence.
- Everyday commands are root verbs (`create`, `chat`, `run`, `eval`, `push`, `lift`, `status`); `kmx agent` is removed rather than aliased.
- Commands find the workspace from the current directory; `create` with no workspace offers to start one. Authoring is the default meaning of `create`.
- Runtime comes from the member and is bound in evaluation evidence; lift resolves and prints it, and is not a way to switch runtimes.
- For the packaged path (#430), only a complete passing run exports evidence. A failed or incomplete run exits nonzero and produces no receipt, so lift refuses a candidate for lack of matching passing evidence, not because a failure was recorded.
- Lifting a previous digest again is a re-lift of that release, not a rollback of a deployed bad revision. Rollback needs the revision and retention records in #348/#346.
- User `agent.yaml` bundles are removed, not converted or migrated. This is an
  explicit breaking retirement, not a claim of full suite-path parity.

## What evaluation evidence binds to

Evidence must bind the selected **member, image digest, composition, inference binding, runtime/target and case-set digest**, as well as suite/artifact identity. Record selection coverage, assertion/evaluator identities where relevant, per-case outcomes and execution references; missing or unknown evidence cannot establish a pass. A local trial before push is not automatically evidence for subsequently built images. Changed packaged identity or runtime profile requires matching evaluation evidence. Explicit `--promote` may retain source evidence only for an eligible destination where both suite-owned resources are absent and the packaged identity/runtime profile matches; that is not destination evaluation or authenticated approval.

OCI referrers provide distribution and discovery across machines. They do not establish trust: signing, accepted signers and promotion policy remain open under [#350](https://github.com/kaimahi-agents/kaimahi/issues/350). Evidence for one target or inference binding must not silently qualify another; policy must state any permitted promotion relationship. Keep credentials and execution payloads out of public evidence.

## Local development versus core

Core capabilities are **validate, resolve, build, package, store, deploy, observe and evaluate**. These are design categories, not proposed Go APIs or package names. The CLI can compose several in one command; machine consumers need stable identities, structured outcomes and diagnostics without parsing terminal text. `local up`, quickstart and console are developer conveniences over those capabilities. They should not force builders, registries, Kubernetes or interactive setup onto every consumer. The experimental interfaces from [#326](https://github.com/kaimahi-agents/kaimahi/pull/326) remain design evidence, not a stable SDK.

The alpha Go interfaces group these into three areas, which map onto the everyday commands:

| Interface | Responsibility | Commands |
|---|---|---|
| `AgentEnvironment` | Establish one logical environment; target resolution and teardown remain deferred | conceptually bare `local up` |
| `AgentSuites` | Produce and publish what runs: validate, build, push (#443) | `push`, `suite …` |
| `AgentDeployments` | Place and manage it on a target | `lift`, `status` |

The selected runtime (Orka, kagent, AgentSessions) executes; evaluation is separately scoped evidence. Three interfaces is the current alpha shape, not a permanent invariant. `AgentEnvironment` deliberately starts with setup only and does not turn a context into a durable target. Still open: wiring it to a second consumer, resolving an exact suite target from an established environment, and evolving `AgentDeployments` from one member image toward suite-scoped deployment.

## Removed user `agent.yaml` bundles

One deliberate breaking retirement removes commands/readers/writers/receipts
and live create/previews/wizard, root run bundle mode and `--bundles` flags.
Old receipts are unreadable. No migration, converter or saved-file/resource
cleanup exists. Public AgentSuite/ToolProvider implementation bundles remain.
Legacy RecordedExecution, Sessions evaluation/replay/judges are removed;
`eval from-sessions` is independent private reference extraction/export without
a native consumer. Native eval requires author-owned cases/full-passing evidence.
Suite status/retirement and workspace-local member execution remain unavailable,
with no replacement/parity claim. Direct run/chat can invoke lifted Orka Agents
by live identity; this does not evaluate their suite. Chat `/lift` and console Lift
print only the digest-Lift move notice; no GUI or source-less live-copy fallback remains. Quickstart composes
native lifecycle before a separate fresh answer; console Ready is placement only.

## Open questions

1. Which fields are authored versus resolved, and how do membership, ToolProviders, invocation edges and inference requirements enter the workspace? **Owners:** #328/#351, coordinated with #447.
2. What immutable member-image association supports required platforms and thin versus fully portable/offline packaging? **Owners:** #345 and #307, coordinated with #447.
3. Which members does `eval` select, and how is full coverage recorded? **Owner:** #350.
4. Which signatures and policies permit promotion across targets or model bindings? **Owner:** #350; signer policy is not yet decided.
5. How are target/binding identities, aggregate deployment revisions, partial outcomes, recovery and retained retirement recorded? **Owners:** #348, #346 and #347; session-runtime conformance belongs to #352.
6. How do lifted-member run/chat selection and aggregate status converge with the implemented root Lift destination/inference contract? **Owners:** #301/#302 and #351, coordinated with #447.
7. What suite/member-aware chat selection is needed beyond today's direct live-Agent path? Orka chat already accepts suite-owned Agents by context/namespace/name, without workspace-local execution or fresh suite evaluation. **Owner:** #344.
