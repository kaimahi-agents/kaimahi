# Workflow and runtime overview

[Project overview](../README.md) · [Documentation index](README.md)

Kaimahi's `kmx` CLI helps developers author and test agents for Kubernetes. A
**workspace** is editable source; its prepared **AgentSuite** is the immutable,
content-addressed OCI artifact. The selected runtime owns execution and
enforcement.

A workspace does not rename or replace AgentSuite. It is the mutable authoring
tree that KMX resolves into the portable AgentSuite artifact used for publication
and Deploy.

<a id="create-prove-lift"></a>
<a id="create-prove-deploy"></a>

## Run, Test, Publish

**Run** invokes an existing live Orka Agent with `kmx run --agent`; it is not
workspace-local execution. **Test** evaluates the workspace's authored cases with
`kmx test`. **Publish** transfers the exact tested package with `kmx push`, not a
numbered local-version workflow. **Deploy** places it on the selected real target.
Create authors the source; runtime and model setup remain separate.

For main's two-command path, start in the workspace root with a
prepared runtime and inference (Orka today), registry authentication, and
`kmx.yaml` containing `publishRepository`, context/namespace/inference selectors
for both targets and the destination's `evaluationTarget`. Create supplies none of
these. Provide an exact model name and an instructions file:

```bash
kmx create NAME --workspace . --model MODEL --instructions FILE
kmx deploy --to NAME
```

Deploy freezes the exact suite and selectors, runs missing evaluation, publication
and placement stages, and discloses source-only promotion (not destination evaluation).
Review the frozen plan before confirming. See [composed Deploy](suite-workspace.md#composed-workspace-lift)
and [named configuration](suite-workspace.md#named-destinations) for setup.

The stages are also available individually:

The individual stages support one-member native Orka and
exact-v0.10.3 Go kagent suites. Bounded CI qualifies root Deploy for Orka exact
reuse, bounded absent-pair registry promotion and kagent fresh-target creation;
it does not prove every promotion, creation or partial-outcome path.

> [!NOTE]
> The table below describes main's one-member workflow. Test and Deploy are unreleased; v0.8.1 uses `eval` and `lift` with the same arguments.
> See [command migration](../CHANGELOG.md#upgrading)
> and [releases](releases.md) when upgrading from an older version.

| Stage | Current checkout |
|---|---|
| **Create** | `kmx create NAME --workspace DIR --model MODEL --instructions FILE` authors one tool-free workspace member offline: Orka by default, exact Go kagent with `--runtime kagent`, or pinned chat with `--runtime agentsessions`. No Kubernetes, image build or model call. |
| **Test** | Orka uses `kmx test --workspace DIR --to-context CTX --inference provider:NAME --out RECEIPT`; kagent uses `--bindings FILE` instead of `--inference`. Native AgentSessions instead requires `--development-host FILE`, a private literal-loopback host JSON, with no cluster selectors. Each evaluates ordered cases with optional `runs`/`minPasses` (default once), stops after a case misses its threshold or any unknown/error, and writes private evidence only when the full policy passes. |
| **Publish** | `kmx push --workspace DIR --evaluation RECEIPT REF` checks that evidence against the exact OCI manifest and publishes that graph. Plain `kmx push REF` requires passing evidence; `--unevaluated` explicitly opts out. Neither form deploys. |
| **Deploy** | `kmx deploy REF@sha256:DIGEST --to-context CTX --evidence RECEIPT` plus Orka `--inference provider:NAME --promote` for candidate evidence or kagent `--bindings FILE` deploys the matching packaged runtime without building, repacking, or publishing. `--plan` is GET-only; kagent refuses occupied targets. Orka candidate evidence requires `--promote`, even in the same environment. Inspect placement with `kmx status --to NAME`; retirement remains unavailable. |

Root test also keeps completed passing metadata in private KMX state, so `--out`
is optional. With named `evalTarget`, `kmx test` needs no selectors; then
`kmx push REPO:TAG` uses the latest completed exact-suite proof and preserves TAG.
Bare `kmx push` requires saved `publishRepository` and derives `s-<full-S-hex>`.
Explicit-reference Deploy still requires its digest and private evidence; use
`kmx deploy --to NAME` without a reference to compose the missing stages.

Create and push discover the nearest ancestor `workspace.yaml`; `--workspace DIR`
selects an exact root without fallback. Read the [workspace lifecycle](suite-workspace.md)
for identities, evidence privacy, exact evaluated-subject reuse, absent-only
source-evidence `--promote`, partial outcomes, and fresh-evaluation requirements.

## Getting Started: Get Your First Answer

**On Windows, use WSL for now.** Install and run kmx inside your Linux
WSL distribution, not PowerShell or Command Prompt. Native Windows builds
are not supported yet; track [Windows support in #578](https://github.com/kaimahi-agents/kaimahi/issues/578).

Install Docker or Podman (`CONTAINER_ENGINE=podman` selects Podman), then choose
one independent install route for the latest stable release. Go installs require
Go 1.26 or newer:

```bash
brew install kaimahi-agents/tap/kmx
# or
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
# or
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.8.1
```

Check what was installed, then run the fixed, non-interactive local proof:

```bash
kmx version
kmx quickstart
```

`kmx quickstart` creates a local kind environment, starts Ollama and pinned Orka,
and succeeds only after a fresh Task returns a readable answer. Use
`kmx quickstart --interactive` for guided native workspace authoring, full eval, publication and reviewed Deploy.
Follow [getting started](getting-started.md) for prerequisites and explicit
context selection.

## Workspace And Live Paths

| Path | What it means |
|---|---|
| Workspace | Default `kmx create`, root `test`, and root `push` operate on editable source; `kmx deploy --to NAME` composes missing evaluation, publication and placement from that workspace. Digest-based root `deploy` does not consume workspace source; it uses the packaged artifact and separate private evidence with explicit target and inference inputs. Run/chat still target live Agents. |
| Local environment | `kmx local up` prepares kind, model and runtime without creating an Agent. The experimental Go `AgentEnvironment` interface is setup-only and is not wired to this command yet. |
| Live Agent | `kmx run --agent NAME --prompt TEXT` performs one Task; `kmx chat NAME` opens an Orka session. Both target live Agents, including suite-owned Agents; neither is workspace-local execution or suite evaluation. |
| Legacy bundle | Removed outright: user `agent.yaml` readers/writers, commands and receipts, `create --live` including previews/wizard, run's bundle mode and `--bundles` flags. Old receipts are unreadable; no conversion, migration or on-disk/resource deletion. Public AgentSuite implementation bundles are a different format and remain supported. |

## Runtime Boundaries

A **runtime** executes and governs the agent. An **inference provider** is the
separate model-access binding; selecting one does not select the other.

- **Orka** is the default runtime. Its packaged workspace path supports
  evaluation, evaluated publication and root digest Deploy with exact evaluated
  reuse; independent live Orka run and chat remain.
- **kagent** support is exact-version and operation-specific: v0.10.3
  Go/linux-amd64 packaged workspace authoring, `test --bindings`, evaluated
  publication and create-only root digest Deploy. Standalone run/chat, Python,
  RemoteMCP and retirement remain unavailable. Read-only destination status
  observes placement without enabling those execution/lifecycle capabilities.
- **agentsessions** supports pinned-chat offline suite create/prepare/load/ordinary
  push and explicit private `test --development-host FILE`: one fresh
  Session/config/Exec per declared run, full-pass private proof and exact-S evaluated
  push. This unauthenticated literal-loopback development path loads no cluster
  settings; explicit judging and repeated cases share Orka/kagent semantics. It supports no tools, Deploy/chat/status or remote lifecycle.
  Receipt diff refuses its proof. Read-only reference extraction/export and
  availability probing remain separate; legacy bundle evaluation/replay is removed.

The [runtime support matrix](runtime-adapters.md#revision-pinned-support-and-qualification-matrix)
records exact pins and evidence rather than claiming feature parity.

## Safety And Status

> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** It is not recommended
> for production use. Use a dedicated test environment, review every proposed
> mutation, and remember that hosted model calls can incur charges.

- Readiness, a readable answer, and passing evaluation are different evidence.
- [`kmx status --to NAME`](destination-status.md) inventories deployed
  Orka/kagent Agents and per-object Deploy provenance without writes. Unknown is not
  absent, and recorded observations are unsigned. `kmx orka status` retains the
  table-only Orka runtime report.
- Quickstart adds cluster resources and result-reader RBAC, then authors, fully
  evaluates, publishes and Deploys a tool-free native suite. Placement verification
  precedes its separate fresh answer Task. Console Ready means placement only.
- Chat `/lift` and console Deploy only print `Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …`; no release GUI or live-copy fallback remains.
- Automatic wizard tools and live tools/inference editing are retired; ordinary
  host Foundry/Copilot chat controls remain independent.
- Cancellation can leave completed resources in place. `kmx local down` deletes
  the entire named local cluster and its data.
- Unsigned evaluation receipts and provenance are observations, not
  authentication or promotion authority.

> [!NOTE]
> kmx is the Agent Builder and lifecycle layer, not a runtime or generic
> governance control plane. The selected runtime owns execution and enforcement.

## Guides

| Need | Guide |
|---|---|
| Install and get one answer | [Getting started](getting-started.md) |
| Author, evaluate and publish a workspace | [Workspace lifecycle](suite-workspace.md) |
| Understand exact runtime support | [Runtime support](runtime-adapters.md) |
| Find every implemented command | [`kmx` reference](kmx.md) |
| Read the portable artifact contract | [AgentSuite specification](https://github.com/agent-primitives/suite/blob/main/spec.md) |
| Select models and inference | [Models](models.md), [Copilot](copilot-inference.md), [Foundry](local-foundry-inference.md) |
| Run a local-proof to ACR to AKS example | [AKS Create, Test, Deploy demo](aks-demo.md) |
| Install or upgrade | [Releases](releases.md) |
| Browse all guides | [Documentation index](README.md) |

## Development

From a checkout, `make` builds `bin/kmx` without provisioning. Start with the
[developer guide](development.md), [codebase overview](codebase-overview.md),
and [interface baseline](interface-baseline.md). The [repository map](repository-map.md)
classifies tracked source and scaffolding; [contributing](../CONTRIBUTING.md) defines
verification and pull-request expectations. See [naming](NAMING.md) for
publication boundaries. Run `make docs-preview` for the browser-style documentation
preview generated directly from these Markdown files.
