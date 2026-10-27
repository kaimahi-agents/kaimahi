# KMX interface baseline

This document is a reviewable snapshot of the interfaces implemented on current
`main`. It is not a proposal for future syntax. The [`kmx` reference](kmx.md)
remains authoritative for command flags and output contracts.

`test` and `deploy` are unreleased; v0.8.1 uses `eval` and `lift` with the same
arguments. Hidden `eval` and its six helpers remain callable for at least one
released version, with one stderr notice and unchanged arguments, stdout, exit
codes and state. See [Test compatibility](kmx.md#test-compatibility-unreleased). The deprecated
packaged-suite `lift` route stays hidden but callable for at least one released
version, with one stderr notice and unchanged legacy output/exit contracts. See
[Deploy compatibility](kmx.md#deploy-compatibility-unreleased). Public Go/API
`Lift` names and machine fields remain unchanged.

## Installed command tree

Root help shows `create`, `run`, `test`, `chat`, `model`, `package`, `cluster`,
then `version`, with the tagline “Write, run and test agent files.” The tree below
expands those visible product commands:

```text
kmx
├── create <name>                 (native workspace authoring, unchanged)
├── run [FILE [input]] | --agent <name>
├── test FILE
│   └── explain [failure-id] --file FILE
├── chat <name> [message...]
├── model
│   ├── list [--provider local|copilot] [-o table|json]
│   ├── login [--gh]
│   └── logout
├── package
│   ├── test
│   │   ├── cleanup [run-id]
│   │   ├── diff <before.json> <after.json>
│   │   ├── explain [failure-id]
│   │   ├── export --member <id> --out <file>
│   │   ├── verify --suite <layout-or-digest> --evidence <file> --expect <file>
│   │   └── from-sessions <session-uid>...
│   ├── push [reference]
│   ├── deploy [<ref>@<digest>]
│   ├── status --to <destination>
│   ├── ctx [--check]
│   ├── validate <path>
│   ├── build <directory>
│   ├── push-image <archive> <reference>
│   ├── pull <reference>
│   ├── policy <file>
│   ├── suite
│   │   └── push <directory> <reference>
│   └── run-profile
│       └── export FILE
├── cluster
│   ├── up
│   ├── down
│   ├── status                     (same Orka runtime report as orka status)
│   ├── context
│   │   ├── show
│   │   └── use <context>
│   ├── quickstart
│   ├── console
│   ├── targets
│   ├── orka
│   │   ├── install
│   │   ├── status
│   │   ├── show <name>
│   │   └── list
│   └── task
│       └── result <task>
└── version
```

`package test` is an operational workspace command with offline diff and independent
read-only reference extraction/export helpers; explicit `test FILE` is a local
preview whose completed non-pass diagnostics can be explained offline with
`test explain --file FILE`. Explicit `run FILE` is the corresponding preview Run.
`kmx mcp --root DIR`, `kmx completion bash|zsh|fish` and `kmx help [TOPIC...]`
remain operational but are omitted from root help. MCP and Completion are also
omitted from root completion suggestions; explicit invocation still works.
Experimental `package push-image` publishes a verified OCI image graph, including
attestations, independently of deployment admission. Replacing a different tag
requires `--force`; same-digest publication is a no-op. `package build --suite-ref`
checks complete source content through the shared bounded materializer.

On native Windows, the same discovery rules apply. Visibility in help/completion
is not an enabled capability list. Current `main` permits only version/help/
completion, package-free fake `run FILE` / `test FILE`, and offline
`test explain ... --file FILE` (plus the hidden legacy Explain spelling for
compatibility). Every other route refuses before effects. See the
[native Windows command boundary](windows.md#supported-native-commands).

Unreleased `model list` defaults to local discovery; explicit Copilot listing
uses explicit `COPILOT_GITHUB_TOKEN`, then an existing gh sign-in without keychain
setup, then kmx's existing keychain item if gh is unavailable. It never starts sign-in or saves
runtime tokens. `model login` uses GitHub device flow; `--gh` explicitly copies
bounded private gh output to the OS keychain. Without a usable keychain, login
explains runtime options instead. `model logout` clears only kmx's item/memory,
not gh or environment credentials. There is no plaintext token-file fallback. Internal
`modelaccess/copilot.New(Store).Client(ctx, Route)` returns an authenticated
selected-route Chat-shaped RoundTripper with catalog-selected Chat/Responses
JSON/SSE upstream access, not a proxy. Markdown-file Run/Test uses explicit
`--provider copilot`; unbound MCP defaults the effective real model to Copilot.
File preview model precedence is runtime picker/tool override, authored model,
then `gpt-5.6-terra`; the source is never rewritten. Run/Test v1 accept optional
`model`; read-only `kmx://models` discovers choices without a fifth tool. Both
use the exact selected model and existing runtime credentials, with no login
step for a working gh sign-in. The Copilot endpoint is fixed. This remains a
tool-free journaled embedded-Sessions preview, not a new harness.
No inference replay, model substitution or persisted selection. See
[direct model access](copilot-inference.md#direct-model-access). These model and
provider routes remain outside the narrow native Windows CLI, regardless of
package-level Windows tests.

Hidden but callable:

| Route | Contract |
|---|---|
| `kmx eval` and `cleanup/diff/explain/export/verify/from-sessions` | Deprecated Test spellings; same arguments/flags/stdout/exit/state, exactly one stderr notice per legacy invocation |
| `kmx lift [<ref>@<digest>]` | Deprecated deployment spelling; same arguments/flags, one stderr notice, legacy stdout and stderr stage labels retained |
| `kmx cluster aks up/down` | Guarded AKS compatibility path with historical ownership records |
| `kmx cluster quickstart-wizard` | Retirement stub directing callers to `quickstart --interactive` |

Removed outright: `kmx agent`, root `ctx`, `up`, `down`, the former
AKS-provisioning meaning of root `lift`, and `lift down`. They are not
compatibility aliases. Root `deploy <ref>@<digest>` is now the distinct deploy-only
one-member packaged native Orka/exact-v0.10.3 Go kagent operation.
User `agent.yaml` readers/writers/receipts, `bundle` commands, `create --live`
including previews/wizard, root run bundle mode and `--bundles` flags are also
removed outright. Old bundle receipts are unreadable; no migration/resource deletion.

## Everyday object flow

```text
Workspace --create/test--> AgentSuite artifact --push--> OCI location
                                                    |
                                                    +-> exact-digest native deploy
```

| Command | Primary object | Current effect |
|---|---|---|
| `create` | Workspace | Author one offline member; no cluster or model call |
| `test` | Workspace/prepared AgentSuite | Run full ordered native Orka/kagent cases, or pinned AgentSessions chat with explicit `--development-host FILE` and `--bindings FILE` (`AgentSessionsBinding`); stop later cases after the first non-pass |
| `test export` | Latest completed exact-suite/member managed proof | Write new detached public evidence offline; captured tagged module/checksum or clean VCS provenance required, no evaluation or private-gate replacement |
| `run-profile export FILE` | Canonical agent file | Content-bearing frozen JSON on stdout, with source/instruction/model/example hashes and target examples `not_run`; no configuration, execution, evidence, package or deployment authority |
| `test verify` | Local single-manifest OCI layout + detached evidence + reviewed ExpectedEvaluation | State-free offline CI gate; digest input also requires explicit layout, text/JSON success exits0, coded refusal exits1; no private-gate authority |
| `push` | Prepared AgentSuite | Transfer exact OCI graph with latest completed exact-S proof, explicit --evaluation override or --unevaluated opt-out; explicit tags unchanged, bare push uses saved `repository:s-<S>`; no deployment |
| `deploy` | Workspace or exact supported packaged native AgentSuite | No-reference `--to NAME` composes missing Orka eval/push/Deploy with one frozen-plan confirmation and verified exact-version skips. Explicit reference stays deploy-only by digest with private evidence; Orka uses `--inference`, kagent uses `--bindings`; `--plan` has no execution/publication/admission; a named AgentSessions destination creates an immutable Agent with the digest/member/operator default binding, with GET-only exact-spec no-op and bounded registration wait; `--name` selects a new name. The offline `--harness-image`/`--bindings --plan` render remains separate |
| `run`, `chat` | Existing live Orka Agent | One Task or interactive session; never workspace execution |
| `status --to NAME` | Named destination namespace | GET-only table/JSON deployed-Agent provenance/readiness inventory; separate candidates, unknown is not absent |
| `orka status` | Orka installation | Table-only runtime report, not destination placement inspection |

## Workspace versus live

Default `create` requires one name and exact model/file-valued instructions.
With visible stdin/stderr terminals, missing inputs prompt; nonterminal use
requires `--model` and `--instructions`, and explicitly blank flags refuse.
No model or instructions defaults are inferred. It discovers the nearest `workspace.yaml`; `--workspace DIR`
selects or initializes one exact root. The current source profile has a
one-member limit.

Live create/previews/wizard are removed; their flags are unknown before config.
Root test runs full native authored cases only: Orka uses `--inference`, kagent
uses `--bindings`; pinned AgentSessions chat instead requires a private
`--development-host FILE` plus an `AgentSessionsBinding` (`--bindings`), with no
cluster selectors or fallback. All export only
runtime-specific full-passing evidence. Orka/kagent and development AgentSessions support authored `llmJudge`
with explicit judge transport and identity, plus optional KMX case `runs`/`minPasses`
(default one run/all passes; unknown vetoes). AgentSessions uses fresh Session/config/Exec per run. Repeat field names/defaults/validation follow
[AgentSuite §8.2.2](https://github.com/agent-primitives/suite/blob/main/spec.md#822-case-documents-and-assertions):
`minPasses` requires `runs`, and both are positive integers. KMX's execution cap
is 100 runs, within the spec's consumer resource-limit allowance.
No subset, recorded Sessions input or runtime/model override. Offline diff reads only Orka/kagent native suite receipts;
AgentSessions evidence is refused by diff and feeds create-only Agent deployment or the separate offline AgentSessions Deploy render;
`test from-sessions` is reference extraction/export without a native consumer.
Root `run --agent` and supported Orka chat select live Agents by
context/namespace/name, including suite-owned Agents. They are not workspace-local
execution or fresh suite evaluation and grant no adoption/update/copy/retirement
authority. Chat `/lift` and console Deploy only print
`Deploy moved: kmx package deploy <ref>@<digest> --to-context … --evidence …`.
No new GUI or source-less copying. Console Ready means placement, not an answer.

## Context, environment, target and inference

- A **context** is a kubeconfig access selector. KMX does not follow kubectl's
  mutable current-context.
- An experimental **Environment** is logical setup identity. The setup-only
  `pkg/kmx.AgentEnvironment` is not wired to `kmx cluster up`.
- A **target** is an exact runtime, physical destination and namespace.
- **Inference** is an independent model route; choosing it does not select a
  runtime.

`kmx cluster targets` is an offline compiled capability report by default. Explicit
probes distinguish present, absent and unreadable; they do not select, install
or deploy a runtime.

## Runtime boundaries

| Runtime/integration | Implemented KMX scope |
|---|---|
| Orka | Default runtime; setup, independent live run/chat, workspace evaluation/evaluated publication and exact-digest native Deploy; GET-only destination status, no retirement |
| kagent v0.10.3 Go | Workspace authoring, packaged evaluation/evaluated publication and create-only root digest Deploy; GET-only destination status, no retirement |
| agentsessions | Pinned-chat native suite author/prepare/source-free load/ordinary push; explicit literal-loopback operator-observed development eval with full-passing private evidence and exact-S evaluated push. One fresh Session/config/Exec per ordered case run, shared explicit judging and repeat policy, no tools, cluster settings, registry/auth or remote lifecycle; eval requires an `AgentSessionsBinding` serving the packaged model; Deploy creates an immutable Agent at a named AgentSessions destination and status reports exact current registration; the offline ActorTemplate/HarnessRegistration render remains available. Chat refuses. Independent read-only reference extraction/export and availability probing remain; removed legacy bundle replay/evaluation is not this native path. |

Support is operation- and version-specific. See the [runtime support
matrix](runtime-adapters.md#revision-pinned-support-and-qualification-matrix).

## Public Go surfaces

The CLI consumes a portable Go library, exposes one concrete read-only package,
and retains its own alpha lifecycle area:

1. `github.com/agent-primitives/suite` is the separate portable format module:
   model, media types, canonical JSON, schemas, offline validation and semantic
   diff. Consumers import focused `core`, `oci`, `evidence` and
   `experimental/flow` packages, not its deprecated root.
2. `pkg/suiteview/` is a concrete local OCI layout/archive loader for one
   explicit member. It returns copied instructions, the complete portable model
   requirement and exact artifact/graph/member/instruction identities with typed
   safe refusals. It has no runtime, evidence, registry or mutation authority.
3. `pkg/kmx/` is alpha design evidence. It has exactly three exported
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
generated recovery instructions, primary docs, a `changes/` release-note fragment and meaningful refusal
tests in the same slice. Follow [command conventions](command-conventions.md),
[command alignment](command-semantics.md), and [AGENTS.md](../AGENTS.md).
