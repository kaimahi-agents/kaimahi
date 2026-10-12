# Suite-first lifecycle: objects and everyday commands

**Status:** Proposal, discussed in [#328](https://github.com/kaimahi-agents/kaimahi/issues/328); not implemented.

## Direction

The workspace is what people author; the suite is the portable release and lifecycle object. Everyday commands stay few, while their internals are split into small capabilities.

This develops the [walkthrough](https://github.com/kaimahi-agents/kaimahi/issues/328#issuecomment-6104051141) and Tatsinnit's reply, alongside the [lifecycle interface decision](kmx-lifecycle-interfaces.md), not as a replacement for it. Follow-ups belong to [#344](https://github.com/kaimahi-agents/kaimahi/issues/344). The merged [#355](https://github.com/kaimahi-agents/kaimahi/pull/355) makes the portable format public in `agentsuite/`; construction and transfer remain internal. Open [#342](https://github.com/kaimahi-agents/kaimahi/pull/342) is a working prototype, not a settled storage or adapter contract.

## What the user holds

These are distinct objects, not interchangeable names for one directory. “Release artifact” and “published release” below describe proposed journey stages, not new API types or a claim of promotion: [#351](https://github.com/kaimahi-agents/kaimahi/issues/351) reserves release terminology until its semantics are agreed.

| What the user calls it | Identity | What produces it (proposed command) | Mutable? | Where it lives |
|---|---|---|---|---|
| Workspace | Directory and authored revision; Git commit is provenance, not the resolved suite digest | `agent create` adds a member | Yes | Local directory, usually a Git repository |
| Resolved AgentSuite | Canonical digest of `agentsuite.json`, rooting the digest-bound agent, ToolProvider, composition and build-profile graph | Resolution inside `suite push` or `lift .` | No; edits produce a new graph | Validated local content, then the artifact payload |
| Member images (Agent Sandbox Images) | Runnable OCI manifest digest per member/platform, linked to the exact suite artifact, composition, build profile and sandbox binding; retain any index digest separately | Build inside `suite push` or `lift .` | No; rebuilding may produce new digests | Local image store/archive, then registry |
| Release artifact | AgentSuite OCI manifest digest plus an immutable association to every required member image digest; association representation is open | Package inside `suite push` or `lift .` | No | Local content store or portable transfer package |
| Published release | The same artifact and image identities, with retrieval references; a tag is a mutable locator, not identity | Publication inside `suite push` | Content no; tags/locations yes | OCI registry; offline transfer should preserve identities |
| Target-bound deployment | Deployment ID tied to artifact, selected members/images, runtime installation, durable target and target/inference binding identities; exact aggregate identity is open | `lift REF` or `lift .` | Live state yes; changed inputs require a new recorded revision | Runtime placement/registration plus private operation and deployment records |
| Evaluation evidence | Evaluation run ID and immutable subject/case-set identities, with per-case outcomes and execution references | `eval` against a selected execution environment | Completed evidence no; another run creates new evidence | Private evidence store; distributable attestations via OCI referrers |

The [AgentSuite specification](../agentsuite/spec.md) distinguishes canonical graph identity from OCI manifest identity. Neither is an image digest. Packaging and registry relocation must not be confused with source edits. A Kubernetes context supplies access; durable target identity is not its name. A deployment is placement or registration; a run is a Task, Session or equivalent invocation, not another deployment.

## Everyday commands

This is the #328 journey with its proposed behavior made explicit. It is not a runnable recipe for today's CLI.

```sh
mkdir my-agents && cd my-agents && git init

kmx agent create greeter --instructions "Greet people briefly."
# proposed: author agents/greeter/ in this workspace, without creating a live agent
kmx agent chat greeter
# exists today: chat with a live Orka Agent; workspace-local execution is proposed
kmx eval
# proposed: evaluate the workspace's selected members and their case sets
kmx suite push ghcr.io/me/my-agents:v1
# proposed: resolve, pack, build every required member, push, print immutable identities
kmx lift ghcr.io/me/my-agents@sha256:... --target orka
# proposed: retrieve, verify, plan, gate and place on a prepared target; never build
kmx lift . --target orka
# proposed: convenience preparation followed by the same lift plan
kmx status
# exists today: Orka runtime report; suite deployment status is proposed
```

The digest ellipsis stands for the actual printed digest. `--target orka` sketches runtime selection, not a durable destination ID; destination and inference selection still need an agreed interface. Runtime support must be qualified, not inferred from this example.

| Current path | Mapping to this proposal |
|---|---|
| `agent create/evaluate/lift` on main | Separate authoring from live creation; evaluate becomes workspace-level `eval`; bundle lift converges on suite-based `lift`. Default create applies to a target and writes a bundle; offline bundle generation already exists, but suite-member authoring is proposed. |
| `suite create/publish/deploy/run` in open #342, not main | Member authoring joins `agent create`; publication joins `suite push`; the prototype's `deploy` spelling is rejected in favor of `lift`; invocation remains member-scoped run/chat, not lift. |
| `suite push <directory> <reference>` on main | Exists today for validated suite-artifact transfer, but does not build/publish the required image set. The one-reference, current-workspace form above is proposed. |

Keep **Create → Prove → Lift**, as [#302](https://github.com/kaimahi-agents/kaimahi/pull/302) and the [command alignment record](command-semantics.md) decided. Bare `kmx lift` today is deprecated AKS provisioning, not artifact lift. Reassigning it needs an explicit compatibility transition; this proposal does not change it. Follow the [command conventions](command-conventions.md), including aligned help, interactive labels, recovery commands and compatibility tests, when any spelling ships.

## Explicit decisions

- `suite push` produces the suite, every required member image, and an immutable link between them.
- Lift from a registry reference is deploy-only: no builder and no registry push credentials; missing images cause refusal, not an implicit build.
- `lift .` is a convenience using the same identities and planning, not a second deployment engine.
- A model binding chosen at lift changes the deployment and evaluation subject even when suite and images are unchanged.
- Old bundle receipts stay readable for their original lifecycle but do not count as suite or image evidence.
- Multi-agent evaluation needs a member-selection rule: every member, exported entrypoints, or a declared invocation closure remains open.
- AgentSuite covers local and remote runs; local commands compose suite operations rather than adding a separate lifecycle. A quick local preview may skip packaging, but only a run of the exact packaged suite counts as promotion evidence.
- The current `agent.yaml` path keeps working until a one-member suite path (create, eval, push, lift to Orka) is proven end to end; then `agent.yaml` is read as a one-agent workspace.

## What evaluation evidence binds to

Evidence must bind the selected **member, image digest, composition, inference binding, runtime/target and case-set digest**, as well as suite/artifact identity. Record selection coverage, assertion/evaluator identities where relevant, per-case outcomes and execution references; missing or unknown evidence cannot establish a pass. A local trial before push is not automatically evidence for the subsequently built images. Changed model bindings require matching evaluation evidence, not reuse based only on unchanged suite bytes.

OCI referrers provide distribution and discovery across machines. They do not establish trust: signing, accepted signers and promotion policy remain open under [#350](https://github.com/kaimahi-agents/kaimahi/issues/350). Evidence for one target or inference binding must not silently qualify another; policy must state any permitted promotion relationship. Keep credentials and execution payloads out of public evidence.

## Local development versus core

Core capabilities are **validate, resolve, build, package, store, deploy, observe and evaluate**. These are design categories, not proposed Go APIs or package names. The CLI can compose several in one command; machine consumers need stable identities, structured outcomes and diagnostics without parsing terminal text. `local up`, quickstart and console are developer conveniences over those capabilities. They should not force builders, registries, Kubernetes or interactive setup onto every consumer. The experimental interfaces from [#326](https://github.com/kaimahi-agents/kaimahi/pull/326) remain design evidence, not a stable SDK.

## Existing `agent.yaml` bundles

Two options are to read a bundle as a one-agent workspace or require a one-time migration. Recommend the **one-agent reader first**: preserve authored bytes and original digests, avoid a compulsory rewrite, and resolve supported behavior into the same suite path. Do not silently drop runtime extensions, tools or delegation; refuse unsupported conversion with an actionable explanation. Creation bindings remain target data, not portable release credentials. An explicit migration can later help adopt richer workspace authoring, but must produce a deliberate new revision. Preserve [bundle readability rules](bundle-format.md) and [original receipt/gate semantics](agent-lift.md); never relabel old receipts as evidence for newly built images.

## Open questions

1. Which fields are authored versus resolved, and how do membership, ToolProviders, invocation edges and inference requirements enter the workspace? **Owners:** #328/#351, coordinated with the #342 prototype.
2. What immutable member-image association supports required platforms and thin versus fully portable/offline packaging? **Owners:** #345 and #307, coordinated with #342.
3. Which members does `eval` select, and how is full coverage recorded? **Owner:** #350.
4. Which signatures and policies permit promotion across targets or model bindings? **Owner:** #350; signer policy is not yet decided.
5. How are target/binding identities, aggregate deployment revisions, partial outcomes, recovery and retained retirement recorded? **Owners:** #348, #346 and #347; session-runtime conformance belongs to #352.
6. How do root `lift` compatibility, destination/inference flags and member run/chat selection converge without changing existing contracts silently? **Owners:** #301/#302 and #351, coordinated with #342.
