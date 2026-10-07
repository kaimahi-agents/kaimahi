# KMX application workflow API

**Status:** Proposed companion design

**Depends on:** [KMX lifecycle interface decision](kmx-lifecycle-interfaces.md)

**Implementation status:** The neutral identities, receipts, workflow contracts,
and platform/runtime capability interfaces exist experimentally in
[`pkg/kmx`](../pkg/kmx). The grouped application facade in this document is not
implemented or wired to the CLI.

## Purpose

The lifecycle decision defines the domain values and implementation
capabilities. Another application, controller, UI, or service should not need to
discover concrete platform and runtime implementations and reproduce KMX
orchestration itself.

Expose product use cases above those implementation ports:

```text
Another application, UI, controller, or transport
                         |
                         v
               KMX workflow facade
        environments / agents / runs / recipes
                         |
                         v
                 KMX orchestration
                         |
               +---------+---------+
               |                   |
          Platform SPI         Runtime SPI
```

Consumers call `Up`, `Lift`, `Status`, and `Retire`. KMX internally calls
`Provision`, `Build`, `Deploy`, and `Observe`.

## Goals

- Let another Go layer perform the same lifecycle as the CLI without invoking
  Cobra commands or parsing terminal output.
- Preserve the ownership boundaries established by the lifecycle contract.
- Keep requests independent of command-line flags, filesystems, Kubernetes
  objects, cloud SDKs, subprocesses, and terminal frameworks.
- Return durable references and receipts needed for recovery and later calls.
- Let consumers depend on one workflow area rather than one large API.
- Keep future HTTP or gRPC adapters thin and behavior-free.

This design does not define a remote runtime plugin protocol, make every
workflow asynchronous, or claim to be a stable SDK.

## Grouped facade

The proposed facade consists of small grouped services and an aggregate access
point:

```go
package kmx

type Client interface {
    Environments() Environments
    Agents() Agents
    Runs() Runs
    Recipes() Recipes
}
```

Consumers should accept the narrowest service they need:

```go
type DeploymentController struct {
    agents kmx.Agents
}

type LocalSetupUI struct {
    environments kmx.Environments
    recipes      kmx.Recipes
}
```

This keeps test fakes small and prevents unrelated methods from becoming one
consumer's dependency.

## Environment workflows

Environment setup composes target infrastructure, inference prerequisites, and
runtime installation. It is broader than `PlatformProvisioner.Provision`.

```go
type Environments interface {
    Up(context.Context, UpRequest) (UpResult, error)
    Register(context.Context, RegisterTargetRequest) (TargetRef, error)
    Inspect(context.Context, TargetRef) (TargetSnapshot, error)
    Down(context.Context, DownRequest) (DownResult, error)
    Forget(context.Context, TargetRef) error
}
```

### Up

`Up` prepares a usable environment:

```text
resolve setup profile
    -> provision or resolve target
    -> prepare inference prerequisites
    -> install or verify runtime
    -> persist independent evidence
```

An initial request and result could be:

```go
type UpRequest struct {
    Target         TargetSpec
    Runtime        RuntimeSelection
    Inference      InferenceSelection
    IdempotencyKey string
}

type UpResult struct {
    Target                TargetRef
    Runtime               RuntimeRef
    InfrastructureReceipt *InfrastructureReceipt
    RuntimeReceipt        RuntimeReceipt
}
```

`InfrastructureReceipt` is optional because resolving a registered target does
not establish infrastructure ownership. Runtime installation has separate
evidence.

The initial CLI mapping is:

```text
kmx up
    -> Environments.Up(local development profile)
```

The current implementation may still compose kind, an inference service, a
model, and Orka. Those names remain implementation configuration rather than
fields in the neutral workflow contract.

### Register and forget

Registration makes an existing destination addressable without claiming
ownership:

```go
type RegisterTargetRequest struct {
    Target TargetSpec
}
```

`Forget` removes only local configuration. It never invokes runtime removal or
platform deprovisioning.

### Down

`Down` tears down only infrastructure KMX can prove it owns:

```go
type DownRequest struct {
    Receipt InfrastructureReceipt
}

type DownResult struct {
    Receipt TeardownReceipt
}
```

The workflow should inspect known deployments before teardown and report what
will be removed. A registered or bring-your-own target has no infrastructure
receipt and therefore cannot use `Down`; its operation is `Forget`.

`Down` may remove deployments because the caller explicitly requested target
teardown. The reverse remains forbidden: `Retire` must never invoke `Down`.

## Agent workflows

```go
type Agents interface {
    Init(context.Context, InitRequest) (InitResult, error)
    Build(context.Context, BuildRequest) (BuildResult, error)
    Lift(context.Context, LiftRequest) (LiftResult, error)
    Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
    Retire(context.Context, RetireRequest) (RetirementReceipt, error)
}
```

These signatures are proposed application contracts, not the complete current
`pkg/kmx` API.

### Init

`Init` scaffolds authored source and performs no target operation. A local path
should not be its only result because a web application or Git service may not
share the CLI filesystem.

```go
type InitRequest struct {
    Name     string
    Template string
}

type InitResult struct {
    Files []SourceFile
}

type SourceFile struct {
    Path string
    Data []byte
    Mode uint32
}
```

The CLI writes these files. Another consumer can commit them to Git, store them,
or return them to a browser.

```text
kmx agent init <name>
    -> Agents.Init
    -> CLI filesystem writer
```

### Build

`Build` validates source and creates the immutable neutral revision:

```go
type BuildRequest struct {
    Source AgentSource
}

type BuildResult struct {
    Revision AgentRevision
    Report   BuildReport
}
```

Runtime-native building remains behind `RuntimeBuilder` during lift.

### Lift

The application result should include durable deployment evidence and the final
observation established by the invocation:

```go
type LiftRequest struct {
    Revision       AgentRevision
    Target         TargetRef
    Runtime        RuntimeSelection
    Binding        TargetBinding
    Reconcile      bool
    IdempotencyKey string
}

type LiftResult struct {
    Deployment DeploymentRef
    Receipt    DeploymentReceipt
    Snapshot   DeploymentSnapshot
}
```

The target in a mature request may be derived from `TargetBinding`, as it is in
the alpha contract. If both remain, construction must reject mismatches rather
than trusting two independent identities.

Application composition is:

```text
Agents.Lift
    -> select registered runtime
    -> RuntimeBuilder.Build
    -> RuntimeDeployer.Deploy
    -> RuntimeObserver.Observe
    -> persist DeploymentReceipt
```

Consumers must not pass a concrete adapter:

```go
// Do not expose this shape.
Lift(ctx, orka.NewAdapter(...), request)
```

Runtime selection expresses intent:

```go
type RuntimeSelection struct {
    ID RuntimeID
}
```

An empty ID means use saved or operator policy. A nonempty ID requests an exact
registered runtime where explicit selection is supported.

### Status

`Status` accepts the durable deployment reference returned by lift. It reports a
neutral snapshot and may later include explicitly namespaced native diagnostics.
It never treats an unreadable deployment as absent.

### Retire

Retirement uses deployment evidence as its sole mutation subject:

```go
type RetireRequest struct {
    Receipt DeploymentReceipt
    Plan    bool
}
```

The workflow verifies the receipt and live native ownership before mutation. It
can delete resources created by KMX or release adopted resources according to
implementation policy. It cannot deprovision the target.

## Run workflows

Runs are separate from deployment lifecycle:

```go
type Runs interface {
    Start(context.Context, RunRequest) (RunRef, error)
    Inspect(context.Context, RunRef) (RunSnapshot, error)
    Cancel(context.Context, CancelRequest) (CancellationReceipt, error)
}
```

This preserves three distinctions:

```text
client context cancellation != remote run cancellation
run cancellation           != deployment retirement
deployment retirement      != target teardown
```

The exact run and cancellation semantics remain deferred until Orka and another
runtime demonstrate a useful common contract.

## Recipe workflows

Recipes compose primitive application workflows into a product journey:

```go
type Recipes interface {
    Quickstart(context.Context, QuickstartRequest) (QuickstartResult, error)
}
```

Quickstart is a recipe, not a platform or runtime capability:

```text
Quickstart
    -> Environments.Up
    -> Agents.Build
    -> Agents.Lift
    -> Runs.Start
    -> Runs.Inspect until a verified answer
```

A result should retain every recovery identity:

```go
type QuickstartResult struct {
    Setup UpResult
    Build BuildResult
    Lift  LiftResult
    Run   RunResult
}
```

Returning only an answer would make the convenience recipe less recoverable
than its underlying operations. The initial recipe can remain explicitly
Orka-oriented and deterministic; a recipe does not imply support from every
runtime.

`quickstart-wizard` remains a presentation-layer composition:

```text
interactive authoring UI -------------------+
                                             v
background Environments.Up -> Build -> Lift -> optional Run
```

Bubble Tea models, terminal streams, and presentation events remain outside the
workflow contract.

## Progress and long-running operations

`Up`, `Lift`, and `Quickstart` can take minutes. An initial in-process facade can
report neutral progress through a sink:

```go
type ProgressSink interface {
    Report(context.Context, ProgressEvent) error
}

type ProgressEvent struct {
    OperationID string
    Phase       string
    State       ProgressState
    Message     string
    At          time.Time
}
```

Progress must not carry terminal styling or raw native command output. The
caller decides whether to render it, persist it, or send it over a transport.

Add a durable operation store and watch API only when workflows need to survive
the initiating process. Context cancellation stops the local call; it does not
imply remote cancel, retire, or down.

## Construction

The public package owns contracts and values. An application package wires the
implementation:

```go
client, err := kmxapp.New(kmxapp.Options{
    Platforms:   platformRegistry,
    Runtimes:    runtimeRegistry,
    Targets:     targetStore,
    Deployments: deploymentStore,
    Receipts:    receiptStore,
})
```

Another layer receives only the facade or a narrow workflow group:

```go
type Server struct {
    environments kmx.Environments
    agents       kmx.Agents
}
```

Concrete target and runtime implementations remain at the composition root.

## Transport adapters

For a non-Go consumer, an HTTP or gRPC adapter sits above the same workflows:

```text
transport authentication and authorization
    -> decode and validate request
    -> call KMX workflow
    -> encode neutral result
```

An illustrative HTTP mapping is:

```text
POST /v1/environments:up
GET  /v1/environments/{id}
POST /v1/environments/{id}:down
POST /v1/agents:build
POST /v1/agents:lift
GET  /v1/deployments/{id}
POST /v1/deployments/{id}:retire
```

This is not a committed wire API. Authentication, authorization, idempotency,
and durable operation semantics must be settled before exposing a network
service.

## CLI mapping

The CLI becomes one adapter over the workflows:

| CLI operation | Workflow |
|---|---|
| `kmx up` | `Environments.Up` |
| `kmx down` | `Environments.Down` |
| `kmx agent init` | `Agents.Init`, then write returned source files |
| `kmx agent lift` | `Agents.Build` plus `Agents.Lift` |
| `kmx agent status` | `Agents.Status` |
| `kmx agent retire` | `Agents.Retire` |
| `kmx agent run` | `Runs.Start` |
| `kmx task result` | `Runs.Inspect` |
| `kmx quickstart` | `Recipes.Quickstart` |

Command flags are translated into request values in `cmd/kmx`; Cobra types do
not cross the workflow boundary.

## Initial exposure

Do not implement every group at once. The first reusable facade should contain
only the operations needed by one second consumer and one CLI vertical slice:

```go
type Environments interface {
    Up(context.Context, UpRequest) (UpResult, error)
    Inspect(context.Context, TargetRef) (TargetSnapshot, error)
    Down(context.Context, DownRequest) (DownResult, error)
}

type Agents interface {
    Build(context.Context, BuildRequest) (BuildResult, error)
    Lift(context.Context, LiftRequest) (LiftResult, error)
    Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
    Retire(context.Context, RetireRequest) (RetirementReceipt, error)
}
```

Add `Init`, `Runs`, and `Recipes` after their implementations use the same
services. This avoids publishing speculative methods while retaining the final
organization.

## Open decisions

- Whether `Up` returns an aggregate setup receipt in addition to separate
  infrastructure and runtime receipts.
- How runtime and inference selection policy is configured and persisted.
- Idempotency-key scope, lifetime, storage, and conflict behavior.
- Whether progress is a per-call sink or a durable operation stream.
- The minimum durable stores required for separate-process lift and status.
- Whether `Init` belongs in the reusable facade or a separate authoring package.
- Which current quickstart steps are general workflow and which remain a local
  Orka recipe.
- The first non-CLI consumer that justifies promoting this as public API.

## Acceptance evidence

Before describing the facade as implemented or stable, require:

1. A CLI operation and a second in-process consumer call the same workflow.
2. Orka and an in-memory fake pass shared workflow conformance.
3. Lift in one process and status or retire in another recover from persisted
   identity and evidence.
4. A registered target can be forgotten but not brought down.
5. Retirement cannot invoke platform deprovisioning.
6. Context cancellation cannot imply remote cancellation, retirement, or target
   teardown.
7. Unknown mutation outcomes are recoverable without blind retry.
8. Transport and presentation adapters contain no lifecycle policy duplicated
   from the workflow implementation.
