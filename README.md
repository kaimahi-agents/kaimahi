<div align="center">

<img src="brand/ketu.svg" alt="Kaimahi ketu mark" width="128" />

# Kaimahi

**Agent Builder CLI for Kubernetes.**

[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)

[Getting started](docs/getting-started.md) · [AgentSuite specification](agentsuite/spec.md) · [Runtime contract](docs/runtime-adapters.md) · [`kmx` reference](docs/kmx.md) · [Contributing](CONTRIBUTING.md)

<sub>[About the ketu mark](brand/README.md#ketu-mark)</sub>

</div>

---

Kaimahi's `kmx` CLI helps developers **Create → Prove → Lift** an agent: write an editable bundle, get a local answer, then move it to a prepared target.

## Create, Prove, Lift

Install Docker or Podman (set `CONTAINER_ENGINE=podman` for Podman), then choose one CLI install route for the latest release (Go needs 1.26+):

```bash
brew install kaimahi-agents/tap/kmx
# or
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
# or
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.5.1
```

**Get your first answer:** run the local quickstart with preset settings and no interactive prompts:

```bash
kmx quickstart
```

This sets up a local test environment and prints an agent's answer. See [getting started](docs/getting-started.md) to use your own cluster.

| Command | Purpose |
|---|---|
| `kmx local up` | Prepare the runtime without creating an agent. |
| `kmx quickstart --interactive` | Choose your own agent and model through the guided wizard. |
| `kmx create` | Author one [native workspace member](docs/suite-workspace.md) offline. |
| `kmx create --live` | Create runtime resources and an editable PortableAgent bundle on a prepared target; omit the name to open the live wizard. |
| `kmx run` | Run one Task and return its answer. |
| `kmx chat` | Open an interactive session with a named live Agent. |
| `kmx push` | Publish an authored native workspace to `REF`, without evaluation or deployment. |

**Workspace authoring:** create needs an exact `--model` and an `--instructions`
file. Create and push share [workspace discovery](docs/suite-workspace.md#create);
`--workspace DIR` selects a directory explicitly.

**Live operations:** resource creation requires `--live`, even for previews.
Run and chat use deployed agents on the default runtime, not workspaces.
`kmx run --agent <name> --prompt <text>` prints only the answer.

Follow the [lift operation guide](docs/agent-lift.md) for target preparation, receipts and evaluation.

## Quickstart

For your own agent, run the experimental wizard:

```bash
kmx quickstart --interactive
```

Choose **Chat with agent**, check a local answer, then enter `/lift` to a prepared target on the default runtime; after connecting, send a **new message** to prove it answers there.
See [releases and versions](docs/releases.md) for installation and upgrades.

**Safety:** use a dedicated test cluster and review [getting started](docs/getting-started.md).
Both paths create cluster resources and result-reader RBAC; the wizard adds a [read-only inventory Tool with cross-namespace permissions](docs/getting-started.md#create-your-own-agent-then-lift-it-from-chat).
Setup starts while the form is open, and cancellation leaves completed resources in place.
Bundle-backed `/lift` needs a prepared target, a **Ready Provider and its Secret**, and referenced Tools; it creates neither a cluster nor cloud inference.
Hosted model calls can incur charges; review [interactive lift](docs/interactive-lift.md) for preparation and recovery.
`kmx local down` deletes the **whole local cluster and its data**; export needed data first.

## Runtime Contract

See [runtime support](docs/runtime-adapters.md) for the default runtime.
kagent v0.10.3 bundle lift/status is supported on a prepared cluster;
see [bundle lift](docs/agent-lift.md#exact-v0103-declarative-bundle-placement) for flags and limits.

## Status

Kaimahi is pre-1.0 and incubating; [runtime support and CI evidence](docs/runtime-adapters.md#revision-pinned-support-and-qualification-matrix) records qualified scope, not production readiness or live cloud proof.

## Documentation

- [Get started](docs/getting-started.md) with a local first answer.
- [Choose models and inference](docs/models.md), including [Copilot](docs/copilot-inference.md) and [Foundry](docs/local-foundry-inference.md).
- [Lift a bundle](docs/agent-lift.md), [evaluate a revision](docs/agent-lift.md#evaluating-a-deployed-revision), or [run evals in CI](docs/agent-lift.md#run-evals-in-ci).
- [Inspect targets and runtime support](docs/runtime-adapters.md#read-only-target-view) or consult the [`kmx` reference](docs/kmx.md).
- Read the [AgentSuite specification](agentsuite/spec.md) for portable artifacts.
- [Install or upgrade a release](docs/releases.md), or review [cloud ownership and teardown](docs/aks.md).
- Browse the [documentation index](docs/README.md) and [repository directory map](docs/repository-map.md).

## Development

From a checkout, `make` builds `bin/kmx` without provisioning; follow [development](docs/development.md) and [CONTRIBUTING.md](CONTRIBUTING.md) for verification and pull requests.
The project's cultural and publication boundaries are recorded in [docs/NAMING.md](docs/NAMING.md).

> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** Commands, generated
> artifacts, and behavior may change between pre-1.0 releases. It is not yet
> recommended for production use. Use a dedicated test environment, review every
> proposed mutation, and [open an issue](https://github.com/kaimahi-agents/kaimahi/issues)
> with feedback, bugs, or ideas.

> [!NOTE]
> kmx is the Agent Builder and lifecycle layer, not a runtime or generic
> governance control plane. The selected runtime owns execution and enforcement.
> See the [runtime contract](docs/runtime-adapters.md) for the current boundary.
