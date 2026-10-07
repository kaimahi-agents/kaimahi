# KMX application API

**Status:** Experimental design evidence

**Depends on:** [KMX lifecycle interface decision](kmx-lifecycle-interfaces.md)

**Implementation status:** `pkg/kmx` defines the interfaces and neutral values
described here. It does not yet provide an orchestration implementation, wire
the CLI to these interfaces, or adapt a production runtime.

## Purpose

Another Go layer should be able to perform KMX lifecycle operations without
calling Cobra commands, parsing terminal output, or constructing concrete
platform and runtime adapters.

The intended dependency direction is:

```text
CLI / UI / controller / in-process service
                    |
                    v
      EnvironmentService / AgentService
                    |
                    v
          KMX orchestration and stores
                    |
          +---------+---------+
          |                   |
     Platform SPI         Runtime SPI
          |                   |
          +---------+---------+
                    |
             native systems
```

`EnvironmentService` and `AgentService` are the northbound usage contracts.
`Platform*` and `Runtime*` are southbound management interfaces implemented by
concrete integrations. A consumer sees the former and does not assemble the
latter.

## Consumer dependency

A consuming layer accepts only the service it needs:

```go
type DeploymentController struct {
    environments kmx.EnvironmentService
    agents       kmx.AgentService
}
```

There is deliberately no aggregate `Client` interface in the alpha API. It
would add indirection without evidence and force one consumer to know about
unrelated workflow areas.

Construction belongs at an application composition root, outside `pkg/kmx`:

```go
services, err := kmxapp.New(kmxapp.Options{
    Platforms:   platformRegistry,
    Runtimes:    runtimeRegistry,
    Targets:     targetStore,
    Deployments: deploymentStore,
    Receipts:    receiptStore,
    Operations:  operationStore,
})

controller := DeploymentController{
    environments: services.Environments,
    agents:       services.Agents,
}
```

`kmxapp` is illustrative; it is not implemented in this change. Concrete
implementations remain internal until a real second consumer proves a stable
construction contract.

## Environment management

`EnvironmentService` owns composed environment setup:

```go
type EnvironmentService interface {
    Up(context.Context, UpRequest) (UpResult, error)
    RecoverUp(context.Context, OperationID) (UpProgress, error)
    Register(context.Context, TargetSpec) (TargetRef, error)
    Inspect(context.Context, TargetRef) (TargetSnapshot, error)
    Down(context.Context, DownRequest) (TeardownReceipt, error)
    RecoverDown(context.Context, OperationID) (TeardownReceipt, error)
    Forget(context.Context, TargetRef) error
}
```

### Up

`Up` is broader than platform provisioning. It composes:

```text
resolve or provision target
    -> install or verify runtime
    -> record target ownership
    -> persist infrastructure and runtime evidence
```

Example:

```go
result, err := environments.Up(ctx, kmx.UpRequest{
    Operation: "setup-local-001",
    Target: kmx.TargetSpec{
        Name:     "local",
        Platform: "local-default",
        Profile:  "development",
    },
    Runtime: "default-runtime",
})
```

The target name is a friendly configuration name. The returned `TargetRef` is
the platform-qualified durable identity. `UpResult.Infrastructure` is nil when
the target was resolved or registered rather than provisioned by KMX. It is not
nil authority to invent teardown ownership.

The application implementation performs roughly:

```text
EnvironmentService.Up
    -> PlatformResolver.Resolve or PlatformProvisioner.Provision
    -> RuntimeInstaller.Ensure
    -> save target, runtime and receipts
    -> return UpResult
```

If target provisioning succeeds and runtime installation later fails, KMX
persists `UpProgress` with the target and infrastructure receipt before
returning. `RecoverUp` exposes that partial state so the caller can explicitly
resume setup or call `Down`; ownership evidence is not trapped behind an
all-or-nothing result.

The current `kmx up` can eventually become a CLI adapter over this workflow.
Its local profile may still choose kind, model prerequisites, and Orka without
putting those implementation names in this interface.

### Register and forget

`Register` records an existing target without creating an
`InfrastructureReceipt`. `Forget` removes only KMX's local registration:

```go
target, err := environments.Register(ctx, kmx.TargetSpec{
    Name:     "production",
    Platform: "existing-target",
    Profile:  "production",
})

err = environments.Forget(ctx, target)
```

`Forget` never removes a runtime or target. A registered target cannot be
brought down because KMX has no infrastructure ownership receipt for it.

### Down

`Down` requires infrastructure evidence and a new operation identity:

```go
receipt, err := environments.Down(ctx, kmx.DownRequest{
    Operation:      "teardown-local-001",
    Infrastructure: *result.Infrastructure,
})
```

The implementation must verify the receipt against platform-owned durable
evidence before mutation. It may refuse while known deployments remain. If an
owned target is deprovisioned, resident workloads disappear as a consequence of
target teardown; `Down` must not fabricate deployment retirement receipts or
claim that each runtime retirement workflow ran.

Runtime uninstall on a registered target is deliberately deferred. Neither
`Forget` nor target `Down` silently provides that missing operation.

## Agent management

`AgentService` is the northbound deployment lifecycle:

```go
type AgentService interface {
    Build(context.Context, BuildRequest) (BuildResult, error)
    Lift(context.Context, LiftRequest) (DeploymentReceipt, error)
    RecoverLift(context.Context, OperationID) (DeploymentReceipt, error)
    Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
    Retire(context.Context, RetireRequest) (RetirementReceipt, error)
    RecoverRetire(context.Context, OperationID) (RetirementReceipt, error)
}
```

### Build

Build turns exact authored bytes into a byte-addressed revision and reports
validation diagnostics:

```go
source, err := kmx.NewAgentSource(agentYAML)
if err != nil {
    return err
}

built, err := agents.Build(ctx, kmx.BuildRequest{Source: source})
```

`AgentRevision` itself identifies exact bytes. The `AgentService.Build`
implementation owns authoring validation. Runtime builders must independently
parse the exact source and reject behavior they cannot honor.

### Lift

The caller supplies a stable operation ID before any mutation. Target identity
comes from `TargetBinding`, so the request does not carry a second target that
could disagree:

```go
binding, err := kmx.NewTargetBinding(
    result.Target,
    "example.dev/v1alpha1",
    "RuntimeBinding",
    bindingBytes,
)
if err != nil {
    return err
}

deployment, err := agents.Lift(ctx, kmx.LiftRequest{
    Operation: "lift-support-001",
    Revision:  built.Revision,
    Binding:   binding,
    Runtime:   result.Runtime.Runtime,
    Options:   kmx.LiftOptions{Reconcile: true},
})
```

KMX orchestration performs:

```text
AgentService.Lift
    -> resolve the recorded runtime installation
    -> NewRuntimeBuildInput
    -> RuntimeBuilder.Build
    -> RuntimeDeployer.Deploy
    -> persist DeploymentReceipt by operation ID
```

The consumer never passes a concrete adapter. Runtime selection is an ID or
saved policy resolved through KMX's internal registry.

### Status

Status uses the durable reference in the deployment receipt:

```go
snapshot, err := agents.Status(ctx, deployment.Deployment)
```

An absent deployment is a successful observation. Authentication, permission,
timeout, malformed response, and network failures return an error and cannot be
reported as absence.

### Retire

Retirement derives its deployment identity from scoped evidence:

```go
retired, err := agents.Retire(ctx, kmx.RetireRequest{
    Operation:  "retire-support-001",
    Deployment: deployment,
})
```

The runtime verifies the receipt and native ownership evidence before mutation.
It may delete KMX-created resources or release adopted resources according to
implementation policy. It receives no `InfrastructureReceipt` and cannot
deprovision the target.

Planning should be a separate future operation with a non-mutating result. A
`Plan` flag is not included in `RetireRequest` because a plan must not return a
retirement receipt.

## Unknown outcomes and recovery

Every remotely mutating request carries a caller-known `OperationID`. If a timeout or
connection failure happens after a possible mutation, KMX returns
`OutcomeUnknownError` containing that ID:

```go
deployment, err := agents.Lift(ctx, request)
var unknown *kmx.OutcomeUnknownError
if errors.As(err, &unknown) {
    deployment, err = agents.RecoverLift(ctx, unknown.OperationID())
}
```

Recovery reads durable operation or implementation evidence; it does not repeat
the mutation blindly. Equivalent recovery methods exist for `Up`, `Down`, and
`Retire`, with southbound recovery capabilities for implementations that own the
native operation. Unknown outcomes do not return successful retirement or
teardown receipts; a receipt records only an established result after recovery.

Operation ID scope, retention, and same-ID/different-input conflict behavior
remain part of the first orchestration implementation design. The key invariant
is already represented: an ambiguous mutation always has a caller-known lookup
identity.

## Management interfaces

The application service composes narrow southbound ports:

| Responsibility | Interface |
|---|---|
| Resolve an existing target | `PlatformResolver` |
| Provision target infrastructure | `PlatformProvisioner` |
| Recover target provisioning | `PlatformProvisionRecoverer` |
| Inspect target infrastructure | `PlatformInspector` |
| Deprovision owned infrastructure | `PlatformDeprovisioner` |
| Recover target deprovisioning | `PlatformDeprovisionRecoverer` |
| Install or verify a runtime | `RuntimeInstaller` |
| Recover runtime installation | `RuntimeInstallRecoverer` |
| Validate and render a revision | `RuntimeBuilder` |
| Deploy a runtime bundle | `RuntimeDeployer` |
| Recover deployment | `RuntimeDeployRecoverer` |
| Observe a deployment | `RuntimeObserver` |
| Retire a deployment | `RuntimeRetirer` |
| Recover retirement | `RuntimeRetireRecoverer` |

An Orka implementation may satisfy most runtime interfaces. A create-only
runtime can implement only build and deploy. An existing-target platform can
implement resolve and inspect without implementing provision or deprovision.

The composition root performs interface assertions and returns typed
`UnsupportedCapabilityError` instead of calling an undeclared approximation.

## Persistence boundary

Another in-process Go layer can pass `AgentSource`, `AgentRevision`,
`TargetBinding`, and `RuntimeBundle`. These exact-byte values intentionally
reject JSON encoding and decoding.

Durable references and validated receipts are the cross-process boundary:

```text
In process                 Persisted
----------                 ---------
AgentSource                authored source file
AgentRevision              AgentRevisionRef
TargetBinding              binding source file
RuntimeBuildInput          not persisted; a separate operation DTO stores the ID and input fingerprint
RuntimeBundle              DeploymentReceipt plus native evidence
```

Receipt IDs are lookup keys for implementation-owned native evidence such as
resource IDs, object UIDs, prior state, and ownership markers. Missing or
mismatched native evidence must fail closed.

`RenderedDigest` covers all artifact documents, including review-only material.
Runtime deployers must write only `RuntimeBundle.DeployDocuments()`; they must
not bulk-apply `RuntimeBundle.Documents()`.

A future HTTP or gRPC layer requires separate wire DTOs. It cannot directly
JSON-encode the in-process values, and it must not add independent target or
deployment path IDs alongside receipt-derived destructive identities.

## CLI and recipes

The CLI can eventually become an adapter over the same services:

| CLI operation | Service call |
|---|---|
| `kmx up` | `EnvironmentService.Up` |
| `kmx down` | `EnvironmentService.Down` |
| `kmx agent lift` | `AgentService.Build`, then `Lift` |
| `kmx agent status` | `AgentService.Status` |
| `kmx agent retire` | `AgentService.Retire` |

`agent init`, run/cancel, quickstart, durable progress streams, and a network API
are intentionally deferred. They are application recipes or separate lifecycle
domains, not requirements for the first reusable facade.

Current quickstart would eventually compose environment setup, build, lift, and
a run operation, but it may remain an explicitly local Orka recipe. The current
interactive wizard remains a presentation concern and must not put terminal
types into these services.

## What is internal

The consuming layer does not own:

- concrete platform or runtime construction;
- runtime selection policy or adapter detection;
- target, deployment, receipt, and operation stores;
- native object identity and ownership evidence;
- retries, reconciliation, or unknown-outcome recovery policy;
- terminal progress rendering;
- cloud or Kubernetes clients.

Those belong to KMX orchestration, implementation packages, and the composition
root.

## Evidence required before stability

1. One CLI path and one second in-process consumer call the same service.
2. Orka and an in-memory fake pass shared service conformance.
3. Lift in one process and status, recovery, or retire in another use persisted
   identities successfully.
4. Same operation ID with different input returns a typed conflict.
5. A registered target can be forgotten but not brought down.
6. Retirement cannot invoke platform deprovisioning.
7. Context cancellation cannot imply remote cancellation, retirement, or target
   teardown.
8. Ambiguous mutations recover by operation ID without blind retry.
9. A second real runtime validates every claimed common semantic before the API
   is stabilized.
