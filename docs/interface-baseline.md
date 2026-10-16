# KMX interface baseline

This document is a reviewable snapshot of the interfaces implemented on current
`main`. It is not a proposal for future syntax. The [`kmx` reference](kmx.md)
remains authoritative for command flags and output contracts.

## Installed command tree

Visible product commands:

```text
kmx
├── create <name>
├── eval
│   ├── diff <before.json> <after.json>
│   └── from-sessions <session-uid>...
├── push [reference]
├── lift [<ref>@<digest>]
├── run --agent <name>
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
│   ├── policy <file>
│   ├── push <directory> <reference>
│   └── pull <reference>
├── task
│   └── result <task>
├── completion bash|zsh|fish
└── version
```

`eval` is an operational workspace command with offline diff and independent
read-only reference extraction/export helpers. Cobra also supplies `help`.

Hidden but callable:

| Route | Contract |
|---|---|
| `kmx aks up/down` | Guarded AKS compatibility path with historical ownership records |
| `kmx quickstart-wizard` | Retirement stub directing callers to `quickstart --interactive` |

Removed outright: `kmx agent`, root `ctx`, `up`, `down`, the former
AKS-provisioning meaning of root `lift`, and `lift down`. They are not
compatibility aliases. Root `lift <ref>@<digest>` is now the distinct deploy-only
one-member packaged native Orka/exact-v0.10.3 Go kagent operation.
User `agent.yaml` readers/writers/receipts, `bundle` commands, `create --live`
including previews/wizard, root run bundle mode and `--bundles` flags are also
removed outright. Old bundle receipts are unreadable; no migration/resource deletion.

## Everyday object flow

```text
Workspace --create/eval--> AgentSuite artifact --push--> OCI location
                                                    |
                                                    +-> exact-digest native lift
```

| Command | Primary object | Current effect |
|---|---|---|
| `create` | Workspace | Author one offline member; no cluster or model call |
| `eval` | Workspace/prepared AgentSuite | Run full ordered native Orka/kagent cases, or pinned AgentSessions chat with explicit `--development-host FILE`; stop later cases after the first non-pass |
| `eval export` | Latest completed exact-suite/member managed proof | Write new detached public evidence offline; captured tagged module/checksum or clean VCS provenance required, no evaluation or private-gate replacement |
| `push` | Prepared AgentSuite | Transfer exact OCI graph with latest completed exact-S proof, explicit --evaluation override or --unevaluated opt-out; explicit tags unchanged, bare push uses saved `repository:s-<S>`; no deployment |
| `lift` | Workspace or exact supported packaged native AgentSuite | No-reference `--to NAME` composes missing Orka eval/push/Lift with one frozen-plan confirmation and verified exact-version skips. Explicit reference stays deploy-only by digest with private evidence; Orka uses `--inference`, kagent uses `--bindings`; `--plan` has no execution/publication/admission |
| `run`, `chat` | Existing live Orka Agent | One Task or interactive session; never workspace execution |
| `status`, `orka status` | Orka installation | Table-only runtime report, not suite deployment status |

## Workspace versus live

Default `create` requires one name and exact model/file-valued instructions.
With visible stdin/stderr terminals, missing inputs prompt; nonterminal use
requires `--model` and `--instructions`, and explicitly blank flags refuse.
No model or instructions defaults are inferred. It discovers the nearest `workspace.yaml`; `--workspace DIR`
selects or initializes one exact root. The current source profile has a
one-member limit.

Live create/previews/wizard are removed; their flags are unknown before config.
Root eval runs full native authored cases only: Orka uses `--inference`, kagent
uses `--bindings`; pinned AgentSessions chat instead requires a private
`--development-host FILE`, with no cluster selectors or fallback. All export only
runtime-specific full-passing evidence. Orka/kagent and development AgentSessions support authored `llmJudge`
with explicit judge transport and identity, plus optional KMX case `runs`/`minPasses`
(default one run/all passes; unknown vetoes). AgentSessions uses fresh Session/config/Exec per run. Repeat field names/defaults/validation follow
[AgentSuite §8.2.2](https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions):
`minPasses` requires `runs`, and both are positive integers. KMX's execution cap
is 100 runs, within the spec's consumer resource-limit allowance.
No subset, recorded Sessions input or runtime/model override. Offline diff reads only Orka/kagent native suite receipts;
AgentSessions evidence is explicitly refused by diff and Lift;
`eval from-sessions` is reference extraction/export without a native consumer.
Suite-owned live resources refuse legacy invocation. Chat `/lift` and console
Lift only print `Lift moved: kmx lift <ref>@<digest> --to-context … --evidence …`.
No new GUI or source-less copying. Console Ready means placement, not an answer.

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
| Orka | Default runtime; setup, independent live run/chat, workspace evaluation/evaluated publication and exact-digest native Lift; no suite status/retirement |
| kagent v0.10.3 Go | Workspace authoring, packaged evaluation/evaluated publication and create-only root digest Lift; no packaged status/retirement |
| agentsessions | Pinned-chat native suite author/prepare/source-free load/ordinary push; explicit literal-loopback operator-observed development eval with full-passing private evidence and exact-S evaluated push. One fresh Session/config/Exec per ordered case run, shared explicit judging and repeat policy, no tools, cluster settings, registry/auth or remote lifecycle; Lift/chat/status refuse. Independent read-only reference extraction/export and availability probing remain; removed legacy bundle replay/evaluation is not this native path. |

Support is operation- and version-specific. See the [runtime support
matrix](runtime-adapters.md#revision-pinned-support-and-qualification-matrix).

## Public Go surfaces

The CLI consumes a portable Go library and retains its own alpha public area:

1. `github.com/agent-primitives/suite` is the separate portable format module:
   model, media types, canonical JSON, schemas, offline validation and semantic
   diff. Consumers import it explicitly as `agentsuite`.
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

- Stable v0.7.0 includes root create/eval/push/lift and legacy bundle removal.
  Historical v0.5.1 retains the former `kmx agent ...` routes.
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
