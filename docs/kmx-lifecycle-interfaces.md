# KMX lifecycle interface decision

**Status:** Experimental design evidence

**Scope:** The alpha northbound contracts in [`pkg/kmx`](../pkg/kmx) and focused
implementation-only ports under `internal/kmx`. They are not wired to the CLI,
do not replace `internal/kmx/runtime`, and make no compatibility commitment.

**Companion guide:** [KMX application API](kmx-application-api.md) is the
authoritative signature, example, persistence, and management-port reference.
This decision records rationale and invariants instead of repeating those
details.

## Context

KMX needs one understandable developer lifecycle across more than one runtime
without turning every command into branches over concrete runtimes, Kubernetes
resources, cloud APIs, subprocesses, and terminal behavior.

The production code already demonstrates useful parts of this boundary:

- a strict portable agent document and exact source digest;
- immutable rendered output with a separate rendered digest;
- an internal lifecycle adapter used by the default runtime and one narrow
  create-only integration;
- a draft AgentSuite specification for OCI-distributed definitions and derived
  runnable sandbox images;
- explicit target pinning and ownership checks;
- separate workload retirement and cloud teardown paths.

It also shows the coupling this decision addresses. Runtime composition remains
inside the broad application package, lifecycle-only implementations must
satisfy chat methods, and lift, status, evaluation, and retirement still contain
runtime-specific orchestration. A wider contract should reduce that coupling
without pretending every runtime has identical behavior.

## Decision

The initial model includes definition, shipping, destination, and deployment:

```text
Agent source -> Agent revision ------------------------+
                                                        |
AgentSuite source -> AgentSuite OCI artifact            |
                    -> Agent Sandbox Image --------------+-> Deployment
                                                        |
AgentEnvironment ---------------------------------------+
Target -> Runtime --------------------------------------+
```

| Concept | Responsibility |
|---|---|
| Agent revision | Immutable authored bytes identified by the portable digest |
| AgentSuite | OCI-distributed definition of agents, tools, compositions, and build profiles |
| Agent Sandbox Image | Runnable OCI image derived for one suite agent and platform |
| Environment | Established logical setup, without exact placement identity |
| Target | Durable destination identity supplied or resolved by a platform |
| Runtime | Builds, deploys, observes, and retires runtime-native workloads |
| Deployment | One revision or sandbox image bound to one runtime installation on one target |

The guiding relationship is:

> `AgentSuites` validates one immutable definition, builds the complete member-
> image set required by the initial image-backed execution profile, and pushes
> those identities without rebuilding.
> `AgentEnvironment` establishes a logical environment without selecting an
> exact target or runtime. `AgentDeployments` places an authored revision or
> sandbox image on separately resolved target/runtime identities without
> transferring authority between those concerns.

## Naming choices

### KMX is the namespace

The package name already establishes ownership, so exported names remain concise:

```go
kmx.AgentRevision
kmx.AgentSuiteArtifact
kmx.AgentSandboxImage
kmx.EnvironmentRef
kmx.TargetRef
kmx.RuntimeRef
kmx.DeploymentRef
```

They are not repeated as `KMXAgentRevision` or `KMXTargetRef`. KMX remains
explicit at external boundaries: package path, CLI, API group, labels,
documentation, and any future protocol or media-type names.

### Environment and target are different identities

`EnvironmentRef` identifies a logical setup such as `local`, while `TargetRef`
identifies an exact deployment destination. Replacing the physical destination
may preserve logical environment identity, but it must produce a different
target identity so old deployment or evaluation evidence cannot follow it.

The model deliberately does not define:

```text
local  == kind
remote == AKS
```

Local and remote describe topology from a caller's perspective. Kind, AKS, and
an existing cluster are possible environment implementations. A local
environment may later use another implementation, and a remote environment may
use AKS, another managed service, or a cluster KMX does not own.

`EnvironmentSpec` contains only its logical name. It carries no context,
platform, runtime, inference or implementation selector. `TargetRef` remains
qualified by `PlatformID` and `TargetID`; resolving one from an environment is a
separate deferred capability.

### Environment implementation and runtime are independent

The environment implementation answers **how a logical setup is established**;
the runtime answers **how the agent executes on an exact target**. Neither is an
author-facing selector in `EnvironmentSpec`.

```text
Environment implementation Runtime
-------------------------- -------
establish logical setup    build native artifact
                           deploy artifact
                           observe deployment
                           retire deployment
```

Future target resolution may permit the same runtime on a locally established
environment or an existing remote environment. Model inference remains another
independent binding; it is not an environment or runtime selector.

### Deployment is not a run

A deployment is one durable deployment source installed through a runtime on a
target. The source is either an authored revision or a derived sandbox image. A
run is one invocation, task, or conversation against that deployment.
The first interface version models deployment lifecycle only. Run start,
observation, and cancellation should be added separately when common semantics
are proven.

## Interface layers

### Public workflows use product verbs

The public package exports exactly three interfaces: `AgentEnvironment`,
`AgentSuites`, and `AgentDeployments`. The application guide contains their
[current signatures](kmx-application-api.md#three-northbound-interfaces).

The verbs form separate lifecycle pairs:

| Scope | Establish | Remove |
|---|---|---|
| Agent environment | `Up` | Deferred |
| AgentSuite build set | `Build` | Registry retention policy |
| Published build set | `Push` | Registry retention policy |
| Agent deployment | `Lift` | `Retire` |

`AgentEnvironment.Up` accepts an `EnvironmentSpec` and returns an established
`Environment`. It does not expose internal setup phases or imply that target,
runtime, inference, ownership, inspection, teardown or recovery semantics have
been settled. `AgentSuites` owns validation and the initial image-backed
build/push workflow, but not concrete builders, storage transports, image-free
execution profiles or deployment.

### AgentSuite is part of the shipping lifecycle

An AgentSuite Artifact is immutable definition and packaging data. It is an OCI
artifact but is not directly runnable. `AgentSuites.Validate` materializes the
exact checked graph; `Build` returns one sandbox image for every composition the
current alpha result can represent; `Push` relocates those identities without
rebuilding. Incomplete recovery progress may contain a verified subset, but a
complete result cannot omit a composition.

This first completeness rule is intentionally profile-specific. The native Orka
and exact-v0.10.3 Go kagent profiles declare `requiredImages: []`; both now have
image-free packaged workspace evaluation and remain outside this image-backed
Build/Push contract. A future general workflow derives its required artifact set
from an explicit execution profile, including profiles that require zero member
images.

The current staged build/push results do not define an aggregate release root.
They are not a settled `AgentSuiteRelease` or a suite-level deployment source.

Staged suite values are evidence returned by the selected `AgentSuites`
implementation, not self-authenticating proof. Build and Push rematerialize the
immutable artifact and rederive the closed graph before accepting a report or a
claim of completeness.

`AgentDeployments.Lift` accepts one `DeploymentSource`: either an authored
`AgentRevision` or a derived `AgentSandboxImage`. The mutually exclusive union
keeps the current bundle path available while allowing OCI-shipped agents to be
used at any compatible destination.

### Internal implementations use narrow capabilities

A runtime is not only a builder. Runtime-specific translation is one capability,
followed by deployment, observation, and optional retirement.

The internal deployment SPI therefore uses narrow interfaces in
`internal/kmx/lifecycle`:

```text
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

Lift composes runtime-native building and deployment:

```text
resolve target and runtime installation
    -> build runtime-native artifact
    -> deploy or reconcile it
    -> persist deployment evidence
```

The internal runtime SPI exposes precise `Build` and `Deploy` capabilities. Observation
is the separate capability used by `Status`; create-only support does not imply
status support. `AgentDeployments.Lift` supplies the sticky product operation.

### Build and shipping have three stages

The model distinguishes:

```text
AgentDeployments.BuildRevision -> validated AgentRevision
AgentSuites.Validate           -> ValidatedAgentSuite
AgentSuites.Build/Push         -> complete image-backed member set
internal RuntimeBuilder        -> DeploymentSource -> runtime-native bundle
```

`AgentRevision` itself is an immutable byte identity; constructing one does not
prove schema or behavior validation. The northbound build workflow owns authoring
validation. AgentSuite validation owns its closed content graph and OCI binding.
The runtime builder parses a revision or verifies a sandbox image and refuses
behavior or bindings it cannot honor. `RuntimeBuildInput` requires the runtime
installation and target binding to identify the same exact target before native
building.

Validation reports expose one exact composition selection per `(agent,
platform)`, including the selected build profile and composition digest. Build
implementations consume every selection instead of asking callers to guess
digests or exposing member builders in the northbound interface.

## Ownership and safety

### Receipts preserve authority boundaries

Receipts are separated by responsibility:

| Receipt | Records | Accepted by |
|---|---|---|
| `DeploymentReceipt` | Deployable source, binding, artifact, and deployment identity | Agent `Retire` |
| `RetirementReceipt` | Established workload retirement | Evidence consumers |

The retained destructive signature encodes its safety boundary:

```go
Retire(context.Context, RetireRequest) (RetirementReceipt, error)
```

`RetireRequest` contains a `DeploymentReceipt` rather than another independently
supplied deployment identity that could disagree. Environment teardown is not
part of the alpha contract; adding it requires its own ownership evidence and
must not accept deployment receipts as authority.

Receipts are local evidence, not authentication or cryptographic attestation.
Before mutation, implementations must verify receipt identity and native
ownership against durable external state. Type separation prevents accidental
authority crossover; it does not make an edited local file trustworthy.

Unknown outcomes do not produce successful receipts. They return
`OutcomeUnknownError` and are resolved through operation recovery before a
receipt can claim an established result.

### Identity is computed, not asserted

The package computes domain-separated SHA-256 identities for:

- exact authored agent bytes;
- target-qualified, versioned binding bytes;
- exact runtime-native artifact bytes.

Portable and rendered identities use the shipped logical-path and length framing
so an adapter can preserve existing ownership annotations and receipts. Callers
cannot attach arbitrary digests to in-process values. Internal runtime bundles
retain the exact deployment source, runtime installation, target binding digest,
rendered digest, and disposition-aware deploy digest. `RenderedDigest` covers
ordered document bytes; `DeployDigest` also authenticates whether each document
is applied or review-only. A deployment receipt persists all three relevant
binding/rendered/deploy identities.

### Persistence is explicit

Values carrying source bytes, bindings or native artifact bytes are in-process
values and reject JSON encoding and decoding. Staged suite operation values may
persist private retrieval hints but are revalidated after decoding; location-free
references and validated receipts are durable identity. In particular,
`AgentSandboxImage` carries a retrieval location, while location-free
`AgentSandboxRef` is persisted inside `DeploymentSourceRef`; publication and
relocation do not change deployment identity. The application guide contains the full
[persistence matrix](kmx-application-api.md#persistence-boundary).

### Absence and unreadability are distinct

`DeploymentAbsent` is a successful observation. A deployment that cannot be
read because of authentication, authorization, timeout, malformed response,
throttling, or network failure returns an error. It must not be reported as
absent. Future environment inspection must preserve the same distinction.

Remotely mutating suite and deployment requests have caller-known
`OperationID`s. A mutation that may have reached the remote system but whose
result cannot be established returns `OutcomeUnknownError` carrying that ID.
Callers use the corresponding recovery method; they do not assume failure or
repeat a non-idempotent operation. Environment recovery is deferred.

## Neutrality boundary

The public package contains no concrete runtime, Kubernetes, cloud, subprocess,
CLI, or terminal types. Concrete knowledge belongs in bounded implementation
packages and at the application composition root.

Architecture tests enforce:

- a standard-library-only production dependency closure;
- no imports from internal implementation packages;
- rejection of known concrete implementation names in exported and serialized
  surfaces;
- exact northbound workflow method sets and the receipt-scoped retirement signature;
- no implementation ports or runtime-native artifact declarations in `pkg/kmx`;
- an internal lifecycle dependency closure containing only `pkg/kmx` and the
  standard library.

The name checks are guardrails for known coupling risks, not proof that every
future abstraction is neutral. Semantic review remains necessary.

## Initial scope

The first production proof should remain smaller than the full model:

1. Implement one `AgentEnvironment.Up` service over focused internal setup
   capabilities; do not mirror it in a broad southbound interface.
2. Adapt one real Orka build, deploy, status, and retire path.
3. Add one in-memory environment fake and shared deployment-runtime conformance.
4. Adapt the current AgentSuite validator, then implement an immutable validated
   snapshot and a complete conformant suite build/push path.
5. Persist suite/deployment operation evidence for recovery from another process.
6. Prove retirement cannot invoke environment or cloud teardown.
7. Validate common semantics against another real runtime before declaring them
   stable.

The package does not currently add CLI commands, provision managed cloud
infrastructure, install alternate runtimes, publish OCI artifacts, or widen any
create-only integration.

## Deferred choices

The alpha contract deliberately defers:

- a universal client containing every lifecycle operation;
- environment inspection, teardown, ownership and recovery until an implementation proves them;
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
ownership, or teardown authority. `local` remains a useful logical environment
name.

### Make kind and AKS public target types

Rejected because that would expose current implementations as permanent product
concepts and would not represent existing or future targets cleanly.

### Put the complete lifecycle on one Runtime interface

Rejected because runtimes support different operations. A large interface would
require unsupported stubs, repeating the coupling visible in the current
chat-plus-lifecycle adapter.

### Make Lifter a runtime interface

Rejected because lift is a KMX workflow that composes target resolution,
runtime-native building, deployment, and receipt persistence. Observation is a
separate status capability. The runtime supplies capabilities used by those
workflows.

### Use Provider as the implementation term

Rejected because provider already means cloud provider, inference provider, and
an Orka resource. `Platform`, `Runtime`, and `Target` state the responsibilities
more precisely.

### Add environment teardown before ownership is implemented

Deferred because current local and cloud teardown have different ownership and
recovery models. A future destructive operation must derive its subject from
evidence in its own ownership domain rather than accept independently supplied
identities that can disagree.

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
- workload retirement cannot be mistaken for environment teardown authority;
- local and managed targets can share lifecycle vocabulary without sharing
  ownership rules.

Costs and risks:

- adapters require explicit translation between neutral and native identities;
- receipt verification remains implementation work and cannot rely on Go types
  alone;
- the public alpha package temporarily overlaps the production internal
  lifecycle contract;
- environment, target, runtime installation, and deployment storage still need a production
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
