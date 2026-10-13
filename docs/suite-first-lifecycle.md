# Suite-first lifecycle: objects and everyday commands

**Status:** Direction, partly implemented; discussed in [#328](https://github.com/kaimahi-agents/kaimahi/issues/328). The native one-member path is landing under [#430](https://github.com/kaimahi-agents/kaimahi/issues/430) (#437, #445, #450 merged). Also merged: suite diff (#441), the image-backed `AgentSuites` contract (#443) and offline kagent receipts (#446). Open questions below are owned by the linked issues.

## Direction

The workspace is what people author; the suite is the portable release and lifecycle object. Everyday commands stay few, while their internals are split into small capabilities.

This develops the [walkthrough](https://github.com/kaimahi-agents/kaimahi/issues/328#issuecomment-6104051141) and Tatsinnit's reply, alongside the [lifecycle interface decision](kmx-lifecycle-interfaces.md), not as a replacement for it. Follow-ups belong to [#344](https://github.com/kaimahi-agents/kaimahi/issues/344). The merged [#355](https://github.com/kaimahi-agents/kaimahi/pull/355) makes the portable format public in `agentsuite/`; construction and transfer remain internal. The image-backed prototype from #342 is being split into drafts #448 and #449 (tracked in [#447](https://github.com/kaimahi-agents/kaimahi/issues/447)); it comes in through root `lift`, the same as the native path, not a second command (agreed on #404).

## What the user holds

These are distinct objects, not interchangeable names for one directory. “Release artifact” and “published release” below describe proposed journey stages, not new API types or a claim of promotion: [#351](https://github.com/kaimahi-agents/kaimahi/issues/351) reserves release terminology until its semantics are agreed.

| What the user calls it | Identity | What produces it (proposed command) | Mutable? | Where it lives |
|---|---|---|---|---|
| Workspace | Directory and authored revision; Git commit is provenance, not the resolved suite digest | `create` adds a member | Yes | Local directory, usually a Git repository |
| Resolved AgentSuite | Canonical digest of `agentsuite.json`, rooting the digest-bound agent, ToolProvider, composition and build-profile graph | Resolution inside `push` or `lift .` | No; edits produce a new graph | Validated local content, then the artifact payload |
| Member images (Agent Sandbox Images) | Runnable OCI manifest digest per member/platform, linked to the exact suite artifact, composition, build profile and sandbox binding; retain any index digest separately | Build inside `push` or `lift .` | No; rebuilding may produce new digests | Local image store/archive, then registry |
| Release artifact | AgentSuite OCI manifest digest plus an immutable association to every required member image digest; association representation is open | Package inside `push` or `lift .` | No | Local content store or portable transfer package |
| Published release | The same artifact and image identities, with retrieval references; a tag is a mutable locator, not identity | Publication inside `push` | Content no; tags/locations yes | OCI registry; offline transfer should preserve identities |
| Target-bound deployment | Deployment ID tied to artifact, selected members/images, runtime installation, durable target and target/inference binding identities; exact aggregate identity is open | `lift REF` or `lift .` | Live state yes; changed inputs require a new recorded revision | Runtime placement/registration plus private operation and deployment records |
| Evaluation evidence | Evaluation run ID and immutable subject/case-set identities, with per-case outcomes and execution references | `eval` against a selected execution environment | Completed evidence no; another run creates new evidence | Private evidence store; distributable attestations via OCI referrers |

The [AgentSuite specification](../agentsuite/spec.md) distinguishes canonical graph identity from OCI manifest identity. Neither is an image digest. Packaging and registry relocation must not be confused with source edits. A Kubernetes context supplies access; durable target identity is not its name. A deployment is placement or registration; a run is a Task, Session or equivalent invocation, not another deployment.

## Everyday commands

This is the #328 journey with its proposed behavior made explicit. It is not a runnable recipe for today's CLI.

```sh
mkdir my-agents && cd my-agents && git init

kmx create greeter --instructions "Greet people briefly."
# no workspace found, so offer to start one here; then author agents/greeter/
kmx chat greeter
# chat with a member; workspace-local execution is proposed
kmx eval
# evaluate the exact packaged suite's selected members and their case sets
kmx push ghcr.io/me/my-agents:v1
# resolve, pack, build every required member, push, print immutable identities
kmx lift ghcr.io/me/my-agents@sha256:... --target prod
# retrieve, verify, plan, gate and place on a prepared target; never build
kmx lift . --target prod
# convenience preparation followed by the same lift plan
kmx status
# suite deployment status
```

The digest ellipsis stands for the actual printed digest. `--target prod` stands for a durable destination; its interface (context, namespace, inference binding) still needs agreement. Runtime is not a lift option: it comes from the member, chosen at create, and evaluation evidence binds it. Lift resolves the runtime, prints it, and refuses when the target can't run it or the evidence covers a different runtime. Runtime support must be qualified, not inferred from this example.

Commands find the workspace by walking up from the current directory to `workspace.yaml`, the way git finds `.git`; `--workspace` overrides. Evaluate and push print the workspace and suite digest they used, because evidence binds to that suite. When `create` finds no workspace, it asks in a terminal whether to start one in the current directory; without a terminal (CI, scripts) it refuses with a hint to pass `--workspace`. Other workspace commands refuse with the same hint. Lift by digest never needs a workspace. This makes authoring the default meaning of `create`; where today's live-agent create moves is an open question.

Everyday commands sit at the root; `kmx agent` goes away. With no released users to protect, this is the time for the breaking change. Less frequent operations stay under nouns: `suite diff|pull|validate`, `suite push DIRECTORY REFERENCE`, `eval diff`, `task`, `aks`, `local`, `orka`, `targets`. Root `lift` today is only a deprecated alias for `aks up`; it is removed before root `lift` takes the suite meaning. Today's root `status` Orka report moves to `orka status`.

| Current path | Mapping to this proposal |
|---|---|
| `agent create/chat/run/evaluate/lift/status` on main | Move to root `create`, `chat`, `run`, `eval`, `lift`, `status`. Create defaults to authoring. Legacy bundle commands move under `bundle` (#460) and are removed with `agent.yaml` once the suite path reaches parity. |
| Image-backed path in drafts #448/#449 (#447; #342 closed) | Member authoring joins `create`; publication joins `push`; deployment uses root `lift` rather than a separate `suite lift` (agreed); invocation remains member-scoped run/chat, not lift. |
| `suite push <directory> <reference>` on main | Exists today for validated suite-artifact transfer. `suite push --workspace . <reference>` (#437) prepares and publishes the exact native suite with no member images; root `push <reference>` with workspace discovery is proposed. |

Keep **Create → Prove → Lift**, as [#302](https://github.com/kaimahi-agents/kaimahi/pull/302) and the [command alignment record](command-semantics.md) decided. Bare `kmx lift` today is deprecated AKS provisioning, not artifact lift. Reassigning it needs an explicit compatibility transition; this proposal does not change it. Follow the [command conventions](command-conventions.md), including aligned help, interactive labels, recovery commands and compatibility tests, when any spelling ships.

## Explicit decisions

- `push` produces the suite and every member image the selected execution path needs (none for native-Orka declarative members), with an immutable link to each.
- Lift from a registry reference is deploy-only: no builder and no registry push credentials; missing images cause refusal, not an implicit build.
- `lift .` is a convenience using the same identities and planning, not a second deployment engine.
- A model binding chosen at lift changes the deployment and evaluation subject even when suite and images are unchanged.
- Old bundle receipts stay readable for their original lifecycle but do not count as suite or image evidence.
- Multi-agent evaluation needs a member-selection rule: every member, exported entrypoints, or a declared invocation closure remains open.
- AgentSuite covers local and remote runs; local commands compose suite operations rather than adding a separate lifecycle. A quick local preview may skip packaging, but only a run of the exact packaged suite counts as promotion evidence.
- Everyday commands are root verbs (`create`, `chat`, `run`, `eval`, `push`, `lift`, `status`); `kmx agent` is removed rather than aliased.
- Commands find the workspace from the current directory; `create` with no workspace offers to start one. Authoring is the default meaning of `create`.
- Runtime comes from the member and is bound in evaluation evidence; lift resolves and prints it, and is not a way to switch runtimes.
- For the packaged path (#430), only a complete passing run exports evidence. A failed or incomplete run exits nonzero and produces no receipt, so lift refuses a candidate for lack of matching passing evidence, not because a failure was recorded.
- Lifting a previous digest again is a re-lift of that release, not a rollback of a deployed bad revision. Rollback needs the revision and retention records in #348/#346.
- `agent.yaml` bundles are removed, not read or migrated. They keep working only until the suite path reaches parity; no new bundle-only features meanwhile.

## What evaluation evidence binds to

Evidence must bind the selected **member, image digest, composition, inference binding, runtime/target and case-set digest**, as well as suite/artifact identity. Record selection coverage, assertion/evaluator identities where relevant, per-case outcomes and execution references; missing or unknown evidence cannot establish a pass. A local trial before push is not automatically evidence for the subsequently built images. Changed model bindings require matching evaluation evidence, not reuse based only on unchanged suite bytes.

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

## Removing `agent.yaml` bundles

There are no released users to protect, so bundles are removed rather than read as one-agent workspaces or migrated. That keeps one model and no converter code. Removal waits for parity on the suite path, in order:

1. Native Orka: create, eval, push and lift by digest (#430).
2. The lift gate on suite evidence, replacing `lift-policy.yaml` over bundle receipts.
3. kagent as a suite target, replacing kagent bundle create, lift, status and evaluation. kagent remote MCP tools are not a precondition: they may be unavailable for a while and return on the suite path.
4. Quickstart, console and interactive lift through create → lift, so `create --live` can go.

Then one deliberate breaking change deletes the `bundle` commands, bundle receipts and their code. Old receipts stop being readable; no new bundle-only features land in the meantime.

## Open questions

1. Which fields are authored versus resolved, and how do membership, ToolProviders, invocation edges and inference requirements enter the workspace? **Owners:** #328/#351, coordinated with #447.
2. What immutable member-image association supports required platforms and thin versus fully portable/offline packaging? **Owners:** #345 and #307, coordinated with #447.
3. Which members does `eval` select, and how is full coverage recorded? **Owner:** #350.
4. Which signatures and policies permit promotion across targets or model bindings? **Owner:** #350; signer policy is not yet decided.
5. How are target/binding identities, aggregate deployment revisions, partial outcomes, recovery and retained retirement recorded? **Owners:** #348, #346 and #347; session-runtime conformance belongs to #352.
6. How do destination/inference flags and member run/chat selection converge with root `lift`? **Owners:** #301/#302 and #351, coordinated with #447.
7. How does a lifted suite member get chatted with on its destination? Today's chat targets live Agents, and suite-owned objects deliberately refuse legacy access. **Owner:** #344.
