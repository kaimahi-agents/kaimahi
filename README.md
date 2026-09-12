<p align="center">
  <img src="brand/hero.png"
       alt="Kaimahi night worker guarding paths for AI agents"
       width="100%">
</p>

# Kaimahi

## Get agents onto Orka

**[Orka](https://github.com/orka-agents/orka) is the platform. Kaimahi is
incubating tooling that helps people get agents onto it**, with particular
attention to Kubernetes and AKS. It is not another agent platform.

`kmx` prepares a cluster, installs a pinned Orka, reports what is actually
running, authors native Orka Agents, and helps route an existing application's
model traffic through Orka. The application's owner keeps its Deployment and
lifecycle. The seam between the application and Orka is a bridge: shrinking it
to nothing is success, not lost product scope.

## Quickstart

The Orka helpers are on `main`; the latest tagged release, `v0.1.0`, predates
them. For this development path, install Go 1.26+ and Docker or Podman,
ensure your Go binary directory is on `PATH`, then:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx up
kmx orka install
kmx orka status
```

`@main` follows a moving development branch, not a stable release. Use a
reviewed commit instead when you need a reproducible CLI build.

`kmx up` currently creates a local kind cluster, Ollama and the existing
kagent runtime. It is not an Orka-native setup command. `kmx orka install`
then installs Orka's pinned manifest and a keyless local Provider; it does
not migrate or govern an application. `kmx orka status` distinguishes the
running controller version from the version kmx pins.

Already have a cluster? Read [getting started](docs/getting-started.md) and
the [Orka installer contract](docs/orka.md), including target confirmation,
`--no-apply`, `--dry-run`, and the Provider prerequisites. For cloud setup,
read [AKS](docs/aks.md) before creating billable resources.

From a checkout, `make` builds `bin/kmx`; use that binary for the same
commands. [Installation and releases](docs/releases.md) describes tagged
binaries, checksums, and upgrade limits.

## Migrate model traffic

For an application **already deployed and managed by its owner**, the
current bridge uses the retained plane implementation:

```bash
kmx plane
kmx migrate <deployment> --namespace <namespace> --model <provider>/<model>
```

The placeholders must name your existing workload and an Orka Provider.
Read the [migration guide](docs/migrate.md) before running this: inspect
and apply the workload patch the command prints. `kmx migrate` does not
patch your Deployment for you, adopt it, convert it into an Orka Agent, or
create Orka Tasks for its requests.

**Governance here means governed model traffic**, not application ownership
or automatic governance of every tool, network connection, and inbound
event. Authentication, Provider scope, recording, protocol translation,
credential renewal and known limitations are documented at the migration
boundary. Installing Orka alone enables none of this routing.

## Status

- **Current tooling:** Orka installation/status, native `kmx agent create`
  (Provider + Agent, optionally a Task with an actual answer), and model-traffic
  migration. See the [native create guide](docs/orka.md#author-an-orka-agent-and-get-an-answer).
  Migration was exercised on kind and AKS; cloud runs are measurements,
  not a continuously maintained deployment. See [migration](docs/migrate.md).
- **Authoring is open:** whether the supported authoring surface will be
  native Orka only or also kagent YAML over Orka is not decided.
  [docs/orka.md](docs/orka.md) recommends native resources; that is a
  recommendation, not a ruling. Today's `kmx agent create` emits native Orka
  resources; it does not convert kagent YAML, ModelConfigs, MCP wiring or BYO
  images. The isolated conversion spike is not a supported CLI interface.
- **The bridge is shrinking:** the model seam, budgets/ledger and existing
  kagent commands remain. The custom MCP gateway, all custom approvals/grants,
  workflows and connector fixtures are retired; native Orka tools and direct
  kagent MCP/HITL are not. Existing installations need
  [deliberate upgrade review](docs/operations.md#upgrading-after-approval-retirement),
  including old-replica and rollback risks. Historical SQL and stored data remain
  intact, accessible through SQL/backups rather than removed approval APIs.
- **Upstream first:** do not rebuild what Orka supplies. `orka.harness.v2`
  is not a direction for this project. OTLP with GenAI conventions ships
  in Orka; it is not an outstanding Kaimahi upstream candidate.

## Documentation

Start at the [documentation index](docs/README.md), which separates current
operator paths from references for the legacy code still in this tree.

- [Getting started](docs/getting-started.md) and [kmx reference](docs/kmx.md)
- [Installing Orka](docs/orka.md) and [migrating an application](docs/migrate.md)
- [AKS](docs/aks.md) and [troubleshooting](docs/FAQ.md)
- [Repository map](docs/repository-map.md) and [current coordination](docs/COORDINATION.md)

## Development

Read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[entry-point principles](docs/entry-point-principles.md). Changes land via
pull requests to `main` with checks green and verification actually run.
The project name's cultural and publication boundaries remain in
[docs/NAMING.md](docs/NAMING.md).
