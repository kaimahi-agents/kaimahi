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
  |                   | Build/Recover  | Lift / RecoverLift |
  |                   | Push/Recover   | Status             |
  |                   |                | Retire/Recover     |
  +-------------------+----------------+--------------------+
                         |
                         v
       KMX application orchestration and durable stores
               (not implemented end to end yet)
                         |
                         v
  +-------------------+----------------+--------------------+
  | Environment impl  | Suite/OCI SPIs | Runtime SPIs       |
  | setup composition | validate/plan  | build/deploy       |
  |                   | build/pack     | observe/retire     |
  |                   | store/transfer | recover            |
  +-------------------+----------------+--------------------+
                         |
                         v
       Concrete platform, registry, and runtime adapters
```

The top row is the public, implementation-neutral contract. The SPI row remains
internal so a consumer cannot select concrete adapters directly or couple itself
to Kubernetes, a cloud, a registry transport, or one runtime.

This diagram is architectural direction rather than a claim that the complete
stack exists. `pkg/kmx` contains the public contracts; focused internal ports
live under `internal/kmx/agentsuite` and `internal/kmx/lifecycle`. Application
orchestration, durable stores, production adapters, and CLI wiring are not yet
implemented end to end.

## Public Go interfaces

`pkg/kmx` exposes three consumer-facing interfaces:

| Interface | Responsibility | Main operations |
|---|---|---|
| `AgentEnvironment` | Establish one logical environment | `Up` |
| `AgentSuites` | Materialize a validated definition, build its initial image-backed member set, and push those immutable outputs | `Validate`, `Build`, `RecoverBuild`, `Push`, `RecoverPush` |
| `AgentDeployments` | Build and deploy authored revisions or sandbox images | `BuildRevision`, `Lift`, `RecoverLift`, `Status`, `Retire`, `RecoverRetire` |

The definitions are in:

- `pkg/kmx/environment.go`: `AgentEnvironment`
- `pkg/kmx/suite.go`: `AgentSuites`
- `pkg/kmx/agent.go`: `AgentDeployments`

The package also exposes the request, result, reference, and receipt values used
by these interfaces. Important durable identities include `EnvironmentRef`,
`TargetRef`, `RuntimeRef`, `DeploymentRef`, and the receipt types.

## Conceptual mapping for `kmx local up`

The setup-only Go operation maps conceptually to a bare `kmx local up`; the
installed command does not call it yet:

```go
package example

import (
	"context"
	"fmt"

	"github.com/kaimahi-agents/kaimahi/pkg/kmx"
)

func prepareEnvironment(ctx context.Context, environments kmx.AgentEnvironment) (kmx.Environment, error) {
	prepared, err := environments.Up(ctx, kmx.EnvironmentSpec{Name: "local"})
	if err != nil {
		return kmx.Environment{}, err
	}

	fmt.Printf("environment: %s\n", prepared.Ref.ID)
	return prepared, nil
}
```

The caller receives an `AgentEnvironment` implementation through dependency
injection. There is intentionally no package-global client, concrete adapter, or
public constructor yet.

### Result

`Up` returns:

```go
type Environment struct {
	Ref  EnvironmentRef
	Spec EnvironmentSpec
}
```

The reference identifies a logical environment, not an exact deployment target.
`EnvironmentSpec` deliberately carries no context, runtime, inference or
implementation selector. Inspection, target resolution, teardown, ownership
receipts and recovery remain deferred until production implementations prove
their semantics. `EnvironmentSpec`, `EnvironmentRef` and `Environment` have
closed validated JSON representations; no environment store, ownership record
or recovery protocol is implemented.

## Deployment remains separate

Environment setup and agent deployment are separate domains. `Up` does not yet
return the exact target/runtime values required by `AgentDeployments`:

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
	target,
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
	Runtime:   runtime.Runtime,
	Options:   kmx.LiftOptions{Reconcile: true},
})
```

Here `target` and `runtime` must come from a future exact resolution capability,
not from `Environment`. `TargetBinding` identifies exactly one target and
contains opaque, runtime-specific binding bytes. Generic KMX workflows include
those bytes in identity but do not interpret them.

## CLI-to-Go mapping

The intended conceptual mapping is:

| CLI operation | Go service operation |
|---|---|
| bare `kmx local up` | Conceptually `AgentEnvironment.Up`; not wired today |
| `kmx local down` | No `AgentEnvironment` method; current behavior is unchanged |
| `kmx suite validate` | Related to `AgentSuites.Validate`, but current command does not materialize its staged result |
| `kmx suite build` | Current one-member experimental command is not `AgentSuites.Build` |
| `kmx suite push` / root `kmx push --workspace DIR REF` | Current extracted-definition/workspace transfer is not `AgentSuites.Push` |
| Future suite-wide build/push workflow | `AgentSuites.Build` / `AgentSuites.Push` |
| Root digest `kmx lift` | Related lifecycle vocabulary, not `AgentDeployments.Lift` wiring |
| Suite status (unavailable) | `AgentDeployments.Status` remains alpha design evidence |
| Suite retirement (unavailable) | `AgentDeployments.Retire` remains alpha design evidence |

This is a product-level direction, not shared implementation today. The current
CLI does not call these northbound interfaces. It also provides `kmx suite pull`
for artifact retrieval; retrieval remains an internal input/materialization
capability rather than another northbound suite workflow method.

## Working CLI today

For an end-to-end local runtime, use the CLI:

```bash
export KIND_CLUSTER=kmx-local
export KUBE_CTX=kind-kmx-local
kmx local up
kmx status
```

`kmx local up` without `--step` runs these steps in order:

```text
cluster -> ollama -> model -> orka
```

One step can be selected explicitly:

```bash
kmx local up --step cluster
kmx local up --step ollama
kmx local up --step model
kmx local up --step orka
```

`kmx local up` prepares the runtime but does not create an agent. Use one of these
afterward:

```bash
# Author and prove one native AgentSuite workspace locally.
kmx create greeter --workspace ./greeter --model qwen2.5:3b --instructions instructions.md
kmx eval --workspace ./greeter --to-context kind-kmx-local \
  --inference provider:local --out ../private/passing.json

# Fixed demonstration that ends with an answer.
kmx quickstart

# Interactive custom authoring on current development builds.
kmx quickstart --interactive
```

Tagged v0.4.1 and earlier use `kmx quickstart-wizard` for custom authoring;
`quickstart --interactive` is not available in those releases.

## Current limitations

The Go package is alpha design evidence rather than a ready-to-instantiate SDK:

- No exported concrete implementation or constructor exists.
- The interfaces are not wired to the current CLI.
- A consumer must receive or implement `AgentEnvironment`, `AgentSuites`, and
  `AgentDeployments`.
- The Go `Up` contract establishes logical identity only. It does not expose the
  CLI's `cluster`, `ollama`, `model`, and `orka` recipe steps, nor resolve an
  exact target/runtime for deployment.
- An environment persistence store, inspection, teardown, ownership and recovery
  are not yet public contracts; the values themselves have validated JSON.
- Model or inference provisioning is not yet a separate public lifecycle
  contract.
- Concrete implementations of the northbound suite and runtime services remain
  unimplemented. The separate CLI already authors native workspace source,
  evaluates the image-free Orka and exact-v0.10.3 Go kagent profiles,
  validates/transfers AgentSuite definitions and builds one experimental image
  with Docker buildx. It neither builds a complete suite nor implements the
  northbound `Push` contract, and its images are not AgentSuite-conformant; see the
  [suite build reference](kmx.md#experimental-agentsuite-image-build).
- Native Orka and exact-v0.10.3 Go kagent workspaces with `requiredImages: []`
  are valid, evaluable and publishable, but are outside this first image-backed
  northbound Build/Push contract.

The authoritative contract details remain in:

- `pkg/kmx/doc.go`
- `docs/kmx-application-api.md`
- `docs/kmx-lifecycle-interfaces.md`
