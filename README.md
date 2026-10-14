<div align="center">

<img src="brand/ketu.svg" alt="Kaimahi ketu mark" width="128" />

# Kaimahi

**Agent Builder CLI for Kubernetes.**

[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)

[Getting started](docs/getting-started.md) · [Workspace lifecycle](docs/suite-workspace.md) · [Runtime support](docs/runtime-adapters.md) · [`kmx` reference](docs/kmx.md) · [Developer guide](docs/development.md) · [Contributing](CONTRIBUTING.md)

<sub>[About the ketu mark](brand/README.md#ketu-mark)</sub>

</div>

---

Kaimahi's `kmx` CLI helps developers author and prove agents for Kubernetes. A
**workspace** is editable source; its prepared **AgentSuite** is the immutable,
content-addressed OCI artifact. The selected runtime owns execution and
enforcement.

A workspace does not rename or replace AgentSuite. It is the mutable authoring
tree that KMX resolves into the portable AgentSuite artifact used for publication
and Lift.

## Create, Prove, Lift

**Create → Prove → Lift** is implemented on current `main` for one-member native
Orka and exact-v0.10.3 Go kagent suites. The commands remain unreleased. Bounded
CI qualifies root Lift for Orka exact reuse and kagent fresh-target creation; root
Lift qualification does not prove every promotion, creation or partial-outcome path.

> [!NOTE]
> The table below describes the current development build, which is unreleased.
> The latest tagged release is v0.5.1 and still uses `kmx agent ...` for its
> PortableAgent lifecycle. See [command migration](CHANGELOG.md#upgrading) and
> [releases](docs/releases.md) before scripting against `main`.

| Stage | Current development build |
|---|---|
| **Create** | `kmx create NAME --workspace DIR --model MODEL --instructions FILE` authors one tool-free workspace member offline. It does not contact Kubernetes, build an image, or run a model. |
| **Prove** | `kmx eval --workspace DIR --to-context CTX --inference provider:NAME --out RECEIPT` runs ordered native Orka cases once, stops later cases after the first non-pass, and writes private evidence only when all cases pass. |
| **Publish** | `kmx push --workspace DIR --evaluation RECEIPT REF` checks that evidence against the exact OCI manifest and publishes that graph. Plain `kmx push REF` remains explicitly unevaluated. Neither form deploys. |
| **Lift** | `kmx lift REF@sha256:DIGEST --to-context CTX --evidence RECEIPT` plus Orka `--inference provider:NAME` or kagent `--bindings FILE` deploys the matching packaged runtime without building, repacking, or publishing. `--plan` is GET-only; kagent refuses occupied targets; hidden `kmx bundle lift` remains the separate legacy PortableAgent operation. |

Create and push discover the nearest ancestor `workspace.yaml`; `--workspace DIR`
selects an exact root without fallback. Read the [workspace lifecycle](docs/suite-workspace.md)
for identities, evidence privacy, exact evaluated-subject reuse, absent-only
source-evidence `--promote`, partial outcomes, and fresh-evaluation requirements.

## Getting Starter: Get Your First Answer

Install Docker or Podman (`CONTAINER_ENGINE=podman` selects Podman), then choose
one independent install route for the latest stable release. Go installs require
Go 1.26 or newer:

```bash
brew install kaimahi-agents/tap/kmx
# or
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
# or
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.5.1
```

Check what was installed, then run the fixed, non-interactive local proof:

```bash
kmx version
kmx quickstart
```

`kmx quickstart` creates a local kind environment, starts Ollama and pinned Orka,
and succeeds only after a fresh Task returns a readable answer. Use
`kmx quickstart --interactive` for the experimental guided live-agent path.
Follow [getting started](docs/getting-started.md) for prerequisites and explicit
context selection.

## Workspace And Live Paths

| Path | What it means |
|---|---|
| Workspace | Default `kmx create`, root `eval`, and root `push` operate on editable source. Digest-based root `lift` does not consume workspace source; it uses the packaged artifact and separate private evidence with explicit target and inference inputs. Run/chat still target live Agents. |
| Local environment | `kmx local up` prepares kind, model and runtime without creating an Agent. The experimental Go `AgentEnvironment` interface is setup-only and is not wired to this command yet. |
| Live Agent | `kmx create --live` creates runtime resources and a PortableAgent bundle. `kmx run --agent NAME --prompt TEXT` performs one Task; `kmx chat NAME` opens an Orka session. |
| Legacy bundle | Hidden but callable `kmx bundle ...` commands retain PortableAgent lift, status, evaluation, verification and retirement until suite-path parity. They are not AgentSuite commands. |

## Runtime Boundaries

A **runtime** executes and governs the agent. An **inference provider** is the
separate model-access binding; selecting one does not select the other.

- **Orka** is the default runtime. Its packaged workspace path supports
  evaluation, evaluated publication and root digest Lift with exact evaluated
  reuse; it also owns live create, run, chat and the legacy bundle lifecycle.
- **kagent** support is exact-version and operation-specific: v0.10.2 live
  create; v0.10.3 Go packaged workspace authoring, `eval --bindings`, evaluated
  publication, create-only root digest Lift, and legacy bundle
  lift/status/evaluation. Packaged status and retirement are unavailable.
- **agentsessions** is evaluation and replay integration for the legacy bundle
  path, not a KMX deployment target.

The [runtime support matrix](docs/runtime-adapters.md#revision-pinned-support-and-qualification-matrix)
records exact pins and evidence rather than claiming feature parity.

## Safety And Status

> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** It is not recommended
> for production use. Use a dedicated test environment, review every proposed
> mutation, and remember that hosted model calls can incur charges.

- Readiness, a readable answer, and passing evaluation are different evidence.
- `kmx status` and `kmx orka status` report Orka runtime state, not AgentSuite
  deployment status.
- Quickstart and guided live creation add cluster resources and result-reader
  RBAC. The wizard also adds a [read-only Kubernetes Tool with cross-namespace
  permissions](docs/orka-k8s-tool.md).
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
| Install and get one answer | [Getting started](docs/getting-started.md) |
| Author, evaluate and publish a workspace | [Workspace lifecycle](docs/suite-workspace.md) |
| Understand exact runtime support | [Runtime support](docs/runtime-adapters.md) |
| Find every implemented command | [`kmx` reference](docs/kmx.md) |
| Read the portable artifact contract | [AgentSuite specification](agentsuite/spec.md) |
| Operate the old `agent.yaml` path | [Legacy PortableAgent bundle lifecycle](docs/agent-lift.md) |
| Select models and inference | [Models](docs/models.md), [Copilot](docs/copilot-inference.md), [Foundry](docs/local-foundry-inference.md) |
| Install or upgrade | [Releases](docs/releases.md) |
| Browse all guides | [Documentation index](docs/README.md) |

## Development

From a checkout, `make` builds `bin/kmx` without provisioning. Start with the
[developer guide](docs/development.md), [codebase overview](docs/codebase-overview.md),
and [interface baseline](docs/interface-baseline.md). The [repository map](docs/repository-map.md)
classifies tracked source and scaffolding; [contributing](CONTRIBUTING.md) defines
verification and pull-request expectations. See [naming](docs/NAMING.md) for
publication boundaries.
