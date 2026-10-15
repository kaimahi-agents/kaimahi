<div align="center">

<img src="brand/ketu.svg" alt="Kaimahi ketu mark" width="128" />

# Kaimahi

**Agent Builder CLI for Kubernetes.**

[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)

[Documentation](https://kaimahi-agents.github.io/kaimahi/) · [Getting started](docs/getting-started.md) · [Workspace lifecycle](docs/suite-workspace.md) · [Runtime support](docs/runtime-adapters.md) · [`kmx` reference](docs/kmx.md) · [Developer guide](docs/development.md) · [Contributing](CONTRIBUTING.md)

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

**Create → Prove → Lift** is available in v0.6.0 for one-member native Orka and
exact-v0.10.3 Go kagent suites. Bounded CI qualifies root Lift for Orka exact
reuse, bounded absent-pair registry promotion and kagent fresh-target creation;
it does not prove every promotion, creation or partial-outcome path.

> [!NOTE]
> The table below describes unreleased main; v0.6.0 keeps its released defaults.
> See [command migration](CHANGELOG.md#upgrading)
> and [releases](docs/releases.md) when upgrading from an older version.

| Stage | Current checkout |
|---|---|
| **Create** | `kmx create NAME --workspace DIR --model MODEL --instructions FILE` authors one tool-free workspace member offline: Orka by default, exact Go kagent with `--runtime kagent`, or pinned chat with `--runtime agentsessions`. No Kubernetes, image build or model call. |
| **Prove** | Orka uses `kmx eval --workspace DIR --to-context CTX --inference provider:NAME --out RECEIPT`; kagent uses `--bindings FILE` instead of `--inference`. Native AgentSessions instead requires `--development-host FILE`, a private literal-loopback host JSON, with no cluster selectors. Each evaluates ordered cases with optional `runs`/`minPasses` (default once), stops after a case misses its threshold or any unknown/error, and writes private evidence only when the full policy passes. |
| **Publish** | `kmx push --workspace DIR --evaluation RECEIPT REF` checks that evidence against the exact OCI manifest and publishes that graph. Plain `kmx push REF` requires passing evidence; `--unevaluated` explicitly opts out. Neither form deploys. |
| **Lift** | `kmx lift REF@sha256:DIGEST --to-context CTX --evidence RECEIPT` plus Orka `--inference provider:NAME` or kagent `--bindings FILE` deploys the matching packaged runtime without building, repacking, or publishing. `--plan` is GET-only; kagent refuses occupied targets; suite status and retirement remain unavailable. |

Root eval also keeps completed passing metadata in private KMX state, so `--out`
is optional. With named `evalTarget`, `kmx eval` needs no selectors; then
`kmx push REPO:TAG` uses the latest completed exact-suite proof and preserves TAG.
Bare `kmx push` requires saved `publishRepository` and derives `s-<full-S-hex>`.
These conveniences do not compose Lift or relax its digest/evidence requirements.

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
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.6.0
```

Check what was installed, then run the fixed, non-interactive local proof:

```bash
kmx version
kmx quickstart
```

`kmx quickstart` creates a local kind environment, starts Ollama and pinned Orka,
and succeeds only after a fresh Task returns a readable answer. Use
`kmx quickstart --interactive` for guided native workspace authoring, full eval, publication and reviewed Lift.
Follow [getting started](docs/getting-started.md) for prerequisites and explicit
context selection.

## Workspace And Live Paths

| Path | What it means |
|---|---|
| Workspace | Default `kmx create`, root `eval`, and root `push` operate on editable source. Digest-based root `lift` does not consume workspace source; it uses the packaged artifact and separate private evidence with explicit target and inference inputs. Run/chat still target live Agents. |
| Local environment | `kmx local up` prepares kind, model and runtime without creating an Agent. The experimental Go `AgentEnvironment` interface is setup-only and is not wired to this command yet. |
| Live Agent | `kmx run --agent NAME --prompt TEXT` performs one Task on an independent live Orka Agent; `kmx chat NAME` opens an Orka session. Suite-owned Agents refuse legacy invocation; neither is workspace-local execution. |
| Legacy bundle | Removed outright: user `agent.yaml` readers/writers, commands and receipts, `create --live` including previews/wizard, run's bundle mode and `--bundles` flags. Old receipts are unreadable; no conversion, migration or on-disk/resource deletion. Public AgentSuite implementation bundles are a different format and remain supported. |

## Runtime Boundaries

A **runtime** executes and governs the agent. An **inference provider** is the
separate model-access binding; selecting one does not select the other.

- **Orka** is the default runtime. Its packaged workspace path supports
  evaluation, evaluated publication and root digest Lift with exact evaluated
  reuse; independent live Orka run and chat remain.
- **kagent** support is exact-version and operation-specific: v0.10.3
  Go/linux-amd64 packaged workspace authoring, `eval --bindings`, evaluated
  publication and create-only root digest Lift. Standalone run/chat, Python,
  RemoteMCP, suite status and retirement remain unavailable.
- **agentsessions** supports pinned-chat offline suite create/prepare/load/ordinary
  push and explicit private `eval --development-host FILE`: one fresh
  Session/config/Exec per declared run, full-pass private proof and exact-S evaluated
  push. This unauthenticated literal-loopback development path loads no cluster
  settings; explicit judging and repeated cases share Orka/kagent semantics. It supports no tools, Lift/chat/status or remote lifecycle.
  Receipt diff refuses its proof. Read-only reference extraction/export and
  availability probing remain separate; legacy bundle evaluation/replay is removed.

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
- Quickstart adds cluster resources and result-reader RBAC, then authors, fully
  evaluates, publishes and Lifts a tool-free native suite. Placement verification
  precedes its separate fresh answer Task. Console Ready means placement only.
- Chat `/lift` and console Lift only print `Lift moved: kmx lift <ref>@<digest> --to-context … --evidence …`; no release GUI or live-copy fallback remains.
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
| Install and get one answer | [Getting started](docs/getting-started.md) |
| Author, evaluate and publish a workspace | [Workspace lifecycle](docs/suite-workspace.md) |
| Understand exact runtime support | [Runtime support](docs/runtime-adapters.md) |
| Find every implemented command | [`kmx` reference](docs/kmx.md) |
| Read the portable artifact contract | [AgentSuite specification](agentsuite/spec.md) |
| Select models and inference | [Models](docs/models.md), [Copilot](docs/copilot-inference.md), [Foundry](docs/local-foundry-inference.md) |
| Install or upgrade | [Releases](docs/releases.md) |
| Browse all guides | [Documentation index](docs/README.md) |

## Development

From a checkout, `make` builds `bin/kmx` without provisioning. Start with the
[developer guide](docs/development.md), [codebase overview](docs/codebase-overview.md),
and [interface baseline](docs/interface-baseline.md). The [repository map](docs/repository-map.md)
classifies tracked source and scaffolding; [contributing](CONTRIBUTING.md) defines
verification and pull-request expectations. See [naming](docs/NAMING.md) for
publication boundaries. Run `make docs-preview` for the browser-style documentation
preview generated directly from these Markdown files.
