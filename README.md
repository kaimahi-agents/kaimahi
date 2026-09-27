<div align="center">

<img src="brand/ketu.svg" alt="Kaimahi ketu mark" width="128" />

# Kaimahi

**Agent Builder CLI for Kubernetes.**

[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)

[Getting started](docs/getting-started.md) · [Runtime contract](docs/runtime-adapters.md) · [`kmx` reference](docs/kmx.md) · [Contributing](CONTRIBUTING.md)

<sub>[About the ketu mark](brand/README.md#ketu-mark)</sub>

</div>

---

Kaimahi's `kmx` CLI helps developers create an agent, prove it locally, and
move it into a real environment without learning each runtime's manifests first.

## Create, Prove, Lift

```bash
kmx agent create
kmx agent lift
```

`create` is the path from an idea to an editable local agent. `lift` is the path
from "it works here" to a selected Kubernetes or AKS environment. KMX keeps the
target and changes explicit while the runtime handles execution.

These are the simple commands KMX is converging on. `kmx agent create` exists
today for a prepared target. A standalone `kmx agent lift` is not implemented
yet; the current lift is available as `/lift` from interactive chat. See the
[lifecycle direction](https://github.com/kaimahi-agents/kaimahi/issues/194).

## Quickstart

The current end-to-end workflow is interactive. The required commands are on
`main`; the latest tagged release, `v0.1.0`, predates them. Install Go 1.26+ and
Docker or Podman, ensure the Go binary directory is on `PATH`, then run:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx quickstart-wizard
```

The wizard prepares a local Kubernetes target while you describe the agent. To
complete the journey:

1. Choose **Chat with agent** when setup is ready.
2. Send a prompt and wait for an answer to prove the selected execution path.
3. Enter `/lift`, choose an existing Kubernetes or AKS target, review the
   destination and inference choice, then confirm.

The current lift reads the live source agent, deploys to an existing target, and
does not replay a task or delete the source. Choosing new cloud inference can
create billable resources. Read the [interactive lift guide](docs/interactive-lift.md)
for the complete behavior and recovery boundaries.

Quickstart creates local cluster resources, result-reader RBAC, and a read-only
Kubernetes inventory tool. Completed setup can remain after cancellation. Read
[getting started](docs/getting-started.md) before using it on a shared machine.

Use Podman explicitly with:

```bash
kmx --container-engine podman quickstart-wizard
```

`@main` is a moving development branch. Use a reviewed commit for a reproducible
build. From a checkout, `make` builds `bin/kmx` without provisioning anything.

## Runtime Contract

[Orka](https://github.com/orka-agents/orka) is the first-class runtime. The
selected runtime owns execution and enforcement. Read the [runtime adapter
contract](docs/runtime-adapters.md) for the boundaries between KMX and runtimes.

## Current Commands

| Goal | Current interface | Boundary |
|---|---|---|
| Create a complete local agent environment | `kmx quickstart-wizard` | Interactive local workflow |
| Create on a prepared target | `kmx agent create` | Does not install the runtime or provision credentials |
| Prove an answer | Interactive chat or `kmx agent create --task ...` | Readiness alone is not execution proof |
| Lift an agent | `/lift` in interactive chat | Uses a live agent and an existing destination |
| Inspect agents | `kmx agent list`, `show`, and interactive `chat` | Orka-only; the namespace is explicit and defaults to `orka-system` |
| Provision an AKS target | `kmx aks up` | Billable platform workflow; does not create the agent |

`kmx agent create` writes reviewable YAML, validates it against the selected
target, creates dependencies in order, and waits for current-generation
readiness. A real answer requires `--task` plus pre-existing result access.
Credentials, namespaces, and RBAC remain separate operator responsibilities.
Use `--out -` or `--no-apply` for offline output and `--dry-run` for server
admission without cluster writes. Read the complete
[`agent create` contract](docs/kmx.md#kmx-agent-create).

## Lifecycle

The simple front door does not remove deeper lifecycle needs. The direction in
[#194](https://github.com/kaimahi-agents/kaimahi/issues/194) includes Git-tracked
agent definitions, immutable revision digests, deployment receipts, evaluation,
target-aware status, diff, and rollback.

Those operations do not have full standalone command parity on `main`. The
initial state model should use Git and the selected runtime rather than introduce
a second KMX server or controller. Rollback means deploying and verifying an
earlier revision; it cannot undo external actions already completed by an agent.

## Migrate Model Traffic

`kmx migrate` is a separate compatibility bridge for an existing application:

```bash
kmx plane
kmx migrate <deployment> --namespace <namespace> --model <provider>/<model>
```

The application owner keeps the Deployment and reviews the generated patch. The
bridge covers a supported model-client shape; it does not convert the application
into an agent, govern all of its activity, or replace runtime enforcement. Read
the [migration guide](docs/migrate.md) for protocol, credential, and ownership
limits.

## Status

Kaimahi is pre-1.0 and incubating. Interactive local creation, creation on the
first-class runtime, inspection, chat, lift to an existing target, AKS platform
provisioning, and model-traffic migration are implemented. Standalone agent lift
and the complete lifecycle remain directional. Legacy commands and the retained
model-traffic bridge stay available while migration paths mature. AKS paths use
billable resources and are not continuously re-proved in CI.

## Documentation

| Start here | Purpose |
|---|---|
| [Getting started](docs/getting-started.md) | Prerequisites and current local workflows |
| [`kmx` reference](docs/kmx.md) | Commands, safety rules, and output contracts |
| [Runtime contract](docs/runtime-adapters.md) | Runtime, context, session, inference, lifecycle, and enforcement boundaries |
| [Runtime setup](docs/orka.md) | First-class implementation setup, native creation, and first task |
| [Interactive lift](docs/interactive-lift.md) | Current agent-to-target behavior |
| [AKS](docs/aks.md) | Billable resource ownership, provisioning, and teardown |
| [Migration](docs/migrate.md) | Existing-application model-traffic bridge |
| [Direction issue #194](https://github.com/kaimahi-agents/kaimahi/issues/194) | Proposed definitions, adapters, and lifecycle |
| [Documentation index](docs/README.md) | All current guides and maintainer references |

## Development

Read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[developer entry-point principles](docs/entry-point-principles.md). Changes land
through pull requests to `main` with checks green and verification actually run.
The project name's cultural and publication boundaries are documented in
[docs/NAMING.md](docs/NAMING.md).

> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** Commands, generated
> artifacts, and behavior may change between pre-1.0 releases. It is not yet
> recommended for production use. Use a dedicated test environment, review every
> proposed mutation, and [open an issue](https://github.com/kaimahi-agents/kaimahi/issues)
> with feedback, bugs, or ideas.

> [!NOTE]
> KMX is the Agent Builder and lifecycle layer, not a runtime or generic
> governance control plane. The selected runtime owns execution and enforcement.
> See the [runtime contract](docs/runtime-adapters.md) for the current boundary.
