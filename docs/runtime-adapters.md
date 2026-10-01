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
