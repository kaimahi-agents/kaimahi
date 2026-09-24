# Runtime contract

KMX separates agent intent from the system that executes it. The vocabulary in
this document is the contract between the developer experience and runtime
implementations; it prevents platform identity, model choice, cluster location,
and policy from collapsing into one ambiguous "provider" concept.

## Terms and ownership

| Term | Meaning | Owner |
|---|---|---|
| **Agent** | The named intent and configuration a developer wants to run | Developer and source control |
| **Runtime** | The platform that discovers and executes an Agent | Runtime adapter and platform |
| **Context** | Runtime, cluster context, namespace, and Agent name; adapters may add observed kind and UID | KMX target selection and runtime discovery |
| **Session** | A connected interaction with one resolved Agent | Runtime implementation |
| **Inference provider** | The model endpoint or host strategy used for a turn | Agent/environment configuration |
| **Lifecycle** | Create, render, deploy, inspect, evaluate, diff, and recover operations | KMX orchestration over runtime-specific operations |
| **Enforcement** | Isolation, policy, authorization, and governance applied during execution | Selected platform and surrounding infrastructure |

KMX must show which context it will read or mutate. A friendly Agent name is not
enough to identify a target, and an unreadable target is not the same as an absent
Agent.

## Runtime IDs

There are three IDs, and `auto` is not one of them — it is a selection policy,
so `--runtime` defaults to empty rather than to a word that looks like a
runtime.

| ID | What it is | Registered in this build |
|---|---|---|
| `orka` | Native `core.orka.ai/v1alpha1` Provider + Agent, the first-class runtime for this CLI | Yes: chat, list, render, deploy, status |
| `kagent` | The **legacy** kagent runtime, pinned at **v0.10.1**, installed from fixed `v1alpha2` manifests | Yes: chat, list, status only |
| `kagent-v1` | kagent v1's `kagent.dev/v1alpha3` platform | **No adapter.** Detection recognizes it; nothing implements it — see [kagent v1](#kagent-v1-is-detected-not-implemented) |

`kagent` and `kagent-v1` are different runtimes sharing one API group. Legacy
kagent is **explicit-only**: it is never auto-detected, so it can never be
mistaken for kagent v1.

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

The current registry includes Orka and kagent. It preserves explicit namespace
rules and Orka-first automatic discovery. A third runtime is exercised in tests to
ensure capabilities and commands do not leak between implementations.

## Platform detection, and what it intentionally changed

Chat keeps Agent-level auto behavior unchanged: `agent chat --runtime
auto|orka|kagent` probes for a named Agent, Orka first.

The read commands select a *platform installation* instead, and that is an
intentional break with the old bare defaults:

- `kmx agent list` and `kmx status` with no `--runtime` inspect installed API
  resources once — Orka's `agents.core.orka.ai`, then kagent v1's exact
  `kagent.dev/v1alpha3` `AgentTemplate` — and select Orka when both are
  installed.
- **A bare `kmx agent list` no longer lists legacy kagent, and a bare
  `kmx status` no longer reports it.** On a legacy-only cluster — including a
  `kmx quickstart` or `kmx up` cluster — both now fail with the same named
  install error rather than silently selecting legacy kagent:

  ```text
  no supported runtime platform is installed: install orka (core.orka.ai Agent CRD)
  or kagent-v1 (kagent.dev/v1alpha3 AgentTemplate CRD).
    The legacy kagent runtime is not detected and is selected only explicitly,
    with `--runtime kagent`
  ```

  Add `--runtime kagent` to get exactly the previous output. `kmx up` already
  does this for the status it prints when it finishes.
- A detection **read error** is reported as a read error. It never becomes
  "not installed", and it never falls through to another runtime.
- kagent v1 detection asks for `/apis/kagent.dev/v1alpha3` directly, which is
  the only discovery shape that can tell "AgentTemplate is served at
  v1alpha3" apart from "something under `kagent.dev` is served at some other
  version" — legacy kagent's `v1alpha2` lives in the same group.

Explicit `--runtime kagent` keeps the combined `kmx status` table, its
governance/Ollama/MCP/certificate sections and its JSON `items` shape
byte-compatible, and keeps `agent list`'s legacy rows and fixed namespace.
Legacy kagent refuses any other namespace rather than reading past it.

After detection Orka still requires the namespace it watches, so a detected
Orka `list` or `status` names the platform it selected and the flag it needs
instead of guessing. `kmx status` also needs `--agent`, because lifecycle
`Status` takes one runtime-qualified `AgentRef` and a namespace alone cannot
identify a workload. Those refusals happen before anything is collected: no
partial table or JSON is printed, and nothing then claims the ancillary
sections were checked.

## Lifecycle contract

`LifecycleAdapter` sits beside the chat `Adapter` and embeds it, so both share
one `AgentRef` identity and one registry:

```go
type LifecycleAdapter interface {
    Adapter
    Capabilities() Capabilities
    Render(context.Context, PortableAgent, RenderOptions) (RenderedBundle, error)
    Deploy(context.Context, RenderedBundle, DeployOptions) (AgentRef, error)
    Status(context.Context, AgentRef, StatusOptions) (LifecycleStatus, error)
    Evaluate(context.Context, AgentRef, EvaluationRequest) (EvaluationReceipt, error)
}
```

This is **not** a universal Agent CRUD, manifest conversion, deployment, or
evaluation API. `Capabilities` is a **static declaration**, not plugin
negotiation. A verb a runtime declares unsupported is refused instead of
attempted:

| Runtime | Render | Deploy | Status | Evaluate | list | show |
|---|---|---|---|---|---|---|
| `orka` | yes | yes | yes | no | yes | yes (no `--runtime` flag yet) |
| `kagent` | no | no | yes | no | yes | no |

Orka's `Evaluate` is permanently unsupported: a native Orka Task supplies no
frozen target revision, and kmx will not fabricate one. Legacy kagent's
`Render`, `Deploy` and `Evaluate` are permanently unsupported: it is installed
from fixed manifests and implies no portable conversion.

`list` and `show` are presentation, not lifecycle verbs. They are app-owned
handlers registered by the same runtime ID, so no capability flag governs
them — but a missing handler returns the same shared error.

There is exactly one error type for all of this, recovered with `errors.As`
rather than by matching a message. It prints plainly:

```text
runtime kagent does not support render
```

An ID kmx does not name at all is a separate typed `unknown runtime "…"`,
never a fallback to a different runtime. A runtime it DOES name but this
build does not implement — `kagent-v1` — is not that: it declines the verb
that was asked for, through the same shared error above, so an operator whose
spelling was right is not told it was wrong.

`LifecycleStatus` keeps pair status and optional instance status side by side
and **publishes no merged readiness boolean**. Legacy kagent has no
template/instance split, so it fills the pair fields with the one retained
runtime slice (`kagent: N/M pods ready, R restarts`) and leaves instance nil.
That slice and the `items` array `kmx status` publishes come from one combined
read, so the printed counts and the published objects are the same moment.

The broader lifecycle direction is tracked in
[#194](https://github.com/kaimahi-agents/kaimahi/issues/194):

- behavior-defining inputs produce an immutable revision digest;
- deployments point to accepted revisions;
- deploy, promote, and restore operations produce receipts;
- evaluation evidence is associated with the revision it tested;
- status and diff compare desired, deployed, and runtime-observed state;
- rollback deploys and verifies an earlier revision but cannot undo completed
  external actions.

Built-in lifecycle adapters advertise capabilities and return explicit
unsupported results. They must preserve platform-specific fields they do not
understand. Git and the selected runtime are the initial state stores; this
contract does not require a KMX server or controller.

## The portable agent document

`--file` supplies a closed `kmx.kaimahi.dev/v1alpha1` `PortableAgent`:
`metadata.name`, `spec.instructions`, `spec.model.name`, and optional
`extensions.orka` / `extensions.kagent` blocks that each require their target
`apiVersion` and namespace.

Decoding is strict and fails rather than dropping anything. Unknown fields,
duplicate YAML keys, a second document in the stream, an inline credential
value, a runtime/extension mismatch and an inexact tool or skill identity are
all refused. Lossy mappings are errors, not warnings, so a render's loss list
is normally empty.

`kmx agent create --file` requires the name argument to match the document's
`metadata.name`, and every portable-defined input then conflicts with its
flag (`--namespace`, `--description`, `--provider-type`, `--model`,
`--secret`, `--secret-key`, `--base-url`, `--instructions`, `--tools`,
`--skills` and the four rate-limit flags). Flags that say what to *do* with
the document rather than what it says — `--task`,
`--result-service-account`, `--orka-api-service`, `--result-port`, `--out`,
`--no-apply`, `--dry-run` and Orka's `--schema-target` — stay legal. The
existing zero-argument Orka wizard is unchanged, and `--file` never falls
through to it.

## Two digests, never equivalent

A rendered bundle carries two lowercase 64-hex SHA-256 digests, and they
answer different questions. Both use the same catalogue framing —
`path + " " + decimal_length + "\n" + bytes + "\n"`.

- **Portable bundle digest** frames the exact validated portable source bytes
  once, under `portable-agent.yaml`. It identifies **authored behavior**. The
  Orka shorthand flags are deterministically encoded into a portable document
  first and framed under that same path, so a flag-built create has the same
  kind of identity as a `--file` one.
- **Rendered bundle digest** frames every rendered document in order under
  `rendered/000.yaml`, `rendered/001.yaml`, … and hashes the concatenation. It
  identifies **exact adapter output**.

Orka's optional random Task changes the rendered digest and leaves the
portable digest alone, because the portable document never contained it.

`RenderedBundle` is immutable after construction and distinguishes the full
artifact from what may be written. `Documents()` is every rendered byte in
artifact order — including Orka's value-free Secret skeleton, which belongs to
the artifact and the digest. `DeployDocuments()` is the explicit subset Deploy
applies. **Never infer bulk-apply safety from artifact order**: that skeleton
names a prerequisite an operator provisions separately, and applying it would
create an empty credential.

## Inference provider contract

Runtime and inference are independent choices. A runtime may execute through its
configured Provider, while a host inference strategy may use Foundry or Copilot.
The model strategy receives resolved instructions and tools; it does not discover
the Agent's runtime. Unknown inference modes fail instead of silently selecting a
different execution path.

Status and evidence must name both choices. A successful answer through host
inference does not prove that a native runtime task executed.

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

`app/runtime_registry.go` is the composition root. It registers Orka and legacy
kagent, preserves Orka-first auto selection for chat, and owns the
Kubernetes-aware halves of platform detection. The neutral package receives only
installed/error results; it never imports Kubernetes and never shells out. New
runtimes register discovery, construction, lifecycle and their presentation
driver here.

`runtime_orka.go` routes Orka create through the seam without changing a byte
it renders or the order it deploys in. Render delegates to the existing
scaffold generator, and to the pinned schema validator only for offline
artifacts — an online create validates against the target cluster's installed
CRDs with no fixture fallback, inside the guarded staged deploy where that has
always run. Deploy consumes only the bundle's deploy documents and keeps the
mutation guard, collision checks, Secret-key proof, strict server dry-runs,
artifact emission and Provider → Ready → Agent → Ready → optional Task/result
ordering.

`runtime_kagent.go` wraps the legacy runtime's existing install, list,
combined-status and chat mechanics without changing any of them.

`runtime_session.go` bridges typed events to the existing renderers. Shared
timeline and scanner commands come from the backend; Orka tool/lift commands
are no longer hardcoded in those drivers. Configuration pickers remain
application UI coordinators, outside Session. The legacy kagent Session keeps
its native session IDs, history, resume and HITL continuation, and its own
terminal driver still owns history/resume/governance commands and supplies the
approval callback.

`runtime_inference.go` accepts resolved prompt/tools and an injected tool
executor. Copilot and Foundry model strategies no longer perform Orka
discovery. Unknown inference modes fail instead of silently selecting native
execution.

`kmx up` and `kmx orka install` are installation, not lifecycle verbs, and are
unaffected. `lift`, `migrate` and the governance plane stay outside the seam.
Native platform and compatibility drivers remain responsible for their existing
protocol, cancellation, history, and approval semantics.

Moving implementations into standalone packages can happen without changing the
contract. Catalogue digests and deployment receipts remain lifecycle concerns,
not chat Session fields, and platform editors must preserve fields they do not
understand.

## kagent v1 is detected, not implemented

`kagent-v1` has no adapter in this build. Detection knows the platform so that
a v1 cluster is not misreported as "nothing installed", but selecting the ID —
explicitly or by detection — reports that it is unknown or that the verb is
unsupported. **There is no kagent v1 authoring, deploy, status, chat,
evaluation or receipt path here, and the portable document's `kagent`
extension is validated but not yet rendered by anything.**

That is deliberate. kagent `v1.0.0-alpha2` was installed and exercised on a
dedicated throwaway cluster to decide whether to build the adapter. Install,
a `v1alpha3` `ModelConfig`, a digest-pinned runtime image, an
`AgentTemplate`/`Harness` pair reaching `Ready=True` on its golden snapshot,
and an `AgentInstance` reaching `READY` through the upstream CLI all worked.
The A2A `invoke` call did not: the controller rejected its own chart's
documented runtime address, requiring an actor DNS authority under
`*.actors.resources.substrate.ate.dev` that the pinned Substrate **v0.1.0**
used for that proof does not serve. kagent alpha2 is built against Substrate
`v0.2.0-beta5`, and that skew is load-bearing on exactly this path.

Without a working evaluation path there is nothing to verify an adapter
against, so no speculative v1 behavior was merged. The proof is external and
not committed; alpha2 remains proof setup, not a supported install target.

## Tests

Tests exercise a third, non-Kubernetes runtime with typed events and its own
`/inspect` command through scanner and timeline dispatch, verifying no Orka
commands leak. Existing kagent session, streaming, HITL and governance tests
exercise its migrated connect/send path. The registry's unique-ID validation
and no-fallback-after-read-error resolution, the platform-selection policy,
the strict portable decoder, both digests and the shared unsupported-verb
error are covered in `internal/kmx/runtime`; the detector matrix, the
explicit-`kagent` compatibility output and the flag matrix are covered in
`internal/kmx/app` and `cmd/kmx`. A committed golden pins the Orka no-Task
bundle's exact bytes, and it stayed green through every adapter change.
