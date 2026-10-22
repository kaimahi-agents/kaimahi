# What is actually in this repository

Kaimahi provides the kmx CLI and Go packages for authoring and deploying agents.
Orka is the default runtime. The portable AgentSuite format lives in the separate
[agent-primitives/suite](https://github.com/agent-primitives/suite) module;
this repository contains its kmx integrations.

Read [the documentation index](README.md) for current guides. This map describes
directory responsibilities. Go package comments describe package ownership and
limits. Neither the map nor its class labels promise feature support or determine
which files are included in a build.

Edit this map only when adding, removing or moving a top-level directory: a root
area or an immediate child of `cmd/`, `internal/` or `pkg/`. Packages below
`internal/kmx/` do not get individual rows.

`scripts/check-repository-map.py` checks named paths, local Markdown links,
nonempty sections, directory coverage, package comments for immediate packages
under `internal/kmx/`, and the removed plane-module boundary. It reads tracked
paths with `git ls-files` and file contents from the working tree. Stage file
additions and deletions before checking. Changes inside an existing directory,
including package behavior changes, do not need a map edit.

## `cmd/` — installed CLI

| Directory | Class | Responsibility |
|---|---|---|
| `cmd/kmx` | Installed CLI | Command entry point and argument handling. |

## `internal/` — CLI implementation

The plane module and image-build helper are absent.

| Directory | Class | Responsibility |
|---|---|---|
| `internal/kmx` | CLI implementation | Command orchestration and supporting packages. |

## `pkg/` — public Go packages

| Directory | Class | Responsibility |
|---|---|---|
| `pkg/kmx` | Experimental library | Interfaces and values for agent setup, packaging and deployment. |
| `pkg/suiteview` | Read-only library | Local views of AgentSuite content. |

## `testdata/` — consumer snapshots (`testdata/agentsuite/`)

AgentSuite test snapshots and their source records, not the format implementation.

## `ax-harness/` — preview activity

Activity projection and synthetic test support for use from a checkout.

## `k8s/` — Kubernetes manifests

Manifest assets. Go embed declarations determine which are shipped in the CLI.

## `scripts/` — development and release tools

Automation for development, checks, releases and runtime integration tests.

## `docs/` — documentation and site source

User guides, maintainer guidance, design records and documentation-site source.

## `brand/` — identity assets

Logos and other repository and organization artwork.

## `plugins/` — host integrations

Plugin source and packaging tools; see [the package guide](../plugins/copilot/README.md).

## `.github/` — repository automation

Workflows and contributor guidance.

## `changes/` — release notes

Per-change release notes, combined during release preparation.

## Root files

Build and dependency metadata, CLI asset embedding, packaging, installation and
entry-point documentation.
