# What is actually in this repository

**Orka is the platform; Kaimahi provides tools to get agents onto it.**
This map describes the tree that is present, not a promise that every
installed component belongs to that direction. The custom governance plane
and its integrations remain in code pending a separate lane. Their presence,
including inside the binary, does not make them the current product model.
Agent authoring — Orka-native versus kagent YAML — remains open.

Read [the documentation index](README.md) for current direction, guides,
and explicitly legacy references. This map answers a narrower question:
**what files are here, and how are they packaged or used?**

- **Installed / checkout** describes reachability in the existing code,
  not support status or roadmap. `embed.go` names what travels inside
  `kmx`, including six shell scripts. Some of that is legacy plane wiring.
- **Demonstration** describes scenarios and synthetic vendor systems.
  The comment at the top of `internal/demo/erp/server.go` says
  "the gateway in front of it is what the demo is about". That is evidence
  about the fixture, not authority for a future architecture.
- **Scaffolding** describes build, verification, and CI inputs.

The former map treated embedded code as product and used the release
scenario's own document to establish product status. Neither is a sound
basis for describing the current direction. The release scenario is legacy
reference material, not evidence of present adoption or dependency.

**What is checked.** `scripts/check-repository-map.py` checks counts,
paths, membership lists, embedded boundaries, and the caller claims below.
It checks coverage of tracked files under `cmd/`, `internal/`, `k8s/`,
`scripts/`, `docs/`, `brand/` and the root, plus top-level section coverage.
These existing mechanical checks are retained; this is not a new code-removal
inventory. The checker does not decide product status, require historical
quotations, or require documents to remain hard to find. Open questions may
change or resolve; their declared count must match their list.

The checker uses `git ls-files`: additions and deletions must be staged
before its counts describe the intended tree.

## The short version

| Area | Installed / checkout, including legacy | Demonstration | Scaffolding |
|---|---|---|---|
| `cmd/` | `kmx` | `demo/kaimahi-erp` | — |
| `internal/` | `kmx/` (16 packages) | `demo/erp` | — |
| `plane/` | legacy governance module, not the Orka platform | — | test fakes inside packages |
| `k8s/` | embedded artifacts and checkout wiring; includes legacy plane manifests | AP and connector scenarios | — |
| `scripts/` | 13 (6 embedded in the binary, 7 operator) | 4 | 53 (checkers, probes, CI fixtures, mutation specs) |
| `docs/` | 37 tracked files; guides, direction, legacy references and assets | scenario material | maintainer and process docs |
| `brand/` | 6 assets used by the README and the org profile | — | its own checker |

## `cmd/` — installed CLI and demonstration binary

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (19 files) | **Installed** | The CLI, including tests. Existing commands include legacy plane operations; their presence is not the current platform definition. |
| `cmd/demo/kaimahi-erp` (2 files) | **Demonstration** | A fake accounts-payable ERP, applied by `k8s/erp-mcp.yaml` via `scripts/erp-deploy.sh`. |

## `internal/` — packages in the existing CLI

`internal/kmx/` is sixteen packages. The table describes the implementation
that remains, including legacy governance functionality. It is not a list of
capabilities to carry forward onto Orka.

The existing split puts cluster-independent decisions in packages and
shell-out orchestration in `app`. `lift` holds naming, validation and teardown
rules without cloud calls; the five `lift*.go` files in `app` run the cloud
side. Source counts exclude Go test files.

| Package | Non-test source files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 45 | Installed | Command orchestration and shell-outs. |
| `kmx/admin` | 5 | Installed | Legacy plane admin API client. |
| `kmx/blueprint` | 5 | Installed | Existing declarative governed-workflow format. |
| `kmx/scaffold` | 11 | Installed | Existing agent YAML and onboarding artifacts; not a decision on the future authoring format. |
| `kmx/guard` | 1 | Installed | Context-safety checks. |
| `kmx/seam` | 1 | Installed | Upstream credential descriptions. |
| `kmx/seamcert` | 1 | Installed | Certificates for the legacy plane's data seams. |
| `kmx/toolchain` | 2 | Installed | Pinned, checksum-verified kind, kubectl and helm downloads. |
| `kmx/kagentcli` | 1 | Installed | Pinned kagent CLI download. |
| `kmx/planebuild` | 1 | Installed | Legacy plane image build. |
| `kmx/lift` | 2 | Installed | Cloud-independent lift rules. |
| `kmx/config` | 1 | Installed | Settings resolution. |
| `kmx/cliui` | 2 | Installed | Destination-aware CLI presentation. |
| `kmx/run` | 1 | Installed | Shell-out layer. |
| `kmx/secretshapes` | 2 | Installed | Credential shapes: `shapes.json` and `shapes.go`. |
| `kmx/version` | 1 | Installed | Version and upgrade answers. |
| `demo/erp` | 2 | **Demonstration** | Fixture ERP. |

CLI presentation and safety audit coverage is described in
[cli-ux-plan.md](cli-ux-plan.md); these tests are not live-cluster verification.

## `plane/` — legacy governance module

A separate Go module remains in the tree: the former custom governance proxy.
Fourteen internal packages and one binary. This is installed legacy code,
not the definition of Orka or a current product recommendation.

The existing compile-time boundary remains: no `require`, and no `plane/...`
import anywhere in root `cmd/` or `internal/`. The coupling instead runs
through `internal/kmx/planebuild`, which holds the module path and fetches
source at the CLI's revision. Documentation retirement does not change it.

The existing packages cover the LLM proxy and admin API, MCP gateway,
inbound webhooks, egress, budgets, pricing, storage and the database
(Postgres and twelve migrations), configuration, redaction, metrics,
notifications and operations. This description records the existing module;
its disposition belongs to the separate code lane.

## `k8s/` — embedded artifacts and checkout scenarios

The packaging boundary is mechanically recorded by
`TestTheConnectorFamiliesAreNotEmbedded` in
`internal/kmx/app/manifests_test.go`. Its exclusion list also checks that
each named manifest exists. Twenty-six of `k8s/`'s 39 files are embedded,
twelve are named by that test, and the remaining non-manifest is
`k8s/erp-fixtures.json`.

**Embedded in `kmx` (26):** `ollama.yaml`, `kagent-values.yaml`,
`hello-world.yaml`, `tools-agent.yaml`, `kaimahi-tools.yaml`,
`egress-hosted.yaml`, `egress-copilot.yaml`, `wasm/runtime.yaml`,
all five of `plane/`, all nine of `models/`, and all four of `observability/`.

**Checkout — legacy connectors (6):** `inbound-edge.yaml`, `slack-mcp.yaml`,
`kaimahi-slack.yaml`, `kaimahi-github.yaml`, `kaimahi-release-github.yaml`
and `kaimahi-release-ado.yaml`.

**Checkout — legacy release scenario (1):** `release-agent.yaml`.

**Checkout — demonstrations (6):** `ap-agent.yaml`, `erp-mcp.yaml`,
`erp-fixtures.json`, `kaimahi-erp.yaml`, `slack-agent.yaml` and
`github-agent.yaml`.

Embedding explains what the existing binary can apply without a checkout.
It does not establish a future authoring format, current platform support,
or whether anyone depends on a particular scenario.

## `scripts/` — 70 tracked files, three different jobs

**Reference coverage:** 58 of the 70 are named by something outside themselves,
and the twelve `scripts/mutations/*.json` are named by nothing at all —
`check-mutations.py` discovers them by globbing. Map, checker, mutation-fixture
and coordination-board mentions are not caller evidence. A textual reference
is not necessarily an invocation; the specific caller claims below distinguish
those cases.

The table partitions `git ls-files scripts`; a file used in more than one
role is counted once, in the first matching bucket.

| Class | Count | Files |
|---|---|---|
| **Installed** — embedded in the kmx binary, including legacy wiring | 6 | `aks-up.sh`, `aks-down.sh`, `plane-deploy.sh`, `netpol-probe.sh`, `kube-guard.sh`, `release-publish.sh` |
| **Checkout** — existing operator scripts, including legacy integrations | 7 | `plane-pods.sh`, `slack-secret.sh`, `slack-approvers.sh`, `copilot-secret.sh`, `inbound-secret.sh`, `inbound-expose.sh`, `exposure-scan.sh` |
| **Demonstration** | 4 | `erp-deploy.sh`, `ap-demo.sh`, `ap-injection.sh`, `await-approval.sh` |
| **Scaffolding** — checkers and their self-tests | 16 | the twelve `check-*` files, `kube-guard-test.sh`, `release-notes.py`, `verify-chat.py`, `test_check_board.py` |
| **Scaffolding** — live-cluster probes | 16 | `*-probe.sh`, minus the one that is embedded, plus `seam-tls.sh` |
| **Scaffolding** — CI fixtures and synthetic upstreams | 8 | `scripts/ci/`: `synthetic-upstream.sh`, `plain-upstream.sh`, `plain-model.sh`, `mcp-echo-server.py`, `plain-mcp-server.py`, `plain-model-server.py`, `status-unknown-probe.sh`, `workflow-fixture.yaml` |
| **Scaffolding** — mutation specifications | 12 | `scripts/mutations/*.json`, one per checker |
| **Scaffolding** — board checker's recorded findings | 1 | `board-open-drift.json` |

**Existing callers, not product authority.** `await-approval.sh` waits for
the human decision in the accounts-payable demonstration.
Both `ap-demo.sh` and `ap-injection.sh` call it.
`scripts/exposure-scan.sh`: one make recipe.

`kube-guard.sh` has a second role but is counted once above: it is embedded
and is one of the twelve checkers the mutation harness breaks on purpose.
The CI fixtures are synthetic systems for verification, not deployed services.

`verify-chat.py` is a checker, not part of the make chat recipe:
every occurrence in the Makefile is a comment line rather than a recipe.
Its existing callers include `.github/workflows/ci.yml` (fourteen invocations
among nineteen mentions — five are comments).

## `docs/` — 37 tracked files, current direction and legacy references

**Guides and index (19):** `README.md`, `getting-started.md`, `kmx.md`,
`aks.md`, `models.md`, `tools.md`, `spend.md`, `tool-governance.md`,
`approvals.md`, `egress.md`, `hosted-upstreams.md`, `identity.md`,
`operations.md`, `releases.md`, `workflows.md`, `FAQ.md`, `isolation.md`,
`migrate.md` and `orka.md`. Inclusion here does not turn retained descriptions
of legacy commands into current recommendations; follow each document's scope.

**Legacy scenario and integration stubs (6):** `inbound.md`, `slack.md`,
`govern-your-agent.md`, `ap-demo.md`, `release-agent.md` and
`foreign-runtime.md`. These are retirement pointers, not operating playbooks.

**Demonstration reference (1):** `demo.md`.

**Maintainer and process (9):** `development.md`, `repository-map.md`,
`COORDINATION.md`, `reviews/2026-09-09-orka-composition.md`,
`reviews/2026-09-10-substrate-evaluation.md`, `entry-point-principles.md`,
`cli-ux-plan.md`, `charm-ux-followup-plan.md` and `NAMING.md`.

**Assets (2):** `docs/assets/architecture.mmd` and
`docs/assets/architecture.svg`. These depict the legacy plane, not the
current Orka platform model.

**Navigation and asset note:** the documentation index links current and
legacy references explicitly. Old claims that isolation or reviews are
unfindable are retired. The diagram remains a legacy asset: the `.svg` has no
trailing newline, so `wc -l` reports it as 0; a line count is not a content check.

## `brand/` — identity assets

Six image files plus a README. `README.md:2` embeds `brand/hero.png`.
The other five — `mark.svg`, `mark.png`, `wordmark.svg`, `social-preview.png`
and `mascot.png` — have their intended uses recorded in `brand/README.md`.
`scripts/check-brand-assets.py` checks the PNG dimensions and transparency,
SVG title strings, and the separately located legacy architecture SVG.
The latter is not a brand asset or the current architecture diagram.

## `blueprints/`, `.github/` and the root files

| Path | Class | Evidence |
|---|---|---|
| `blueprints/release.yaml` | **Installed legacy workflow** | Embedded by the directory pattern in `embed.go`. Parameterized release scenario, not authority for current adoption. |
| `install.sh` | **Installed tooling** | Release-binary installer with checksum verification. |
| `README.md` | **Documentation** | Repository entry point and current direction. |
| `CHANGELOG.md` | **Build input and history** | Release notes are extracted by `release-notes.py`. |
| `CONTRIBUTING.md`, `LICENSE` | **Documentation** | Contributor entry point and MIT licence. |
| `embed.go` | **Installed tooling** | Root-module embed declarations. |
| `embed_test.go` | **Scaffolding** | Verifies that every embedded asset can be read, independently of Make. |
| `Makefile` | **Scaffolding** | Checkout interface, including legacy and demonstration targets. |
| `.github/workflows/ci.yml`, `release.yml` | **Scaffolding** | CI gates and tag-driven releases. |
| `.github/actions/classify-change/` | **Scaffolding** | Classifies docs-only changes for CI. |
| `staticcheck.conf` | **Scaffolding** | Lint configuration for both modules. |
| `go.mod`, `go.sum` | **Installed tooling** | Root module dependencies. |
| `.gitignore` | **Scaffolding** | Checkout exclusions. |
| `.dockerignore` | **Demonstration** | Build context exclusions for the ERP image. |

## Open questions — one

1. **Agent authoring format.** Orka-native versus kagent YAML is open.
   Existing scaffolding and embedded manifests do not settle it.

## Existing layout

No runtime files are moved or removed by documentation retirement. The fixture
ERP already lives under `internal/demo/erp` and `cmd/demo/kaimahi-erp`.
The embedded and checkout manifests remain interleaved under `k8s/`;
eleven tracked files under `scripts/` contain the literal `k8s/`.
Embedded scripts remain at the paths named by `embed.go`. These are existing
layout facts, not a proposed removal order or an argument to retain the plane
as the product. Code disposition is separate work.
