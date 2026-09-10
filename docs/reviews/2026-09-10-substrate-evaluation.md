# Agent Substrate evaluation boundary

Kaimahi keeps Agent Substrate as an **evaluation path**, not a production
installation or support commitment. This lane adds no installer, runtime,
provider patches, or new orchestration layer.

## Source baseline

Read on 2026-09-10, beginning with
[Orka PR #515](https://github.com/orka-agents/orka/pull/515),
“feat(substrate): support native upstream workspaces and checkpoints”:

| Source | Revision inspected |
| --- | --- |
| Kaimahi main | `3f53bf8` |
| Orka main | `516fdf7` |
| Orka PR #515, open when inspected | `93b8e0a812114d40c170e588f05dec0868dcd2c1` |
| Substrate pin on Orka main | `b80031d260959b1fc5c6f61e3099fe2a6d368af1` |
| Official Substrate pin proposed by #515 | `7a9abab35044670ce357d9eea89175a153718cbc` |
| Substrate main | `f3618ebc9d607b38dd30e293758a839d551426ec` |

PR #515 proposes an unmodified upstream provider and native ate-api resources.
It removes all three local evaluation patch files. Its checkpoint path uses
native Data Tags and a fresh process on restore, not full-memory restoration.
It also includes rotating control authentication, checkpoint authorization,
credential bootstrap, lifecycle recovery, and direct/MCP/ACP conformance.
These are proposed changes at the recorded head, not claims about Orka's main
or a released installation. See its
[native checkpoint decision](https://github.com/orka-agents/orka/blob/93b8e0a812114d40c170e588f05dec0868dcd2c1/docs/adr/0031-native-substrate-checkpoints.md).

## Which layer changes

The unit of selection is a workspace backend, not a second orchestrator:

- **RuntimePool stays.** Orka owns the logical runtime lifecycle, Task attempts,
  Sessions, prompt admission, cancellation, and publication.
- **Harness v2 stays.** It is the runtime protocol and execution contract,
  independent of the physical workspace provider.
- **Substrate replaces workload materialization for selected pools.** A
  Substrate Actor hosts the runtime instead of the ordinary Deployment or
  Agent Sandbox-backed workload. Those alternatives are not stacked for the
  same workspace.
- **Agent Sandbox remains the existing alternative.** Orka depends on
  agent-sandbox `v1.0.0`; its PVC-backed DataOnly suspension already preserves
  workspace files across a fresh Pod. Suspension by itself is therefore not
  a reason to introduce another backend.

The concrete additional workflow to evaluate in #515 is exporting an
independent immutable checkpoint, deleting its source workspace, and restoring
saved files into a fresh Task or Session. The
[adapter's capability advertisement](https://github.com/orka-agents/orka/blob/93b8e0a812114d40c170e588f05dec0868dcd2c1/internal/controller/acp_workspace_provider_adapter.go#L93-L103)
separates the two backends' shared suspension capability from Substrate's
checkpoint and restore capabilities. The
[Agent Sandbox decision](https://github.com/orka-agents/orka/blob/93b8e0a812114d40c170e588f05dec0868dcd2c1/docs/adr/0028-agent-sandbox-pvc-cold-resume.md)
describes the existing PVC lifecycle.

## Evaluation scope

Follow #515 rather than build a parallel native adapter or another patch set.
Keep direct workspaces, MCP actor pools, and ACP RuntimePools distinct when
recording results. A `SubstrateActorPool` density field is not a measurement
of ACP throughput or infrastructure savings.

A future support decision needs an explicit workload, deployment owner,
supported version matrix, upgrade/restore qualification, and measured
cost and recovery objectives. Reuse the existing Orka tracking for
[provider qualification](https://github.com/orka-agents/orka/issues/419) and
[suspend/cold-resume conformance](https://github.com/orka-agents/orka/issues/451),
rechecking their scope against #515 before proposing additional work.

## Verification and publication boundary

This was a source and focused-test evaluation, not a cluster trial. At the
recorded #515 head, focused `internal/workspace` tests passed for native
transport, template identity, readiness, and command timeouts. Focused
`internal/controller` tests passed for cold continuation with rotated
credentials, Tag-source changes, Actor replacement, journal loss, direct-egress
admission, and capability advertisement. These fixture tests are not live
provider qualification or a performance benchmark.

Both projects' PR and issue lists were checked before preparing any drafts.
Detailed support tradeoffs and proposed upstream messages are retained locally,
not published in this repository. No issue, PR, or comment was submitted to
Orka or Substrate by this lane.

No cluster or cloud resource was created, and no hosted model was called.
There was nothing to tear down; direct cloud/model spend was **US$0.00**.
