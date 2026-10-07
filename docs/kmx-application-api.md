# KMX application API

**Status:** Experimental design evidence

**Depends on:** [KMX lifecycle interface decision](kmx-lifecycle-interfaces.md)
and the [AgentSuite Artifact Specification](agentsuite-spec.md)

**Implementation status:** `pkg/kmx` defines only the three northbound interfaces
and their caller-facing values. `internal/kmx/lifecycle` defines the southbound
ports, runtime-native intermediate artifacts, deploy options, recovery SPIs, and
receipt factories. The repository currently validates AgentSuite content and OCI
layouts offline. It includes only a small internal target-selection helper, not
end-to-end northbound service implementations. AgentSuite OCI packaging,
sandbox-image construction, registry publication, CLI wiring, and production
adapters for these interfaces remain unimplemented.

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
| `AgentEnvironment` | Destination target and runtime setup | Agent definition or deployment |
| `AgentSuites` | Definition validation, OCI packaging, sandbox-image derivation | Destination mutation |
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
             Platform / Suite / Runtime SPIs
```

Consumers do not construct concrete target, suite-builder, or runtime adapters.

## Agent environment

`AgentEnvironment` prepares and manages a destination:

```go
type AgentEnvironment interface {
    Up(context.Context, UpRequest) (UpResult, error)
    RecoverUp(context.Context, OperationID) (UpProgress, error)
    Register(context.Context, TargetSpec) (TargetRef, error)
    Inspect(context.Context, TargetRef) (TargetSnapshot, error)
    Down(context.Context, DownRequest) (TeardownReceipt, error)
    RecoverDown(context.Context, OperationID) (TeardownReceipt, error)
    Forget(context.Context, TargetRef) error
}
```

### Prepare a destination

```go
setup, err := controller.environment.Up(ctx, kmx.UpRequest{
    Operation: "setup-local-001",
    Mode:      kmx.EnvironmentProvision,
    Target: kmx.TargetSpec{
        Name:     "local",
        Platform: "local-default",
        Profile:  "development",
    },
    Runtime: "default-runtime",
})
```

`Up` composes target resolution or provisioning with runtime installation. A
platform-qualified `TargetRef` is the durable destination identity; `local`,
`staging`, and `production` remain friendly configuration names.

`UpRequest.Mode` makes target intent explicit:

- `EnvironmentResolve` resolves an existing target and never provisions;
- `EnvironmentProvision` provisions without attempting resolution;
- `EnvironmentResolveOrProvision` provisions only after the resolver returns
  `TargetResolution{Found: false}`.

At the internal boundary, resolver errors are not absence. Authentication,
authorization, timeout, malformed-response, throttling, and network failures
propagate unchanged and never trigger provisioning.

If provisioning succeeds but runtime setup fails, `RecoverUp` returns durable
`UpProgress` containing the infrastructure receipt. The caller can explicitly
resume or tear down without losing ownership evidence.

### Existing destinations

`Register` records an existing target without claiming infrastructure ownership.
`Forget` removes only that registration. A registered target has no
`InfrastructureReceipt` and therefore cannot be brought down by KMX.

### Teardown

```go
receipt, err := controller.environment.Down(ctx, kmx.DownRequest{
    Operation:      "teardown-local-001",
    Infrastructure: *setup.Infrastructure,
})
```

This example uses `EnvironmentProvision`, so successful setup must carry the
infrastructure evidence required by `Down`. A resolved or registered target has
no such receipt and can only be forgotten, not deprovisioned.

`Down` verifies platform-owned evidence before mutation. Workloads may disappear
as a consequence of deleting an owned target, but `Down` does not fabricate
runtime retirement receipts or claim each retirement workflow ran.

## AgentSuite shipping lifecycle

An AgentSuite Artifact is a portable, content-addressed OCI definition of one or
more agents and their tools, compositions, and build profiles. It is not directly
runnable. A producer derives an Agent Sandbox Image for one suite digest, agent,
platform, build profile, and composition.

```text
SuiteSource
    -> Validate
    -> Package as AgentSuite OCI artifact
    -> BuildSandbox for one agent/platform
    -> AgentSandboxImage
    -> AgentDeployments.Lift
```

`AgentSuites` represents that lifecycle:

```go
type AgentSuites interface {
    Validate(context.Context, ValidateSuiteRequest) (SuiteReport, error)
    Package(context.Context, PackageSuiteRequest) (PackageSuiteResult, error)
    RecoverPackage(context.Context, OperationID) (PackageSuiteResult, error)
    BuildSandbox(context.Context, BuildSandboxRequest) (AgentSandboxImage, error)
    RecoverBuildSandbox(context.Context, OperationID) (AgentSandboxImage, error)
    Publish(context.Context, PublishArtifactRequest) (OCIArtifactRef, error)
    RecoverPublish(context.Context, OperationID) (OCIArtifactRef, error)
}
```

### Validate and package

```go
report, err := controller.suites.Validate(ctx, kmx.ValidateSuiteRequest{
    Source: kmx.SuiteSource{Path: suiteDirectory},
})

packaged, err := controller.suites.Package(ctx, kmx.PackageSuiteRequest{
    Operation:   "package-suite-001",
    Source:      kmx.SuiteSource{Path: suiteDirectory},
    Output:      suiteLayoutDirectory,
})

selection, err := packaged.Report.Composition(
    "writer",
    kmx.SandboxPlatform{OS: "linux", Architecture: "amd64"},
)
```

The existing implementation can inform `Validate`; packaging remains follow-up
work. `Package` is an offline operation that returns an immutable OCI identity
and its validation report, not mutable registry state.

### Derive a runnable image

```go
image, err := controller.suites.BuildSandbox(ctx, kmx.BuildSandboxRequest{
    Operation: "build-writer-amd64-001",
    Suite:     packaged.Artifact,
    Agent:     "writer",
    Platform: kmx.SandboxPlatform{
        OS: "linux", Architecture: "amd64",
    },
    BuildProfile: selection.BuildProfile,
    Composition: selection.Digest,
    Output: writerImageLayoutDirectory,
})
```

`AgentSandboxImage` binds the runnable image to the exact AgentSuite digest,
agent, platform, build profile, composition digest, and embedded sandbox-binding
digest. The destination runtime must verify the image label, embedded binding,
selected platform, and OCI manifest identity before deployment.

Both package and sandbox construction are deterministic, network-free operations.
Publication is an explicit separate call:

```go
published, err := controller.suites.Publish(ctx, kmx.PublishArtifactRequest{
    Operation:   "publish-writer-001",
    Artifact:    image.Image,
    Destination: "registry.example/agents/writer",
})

image.Image = published
```

Registry authentication, push transport, retention, and deletion policy remain
publisher concerns. Publication changes only the retrieval location: it must
preserve the input manifest digest, media type, and artifact type. No lifecycle
receipt grants authority to delete an OCI repository or tag.

## Agent deployment lifecycle

`AgentDeployments` manages placements in an `AgentEnvironment`:

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

The sandbox image came from `AgentSuites.BuildSandbox` and can be used at any
compatible destination. Direct suite artifacts cannot be lifted because they are
definition data, not runnable images.

`AgentSandboxImage` carries a retrieval location for build and publication. Its
durable deployment identity is `AgentSandboxRef`, which includes immutable OCI
and AgentSuite binding identities but excludes location. Relocating or publishing
the same manifest therefore does not change `DeploymentSourceRef`.

### Lift

```go
binding, err := kmx.NewTargetBinding(
    setup.Target,
    "example.dev/v1alpha1",
    "RuntimeBinding",
    bindingBytes,
)

deployment, err := controller.deployments.Lift(ctx, kmx.LiftRequest{
    Operation: "lift-writer-001",
    Source:    deployable,
    Binding:   binding,
    Runtime:   setup.Runtime.Runtime,
    Options:   kmx.LiftOptions{Reconcile: true},
})
```

Target identity comes from the binding; the request cannot carry a second
destination that disagrees. KMX resolves the runtime installation and invokes
the runtime build/deploy ports. The caller never passes a concrete adapter.

### Status and retire

```go
snapshot, err := controller.deployments.Status(ctx, deployment.Deployment)

retired, err := controller.deployments.Retire(ctx, kmx.RetireRequest{
    Operation:  "retire-writer-001",
    Deployment: deployment,
})
```

Retirement verifies deployment and native ownership evidence. It receives no
infrastructure receipt and cannot deprovision the environment.

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

Equivalent recovery exists for environment setup/teardown, suite packaging,
sandbox construction, registry publication, and retirement. Recovery reads
durable operation/native evidence and does not blindly repeat a non-idempotent
mutation. Successful receipts are returned only after outcomes are established.

## Internal management ports

`internal/kmx/lifecycle` owns these southbound ports and intermediate values:

| Concern | Interfaces |
|---|---|
| Direct revision validation | `RevisionBuilder` |
| Existing or provisioned target | `PlatformResolver`, `PlatformProvisioner`, `PlatformInspector`, `PlatformDeprovisioner` and recovery ports |
| AgentSuite validation | `AgentSuiteValidator` |
| Offline OCI package and sandbox construction | `AgentSuiteBuilder`, `AgentSuiteBuildRecoverer` |
| Registry publication | `OCIArtifactPublisher`, `OCIArtifactPublishRecoverer` |
| Runtime installation | `RuntimeInstaller`, `RuntimeInstallRecoverer` |
| Runtime-native rendering | `RuntimeBuilder` |
| Deployment | `RuntimeDeployer`, `RuntimeDeployRecoverer` |
| Observation | `RuntimeObserver` |
| Retirement | `RuntimeRetirer`, `RuntimeRetireRecoverer` |

An existing-target implementation can resolve and inspect without gaining
provision/deprovision authority. A create-only runtime can build/deploy without
fake status or retirement methods.

## Persistence boundary

Exact-byte and local-path values are in-process and reject JSON. Durable refs and
validated receipts cross processes:

```text
In process                 Persisted
----------                 ---------
AgentSource                authored source file
AgentRevision              AgentRevisionRef
SuiteSource                AgentSuiteArtifact OCI identity
AgentSuiteArtifact         AgentSandboxImage OCI identity
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
| `kmx up` / `kmx down` | `AgentEnvironment` |
| `kmx suite validate` | `AgentSuites.Validate` |
| future suite package/build/publish commands | `AgentSuites.Package` / `BuildSandbox` / `Publish` |
| `kmx agent lift/status/retire` | `AgentDeployments` |

Run/cancel, quickstart, durable progress streams, and network APIs remain
deferred. Quickstart may eventually compose environment setup, direct revision
build/lift, and a separate run lifecycle while remaining an explicitly local
runtime recipe.

## Evidence required before stability

1. One CLI path and one second in-process consumer use the same interface.
2. The current AgentSuite validator backs `AgentSuites.Validate` without changing
   its closed-format guarantees.
3. OCI packaging and sandbox derivation pass AgentSuite conformance.
4. A sandbox image's suite/agent/platform/composition binding is reverified at
   deployment.
5. Orka and an in-memory fake pass shared environment/deployment conformance.
6. Lift in one process and status/recovery/retire in another use persisted
   identities.
7. Same operation ID with different input returns a typed conflict.
8. A registered target can be forgotten but not brought down.
9. Retirement cannot invoke platform deprovisioning.
10. A second real runtime validates every claimed common deployment semantic.
