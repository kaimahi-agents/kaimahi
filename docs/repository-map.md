# What is actually in this repository

**Orka is the platform; Kaimahi provides tools to get agents onto it.**
The remaining plane is a model-traffic bridge, not an application runtime or
tool-governance platform. Migrated applications keep their owner-managed
Deployment and tools. Agent authoring is Orka-native: the legacy operational
commands and their runtime adapter have been removed, and the remaining kagent
references are installation, status and lift paths retired in later slices.

Read [the documentation index](README.md) for current guides and retirement
records. This map describes tracked files, packaging and actual callers, not
an endorsement of every retained legacy installation path.

- **Installed / checkout** describes reachability. `embed.go` names what travels
  inside kmx, including five shell scripts and one Python tool server. Embedding is not a support guarantee.
- **Scaffolding** describes build, test, CI and synthetic model fixtures.
- The gateway, inbound/notification runtime, tool workflows and their ERP/connector
  demonstrations have been removed. Custom approvals and grants are retired;
  ordinary model budgets and accounting survive.

**What is checked.** `scripts/check-repository-map.py` checks counts, paths,
membership, embedding, source-package coverage and the caller claims below.
It uses `git ls-files`: stage additions/deletions before checking the intended
tree. An empty checkout-only manifest set is valid when everything is embedded;
missing lists, miscounts and unclassified tracked files still fail. Retirement
must not be blocked by dummy examples created to satisfy old feature-specific
checks.

## The short version

| Area | Installed / checkout, including legacy | Demonstration | Scaffolding |
|---|---|---|---|
| `cmd/` | `kmx` | — | — |
| `internal/` | `kmx/` (15 packages), plus embedded schema fixtures | — | — |
| `plane/` | model bridge and ordinary budget administration | — | test fakes inside packages |
| `k8s/` | embedded model/plane/observability and retained kagent artifacts | — | — |
| `scripts/` | 8 (6 embedded in the binary, 2 operator) | 1 | 46 (checkers, probes, CI fixtures, mutation specs) |
| `docs/` | 50 tracked files; guides, direction, retirement records and assets | historical scenario records | maintainer and process docs |
| `brand/` | 7 identity assets for repository and organization surfaces | — | its own checker |

## `cmd/` — installed CLI

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (21 files) | **Installed** | CLI and tests: Orka operations, interactive agent dashboard, migration, retained kagent installation steps, model routing, credentials, budgets, ledger and model flow/watch. |

## `internal/` — packages in the CLI

`internal/kmx/` is fifteen packages. Cluster-independent decisions live in
packages; shell-out orchestration lives in `app`. `lift` holds cloud-independent
rules, while the seven `lift*.go` files in `app` run cloud orchestration, preferences and reuse checks. Interactive lift panes use `chat_lift*.go`. Counts exclude
Go test files but include non-Go data; versioned fixtures are not additional Go
packages.

| Package or data directory | Non-test files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 79 | Installed | Command orchestration, the Orka runtime adapter, interactive agent console, shared chat UI, host inference and native platform operations. |
| `kmx/runtime` | 5 | Installed | Platform-neutral adapter/session and lifecycle contracts, identities, capabilities, events, bundle digests and registry. The only registered identity is Orka. |
| `kmx/admin` | 6 | Installed | Model-plane admin client, ordinary caps, credentials and model ledger views. |
| `kmx/scaffold` | 8 | Installed | Orka authoring, model/migration artifacts, retained kagent checks and shared YAML/name helpers. |
| `kmx/orkaschema` | 3 | Installed | Structural schema validator, attribution and upstream licence. |
| `kmx/orkaschema/fixtures/v0.1.3` | 3 | Installed | Embedded release Agent/Provider/Task CRDs for offline validation, not installation. |
| `kmx/orkaschema/fixtures/main` | 3 | Installed | Immutable main-snapshot CRDs, not a runtime support claim. |
| `kmx/guard` | 2 | Installed | Context-safety checks and read-only target resolution. |
| `kmx/seamcert` | 1 | Installed | Model-seam authority and serving certificates. |
| `kmx/toolchain` | 2 | Installed | Pinned, checksum-verified kind, kubectl and Helm downloads. |
| `kmx/planebuild` | 1 | Installed | Separate plane-module image build/fetch. |
| `kmx/lift` | 2 | Installed | Cloud-independent lift rules. |
| `kmx/config` | 1 | Installed | Settings resolution. |
| `kmx/cliui` | 2 | Installed | Destination-aware CLI presentation. |
| `kmx/run` | 1 | Installed | Shell-out layer. |
| `kmx/secretshapes` | 2 | Installed | Shared credential-shape checks and data. |
| `kmx/version` | 1 | Installed | Version and upgrade answers. |

Removing tool-governance scaffolding does not remove direct kagent MCP tool
references or Orka tool names. Neither is the retired custom gateway. Migration
keeps its original source/generator bytes so a repeated invocation can reuse its
previously generated identity/patch files. Old generated tool-seam comments are
not evidence that the removed service or commands still exist.

## `spikes/` — throwaway experiments

`spikes/kagent-shim/` is **Scaffolding**: an isolated converter/adapter experiment,
fixtures and a manual cluster proof. It is a separate module, not embedded in
kmx, and ships no supported authoring interface.

## `plane/` — the model bridge

Eleven internal packages and one binary. The nested module and binary names stay
stable for revision-pinned image builds; keeping those names does not retain the
former governance platform.

There is no `require`, and no `plane/...` import anywhere in root `cmd/` or `internal/`.
`internal/kmx/planebuild` fetches/builds it at the CLI revision. The coupling is
runtime: model/admin APIs, configuration, Service, credentials, CA and rollout.
A green root build alone cannot prove migration works.

| Package under plane/internal | Retained responsibility |
|---|---|
| proxy | Authenticated model routing, strict Responses translation, usage recording and model/budget administration. |
| store | Credential hashes/expiry, ledger, attribution and exact spend reservations. |
| meter | Ordinary token/cents caps, reservation policy and fail-closed denial mapping. |
| pricing | Model cost calculation; an unpriced Orka route is not free inference. |
| redact | Credential/log redaction. |
| metrics | Model/accounting/expiry/build metrics, without grant authority or decision labels. |
| config | Model routes, protocol/pricing/header validation and model overlays; retired tool configuration is rejected, not ignored. |
| egress | Hardened credential-bearing model transport, DNS/IP restrictions, TLS and redirect controls. |
| seamtls | Model serving certificate and verified transports; existing model trust is preserved. |
| ops | Metrics, database readiness and local liveness for the model process. |
| db | Pool and replica-safe migration engine (Postgres and twelve migrations). |

The runtime has three listeners: model 8080 (TLS), admin 9091 and operations 9092.
Only the model listener has a Service. No custom approval/grant execution or
interfaces remain. Historical requests, grants and audit rows are retained in
SQL and backups, not exposed through retired APIs. The twelve SQL migration
files and stored history are unchanged. No reset, destructive schema cleanup,
artificial grant exhaustion or implicit credential revocation occurs.

The model path retains per-credential locking, UTC month accounting, open
reservations and settlement. An exhausted cap returns 429 without filing an
approval request; metering failure still returns 403 and the ledger breaker
503. Flow/watch read only the model ledger. Native kagent HITL remains part of
that runtime's interaction path, not this retired approval subsystem.

### Final approval retirement

The [final approval inventory](https://github.com/kaimahi-agents/kaimahi/blob/5e7c5da/docs/repository-map.md#final-approval-retirement--inventory-before-deletion)
was published before deletion, on main after PR #184. Shared `rowQuerier` stays
with spend accounting; `App.Budget`, `capOrNone` and credential TTL/cap validation
remain with their surviving callers. Mixed model/admin fixtures and accounting,
flow/watch limit/order/error tests remain rather than being discarded with the
approval-only cases.

The remaining eleven packages all support the model bridge. Removing the
approval branches is not permission to remove pricing, attribution readers,
credential custody/expiry, redaction, hardened egress, TLS, operations or backup.
Their eventual absorption upstream is a separate compatibility decision.

### Inventories, decisions and recovery

The [original fourteen-package inventory](https://github.com/kaimahi-agents/kaimahi/blob/1868732/docs/repository-map.md#pre-deletion-inventory--governance-code-retirement)
was published before PR #183 deleted inbound/notify. The
[gateway inventory and shared-dependency plan](https://github.com/kaimahi-agents/kaimahi/blob/10c561d/docs/repository-map.md#gateway-retirement-slice--inventory-before-deletion)
was published before PR #184 deleted any code. These immutable snapshots
separate the removal reasoning from the diff.

The owner authorized removing legacy gateway/approval scaffold portions and
their tests, **not** changing either agent-authoring path. Shared namespace,
Service-resolution, YAML-selector, overlay, rollout, model-switching, Copilot
custody and test helpers remain with their model callers. The entire
`cmd/kmx/agent_commands.go`, `docs/orka.md`, and model migration implementation
remain unchanged in this slice.

Last-carrying commits:

- Inbound approval commands and notifier: `d036b30d2ceb228ca39b88750d606d635e00a2a1`.
- AP human-wait helper: `0b0ce38cb2c362940b8c75a70c198452968939fb`.
- Gateway, argument-bound approval execution, tool/workflow scaffolding and
  demonstrations: the inventory commit `10c561d` immediately before removal.
- Remaining budget approvals/grants and their interfaces: `5e7c5da`, the
  final inventory commit immediately before removal.

Recover removed source from those commits if useful for an upstream contribution;
possible reuse is not grounds for retaining the implementation here. These
retirement slices are independently main-based. Ordinary model budgets/accounting
remain necessary until Orka absorbs the bridge.

Contract 5 marks final approval API retirement (4 marked tool retirement), not
negotiation. Upgrade kmx and plane together. Existing capability floors still
guard model operations on older planes, including model overlay floor 2. Old
rolling replicas and rollback to an approval-capable binary may still use
preserved grants: verify every replica's build before declaring them inert. [Operations](operations.md) covers stale tool overlays, old Services and
owner-managed workload references: applying the new manifests does not prune
old resources or safely choose replacement tool routing for their owners.

## `k8s/` — embedded artifacts

Twenty-five of `k8s/`'s 25 files are embedded; zero are not embedded.

**Embedded in `kmx` (25):** `ollama.yaml`, `kagent-values.yaml`, `orka-k8s-tool.yaml`,
`hello-world.yaml`, `tools-agent.yaml`, `egress-hosted.yaml`, `egress-copilot.yaml`,
all five of `plane/`, all nine of `models/`, and all four of `observability/`.

**Checkout only (0):** none.

The retained tools agent uses the direct kagent tool server. Keeping its existing
installation/authoring path does not restore the removed Kaimahi MCP gateway.
Model and observability manifests remain part of clone-free deployment.

## `scripts/` — 55 tracked files, three different jobs

**Reference coverage:** 43 of the 55 are named by something outside themselves,
and the twelve `scripts/mutations/*.json` are named by nothing at all — the
mutation harness discovers them by glob. Map/checker/board mentions are not
caller evidence. Textual references are not necessarily invocations.

| Class | Count | Files |
|---|---|---|
| **Installed** — embedded in kmx | 6 | `aks-up.sh`, `aks-down.sh`, `plane-deploy.sh`, `netpol-probe.sh`, `kube-guard.sh`, `orka-k8s-tool.py` |
| **Checkout** — operator scripts | 2 | `plane-pods.sh`, `copilot-secret.sh` |
| **Demonstration** | 1 | `demo-hello-to-governed.sh` |
| **Scaffolding** — checkers and self-tests | 21 | the twelve `check-*` files, `kube-guard-test.sh`, `install-sh-test.sh`, `release-notes.py`, `verify-chat.py`, `test_check_board.py`, `test_model_fixtures.py`, `test_demo_hello_to_governed.py`, `test_orka_k8s_tool.py`, `test_owner_model_client.py` |
| **Scaffolding** — live-cluster probes | 7 | `*-probe.sh`, minus the embedded one, plus `seam-tls.sh` |
| **Scaffolding** — CI fixtures | 5 | `scripts/ci/`: `plain-model.sh`, `plain-model-server.py`, `synthetic-model.sh`, `owner-model-client.sh`, `owner-model-client.py` |
| **Scaffolding** — mutation specifications | 12 | `scripts/mutations/*.json` |
| **Scaffolding** — board checker's recorded findings | 1 | `board-open-drift.json` |

Both `model-seam-probe.sh` and `spend-race-probe.sh` call `seam_ca` directly.
`scripts/copilot-secret.sh`: one make recipe.

`kube-guard.sh` is counted once as embedded, and is one of the twelve checkers
the mutation harness breaks on purpose. Model fixtures are synthetic test
systems, not providers deployed for users.

`verify-chat.py` is a checker: every occurrence in the Makefile is a comment
line rather than a recipe. Its only caller is
`.github/workflows/ci.yml` (one invocation among two mentions — the other
is a comment), and that invocation is its own `--selftest`. No cluster shard
runs it against a live agent any more: the `kmx agent chat` turns it verified
belonged to the legacy runtime and retired with it, so what remains is a
verifier proven against fixtures rather than against a cluster.

`scripts/ci/owner-model-client.{sh,py}` are the owner-managed workload the
`e2e-resilience` and `e2e-spend` shards migrate with `kmx migrate`. The Python half is a
standard-library application fixture rather than a Kaimahi component, and
`scripts/test_owner_model_client.py` pins the three properties the shard's
conclusions rest on: the credential leaves by no route but the bearer
header, an upstream refusal keeps its status, and the seam's authority is
verified with no unverified fallback.

`model-seam-probe.sh` and `spend-race-probe.sh` are also the `e2e-models`
shard's only governed callers, and `model-seam-probe.sh` alone is
`e2e-hosted-models`'. Neither shard holds an agent, so every turn either
meters is a direct TLS call under a credential in the caller's own
namespace. Their `SECRET_NAMESPACE` defaults name the legacy runtime's
namespace, so both shards pass the destination at each call site.

`scripts/ci/synthetic-model.sh` is the `e2e-hosted-models` fixture: a
throwaway CA and a documentation-range address routed over kind's network,
so a public-looking hosted upstream can be dialed without a hosted account.
CI holds no hosted credential.

## `docs/` — 50 tracked files, guides and retirement records

**Guides and index (24):** `README.md`, `getting-started.md`, `kmx.md`,
`aks.md`, `models.md`, `tools.md`, `spend.md`, `tool-governance.md`,
`approvals.md`, `egress.md`, `hosted-upstreams.md`, `identity.md`,
`operations.md`, `releases.md`, `workflows.md`, `FAQ.md`, `isolation.md`,
`migrate.md`, `orka.md`, `copilot-inference.md`, `interactive-chat.md`,
`interactive-lift.md`, `orka-k8s-tool.md` and `runtime-adapters.md`. Retired tool/workflow pages are pointers, not
operating instructions for deleted code.

**Retired scenario/integration records (6):** `inbound.md`, `slack.md`,
`govern-your-agent.md`, `ap-demo.md`, `release-agent.md` and `foreign-runtime.md`.

**Demonstration reference (1):** `demo.md` (the hello-to-governed model journey and other demo paths).

**Maintainer and process (17):** `development.md`, `repository-map.md`,
`COORDINATION.md`, `reviews/2026-09-09-orka-composition.md`,
`reviews/2026-09-10-substrate-evaluation.md`, `entry-point-principles.md`,
`cli-ux-plan.md`, `charm-ux-followup-plan.md`, `interactive-agent-tui-plan.md`, `NAMING.md`,
`azure-discovery-performance.md`, `copilot-performance.md`, `orka-latency.md`,
`orka-startup-performance.md`, `local-foundry-inference.md`,
`chat-performance-profile.md` and `agent-lift.md`.

**Assets (2):** `docs/assets/architecture.mmd` and `docs/assets/architecture.svg`.
These depict the pre-retirement platform, not the current model bridge. The
`.svg` has no trailing newline, so `wc -l` reports it as 0; a line count is not
a content check. The index explicitly labels the historical diagram.

## `brand/` — identity assets

Seven image files plus a README. Their repository and organization uses are
recorded in `brand/README.md`; the root README embeds the compact `ketu.svg` mark
and intentionally has no hero image.
`scripts/check-brand-assets.py` checks their dimensions/transparency/metadata
and the separately located historical architecture SVG.

## `.github/` and the root files

| Path | Class | Evidence |
|---|---|---|
| `install.sh` | **Installed tooling** | Release-binary installer with checksum verification. |
| `README.md` | **Documentation** | Repository entry point and current direction. |
| `CHANGELOG.md` | **Build input and history** | Release notes are extracted by the release-notes script. |
| `CONTRIBUTING.md`, `LICENSE` | **Documentation** | Contribution expectations and MIT licence. |
| `embed.go` | **Installed tooling** | Root-module embed declarations. |
| `embed_test.go` | **Scaffolding** | Verifies every embedded asset is readable. |
| `Makefile` | **Scaffolding** | Build and retained model/kagent checkout commands. |
| `.github/workflows/ci.yml`, `release.yml` | **Scaffolding** | Verification gates and tag-driven releases. |
| `.github/workflows/kagent-shim-spike.yml` | **Scaffolding** | Isolated, keyless converter/adapter tests. |
| `.github/actions/classify-change/` | **Scaffolding** | Classifies docs-only changes for CI. |
| `staticcheck.conf` | **Scaffolding** | Lint configuration for both modules. |
| `go.mod`, `go.sum` | **Installed tooling** | Root module dependencies. |
| `.gitignore` | **Scaffolding** | Checkout exclusions. |
| `.dockerignore` | **Scaffolding** | Defensive exclusions for root Docker contexts; current image builds do not use a root context. |

## Open questions — one

1. **Agent authoring format.** Orka-native versus kagent YAML is open.
   Retaining both existing paths does not settle it.

## Existing layout

Seven tracked files under `scripts/` contain the literal `k8s/`. Embedded scripts
remain at the paths named by `embed.go`; the separate plane module is fetched
at the CLI revision. Removed workflow/ERP directories are not kept as empty
packages or placeholder manifests. Model migration, secret custody, accounting
and both authoring paths have their own surviving tests.
