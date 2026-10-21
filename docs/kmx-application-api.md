# KMX application API

**Status:** Experimental design evidence

**Depends on:** [KMX lifecycle interface decision](kmx-lifecycle-interfaces.md)
and the [AgentSuite Artifact Specification](https://github.com/agent-primitives/suite/blob/main/spec.md)

**Implementation status:** `pkg/kmx` defines only the three northbound interfaces
and their caller-facing values. `AgentEnvironment` currently has one setup
operation; target resolution, inspection, teardown and recovery are deliberately
not public methods. `internal/kmx/lifecycle` defines deployment-runtime ports,
runtime-native intermediate artifacts, deploy options, recovery SPIs, and
receipt factories. Existing installed suite commands validate and
transfer definition artifacts and experimentally build one member image, but do
not implement this suite-wide northbound workflow. There is no exported
composition root or end-to-end implementation of these interfaces.

The concrete [`pkg/suiteview`](kmx-public-interface.md#view-one-members-portable-recipe)
package is related but separate. It verifies and projects one selected member's
local packaged instructions, model requirement and identities. It does not
implement `AgentSuites.Validate`, build or publish a suite, resolve a target, or
create lifecycle evidence, so it is not another northbound interface.

## Three northbound interfaces

Another Go layer receives only the lifecycle areas it needs:

```go
type Controller struct {
    environment kmx.AgentEnvironment
    suites      kmx.AgentSuites
    deployments kmx.AgentDeployments
}
```

Their responsibilities are deliberately separate:

| Interface | Owns | Does not own |
|---|---|---|
| `AgentEnvironment` | Establish one logical environment | Exact target/runtime resolution, inspection, teardown, agent definition or deployment |
| `AgentSuites` | Validate one exact definition, build its image-backed member set, and push those immutable outputs | Image-free execution profiles, destination mutation or implementation-specific build/storage controls |
| `AgentDeployments` | Build direct revisions and lift/status/retire deployable sources | Target teardown or OCI publication |

The dependency direction is:

```text
CLI / UI / controller / in-process service
                      |
        +-------------+-------------+
        |             |             |
 AgentEnvironment  AgentSuites  AgentDeployments
        |             |             |
        +-------------+-------------+
                      |
            KMX orchestration/stores
                      |
             Environment / Suite / Runtime implementations
```

Consumers do not select concrete environment, suite-builder, or runtime adapters.

## Agent environment

`AgentEnvironment` establishes one logical environment:

```go
type AgentEnvironment interface {
    Up(context.Context, EnvironmentSpec) (Environment, error)
}
```

### Establish an environment

```go
environment, err := controller.environment.Up(ctx, kmx.EnvironmentSpec{
    Name: "local",
})
```

`EnvironmentSpec` contains only a logical name. It has no Kubernetes context,
platform, runtime, inference, container-engine or implementation selector. The
injected implementation owns those setup mechanics and returns an `Environment`
with a durable logical `EnvironmentRef` plus the accepted specification.

An environment is not a `TargetRef`. A later target-resolution contract must
bind an established environment to the exact runtime installation, physical
destination and deployment scope required by a deployment operation. Recreating an
environment may therefore preserve its logical environment identity while
producing a different target identity.

This first interface intentionally does not promise inspection, teardown,
registration, operation recovery or ownership receipts. Those methods require
production persistence and fail-closed semantics before they become public
contract. The current `kmx local down`, AKS ownership records and context
selection remain separate installed behavior.

## AgentSuite shipping lifecycle

An AgentSuite Artifact is a portable, content-addressed OCI definition of one or
more agents and their tools, compositions, and build profiles. It is not directly
runnable. The public workflow exposes three product operations while keeping
member planning, concrete builders, temporary layouts and registry transports
internal:

```text
SuiteSource
    -> Validate -> ValidatedAgentSuite
    -> Build    -> BuildSuiteResult
    -> Push     -> PushSuiteResult
```

```go
type AgentSuites interface {
    Validate(context.Context, ValidateSuiteRequest) (ValidatedAgentSuite, error)
    Build(context.Context, BuildSuiteRequest) (BuildSuiteResult, error)
    RecoverBuild(context.Context, OperationID) (BuildSuiteProgress, error)
    Push(context.Context, PushSuiteRequest) (PushSuiteResult, error)
    RecoverPush(context.Context, OperationID) (PushSuiteResult, error)
}
```

### Validate an exact definition

```go
validated, err := controller.suites.Validate(ctx, kmx.ValidateSuiteRequest{
    Source: kmx.SuiteSource{Path: suiteDirectory},
})
```

`Validate` materializes the exact checked graph as an immutable
`AgentSuiteArtifact`; `Build` never relies on reopening the mutable source path.
Its `AgentSuiteArtifactRef` contains only the OCI manifest digest, while the
artifact's location remains a retrieval hint. Implementations must reverify
artifact content at operation boundaries rather than treating this value as
authentication. `ValidatedAgentSuite.Validate` checks the local value shape; it
cannot prove that a caller-authored report came from the artifact. `Build`
therefore rematerializes the artifact and rederives its authoritative report.

### Build the represented suite

```go
built, err := controller.suites.Build(ctx, kmx.BuildSuiteRequest{
    Operation: "build-team-001",
    Suite:     validated,
})
```

The initial `Build` contract is both whole-suite and **image-backed**: it returns
exactly one verified `AgentSandboxImage` for every `(agent, platform)`
composition in the validation report or fails. `BuildSuiteProgress` may report a
verified subset while an operation is incomplete, but a complete result cannot
omit or duplicate a composition. The result also refuses suites requiring
standalone ToolProvider images because that output class is not yet represented.

The native Orka and exact-v0.10.3 Go kagent suite profiles declare
`requiredImages: []` and can be authored, evaluated by the separate installed
workspace CLI, and transferred without member images. Those profiles cannot
reach this alpha `Build`/`Push` workflow.
Before this becomes the general suite workflow, completeness must be
derived from an explicit execution profile's required artifacts, including zero
images, rather than inferred from composition count alone.

Each `AgentSandboxImage` binds one runnable image to the exact AgentSuite
manifest, agent, platform, build profile, composition digest and embedded
sandbox-binding digest. These values define what a conformant implementation
must prove; the installed experimental AgentKit builder does not yet produce
such images and therefore does not implement this method.

`BuildSuiteResult` is the complete image-backed set currently expressible by the
alpha contract. It is deliberately **not** named or identified as an
`AgentSuiteRelease`: no aggregate OCI release root or inventory has been agreed.
`BuildSuiteResult.ValidateAgainst` binds a completed result to the originating
operation and validated suite; recovery progress has the same request-binding
check.

### Push without rebuilding

```go
pushed, err := controller.suites.Push(ctx, kmx.PushSuiteRequest{
    Operation:   "push-team-001",
    Build:       built,
    Destination: "registry.example/team",
})
```

`Push` copies an already-built image-backed suite and its member images. It must
not reopen source, rerun a builder or change any suite, image, composition or
binding identity. `PushSuiteResult.ValidateAgainst` compares location-free refs
and requires every returned retrieval location to equal the request's
destination, while binding the result to the request's operation. `Push` also
rematerializes the immutable inputs before accepting completeness; staged value
validation alone is not authenticity evidence. Registry authentication, tag
policy, transport, retention and orphan cleanup remain internal concerns.

## Agent deployment lifecycle

`AgentDeployments` manages placements on exact targets. The setup-only
`AgentEnvironment` contract does not yet resolve those targets:

```go
type AgentDeployments interface {
    BuildRevision(context.Context, BuildRequest) (BuildResult, error)
    Lift(context.Context, LiftRequest) (DeploymentReceipt, error)
    RecoverLift(context.Context, OperationID) (DeploymentReceipt, error)
    Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
    Retire(context.Context, RetireRequest) (RetirementReceipt, error)
    RecoverRetire(context.Context, OperationID) (RetirementReceipt, error)
}
```

There are two deployable inputs.

### Direct authored revision

```go
source, err := kmx.NewAgentSource(agentYAML)
built, err := controller.deployments.BuildRevision(ctx, kmx.BuildRequest{Source: source})
deployable, err := kmx.NewRevisionDeploymentSource(built.Revision)
```

This preserves the current Git-friendly bundle path. `AgentRevision` identifies
exact authored bytes; build and runtime validation still reject invalid or
unconsumed behavior.

### OCI sandbox image

```go
deployable, err := kmx.NewSandboxDeploymentSource(image)
```

The sandbox image is one member output from `AgentSuites.Build` and can be used
at any compatible destination. This slice does not add suite-level lift: direct
suite artifacts and `BuildSuiteResult` are not deployment sources.

`AgentSandboxImage` carries a retrieval location for build and publication. Its
durable deployment identity is `AgentSandboxRef`, which includes immutable OCI
and AgentSuite binding identities but excludes location. Relocating or publishing
the same manifest therefore does not change `DeploymentSourceRef`.

### Lift

```go
binding, err := kmx.NewTargetBinding(
    target,
    "example.dev/v1alpha1",
    "RuntimeBinding",
    bindingBytes,
)

deployment, err := controller.deployments.Lift(ctx, kmx.LiftRequest{
    Operation: "lift-writer-001",
    Source:    deployable,
    Binding:   binding,
    Runtime:   runtime.Runtime,
    Options:   kmx.LiftOptions{Reconcile: true},
})
```

Here `target` and `runtime` are exact values obtained outside the current
environment contract. Target identity comes from the binding; the request cannot
carry a second destination that disagrees. KMX resolves the runtime installation
and invokes the runtime build/deploy ports. The caller never passes a concrete
adapter.

### Status and retire

```go
snapshot, err := controller.deployments.Status(ctx, deployment.Deployment)

retired, err := controller.deployments.Retire(ctx, kmx.RetireRequest{
    Operation:  "retire-writer-001",
    Deployment: deployment,
})
```

Retirement verifies deployment and native ownership evidence. It has no
environment teardown capability.

## Unknown outcomes and recovery

Every remotely mutating request has a caller-known `OperationID` before the
first side effect. On ambiguous transport failure:

```go
deployment, err := controller.deployments.Lift(ctx, request)
var unknown *kmx.OutcomeUnknownError
if errors.As(err, &unknown) {
    deployment, err = controller.deployments.RecoverLift(
        ctx, unknown.OperationID(),
    )
}
```

Equivalent recovery exists for suite build/push, deployment, and retirement.
Recovery reads
durable operation/native evidence and does not blindly repeat a non-idempotent
mutation. Successful receipts are returned only after outcomes are established.
Environment setup recovery is deferred until an implementation proves its
operation identity and persistence semantics.

## Internal management ports

Internal implementation packages own these southbound ports and intermediate
values:

| Concern | Interfaces |
|---|---|
| Direct revision validation | `RevisionBuilder` |
| AgentSuite validation, planning and immutable snapshots | Existing focused ports under `internal/kmx/agentsuite` |
| Member construction and OCI graph transfer | Existing `SandboxBuilder`, packer, storage and transfer ports under `internal/kmx/agentsuite` |
| Runtime-native rendering | `RuntimeBuilder` |
| Deployment | `RuntimeDeployer`, `RuntimeDeployRecoverer` |
| Observation | `RuntimeObserver` |
| Retirement | `RuntimeRetirer`, `RuntimeRetireRecoverer` |

Environment setup currently has no mirrored southbound SPI. A future
implementation should compose only the narrow setup capabilities it needs. A
create-only runtime can build/deploy without fake status or retirement methods.

## Persistence boundary

Exact-byte source and runtime-native values remain in process. Staged suite
values may be serialized only as private operation state and can include
retrieval hints; their location-free refs are durable identity and must be
revalidated after decoding. Validated durable values and receipts cross
processes:

```text
In process                 Persisted
----------                 ---------
EnvironmentSpec            Environment / EnvironmentRef
AgentSource                authored source file
AgentRevision              AgentRevisionRef
SuiteSource                ValidatedAgentSuite / AgentSuiteArtifact identity
ValidatedAgentSuite        BuildSuiteResult or private operation record
BuildSuiteResult           PushSuiteResult or private operation record
PushSuiteResult            AgentSuiteArtifactRef and AgentSandboxRef values
AgentSandboxImage          AgentSandboxRef (location-free, inside DeploymentSourceRef)
DeploymentSource           DeploymentSourceRef
TargetBinding              binding source file
RuntimeBuildInput          internal in-process value; separate operation record persists identity
RuntimeBundle              internal runtime-native artifact; DeploymentReceipt persists identity
```

Receipt IDs index implementation-owned evidence such as cloud resource IDs,
object UIDs, prior state, OCI descriptors, and ownership markers. Missing or
mismatched evidence fails closed.

Deployment evidence contains three independent hashes:

- `BindingDigest` identifies target-qualified binding input;
- `RenderedDigest` preserves the shipped framing over all rendered document
  bytes in order, including review-only documents;
- `DeployDigest` authenticates those ordered bytes together with each document's
  `apply` or `review` disposition.

Changing only a document's disposition leaves `RenderedDigest` unchanged but
changes `DeployDigest`. Runtime deployers write only internal
`RuntimeBundle.DeployDocuments()`.

A future HTTP/gRPC layer needs separate wire DTOs; it cannot directly encode
in-process source values or add independent destructive target/deployment IDs
beside receipt-derived identities.

## CLI mapping and deferred recipes

Potential CLI adapters are:

| CLI operation | Service |
|---|---|
| bare `kmx local up` | Conceptually `AgentEnvironment.Up`; current command is not wired to the interface |
| `kmx local down` | No current `AgentEnvironment` method; installed teardown behavior is unchanged |
| `kmx suite validate` | Conceptually `AgentSuites.Validate`; current command does not materialize the northbound result |
| future suite-wide build/push workflow | `AgentSuites.Build` / `AgentSuites.Push` |
| Historical `kmx bundle lift/status/retire` (removed) | `AgentDeployments` |

Environment inspection/teardown/recovery, exact target resolution, run/cancel,
quickstart, durable progress streams, and network APIs remain deferred.
Quickstart may eventually compose environment setup, direct revision
build/lift, and a separate run lifecycle while remaining an explicitly local
runtime recipe.

The installed local suite journey already composes independently:

```text
workspace create -> local setup -> workspace eval -> package push
                                                  -> packaged-digest deploy
```

That is evidence for keeping workspace/AgentSuite identity separate from
environment setup, not evidence that the commands use the northbound interfaces.
The native execution profile requires no member image and does not implement the
first image-backed northbound Build/Push contract. Root digest `kmx deploy` is
unreleased; v0.8.1 uses `kmx lift` with the same arguments. This internal
runtime-dispatched Orka/exact-kagent path is not an implementation of the alpha
suite-wide `AgentDeployments` model above. The public `Lift`/`RecoverLift` API
names and request/receipt examples remain unchanged.

## Evidence required before stability

1. One CLI path and one second in-process consumer use the same interface.
2. The current AgentSuite validator backs `AgentSuites.Validate` without changing
   its closed-format guarantees or rereading mutable source during build.
3. Every image returned by `Build` passes AgentSuite sandbox-image conformance;
   current experimental AgentKit output does not qualify.
4. A sandbox image's suite/agent/platform/composition binding is reverified at
   deployment.
5. Bare local setup and a second in-process consumer use the same environment implementation, and an in-memory fake passes its setup conformance.
6. Lift in one process and status/recovery/retire in another use persisted
   identities.
7. Same deployment operation ID with different input returns a typed conflict.
8. Environment teardown and recovery are added only with persisted ownership evidence.
9. Retirement cannot invoke environment teardown.
10. A second real runtime validates every claimed common deployment semantic.
