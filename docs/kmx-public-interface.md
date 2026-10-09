# KMX public interface summary

**Status:** Experimental interface and usage summary

KMX currently has two distinct public surfaces:

1. The working `kmx` CLI.
2. The experimental Go contracts in `pkg/kmx`.

The CLI is the usable end-to-end product interface today. The Go package defines
the intended application boundary, but it does not yet provide exported concrete
implementations or constructors and is not wired to the CLI.

## Layering

The original layering still describes the intended dependency direction, but it
now has three public workflow areas and three groups of internal capabilities:

```text
Another product / CLI / UI / controller / API
                         |
                         v
              Public package: pkg/kmx
  +-------------------+----------------+--------------------+
  | AgentEnvironment  | AgentSuites    | AgentDeployments   |
  | Up                | Validate       | BuildRevision      |
  | RecoverUp         | Package        | Lift / RecoverLift |
  | Register/Inspect  | BuildSandbox   | Status             |
  | Down/RecoverDown  | Publish        | Retire/Recover     |
  | Forget            | Recover...     |                    |
  +-------------------+----------------+--------------------+
                         |
                         v
       KMX application orchestration and durable stores
               (not implemented end to end yet)
                         |
                         v
  +-------------------+----------------+--------------------+
  | Platform SPIs     | Suite/OCI SPIs | Runtime SPIs       |
  | resolve/provision | validate       | install            |
  | inspect/remove    | package/build  | build/deploy       |
  | recover           | publish/recover| observe/retire     |
  +-------------------+----------------+--------------------+
                         |
                         v
       Concrete platform, registry, and runtime adapters
```

The top row is the public, implementation-neutral contract. The SPI row remains
internal so a consumer cannot select concrete adapters directly or couple itself
to Kubernetes, a cloud, a registry transport, or one runtime.

This diagram is architectural direction rather than a claim that the complete
stack exists. `pkg/kmx` contains the public contracts and
`internal/kmx/lifecycle` contains the internal ports, but the application
orchestration, durable stores, production adapters, and CLI wiring are not yet
implemented end to end.

## Public Go interfaces

`pkg/kmx` exposes three consumer-facing interfaces:

| Interface | Responsibility | Main operations |
|---|---|---|
| `AgentEnvironment` | Prepare and manage destination targets and runtimes | `Up`, `RecoverUp`, `Register`, `Inspect`, `Down`, `RecoverDown`, `Forget` |
| `AgentSuites` | Validate, package, build, and publish AgentSuite artifacts | `Validate`, `Package`, `BuildSandbox`, `Publish`, and recovery methods |
| `AgentDeployments` | Build and deploy authored revisions or sandbox images | `BuildRevision`, `Lift`, `RecoverLift`, `Status`, `Retire`, `RecoverRetire` |

The definitions are in:

- `pkg/kmx/target.go`: `AgentEnvironment`
- `pkg/kmx/suite.go`: `AgentSuites`
- `pkg/kmx/agent.go`: `AgentDeployments`

The package also exposes the request, result, reference, and receipt values used
by these interfaces. Important durable identities include `TargetRef`,
`RuntimeRef`, `DeploymentRef`, and the receipt types.

## Equivalent of `kmx up`

The public Go equivalent of the product operation `kmx up` is
`AgentEnvironment.Up`:

```go
package example

import (
	"context"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

func prepareEnvironment(ctx context.Context, environment kmx.AgentEnvironment) (kmx.UpResult, error) {
	setup, err := environment.Up(ctx, kmx.UpRequest{
		Operation: "local-up-001",
		Mode:      kmx.EnvironmentResolveOrProvision,
		Target: kmx.TargetSpec{
			Name:     "local",
			Platform: "local-default",
			Profile:  "development",
		},
		Runtime: "default-runtime",
		Options: kmx.RuntimeOptions{
			Profile: "development",
		},
	})
	if err != nil {
		return kmx.UpResult{}, err
	}

	fmt.Printf("target:  %s/%s\n", setup.Target.Platform, setup.Target.ID)
	fmt.Printf("runtime: %s\n", setup.Runtime.Runtime)
	return setup, nil
}
```

The caller receives an `AgentEnvironment` implementation through dependency
injection. There is intentionally no package-global client, concrete adapter, or
public constructor yet.

### Environment modes

The setup mode makes target ownership intent explicit:

| Mode | Meaning |
|---|---|
| `EnvironmentResolve` | Resolve an existing target and never provision one |
| `EnvironmentProvision` | Provision a target without first trying to resolve one |
| `EnvironmentResolveOrProvision` | Reuse an existing target, or provision only after established absence |

`EnvironmentResolveOrProvision` is the closest match for the convergent behavior
normally expected from `kmx up`. A resolution error is not treated as absence:
authentication, authorization, timeout, malformed-response, throttling, and
network failures must not trigger provisioning.

### Result

`Up` returns:

```go
type UpResult struct {
	Operation      OperationID
	Target         TargetRef
	Runtime        RuntimeRef
	Infrastructure *InfrastructureReceipt
	RuntimeReceipt RuntimeReceipt
}
```

`Infrastructure` is present only when this setup operation provisioned the
target. A resolved or registered target has no infrastructure receipt because
KMX does not own that target.

## Unknown outcomes and recovery

Every remotely mutating request carries a caller-created `OperationID`. If an
operation may have changed remote state but its result cannot be established,
the implementation returns `OutcomeUnknownError`. Recover the same operation;
do not blindly repeat it under a new ID.

```go
setup, err := environment.Up(ctx, request)
if err != nil {
	var unknown *kmx.OutcomeUnknownError
	if errors.As(err, &unknown) {
		progress, recoverErr := environment.RecoverUp(
			ctx,
			unknown.OperationID(),
		)
		if recoverErr != nil {
			return recoverErr
		}
		if !progress.Complete {
			return fmt.Errorf("environment setup recovery is incomplete")
		}
		return nil
	}
	return err
}
```

`UpProgress` retains infrastructure evidence when target provisioning succeeded
but runtime installation or later verification did not complete. This allows a
caller to resume or deliberately tear down without losing ownership evidence.

## Safe teardown

`Down` derives its target from the infrastructure receipt returned by a
provisioning operation:

```go
if setup.Infrastructure == nil {
	return fmt.Errorf("target was not provisioned by this setup")
}

teardown, err := environment.Down(ctx, kmx.DownRequest{
	Operation:      "local-down-001",
	Infrastructure: *setup.Infrastructure,
})
```

The request cannot supply a second target identity that might disagree with the
receipt. Implementations must still verify the receipt against durable ownership
state before mutating infrastructure.

An existing target recorded with `Register` has no infrastructure ownership
receipt. It can be removed from local configuration with `Forget`, but it cannot
be deprovisioned through `Down`.

## Deploying after environment setup

Environment setup and agent deployment are separate ownership domains. After
`Up`, use `AgentDeployments` to build and lift an agent:

```go
source, err := kmx.NewAgentSource(agentYAML)
if err != nil {
	return err
}

built, err := deployments.BuildRevision(ctx, kmx.BuildRequest{
	Source: source,
})
if err != nil {
	return err
}

deployable, err := kmx.NewRevisionDeploymentSource(built.Revision)
if err != nil {
	return err
}

binding, err := kmx.NewTargetBinding(
	setup.Target,
	"example.dev/v1alpha1",
	"RuntimeBinding",
	bindingYAML,
)
if err != nil {
	return err
}

deployment, err := deployments.Lift(ctx, kmx.LiftRequest{
	Operation: "lift-agent-001",
	Source:    deployable,
	Binding:   binding,
	Runtime:   setup.Runtime.Runtime,
	Options:   kmx.LiftOptions{Reconcile: true},
})
```

`TargetBinding` identifies exactly one target and contains opaque,
runtime-specific binding bytes. Generic KMX workflows include those bytes in
identity but do not interpret them.

## CLI-to-Go mapping

The intended conceptual mapping is:

| CLI operation | Go service operation |
|---|---|
| `kmx up` | `AgentEnvironment.Up` |
| `kmx down` | `AgentEnvironment.Down` |
| `kmx suite validate` | `AgentSuites.Validate` |
| Future suite package/build/publish commands | `AgentSuites.Package`, `BuildSandbox`, `Publish` |
| `kmx agent lift` | `AgentDeployments.Lift` |
| `kmx agent status` | `AgentDeployments.Status` |
| `kmx agent retire` | `AgentDeployments.Retire` |

This is a product-level mapping, not shared implementation today. The current
CLI does not call these northbound interfaces.

## Working CLI today

For an end-to-end local runtime, use the CLI:

```bash
export KIND_CLUSTER=kmx-local
export KUBE_CTX=kind-kmx-local
kmx up
kmx status
```

A bare `kmx up` runs these steps in order:

```text
cluster -> ollama -> model -> orka
```

One step can be selected explicitly:

```bash
kmx up --step cluster
kmx up --step ollama
kmx up --step model
kmx up --step orka
```

`kmx up` prepares the runtime but does not create an agent. Use one of these
afterward:

```bash
# Fixed demonstration that ends with an answer.
kmx quickstart

# Interactive custom authoring.
kmx quickstart-wizard

# Author an agent against an already prepared target.
kmx agent create
```

## Current limitations

The Go package is alpha design evidence rather than a ready-to-instantiate SDK:

- No exported concrete implementation or constructor exists.
- The interfaces are not wired to the current CLI.
- A consumer must receive or implement `AgentEnvironment`, `AgentSuites`, and
  `AgentDeployments`.
- The Go `Up` contract represents target plus runtime setup, not the CLI's
  individually selectable `cluster`, `ollama`, `model`, and `orka` recipe steps.
- Model or inference provisioning is not yet a separate public lifecycle
  contract.
- AgentSuite OCI packaging, sandbox construction, publication, and production
  adapters remain unimplemented.

The authoritative contract details remain in:

- `pkg/kmx/doc.go`
- `docs/kmx-application-api.md`
- `docs/kmx-lifecycle-interfaces.md`
