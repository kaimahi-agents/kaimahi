# KMX lifecycle interface decision

**Status:** Experimental design evidence

**Scope:** The alpha contracts in [`pkg/kmx`](../pkg/kmx). They are not wired to
the CLI, do not replace `internal/kmx/runtime`, and make no compatibility
commitment.

**Companion design:** [KMX application workflow API](kmx-application-api.md)
describes how another Go layer, UI, controller, or transport could consume these
contracts through grouped use-case services. That facade is proposed, not
implemented.

## Context

KMX needs one understandable developer lifecycle across more than one runtime
without turning every command into branches over concrete runtimes, Kubernetes
resources, cloud APIs, subprocesses, and terminal behavior.

The production code already demonstrates useful parts of this boundary:

- a strict portable agent document and exact source digest;
- immutable rendered output with a separate rendered digest;
- an internal lifecycle adapter used by Orka and narrow Kagent create support;
- explicit target pinning and ownership checks;
- separate workload retirement and cloud teardown paths.

It also shows the coupling this decision addresses. Runtime composition remains
inside the broad application package, lifecycle-only implementations must
satisfy chat methods, and lift, status, evaluation, and retirement still contain
runtime-specific orchestration. A wider contract should reduce that coupling
without pretending every runtime has identical behavior.

## Decision

The initial model has four concepts:

```text
Agent revision -> Target -> Runtime -> Deployment
```

| Concept | Responsibility |
|---|---|
| Agent revision | Immutable authored behavior identified by a digest |
| Target | Durable destination identity supplied or resolved by a platform |
| Runtime | Builds, deploys, observes, and retires runtime-native workloads |
| Deployment | One revision bound to one runtime installation on one target |

The guiding relationship is:

> A platform provides a target. A runtime turns an agent revision into a
> deployment on that target. KMX composes those operations without transferring
> ownership authority between them.

## Naming choices

### KMX is the namespace

The package name already establishes ownership, so exported names remain concise:

```go
kmx.AgentRevision
kmx.TargetRef
kmx.RuntimeRef
kmx.DeploymentRef
```

They are not repeated as `KMXAgentRevision` or `KMXTargetRef`. KMX remains
explicit at external boundaries: package path, CLI, API group, labels,
documentation, and any future protocol or media-type names.

### Target is the durable abstraction

`Target` is the contract. `local`, `staging`, and `production` are friendly
configuration names, not target kinds or durable identity.

The model deliberately does not define:

```text
local  == kind
remote == AKS
```

Local and remote describe topology from a caller's perspective. Kind, AKS, and
an existing cluster are possible platform implementations. A local target may
later use another implementation, and a remote target may be AKS, another
managed service, or a cluster KMX does not own.

`TargetRef` is therefore qualified by `PlatformID` and `TargetID`. The friendly
name remains in configuration and snapshots, so renaming an alias does not
change durable identity.

### Platform and runtime are independent

Within this contract, platform answers **where the destination comes from** and
runtime answers **how the agent executes there**. This is narrower than informal
uses of "platform" elsewhere in the project.

```text
Platform                   Runtime
--------                   -------
resolve target             install on target
provision target           build native artifact
inspect target             deploy artifact
deprovision target         observe deployment
                           retire deployment
```

This permits the same runtime on a locally provisioned target or an existing
remote target. Model inference remains another independent binding; it is not a
platform or runtime selector.

### Deployment is not a run

A deployment is a durable agent revision installed through a runtime on a
target. A run is one invocation, task, or conversation against that deployment.
The first interface version models deployment lifecycle only. Run start,
observation, and cancellation should be added separately when common semantics
are proven.

## Interface layers

### Public workflows use product verbs

The author-facing interfaces use KMX workflow language:

```go
type TargetService interface {
    Up(context.Context, TargetSpec) (InfrastructureReceipt, error)
    Register(context.Context, TargetSpec) (TargetRef, error)
    Inspect(context.Context, TargetRef) (TargetSnapshot, error)
    Down(context.Context, InfrastructureReceipt) (TeardownReceipt, error)
    Forget(context.Context, TargetRef) error
}

type AgentService interface {
    Lift(context.Context, LiftRequest) (DeploymentReceipt, error)
    Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
    Retire(context.Context, DeploymentReceipt) (RetirementReceipt, error)
}
```

The verbs form separate lifecycle pairs:

| Scope | Establish | Remove |
|---|---|---|
| Target infrastructure | `Up` | `Down` |
| Agent deployment | `Lift` | `Retire` |
| Local target configuration | `Register` | `Forget` |

`Forget` is deliberately not `Down`: forgetting a bring-your-own target removes
local configuration and never mutates the target.

### Runtime uses narrow capabilities

A runtime is not only a builder. Runtime-specific translation is one capability,
followed by deployment, observation, and optional retirement.

The SPI therefore uses narrow interfaces:

```text
RuntimeInstaller
RuntimeBuilder
RuntimeDeployer
RuntimeObserver
RuntimeRetirer
```

This avoids forcing a create-only runtime to implement fake status, retirement,
or chat methods. An implementation satisfies only capabilities it supports.
Support can be discovered through Go interface assertions; a separate
capability list is omitted because it could disagree with the implemented
method set.

### Lift is a workflow, not one runtime primitive

Lift composes several operations:

```text
resolve target and runtime installation
    -> build runtime-native artifact
    -> deploy or reconcile it
    -> observe the result
    -> persist deployment evidence
```

The runtime SPI exposes precise `Build`, `Deploy`, and `Observe` capabilities.
`AgentService.Lift` supplies the sticky product operation.

### Build has two levels

The model distinguishes:

```text
RevisionBuilder: authored source -> immutable AgentRevision
RuntimeBuilder:  AgentRevision + RuntimeRef + TargetBinding -> RuntimeBundle
```

The first operation is runtime-neutral. The second is implemented by the
selected runtime and may understand its native schema. `RuntimeBuildInput`
requires the runtime installation and target binding to identify the same exact
target before native building begins.

## Ownership and safety

### Receipts preserve authority boundaries

Receipts are separated by responsibility:

| Receipt | Records | Accepted by |
|---|---|---|
| `InfrastructureReceipt` | Target infrastructure provisioned by a platform | Target `Down` |
| `RuntimeReceipt` | Runtime installation or reconciliation | Runtime recovery or removal work |
| `DeploymentReceipt` | Agent revision, binding, artifact, and deployment identity | Agent `Retire` |
| `RetirementReceipt` | Result of workload retirement | Evidence consumers |
| `TeardownReceipt` | Result of target teardown | Evidence consumers |

The destructive signatures encode the primary safety rule:

```go
Down(context.Context, InfrastructureReceipt) (TeardownReceipt, error)
Retire(context.Context, DeploymentReceipt) (RetirementReceipt, error)
```

Neither operation accepts another independently supplied target or deployment
that could disagree with its receipt. The subject is derived from evidence in
the correct ownership domain. A deployment receipt cannot be passed to target
teardown.

Receipts are local evidence, not authentication or cryptographic attestation.
Before mutation, implementations must verify receipt identity and native
ownership against durable external state. Type separation prevents accidental
authority crossover; it does not make an edited local file trustworthy.

### Identity is computed, not asserted

The package computes canonical SHA-256 identities for:

- exact authored agent bytes;
- target-qualified, versioned binding bytes;
- exact runtime-native artifact bytes.

Callers cannot attach an arbitrary digest to these in-process values. A runtime
bundle retains the exact revision, runtime installation, target binding digest,
and rendered digest used to create it. A deployment receipt is derived from
that bundle rather than assembled from unrelated identity fields.

### Persistence is explicit

Values carrying source, binding, or native artifact bytes are in-process values
and reject JSON encoding and decoding. Their durable counterparts are references
and validated receipts.

```text
In process                 Persisted
----------                 ---------
AgentSource                authored source file
AgentRevision              AgentRevisionRef
TargetBinding              binding source file
RuntimeBuildInput          not persisted
RuntimeBundle              DeploymentReceipt
```

This prevents private fields from silently serializing as `{}` and makes the
cross-command storage boundary explicit.

### Absence and unreadability are distinct

`TargetAbsent` and `DeploymentAbsent` are successful observations. A target or
deployment that cannot be read because of authentication, authorization,
timeout, malformed response, throttling, or network failure returns an error.
It must not be reported as absent.

A mutation that may have reached the remote system but whose result cannot be
established returns `OutcomeUnknownError`. Callers inspect or reconcile; they do
not assume failure or automatically repeat a non-idempotent operation.

## Neutrality boundary

The public package contains no concrete runtime, Kubernetes, cloud, subprocess,
CLI, or terminal types. Concrete knowledge belongs in bounded implementation
packages and at the application composition root.

Architecture tests enforce:

- a standard-library-only production dependency closure;
- no imports from internal implementation packages;
- rejection of known concrete implementation names in exported and serialized
  surfaces;
- exact destructive workflow method sets and receipt-scoped signatures.

The name checks are guardrails for known coupling risks, not proof that every
future abstraction is neutral. Semantic review remains necessary.

## Initial scope

The first production proof should remain smaller than the full model:

1. Adapt one real Orka build, deploy, status, and retire path.
2. Add one in-memory platform/runtime fake and a shared base conformance suite.
3. Persist a deployment receipt and consume it from a later CLI process.
4. Prove retirement cannot invoke target or cloud teardown.
5. Validate common semantics against another real runtime before declaring them
   stable.

The package does not currently add CLI commands, provision AKS, install Kagent,
or widen Kagent beyond its documented create-only support.

## Deferred choices

The alpha contract deliberately defers:

- a universal client containing every lifecycle operation;
- dynamic plugins or downloadable runtime negotiation;
- capability-based scheduling before multiple runtimes prove common meanings;
- generic log, event, output, and artifact streams without consumers;
- rollback semantics that imply completed external actions can be undone;
- a universal cloud resource model;
- moving Azure implementation or changing historical teardown compatibility;
- treating inference providers as deployment targets;
- claiming runtime extensions are portable when another runtime cannot honor
  them.

These can be added from concrete use cases without changing the ownership
model.

## Alternatives considered

### Use local and remote as the main abstractions

Rejected because they describe topology, not stable identity, capabilities,
ownership, or teardown authority. `local` remains a useful target alias or
profile.

### Make kind and AKS public target types

Rejected because that would expose current implementations as permanent product
concepts and would not represent existing or future targets cleanly.

### Put the complete lifecycle on one Runtime interface

Rejected because runtimes support different operations. A large interface would
require unsupported stubs, repeating the coupling visible in the current
chat-plus-lifecycle adapter.

### Make Lifter a runtime interface

Rejected because lift is a KMX workflow that composes target resolution,
runtime-native building, deployment, observation, and receipt persistence. The
runtime supplies capabilities used by that workflow.

### Use Provider as the implementation term

Rejected because provider already means cloud provider, inference provider, and
an Orka resource. `Platform`, `Runtime`, and `Target` state the responsibilities
more precisely.

### Let retire and down accept refs plus receipts

Rejected because two independently supplied identities can disagree. Each
destructive operation derives its subject from the receipt in its own ownership
domain.

### Serialize every value

Rejected because source and native artifacts have different storage and
sensitivity requirements. Persisted references and receipts are explicit;
ephemeral build values fail instead of silently losing private state.

### Promote the complete interface directly into the CLI

Rejected because the design has not passed conformance against Orka, a fake,
and another real runtime. The package is reviewable design evidence first.

## Consequences

Benefits:

- generic workflows can remain independent of concrete runtime and platform
  mechanics;
- runtime differences are represented by optional capabilities rather than
  approximated behavior;
- durable identity crosses CLI invocations without exposing native resources;
- workload retirement and target teardown have separate, type-checked evidence;
- local and managed targets can share lifecycle vocabulary without sharing
  ownership rules.

Costs and risks:

- adapters require explicit translation between neutral and native identities;
- receipt verification remains implementation work and cannot rely on Go types
  alone;
- the public alpha package temporarily overlaps the production internal
  lifecycle contract;
- target, runtime installation, and deployment storage still need a production
  composition layer;
- names and state models may change after real conformance evidence.

## Maturity path

```text
Alpha contract model
    -> Orka and in-memory conformance
    -> durable lift/status/retire vertical slice
    -> second real runtime validation
    -> capability-specific suites and support matrix
    -> selective API stabilization
```

Only contracts demonstrated by real implementations and consumers should become
stable. The experimental package must not become permanent merely because it is
publicly importable.
