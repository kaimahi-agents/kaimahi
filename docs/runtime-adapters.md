# Runtime contract

KMX separates agent intent from the system that executes it. The vocabulary in
this document is the contract between the developer experience and runtime
implementations; it prevents platform identity, model choice, cluster location,
and policy from collapsing into one ambiguous "provider" concept.

Orka is the first-class runtime for the current create and lift workflow. It
owns agent execution, orchestration, and platform governance. KMX owns the
developer experience and lifecycle around it. The selected platform, not a
generic KMX control plane, owns enforcement.

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

`internal/kmx/runtime` is a platform-neutral chat/session contract. No Kubernetes,
terminal, or vendor SDK types cross the boundary.

An adapter:

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

The current registry contains Orka alone. It preserves explicit namespace rules
and Orka-first automatic discovery; the ordered walk remains a walk so a second
platform can be registered without the caller learning about it. A third runtime
is exercised in tests to ensure capabilities and commands do not leak between
implementations.

## Inference provider contract

Runtime and inference are independent choices. A runtime may execute through its
configured Provider, while a host inference strategy may use Foundry or Copilot.
The model strategy receives resolved instructions and tools; it does not discover
the Agent's runtime. Unknown inference modes fail instead of silently selecting a
different execution path.

Status and evidence must name both choices. A successful answer through host
inference does not prove that a native runtime task executed.

## Lifecycle contract

The shared adapter above is currently a chat/session boundary. It is **not** a
universal Agent CRUD, manifest conversion, deployment, or evaluation API.

The broader lifecycle direction is tracked in
[#194](https://github.com/kaimahi-agents/kaimahi/issues/194):

- behavior-defining inputs produce an immutable revision digest;
- deployments point to accepted revisions;
- deploy, promote, and restore operations produce receipts;
- evaluation evidence is associated with the revision it tested;
- status and diff compare desired, deployed, and runtime-observed state;
- rollback deploys and verifies an earlier revision but cannot undo completed
  external actions.

Built-in lifecycle adapters should advertise capabilities and return explicit
unsupported results. They must preserve platform-specific fields they do not
understand. Git and the selected runtime are the initial state stores; this
contract does not require a KMX server or controller.

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

Runtime registration currently lives in `app/runtime_registry.go`; typed session
events are bridged to the existing renderers by `runtime_session.go`. Native
platform and compatibility drivers remain responsible for their existing
protocol, cancellation, history, and approval semantics.

Moving implementations into standalone packages can happen without changing the
contract. Catalogue digests and deployment receipts remain lifecycle concerns,
not chat Session fields.
