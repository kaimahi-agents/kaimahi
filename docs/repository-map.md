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
These existing mechanical checks are retained; the pre-deletion package
inventory below is a separately reviewable judgment. The checker does not decide
product status, require historical quotations, or require documents to remain
hard to find. Open questions may
change or resolve; their declared count must match their list.

The checker uses `git ls-files`: additions and deletions must be staged
before its counts describe the intended tree.

## The short version

| Area | Installed / checkout, including legacy | Demonstration | Scaffolding |
|---|---|---|---|
| `cmd/` | `kmx` | `demo/kaimahi-erp` | — |
| `internal/` | `kmx/` (17 packages), plus embedded schema fixtures | `demo/erp` | — |
| `plane/` | legacy governance module, not the Orka platform | — | test fakes inside packages |
| `k8s/` | embedded artifacts and checkout wiring; includes legacy plane manifests | AP and connector scenarios | — |
| `scripts/` | 13 (6 embedded in the binary, 7 operator) | 4 | 53 (checkers, probes, CI fixtures, mutation specs) |
| `docs/` | 37 tracked files; guides, direction, legacy references and assets | scenario material | maintainer and process docs |
| `brand/` | 6 assets used by the README and the org profile | — | its own checker |

## `cmd/` — installed CLI and demonstration binary

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (21 files) | **Installed** | The CLI, including tests. Existing commands include legacy plane operations; their presence is not the current platform definition. |
| `cmd/demo/kaimahi-erp` (2 files) | **Demonstration** | A fake accounts-payable ERP, applied by `k8s/erp-mcp.yaml` via `scripts/erp-deploy.sh`. |

## `internal/` — packages in the existing CLI

`internal/kmx/` is seventeen packages. The table describes the implementation
that remains, including legacy governance functionality. It is not a list of
capabilities to carry forward onto Orka.

The existing split puts cluster-independent decisions in packages and
shell-out orchestration in `app`. `lift` holds naming, validation and teardown
rules without cloud calls; the five `lift*.go` files in `app` run the cloud
side. Counts exclude Go test files but include non-Go data; the versioned
fixture directories below are not additional Go packages.

| Package or data directory | Non-test files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 50 | Installed | Command orchestration and shell-outs, including native Orka create/readiness/Task-result handling. |
| `kmx/admin` | 6 | Installed | Legacy plane admin API client. |
| `kmx/blueprint` | 5 | Installed | Existing declarative governed-workflow format. |
| `kmx/scaffold` | 11 | Installed | Native Orka bundles, retained kagent editing and onboarding artifacts; not a decision on the future authoring format. |
| `kmx/orkaschema` | 3 | Installed | Structural schema validator, fixture attribution and upstream license. |
| `kmx/orkaschema/fixtures/v0.1.3` | 3 | Installed | Embedded release Agent/Provider/Task CRDs for offline validation; not an installer. |
| `kmx/orkaschema/fixtures/main` | 3 | Installed | Embedded immutable main-snapshot Agent/Provider/Task CRDs; not a runtime support claim. |
| `kmx/guard` | 2 | Installed | Context-safety checks and read-only target resolution. |
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

## `spikes/` — throwaway experiments

`spikes/kagent-shim/` is **Scaffolding**: an isolated converter/adapter
experiment, fixture tests and a manual cluster proof. It is a separate Go
module, is not embedded in `kmx`, and ships no supported authoring interface.

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
notifications and operations.

### Pre-deletion inventory — governance code retirement

**Status: inventory only; no code removed.** Inspected against current main
`47e71e843560c280ef380a1716c1cb331087aa4d` (PR #182), after documentation
retirement in PR #178. Publish this inventory before a deletion commit, so its
reasoning can be reviewed independently. The end state below is not a claim
about the code currently installed.

The decisions driving it are settled: Orka owns the platform; migrated
applications retain their owner-managed Deployment; governance for migration
means model traffic only; the seam is a temporary bridge whose disappearance
would be success. The custom approval path leaves this repository, including
argument-bound tool permits **and budget approvals**. Its possible upstream
value is not a reason to retain it. Agent authoring is not being decided here.

| Package under plane/internal | Disposition | Decision and source evidence |
|---|---|---|
| proxy | **Shrinks; model seam survives** | Migration still needs model authentication, credential custody, protocol translation, usage recording and admin credential/budget/ledger operations. Keep the model handlers, protocol/translation and validation. Remove tool/inbound admin routes, approval routes and deny-and-pend filing from `plane/internal/proxy/admin.go`, `plane/internal/proxy/admin_approvals.go`, `plane/internal/proxy/handler.go` and the Store interface. |
| store | **Shrinks** | The seam needs credential hashes/expiry, ledger, attribution and exact spend reservations. Remove allowlists, tool/inbound audit access, request/grant operations and grant-derived headroom from `plane/internal/store/store.go`, `plane/internal/store/approvals.go`, `plane/internal/store/inbound.go`, `plane/internal/store/spend.go` and `plane/internal/store/metrics.go`. Keep database-backed model accounting, not a second governance platform. |
| meter | **Shrinks** | Ordinary token/cents caps and fail-closed reservation admission protect model traffic. Remove grant headroom, granted verdicts and inbound-only Preview from `plane/internal/meter/meter.go`; exceeding a cap must deny, not create an approval request. Reservation atomicity across replicas must survive. |
| pricing | **Survives** | `plane/internal/pricing/pricing.go` computes model cost used by the proxy ledger. Orka traffic currently has token usage without configured money prices; retaining accounting does not claim zero-cost inference or create prices. |
| redact | **Survives** | `plane/internal/redact/redact.go` and `plane/internal/redact/slog.go` protect model credential/log custody independently of the retired connectors. Only connector secret collection in main wiring goes. |
| metrics | **Shrinks** | Keep proxy outcomes/latency, ledger totals, credential deadlines, reservations, build identity and certificate expiry. Remove gateway/inbound labels, queue/notifier metrics and live-grant collection in `plane/internal/metrics/metrics.go`, together with their store queries. Orka's OTLP is not missing functionality to recreate here. |
| gateway | **Goes** | Orka owns the platform and migration does not govern application tools. Remove the entire MCP relay, tool capability filtering, argument canonicalization/digests, constraints and grant enforcement in `plane/internal/gateway/`, plus its listener and deployment/caller wiring. |
| inbound | **Goes** | Orka replaces the plane's connector orchestration. Remove webhook verification, queues, dedupe/invocation and approval commands in `plane/internal/inbound/`, plus its listener, ingress and caller wiring. |
| notify | **Goes** | Notifications and replies exist to operate the retired approvals/connectors. Remove `plane/internal/notify/`, its filing wrapper, poster worker and configuration. |
| config | **Shrinks** | Model routes, credential-file/header handling, protocol pairing, pricing and validated model overlays still serve migration. Remove tool upstreams, tool headers, argument policy/constraints, inbound hooks and notifier configuration from `plane/internal/config/config.go`, `plane/internal/config/overlay.go` and `plane/internal/config/policy.go`. Remove corresponding committed config and callers together; do not silently accept configuration for deleted enforcement. |
| egress | **Survives** | `plane/internal/egress/egress.go` also serves hosted model upstreams: vetted DNS/IP dialing, TLS and redirect restrictions protect upstream credentials. Remove tool-host aggregation in main wiring, not the model transport's protections. |
| seamtls | **Survives** | `plane/internal/seamtls/seamtls.go` serves the model certificate and builds verified transports. Migration mounts its CA in the owner's pod. Gateway names/callers can shrink only where compatible with protected scaffolding and existing model certificates. |
| ops | **Survives; listener wiring shrinks** | `plane/internal/ops/ops.go` provides model-seam metrics/readiness/liveness, including DB readiness without turning an upstream outage into a restart. Main must stop probing listeners that are removed; deleting probes or the ops port would break the retained Deployment. |
| db | **Shrinks in schema scope; migration engine survives** | `plane/internal/db/pool.go` and `plane/internal/db/migrate.go` remain necessary for the seam's Postgres ledger and replica-safe startup. Retiring tool/approval/inbound schema is a separate compatibility-bearing step, not permission to reset the database or rewrite applied migration history; see below. |

**The module boundary is runtime, not a Go import.** There is no plane import in
`internal/kmx/scaffold/migrate.go`: it generates identity, model-only ingress and
an owner-applied patch. `internal/kmx/planebuild/planebuild.go` names the separate
module for building/fetching the binary. The actual migration contract includes
admin credential issuance/reconciliation, the model Service/port, CA publication,
Orka token mount, model upstream/header/translation configuration and proxy
rollout. A green root build cannot prove those contracts survived.

### Protected surface — narrow exception authorized

The instruction to leave all of `internal/kmx/scaffold/` unchanged conflicts
with removing every remaining reference to the retired gateway/approvals:

- `internal/kmx/scaffold/upstream.go` declares the gateway address and contains
  argument-binding/approval guidance. `internal/kmx/scaffold/upstream_yaml.go`
  generates gateway overlays, policy fields and a gateway-backed RemoteMCPServer.
- `internal/kmx/scaffold/sidecar.go` generates a credential shim for that gateway.
  These are active generators, not only historical comments.
- `internal/kmx/scaffold/upstream_pin_test.go` contains
  TestTheDerivedGatewayURLMatchesTheCommittedSeam, which reads
  `k8s/kaimahi-tools.yaml` and requires its gateway URL. Deleting the manifest
  breaks a protected test; leaving a dummy manifest would conceal the conflict.

Removing callers cannot make these references disappear. The owner authorized
option 1: narrow the directory protection solely to remove legacy gateway/approval
generators and their tests, **without changing either agent-authoring path or
model migration**. Shared helpers needed by surviving model code stay. This is
not permission to delete the directory wholesale or settle the authoring format.
`cmd/kmx/agent_commands.go` and `docs/orka.md` remain protected unchanged.

### Database and review boundaries

Versions 00009 and 00011 mix surviving ledger changes with retiring tool/inbound
changes. Versions 00007, 00010 and 00012 support retained reservations,
credential expiry and model accounting. Editing or removing historical SQL is
not an upgrade for a database already at version 12; removing creation migrations
also breaks a fresh database while mixed ALTER statements remain.

Separate runtime removal from destructive schema retirement. Do not implicitly
drop historical audit records or reset credentials/ledger. A schema change needs
fresh-install and version-12 upgrade tests against Postgres, a documented data
retention decision, and a deployment sequence that does not break old replicas
still querying grant tables during the current two-replica rolling update.

The code removal reaches beyond these fourteen packages: main wiring, kmx admin
client/commands and flow/watch trails, tool/workflow orchestration, manifests,
Make targets, embedded/operator scripts, probes, observability and CI all have
callers of the retiring endpoints. Removing three package directories alone
would leave an unusable installation and tests for a nonexistent product.

Review order, with the protected boundary resolved:

1. This inventory, independently published with no deletion.
2. Inbound and notification removal: webhook/command listeners and workers,
   connector configuration, audit views, deployment wiring and obsolete probes.
   Gateway and budget approvals remain functional until their own removal slice.
3. Gateway removal: relay, argument-bound approvals, tool/workflow callers and
   the now-authorized legacy scaffold portions, with their manifests and checks.
4. Seam-adjacent removal last: remaining proxy/budget approval filing and grants,
   store/meter/metrics reduction, then schema retirement under an explicit
   compatibility/data plan. Retain ordinary model budgets and reservations.

The first runtime PR stops after inbound/notification removal for reviewability.
Subsequent dependent slices must branch from main after preceding work is
integrated by its owner; do not merge this lane or stack PR bases to accelerate it.

Each removal commit must pass builds and the full test suite in **both** Go
modules, with a disposable Postgres DSN so store tests do not skip. Keep test
coverage for surviving behavior; adjust checks only when their checked feature
is actually removed. If these slices still produce an unreviewable PR, split
into independently based main-targeting PRs rather than stacked bases.

For the retained bridge, document the model/admin/config/ops contract rather
than resurrecting platform guidance. Update [migration verification](migrate.md#verify-the-migration),
the map, CLI references and deployment guidance to match the result. Exercise
`kmx migrate` on a dedicated kind cluster, apply the generated patch as the
workload owner, send model traffic through the seam to Orka and inspect the
ledger; repeat migration to verify the bound credential and owner Deployment
are preserved. Fake-kubectl tests cover orchestration but are not this live proof.
Run doc-link and repository-map checks after staging each changed inventory.

**Recovery record.** No approval code has been removed by this inventory.
The inspected source baseline is commit
`47e71e843560c280ef380a1716c1cb331087aa4d`; argument-binding enforcement is in
`plane/internal/gateway/digest.go` and `plane/internal/gateway/canon.go`, policy
in `plane/internal/config/policy.go`, persistence in
`plane/internal/store/approvals.go`, admin decisions in
`plane/internal/proxy/admin_approvals.go`, and budget grants in
`plane/internal/meter/meter.go` and `plane/internal/store/spend.go`.
For each actual removal, record its parent as the last-carrying commit in the
PR body, with a one-line reason grouped by package. That recovery route is for
possible upstream reuse, not a reason to keep the implementation here.

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
| `.github/workflows/kagent-shim-spike.yml` | **Scaffolding** | Keyless converter fixtures and in-process MCP adapter tests for the throwaway spike. |
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
