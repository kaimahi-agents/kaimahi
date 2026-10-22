# What is actually in this repository

**Orka remains the first-class/default platform; Kaimahi provides tools to get
agents onto it.** The installed CLI creates and runs agents on Kubernetes.
Runtimes and harnesses own execution, enforcement and model access. The portable
AgentSuite format lives in the separate
[agent-primitives/suite](https://github.com/agent-primitives/suite) module;
this repository owns its KMX consumers and integrations.

The plane module, image-build helper and legacy gateway/connector runtime are
removed. Historical source remains in Git history, not this checkout. Their
removal does not delete databases, credentials, caches or deployed resources.

Read [the documentation index](README.md) for current guides. This map describes
stable directory responsibilities, not feature support or an exhaustive file,
caller or embed inventory. Installed, checkout, library and scaffolding labels
are editorial packaging/reachability descriptions, not support guarantees.
Actual embed declarations and the Go compiler remain build authority.

**Where behavior is documented.** Go package doc comments (in doc.go or an
existing package comment) own behavior and limits. Edit this map only when
adding, removing or moving a top-level responsibility directory: a root area
or an immediate child of `cmd/`, `internal/` or `pkg/`. Packages below
`internal/kmx/` do not get individual rows.

**What is checked.** `scripts/check-repository-map.py` checks named paths,
local Markdown links, nonempty area sections, top-level directory coverage,
package doc comments for immediate packages under `internal/kmx/`, and the
retired-module boundary. It uses `git ls-files`: stage additions/deletions
before checking the intended tree. Descendants inherit their root's role;
ordinary files, nested fixtures and package behavior changes need no map edit.

## `cmd/` — installed CLI

| Directory | Class | Responsibility |
|---|---|---|
| `cmd/kmx` | Installed | CLI entry point and command adapters. |

## `internal/` — CLI implementation

The plane module and image-build helper are absent.

| Directory | Class | Responsibility |
|---|---|---|
| `internal/kmx` | Installed / checkout | CLI orchestration and supporting implementation packages. |

## `pkg/` — public Go packages

| Directory | Class | Responsibility |
|---|---|---|
| `pkg/kmx` | Alpha design evidence | Experimental setup, suite and deployment contracts. |
| `pkg/suiteview` | Read-only library | Local AgentSuite projections. |

## `testdata/` — consumer snapshots (`testdata/agentsuite/`)

Owned consumer-test snapshots and provenance, not the external format implementation.

## `ax-harness/` — preview activity source

Checkout-only activity projection and synthetic test support, not a release image.

## `k8s/` — manifest assets

Native Kubernetes manifest assets; actual embed declarations determine packaging.

## `scripts/` — helpers and scaffolding

Checkout automation, checkers, release tooling and native CI qualification support.

## `docs/` — guides and site source

Current guides, maintainer guidance, design/history, site source and schema provenance.

## `brand/` — identity assets

Repository and organization identity assets.

## `plugins/` — integration packages

Host plugin templates, packaging and integration-test support.

## `.github/` — repository automation

Workflows, reusable actions and review guidance.

## `changes/` — per-PR release notes

Independent release-note inputs; release preparation combines reviewed fragments.

## Root files

Build/dependency metadata, packaging, installation and entry-point documentation.
