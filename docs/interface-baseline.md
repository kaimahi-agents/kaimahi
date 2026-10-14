# KMX interface baseline

This document is a reviewable snapshot of the interfaces implemented on current
`main`. It is not a proposal for future syntax. The [`kmx` reference](kmx.md)
remains authoritative for command flags and output contracts.

## Installed command tree

Visible product commands:

```text
kmx
├── create [name]
├── eval
│   ├── diff <before.json> <after.json>
│   └── from-sessions <session-uid>...
├── push <reference>
├── lift <ref>@<digest>
├── run [<bundle-dir>]
├── chat <name> [message...]
├── quickstart
├── status
├── console
├── targets
├── context
│   ├── show
│   └── use <context>
├── local
│   ├── up
│   └── down
├── orka
│   ├── install
│   ├── status
│   ├── show <name>
│   └── list
├── suite
│   ├── validate <path>
│   ├── diff <before> <after>
│   ├── build <directory>
│   ├── push <directory> <reference>
│   └── pull <reference>
├── task
│   └── result <task>
├── completion bash|zsh|fish
└── version
```

`eval` is both an operational workspace command and the parent of two offline
helpers. Cobra also supplies `help`.

Hidden but callable:

| Route | Contract |
|---|---|
| `kmx bundle evaluate/lift/retire/status/verify` | Legacy PortableAgent lifecycle; active until suite parity, not an AgentSuite command |
| `kmx aks up/down` | Guarded AKS compatibility path with historical ownership records |
| `kmx quickstart-wizard` | Retirement stub directing callers to `quickstart --interactive` |

Removed outright: `kmx agent`, root `ctx`, `up`, `down`, the former
AKS-provisioning meaning of root `lift`, and `lift down`. They are not
compatibility aliases. Root `lift <ref>@<digest>` is now the distinct deploy-only
one-member packaged native Orka/exact-v0.10.3 Go kagent operation.

## Everyday object flow

```text
Workspace --create/eval--> AgentSuite artifact --push--> OCI location
                                                    |
                                                    +-> exact-digest native lift

create --live -----------> Runtime resources + PortableAgent bundle
                                               -> hidden bundle lifecycle
```

| Command | Primary object | Current effect |
|---|---|---|
| `create` | Workspace | Author one offline member; no cluster or model call |
| `eval` | Workspace/prepared AgentSuite | Place exact packaged Orka or exact-v0.10.3 Go kagent content, run cases in order, and stop later cases after the first non-pass |
| `push` | Prepared AgentSuite | Transfer exact OCI graph; optional full-passing receipt check; no deployment |
| `lift` | Exact supported packaged native AgentSuite | Deploy by immutable digest with explicit private evidence and target; Orka uses `--inference`, kagent uses `--bindings`; GET-only with `--plan` |
| `create --live` | Runtime resources + PortableAgent bundle | Explicit legacy/live path |
| `run`, `chat` | Existing live Orka Agent | One Task or interactive session; never workspace execution |
| `status`, `orka status` | Orka installation | Table-only runtime report, not suite deployment status |
| `bundle status` | Legacy PortableAgent deployment | Readiness, revision, drift and evaluation evidence |

## Workspace versus live

Default `create` requires one name, exact `--model`, and file-valued
`--instructions`. It discovers the nearest `workspace.yaml`; `--workspace DIR`
selects or initializes one exact root. The current source profile has a
one-member limit.

`--live` changes the operation rather than selecting another workspace mode. It
is required for resource creation, previews, no-name wizard use, and exact
kagent v0.10.2 live create. `--workspace` and `--live` conflict.

Root `eval` is packaged native workspace execution, not legacy bundle evaluation.
Orka uses `--inference`; exact-v0.10.3 Go kagent uses `--bindings`. Both can
produce runtime-specific evidence for evaluated push. Legacy bundle evaluation
remains `kmx bundle evaluate` and can explicitly select exact kagent v0.10.3 Go
or agentsessions within their qualified boundaries.

## Context, environment, target and inference

- A **context** is a kubeconfig access selector. KMX does not follow kubectl's
  mutable current-context.
- An experimental **Environment** is logical setup identity. The setup-only
  `pkg/kmx.AgentEnvironment` is not wired to `kmx local up`.
- A **target** is an exact runtime, physical destination and namespace.
- **Inference** is an independent model route; choosing it does not select a
  runtime.

`kmx targets` is an offline compiled capability report by default. Explicit
probes distinguish present, absent and unreadable; they do not select, install
or deploy a runtime.

## Runtime boundaries

| Runtime/integration | Implemented KMX scope |
|---|---|
| Orka | Default runtime; setup, live create, run/chat, workspace evaluation/evaluated publication, exact-digest native Lift, and legacy bundle lifecycle |
| kagent v0.10.2 | Explicit live create and optional one-shot A2A only; KMX does not install it |
| kagent v0.10.3 Go | Workspace authoring, packaged evaluation/evaluated publication and create-only root digest Lift; legacy bundle lift/status/evaluation; no packaged status/retirement |
| agentsessions | Pinned-chat suite author/prepare/load/push; explicit loopback operator-observed development eval/full-pass evidence and exact-S evaluated push; legacy bundle evaluation and zero-call reference replay. No registry/auth, Lift/chat/status or remote lifecycle |

Support is operation- and version-specific. See the [runtime support
matrix](runtime-adapters.md#revision-pinned-support-and-qualification-matrix).

## Public Go surfaces

The repository has two distinct public Go areas:

1. `agentsuite/` is the implemented portable format library: model, media types,
   canonical JSON, schemas, offline validation and semantic diff.
2. `pkg/kmx/` is alpha design evidence. It has exactly three exported
   interfaces and no concrete implementation or CLI wiring.

```go
type AgentEnvironment interface {
    Up(context.Context, EnvironmentSpec) (Environment, error)
}

type AgentSuites interface {
    Validate(context.Context, ValidateSuiteRequest) (ValidatedAgentSuite, error)
    Build(context.Context, BuildSuiteRequest) (BuildSuiteResult, error)
    RecoverBuild(context.Context, OperationID) (BuildSuiteProgress, error)
    Push(context.Context, PushSuiteRequest) (PushSuiteResult, error)
    RecoverPush(context.Context, OperationID) (PushSuiteResult, error)
}

type AgentDeployments interface {
    BuildRevision(context.Context, BuildRequest) (BuildResult, error)
    Lift(context.Context, LiftRequest) (DeploymentReceipt, error)
    RecoverLift(context.Context, OperationID) (DeploymentReceipt, error)
    Status(context.Context, DeploymentRef) (DeploymentSnapshot, error)
    Retire(context.Context, RetireRequest) (RetirementReceipt, error)
    RecoverRetire(context.Context, OperationID) (RetirementReceipt, error)
}
```

Important limits:

- `AgentEnvironment` establishes logical identity only; target resolution,
  inspection, persistence, recovery and teardown are deferred.
- `AgentSuites.Build/Push` is an initial image-backed model. Native profiles
  requiring zero member images do not implement that northbound workflow.
- `AgentDeployments` still models one revision or sandbox image, not a complete
  suite deployment.
- The installed CLI uses `internal/kmx/runtime`, not these interfaces.

## Compatibility and evidence

- Latest stable v0.5.1 retains the former `kmx agent ...` routes. Root
  create/eval/push/lift and hidden bundle routes are unreleased development behavior.
- Help and completion must remain inert.
- Unknown is not absent; readiness is not a result; a result is not evaluation.
- Answers and report documents own stdout. Progress, warnings and notices use
  stderr.
- Receipts and provenance omit credentials and are not authentication.
- Effectful sends are not retried after an ambiguous outcome.

Before changing an interface, update command registration/help/completion,
generated recovery instructions, primary docs, CHANGELOG and meaningful refusal
tests in the same slice. Follow [command conventions](command-conventions.md),
[command alignment](command-semantics.md), and [AGENTS.md](../AGENTS.md).
