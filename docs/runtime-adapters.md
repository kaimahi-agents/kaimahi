# Runtime contract

KMX separates agent intent from the system that executes it. The vocabulary in
this document is the contract between the developer experience and runtime
implementations; it prevents platform identity, model choice, cluster location,
and policy from collapsing into one ambiguous "provider" concept.

Orka remains the first-class and default runtime for create, chat, inspection,
lift, status, evaluation, console, quickstart, local setup, and AKS. KMX also
has one explicit lifecycle-only integration:
`kmx agent create --runtime kagent <name>` can render and create against an
already-installed exact Kagent v0.10.2. It does not install or upgrade that
runtime and does not change any no-flag behavior. The selected platform, not a
generic KMX control plane, owns execution and enforcement.

## Read-only target view

```bash
kmx targets                         # offline compiled support
kmx targets -o json                 # schemaVersion 1; same capability facts
kmx --context <context> targets --detect
kmx targets --sessions 127.0.0.1:8080
kmx targets --detect --sessions <host:port> --sessions-ca <ca.pem>
```

The default view loads no operational configuration, runs no tools, contacts no
host, and writes no state. It lists only compiled Orka and explicit Kagent
create adapters plus the **eval-only agentsessions integration**, not the
research candidates below. Per-operation support and refusal reasons describe
compiled command behavior, independently of detection and exact-version evidence.
The catalog is diagnostic composition, not a new lifecycle registry or SDK.

`--detect` reads controller Deployments in **`orka-system`**, not workload
namespaces, using the existing `--context`, `KUBE_CTX`, saved `kmx ctx`, or kind
context resolution. It uses an already available kubectl: no tool download,
installation, Helm mutation or cluster creation. A labelled controller is
`present`; no matching controller in that successful scoped list is `absent`.
Only the exact pinned controller image digest identifies the qualified version;
a familiar tag, foreign image digest, sidecar image or unavailable image leaves
version unknown. Presence proves neither readiness nor a successful workload,
and absence in this namespace says nothing about other namespaces.

`--sessions` independently reads at most one Session page, discarding returned
metadata. It does not load Kubernetes configuration unless `--detect` is also
requested. Non-loopback connections require verified TLS; `--sessions-ca`
supplies a private CA and also enables TLS on loopback. Plaintext is limited to
literal loopback. A readable Sessions API proves no host version, chat harness,
model, registration or image identity. **HarnessRegistry is not probed.**

Every requested read failure is `unreadable`, with a typed code and fixed,
actionable diagnostic; it is never absent, unsupported-version evidence or
permission to fall through. The command emits the complete report, including
independent successful probes, then exits nonzero if any requested probe is
unreadable. Authentication, RBAC, timeout, network and malformed-response
failures do not echo external bodies. Unrequested reads are `not-probed`.
Kagent stays not-probed: only explicit create validates its exact installation.

This view does **not select a runtime** or change existing command defaults. It
can show both Orka and Sessions without preferring either. Multi-runtime
lifecycle selection and durable target bindings remain separate contract work.

## Revision-pinned support and qualification matrix

**Evidence snapshot: 2026-10-10.** Versions below have different support scopes,
not uniform parity. Native readiness is never semantic-result proof. Compiled
capabilities are reported by `kmx targets`; the remaining entries are research
pins, **not supported adapters**. Upstream release pins identify material to
qualify, not completed installation or conformance evidence.

### Compiled paths

| Implementation | Exact source / immutable artifact evidence | Preparation owner | Supported KMX operations and evidence limits |
|---|---|---|---|
| Orka reference adapter | [v0.2.0 source](https://github.com/orka-agents/orka/tree/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1); release chart SHA-256 `b7596c4e35d7189a3b2cf25921cd50e6c31328dbb83bdee50e20c757e0f03b79`; controller `ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370` | Operator or existing explicit KMX Orka environment path; targets does neither | Render, deploy/reconcile, status, evaluation, chat, one-shot run, native workload retire. Retire deletes/releases owned workload resources; it is not retained registration retirement. Required Orka CI proves a native result, not every optional capability. |
| Kagent exact-v0.10.2 create adapter | [release source](https://github.com/kagent-dev/kagent/tree/68df64f671800c37c4204d81ebe0dd66ec35d223); controller OCI index SHA-256 `6adeef9ac70056e5871773a78233a77f12d1357f9f3484aaee59fe8abc6450b9`; official CRD/chart OCI digests are pinned in [CI](../.github/workflows/ci.yml) | Operator; CI installation is an external test precondition, not a KMX installer | Render and explicit new-object create only, optionally one A2A turn within create. No standalone run, chat, discovery, status, evaluation, retirement, reconciliation, diff or rollback. Required create CI checks exact controller/commit/images, admission and a real answer. |
| agentsessions eval integration, not a lifecycle adapter | Go module/local reference `v0.1.3-0.20261008190619-e680ea10d3b4`; [source](https://github.com/aramase/agentsessions/tree/e680ea10d3b4); daemon built from that exact module by [live eval CI](../scripts/ci/live-eval-loop.sh); its AIKit image is digest-pinned there | Operator supplies host/journal and matching model; KMX runs no infrastructure | Text-only evaluation, one Session per case; bounded read-only verification with the local reference chat harness and zero live model calls. Neither receipt nor verification attests the remote host/image/version or satisfies an Orka lift gate. Deploy/status/retire/re-lift remain unsupported. |

Orka and Kagent common bundle inputs produce a portable source digest; target
bindings and Secret references remain separate, and rendered resources have
their own digest. Sessions sends exact instructions in per-execution config and
checks the host-selected model; Session metadata does not select it. Its
receipts hold Session/journal/output identities, not prompts or answers.
KMX documents and receipts contain **Secret references only**, never credential
values. Model credentials remain operator/runtime-owned; execution config is
journaled and must not contain credentials.

For all three paths, a standalone `logs`, arbitrary `diff` or `rollback`
capability is not advertised. Orka status can report drift, but that is not a
new universal diff API. Targets reports the shared unsupported-operation
wording with reasons; it does not invoke unsupported commands. Historical
workload results and infrastructure cleanup are different authorities.

### Second-adapter promotion gate

[agentsessions #100](https://github.com/aramase/agentsessions/pull/100) merged at
`8d97860b4b9c923b4140b6f5bfcadb718d689014`: it provides a persisted **in-process**
HarnessRegistry, keyed by name/spec digest, with retained retirement and
same-spec reactivation. At that pin, `agentsessionsd` serves Sessions but **not
HarnessRegistry**. An operator-only admin listener is a prerequisite; startup
`--harness` flags and static built-in `chat` are not deployable registrations.

Promotion to a second real adapter stops until the admin API is served safely,
registration identity is linked to the exact source/suite member and immutable
image, and [shared conformance](https://github.com/kaimahi-agents/kaimahi/issues/352)
proves a bound Session result, restart/unknown outcomes, retained retirement and
reactivation. Descriptor IDs and recorded replay versions are host-reported,
not authentication or image attestation. AgentKit remote-harness profiles await
[its #34](https://github.com/orka-agents/agentkit/pull/34) and
[#35](https://github.com/orka-agents/agentkit/pull/35); arbitrary-harness verify
awaits [agentsessions #112](https://github.com/aramase/agentsessions/issues/112).
No runtime registry or public SDK is promoted by the target view.

### Unqualified candidate pins

Every entry below has **no compiled KMX lifecycle capabilities**. An operator
must prepare it; KMX does not install, publish its images or own its
infrastructure. Immutable installation artifacts, Secret-reference behavior,
revision/result binding, replay posture and workload-only cleanup must be
qualified before any support claim. Until then render/deploy/status/eval/chat,
logs/diff/rollback and cleanup are all unsupported by KMX for these candidates.

| Candidate / category | Pinned research source | Blocker and explicit stop rule |
|---|---|---|
| Kagent 1.0 alpha11 — agent runtime, unsupported candidate | [source `30e8a2c2ceb9a1fadc16e969aa7ee0107a42d7b2`](https://github.com/kagent-dev/kagent/tree/30e8a2c2ceb9a1fadc16e969aa7ee0107a42d7b2) | Alpha line remains unqualified; revisit at beta. Stop without immutable installation, native ready state, revision-pinned definition, documented execution and a result bound to that revision. No legacy create support is expanded. |
| KARS — agent/security platform with BYO-image support | [v0.1.26 `4288a3a00e88076a0c779c40fccb90790d4de6db`](https://github.com/Azure/kars/tree/4288a3a00e88076a0c779c40fccb90790d4de6db) | Requalify its current definition/image, policy and result APIs. Stop if cleanup can destroy shared/cloud infrastructure or exact input/result identity cannot be proven. |
| AX — image execution target | [v0.3.1 `e70162a34037c221fe6fadefd98308c05a4ad8f3`](https://github.com/google/ax/tree/e70162a34037c221fe6fadefd98308c05a4ad8f3) | Preview projector is not an adapter/image. Stop without an immutable instructions/model/image/result contract, logs/cancellation and scoped cleanup evidence. |
| OpenShell — sandbox/image execution target | [v0.1.2 `6648bd0c290efbc41ba131ee9831ee45cd431f94`](https://github.com/NVIDIA/OpenShell/tree/6648bd0c290efbc41ba131ee9831ee45cd431f94) | Sandbox access is not authored-agent revision/evaluation evidence. Stop without a complete qualified image input/result contract. |
| Mecatl — agent harness/runtime | [v0.0.41 `89316cd9ed449e2577344723b6df17dec4cde034`](https://github.com/stacklok/mecatl/tree/89316cd9ed449e2577344723b6df17dec4cde034) | Candidate one-shot runner only. Stop without immutable installation, revision-bound terminal results and workload-only cleanup. |
| Agent Runtime Operator — emerging Kubernetes runtime | [v0.28.1 `cc21d887c14121086a95c54c7122f6786001afed`](https://github.com/agentic-layer/agent-runtime-operator/tree/cc21d887c14121086a95c54c7122f6786001afed) | Watch/conformance probe, not support. Stop at schema/readiness-only evidence without a revision-bound execution result and scoped cleanup. |
| Agent Substrate — workspace/execution substrate | [v0.2.0 `10a1bfb2f58039608a0b8419379714945e4d65ac`](https://github.com/agent-substrate/substrate/tree/10a1bfb2f58039608a0b8419379714945e4d65ac) | Consume through Orka or another runtime. No peer adapter unless it independently proves the full authored revision, execution/result and cleanup lifecycle; preserve retained templates/snapshots. |
| Kubernetes Agent Sandbox — workspace/sandbox substrate | [v1.0.4 `810726d89c71da77cdc82668bca1b00f5cd21ed8`](https://github.com/kubernetes-sigs/agent-sandbox/tree/810726d89c71da77cdc82668bca1b00f5cd21ed8) | Consume beneath a runtime; sandbox readiness alone supplies no agent/evaluation semantics. Stop without independent complete lifecycle evidence. |

AgentKit, ADK, Microsoft Agent Framework, LangGraph, CrewAI, Dapr Agents,
AgentScope and Agno are frameworks/harness libraries, not automatically lifecycle
targets. Foundry, Copilot, Ollama, KServe, vLLM and KAITO are inference backends,
not runtime adapters. See [#238](https://github.com/kaimahi-agents/kaimahi/issues/238)
for qualification ownership and [the Substrate evaluation](reviews/2026-09-10-substrate-evaluation.md)
for the workspace boundary.

## Terms and ownership

| Term | Meaning | Owner |
|---|---|---|
| **Agent** | The named intent and configuration a developer wants to run | Developer and source control |
| **Runtime** | The platform that discovers and executes an Agent | Runtime adapter and platform |
| **Context** | Runtime, cluster context, namespace, and Agent name; adapters may add observed kind and UID | KMX target selection and runtime discovery |
| **Session** | A connected interaction with one resolved Agent across turns | Runtime implementation |
| **Inference provider** | The model endpoint or host strategy used for a turn | Agent/environment configuration |
| **Lifecycle** | Create, render, deploy, inspect, evaluate, diff, and recover operations | KMX orchestration over runtime-specific operations |
| **Enforcement** | Isolation, policy, authorization, and governance applied during execution | Selected platform and surrounding infrastructure |

KMX must show which context it will read or mutate. A friendly Agent name is not
enough to identify a target, and an unreadable target is not the same as an absent
Agent.

## Adapter contract implemented today

`internal/kmx/runtime` is a platform-neutral session and lifecycle contract. No
Kubernetes, terminal, or vendor SDK types cross the boundary.

A session-capable adapter:

1. Has a stable runtime identity.
2. Probes a target and returns found, absent, or an error. Some compatibility
   runtimes defer final existence and readiness checks to `Connect`.
3. Opens a Session for the resolved Agent.

Discovery errors never trigger fallback to another runtime with a same-named
Agent. Automatic selection has an explicit preference order; callers can select a
runtime directly when ambiguity is unacceptable.

A Session exposes:

- the adapter's Agent reference and the context fields it resolved;
- capabilities such as streaming, resume, approvals, tool editing, Agent
  switching, lift, and inference selection;
- runtime-specific commands;
- connection status and typed events;
- `Send` and `Close` lifecycle operations.

Events are observations, not completion receipts. Only a successful `Send` return
means the runtime's terminal-state checks completed successfully. Terminal input
and presentation stay outside the Session contract.

The chat/session registry contains Orka alone. It preserves explicit namespace
rules and Orka-first automatic discovery; Kagent is not registered for
discovery or sessions. Its lifecycle-only adapter is instantiated directly by
explicit create. A test runtime still ensures session capabilities and commands
do not leak between implementations.

## Inference provider contract

Runtime and inference are independent choices. A runtime may execute through its
configured Provider, while a host inference strategy may use Foundry or Copilot.
The model strategy receives resolved instructions and tools; it does not discover
the Agent's runtime. Unknown inference modes fail instead of silently selecting a
different execution path.

Status and evidence must name both choices. A successful answer through host
inference does not prove that a native runtime task executed.

## Lifecycle contract

The shared adapter is a capability-gated session and lifecycle boundary, not a
universal Agent CRUD or manifest-conversion API. Each adapter instance declares
the lifecycle verbs it can execute; unsupported verbs return a typed refusal.
The configured Orka adapter implements render and deploy, while Orka status and
revision-bound evaluation do not require create-time configuration. The
configured Kagent adapter declares only render and deploy, where deploy means
the exact v0.10.2 create-only sequence: it proves the selected controller watches
the target namespace and has the required namespaced RBAC, binds server admission
and later live specs to the reviewed intent, and refuses reconciliation,
adoption, or same-name generated-child collisions.
Its status and evaluate verbs return typed unsupported results, and `Open`
refuses chat.

| Runtime | Render | Deploy/create | Status | Evaluate | Session/chat |
|---|---|---|---|---|---|
| Orka (default) | yes | reconcile on supported Orka paths | yes | yes | yes |
| Kagent v0.10.2 (explicit create only) | yes | new ModelConfig then new Agent; no adopt/update/rollback | no | no | no |

The broader lifecycle direction is tracked in
[#194](https://github.com/kaimahi-agents/kaimahi/issues/194):

- behavior-defining inputs produce an immutable revision digest;
- deployments point to accepted revisions;
- deploy, promote, and restore operations produce receipts;
- evaluation evidence is associated with the revision it tested;
- status and diff compare desired, deployed, and runtime-observed state;
- rollback deploys and verifies an earlier revision but cannot undo completed
  external actions.

Built-in lifecycle adapters advertise capabilities and the extensions whose
behavior they consume. Before rendering, `PreparePortableRender` validates the
exact authored source and refuses every behavior field from an extension the
target does not consume. An adapter also declares whether it honors portable
core `spec.coordination.allowedAgents`; preparation refuses it when unsupported
and binds that choice to the prepared document. Orka supports it; Kagent does
not. `LifecycleAdapter.Render` accepts only that prepared, target-bound
document; missing or wrong-target preparation is refused. An
extension's `apiVersion` alone does not add behavior. Unsupported lifecycle
verbs return explicit errors. Git and the selected runtime are the initial state stores; this
contract does not require a KMX server or controller.

### Portable revision and target bindings

A newly authored agent has a Git-friendly bundle directory at
`agents/<name>/` by default. `agent.yaml` is a closed, versioned portable
revision with zero or one runtime extension. Absent `extensions` and
`extensions: {}` are core-only; `extensions: null` is refused. Its **exact
bytes**, including comments and whitespace, are hashed for the portable
digest; even a formatting-only edit creates a new revision. Unknown fields and
multiple runtime extensions are errors, not ignored settings.

For Orka, behavior-defining inputs remain name, optional description,
instructions, model name, portable named-helper coordination, tools, skills,
and Provider/Agent rate limits. Legacy Orka coordination can additionally
state Orka-specific enabled and limit settings, but cannot coexist with core
coordination. A nonempty core helper list enables only those same-scope names;
Kagent refuses core coordination instead of silently discarding it. For
Kagent, they are name, required description, instructions, model name,
declarative runtime (`go|python`), and at most one explicit same-namespace MCP
server/tool allowlist. Runtime identity and the model provider are separate:
the Kagent extension chooses the execution runtime, while target bindings choose
`openai|anthropic` and its endpoint/Secret reference.

`bindings.yaml` records **only the creation target**: namespace, model-provider
type and endpoint, and the name and key of a separately provisioned Secret. It
holds references, never credential values. Neither bindings document changes
the portable digest. Rendering combines the portable revision with explicit
target bindings; the resulting resources and rendered digest do reflect them.

For Orka, later lift obtains another target's bindings from its flags and local
state. Kagent bundles currently have no lifecycle consumer beyond create: lift,
retire, status, evaluate, console bundle operations, and interactive `/lift`
intentionally refuse them.
Kagent create also omits `eval/example.yaml`. Its rendered artifact contains a
review-only Secret skeleton followed by ModelConfig and Agent and must not be
bulk-applied. A successful online create writes a private mode-0600 receipt with
cluster/resource identities and digests, never prompt or answer text; that
receipt does not expand the adapter's capabilities.

## AX preview activity source

`ax-harness/` contains a bounded, synthetic-tested OpenCode child-event
projector. It is not embedded in kmx, run by a Task command, built into a
signed image, registered as an AX adapter or authorized as a runtime
result reader. The [activity protocol](../ax-harness/README.md) describes the
safe JSONL boundary and its limits; AX lift and status remain unavailable.

## Enforcement contract

KMX is not a generic enforcement plane. It names mutation targets, obtains
consent, keeps credentials out of generated artifacts, and reports observed
evidence. The selected platform owns runtime enforcement such as policy,
isolation, authorization, tool execution controls, and governance.

Compatibility components can enforce narrower boundaries, such as the retained
model-traffic bridge's credentials, caps, and accounting. Those controls must be
described at that boundary and must not be presented as governance of the whole
Agent or application.

There is no shared `Enforcer` interface today. A future enforcement contract must
come from concrete common operations across runtimes rather than wrapping one
implementation in a generic name.

## Composition boundary

Chat registration currently lives in `app/runtime_registry.go`; typed session
events are bridged to the existing renderers by `runtime_session.go`. Orka's
lifecycle composition is in `runtime_orka_lifecycle.go`. The Kagent adapter in
`runtime_kagent_lifecycle.go` is composed only by the explicit create path; it
does not enter the chat registry. Native platforms remain responsible for their
protocol, cancellation, history, retention, and approval semantics.

Moving implementations into standalone packages can happen without changing the
contract. Catalogue digests and deployment receipts remain lifecycle concerns,
not chat Session fields.

## Experimental wider interface model

`pkg/kmx` contains an alpha, unwired contract model. It does not replace the
production `internal/kmx/runtime` contract. It exposes only northbound workflows
and caller-facing values:

- `AgentEnvironment.Up` and `Down` compose target and runtime setup;
- `AgentSuites` validates/packages OCI definitions and derives sandbox images;
- `AgentDeployments.BuildRevision`, `Lift`, `Status`, and `Retire` manage placements;

Implementation ports, runtime-native artifacts, deploy options, recovery SPIs,
and receipt factories live in `internal/kmx/lifecycle`. Public
`AgentEnvironment.Down` accepts `DownRequest`; internal
`PlatformDeprovisioner.Deprovision` accepts `DeprovisionRequest`. Both carry the
same scoped `InfrastructureReceipt`. Retirement separately accepts
`RetireRequest` carrying `DeploymentReceipt` and has no target teardown
authority.

Read the [lifecycle decision](kmx-lifecycle-interfaces.md) for rationale and the
[application API](kmx-application-api.md) for current signatures, examples,
persistence boundaries, and internal port placement. Concrete orchestration and
adapters remain follow-up work.
