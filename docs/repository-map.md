# What is actually in this repository

**Orka remains the first-class/default platform; Kaimahi provides tools to get
agents onto it.** The remaining plane is a model-traffic bridge, not an
application runtime or tool-governance platform. Migrated applications keep
their owner-managed Deployment and tools. Agent authoring defaults to native
Orka. The only current Kagent implementation is explicit create against an
already-installed exact v0.10.2, optionally followed by one A2A message in that
same create invocation; the old operational commands, installer,
manifests, status adapter, and AKS payload remain removed. Historical teardown
support still reads lift records created before retirement.

Read [the documentation index](README.md) for current guides and retirement
records. This map describes tracked files, packaging and actual callers, not
an endorsement of historical compatibility material.

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
| `internal/` | `kmx/` (17 packages), plus embedded schema fixtures | — | — |
| `ax-harness/` | preview projector source only; no built image or kmx adapter | — | synthetic Python tests |
| `plane/` | model bridge and ordinary budget administration | — | test fakes inside packages |
| `k8s/` | embedded runtime/plane/observability artifacts | — | — |
| `scripts/` | 7 (6 embedded in the binary, 1 operator) | 1 | 45 (checkers, release packaging, probes, CI fixtures, mutation specs) |
| `docs/` | 47 tracked files; guides, direction, retirement records and assets | historical scenario records | maintainer and process docs |
| `brand/` | 7 identity assets for repository and organization surfaces | — | its own checker |

## `cmd/` — installed CLI

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (33 files) | **Installed** | CLI and tests: top-level offline AgentSuite validation, default Orka operations including native Azure OpenAI Providers and coordination, explicit exact Kagent v0.10.2 create, Orka bundle lift/status/evaluation gates, safe retirement and one-shot Task execution, interactive agent dashboard, migration, model routing, credentials, budgets, ledger and model flow/watch. |

## `internal/` — packages in the CLI

`internal/kmx/` is seventeen packages at the top level (eighteen Go packages
including nested `runview/orka`). The short version counts top-level directories.
Cluster-independent decisions live in packages; shell-out orchestration lives in `app`. `lift` holds cloud-independent
rules, while the seven `lift*.go` files in `app` run cloud orchestration, preferences and reuse checks. Interactive lift panes use `chat_lift*.go`. Counts exclude
Go test files but include non-Go data; versioned fixtures are not additional Go
packages.

| Package or data directory | Non-test files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 99 | Installed | Command orchestration, the offline AgentSuite validator entry point, the read-only Orka run source and console run view, the Orka lifecycle adapter, exact Kagent v0.10.2 create-only lifecycle adapter and online proof, agent bundle persistence, Orka lift/status/evaluation gates, safe retirement, Task execution and result retrieval, interactive Orka console, shared chat UI, host inference and native platform operations. The three Kagent non-test files are `create_kagent.go`, `kagent_create_online.go` and `runtime_kagent_lifecycle.go`; app also contains Kagent create and Orka-only bundle-refusal tests. |
| `kmx/agentsuite` | 7 | Installed | Strict JSON and JCS identities, OCI image-layout and content-layer validation, closed agent/tool/composition/build-profile graph validation, sandbox binding validation and conformance tests for portable AgentSuite artifacts. |
| `kmx/agentsuite/schema` | 6 | Checkout | Closed JSON Schema 2020-12 reference documents for suite, agent, tool, composition, build-profile and sandbox-binding records; published with the source checkout, not embedded in or loaded by the binary. |
| `kmx/agentsuite/testdata/minimal` | 1 | Scaffolding | Root manifest for the checked-in minimal conformant AgentSuite layout used by package and CLI validation tests. |
| `kmx/agentsuite/testdata/minimal/agents` | 1 | Scaffolding | Minimal writer agent manifest. |
| `kmx/agentsuite/testdata/minimal/build-profiles` | 1 | Scaffolding | Minimal exact Linux build profile with pinned example descriptors. |
| `kmx/agentsuite/testdata/minimal/instructions` | 1 | Scaffolding | Digest-bound instruction content for the minimal writer agent. |
| `kmx/agentsuite/testdata/minimal/compositions` | 1 | Scaffolding | Empty composition manifest for the minimal agent and platform. |
| `kmx/agentsuite/testdata/minimal/tools` | 1 | Scaffolding | Empty closed tool catalog for the minimal suite. |
| `kmx/agentsuite/testdata/coordinator-workers` | 1 | Scaffolding | Root manifest for the conformant coordinator, writer and reviewer fixture. |
| `kmx/agentsuite/testdata/coordinator-workers/agents` | 3 | Scaffolding | Coordinator, writer and reviewer manifests; only the coordinator declares invocation edges. |
| `kmx/agentsuite/testdata/coordinator-workers/build-profiles` | 1 | Scaffolding | Shared exact Linux build profile for all three agents. |
| `kmx/agentsuite/testdata/coordinator-workers/instructions` | 3 | Scaffolding | Digest-bound coordinator, writer and reviewer instructions. |
| `kmx/agentsuite/testdata/coordinator-workers/compositions` | 3 | Scaffolding | Empty composition manifests for all three agents on Linux amd64. |
| `kmx/agentsuite/testdata/coordinator-workers/tools` | 1 | Scaffolding | Empty closed tool catalog for the coordinator-workers suite. |
| `kmx/app/testdata` | 2 | Scaffolding | Golden bytes pin the no-Task Orka artifact for both v0.1.3 and v0.2.0. |
| `kmx/app/testdata/bundle-format` | 2 | Scaffolding | Exact rendered documents for historical and current portable bundle fixtures. |
| `kmx/app/testdata/bundle-format/kagent` | 2 | Scaffolding | Kagent portable agent and creation bindings; the parser and portable digest are pinned in compatibility tests. |
| `kmx/app/testdata/bundle-format/main` | 2 | Scaffolding | Current-writer portable agent and creation bindings. |
| `kmx/app/testdata/bundle-format/main/eval` | 1 | Scaffolding | Current-writer evaluation case. |
| `kmx/app/testdata/bundle-format/v0.3.0` | 2 | Scaffolding | First bundle-writer portable agent and creation bindings. |
| `kmx/app/testdata/bundle-format/v0.3.0/eval` | 1 | Scaffolding | First bundle-writer evaluation case. |
| `kmx/runview` | 1 | Installed | In-memory, runtime-neutral run, Task, agent, hand-off and missing-evidence presentation types. |
| `kmx/runview/orka` | 1 | Installed | Bounded Orka Task lineage, event and trace reader with caller-scoped reads and safe projection. |
| `kmx/runtime` | 10 | Installed | Platform-neutral adapter/session and lifecycle contracts, identities, capabilities, events, bundle digests, registry, portable Orka/Kagent authoring union, target bindings and evaluation cases. `prepared.go` checks behavior before target-bound rendering; `kagent_bindings.go` adds closed creation-target bindings. Only Orka is registered for chat/discovery. |
| `kmx/admin` | 6 | Installed | Model-plane admin client, ordinary caps, credentials and model ledger views. |
| `kmx/scaffold` | 9 | Installed | Orka authoring, exact Kagent v0.10.2 review scaffolding in `kagent.go`, model/migration artifacts and shared YAML/name helpers. |
| `kmx/orkaschema` | 3 | Installed | Structural schema validator, attribution and upstream licence. |
| `kmx/orkaschema/fixtures/v0.1.3` | 3 | Installed | Historical release Agent/Provider/Task CRDs for explicit offline validation, not installation. |
| `kmx/orkaschema/fixtures/v0.2.0` | 3 | Installed | Default offline validation CRDs; the verified chart, not these fixtures, installs Orka. |
| `kmx/orkaschema/fixtures/main` | 3 | Installed | Immutable old main-snapshot CRDs for explicit offline validation, not a runtime support claim. |
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

Removing the legacy installer does not remove historical compatibility
fixtures or Orka tool names. Neither is the retired custom gateway. Migration
keeps its original source/generator bytes so a repeated invocation can reuse its
previously generated identity/patch files. Old generated tool-seam comments are
not evidence that the removed service or commands still exist.

## `ax-harness/` — preview activity source

The stdlib Python projector (`activity.py`) is checkout-only, not embedded
in kmx or built into a release image. `test_activity.py` uses synthetic
native event and journal metadata. No Task command wrapper, AX target or
runtime read is enabled by these files alone; image packaging, verification
and publishing require separate reviewed changes.

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
503. Flow/watch read only the model ledger. The legacy runtime's own HITL is
part of that runtime's interaction path, not this retired approval subsystem.

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

Thirteen of `k8s/`'s 13 files are embedded; zero are not embedded.

**Embedded in `kmx` (13):** `ollama.yaml`, `orka-k8s-tool.yaml`,
`egress-hosted.yaml`, `egress-copilot.yaml`, all five of `plane/`, and all four
of `observability/`.

**Checkout only (0):** none.

What is no longer here: the legacy chart values, the two demonstration agent
manifests, and the nine model presets. All were objects of the legacy runtime
applied by the retired installer; nothing left in kmx reads or applies one. Plane and
observability manifests remain part of clone-free deployment.

## `scripts/` — 53 tracked files, three different jobs

**Reference coverage:** 43 of the 53 are named by something outside themselves,
and the ten `scripts/mutations/*.json` are named by nothing at all — the
mutation harness discovers them by glob. Map/checker/board mentions are not
caller evidence. Textual references are not necessarily invocations.

| Class | Count | Files |
|---|---|---|
| **Installed** — embedded in kmx | 6 | `aks-up.sh`, `aks-down.sh`, `plane-deploy.sh`, `netpol-probe.sh`, `kube-guard.sh`, `orka-k8s-tool.py` |
| **Checkout** — operator scripts | 1 | `plane-pods.sh` |
| **Demonstration** | 1 | `demo-hello-to-governed.sh` |
| **Scaffolding** — checkers, self-tests and release packaging | 20 | the eleven `check-*` files, `comment-history-go.go`, `kube-guard-test.sh`, `install-sh-test.sh`, `release-notes.py`, `homebrew-formula.py`, `test_model_fixtures.py`, `test_demo_hello_to_governed.py`, `test_orka_k8s_tool.py`, `test_owner_model_client.py` |
| **Scaffolding** — live-cluster probes | 7 | `*-probe.sh`, minus the embedded one, plus `seam-tls.sh` |
| **Scaffolding** — CI fixtures | 7 | `scripts/ci/`: `plain-model.sh`, `plain-model-server.py`, `synthetic-model.sh`, `owner-model-client.sh`, `owner-model-client.py`, `orka-tool-model.py`, `orka-tool-model.yaml` |
| **Scaffolding** — mutation specifications | 10 | `scripts/mutations/*.json` |
| **Scaffolding** — legacy-runtime scanner's approved exemptions | 1 | `legacy-runtime-allowlist.json` |

Both `model-seam-probe.sh` and `spend-race-probe.sh` call `seam_ca` directly.
`kube-guard.sh` is counted once as embedded, and is one of the ten checkers
the mutation harness breaks on purpose. Model fixtures are synthetic test
systems, not providers deployed for users.

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
namespace. `SECRET_NAMESPACE` has no default, so both shards must pass the
credential's destination at each call site.

`scripts/ci/synthetic-model.sh` is the `e2e-hosted-models` fixture: a
throwaway CA and a documentation-range address routed over kind's network,
so a public-looking hosted upstream can be dialed without a hosted account.
CI holds no hosted credential.

## `docs/` — 47 tracked files, guides and retirement records

**Guides and index (23):** `README.md`, `getting-started.md`, `kmx.md`,
`aks.md`, `models.md`, `spend.md`,
`approvals.md`, `egress.md`, `hosted-upstreams.md`, `identity.md`,
`operations.md`, `releases.md`, `workflows.md`, `FAQ.md`,
`migrate.md`, `orka.md`, `copilot-inference.md`, `interactive-chat.md`,
`interactive-lift.md`, `orka-k8s-tool.md`, `bundle-format.md`,
`agentsuite-spec.md` and `runtime-adapters.md`. Retired tool/workflow pages are pointers, not
operating instructions for deleted code.

**Retired scenario/integration records (5):** `inbound.md`, `slack.md`,
`ap-demo.md`, `release-agent.md` and `foreign-runtime.md`.

**Demonstration reference (1):** `demo.md` (the hello-to-governed model journey and other demo paths).

**Maintainer and process (16):** `development.md`, `repository-map.md`,
`reviews/2026-09-09-orka-composition.md`,
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
| `Makefile` | **Scaffolding** | Build/check targets plus plane-image, AKS-credential, network-policy and egress helpers. |
| `.github/workflows/ci.yml`, `release.yml` | **Scaffolding** | Verification gates and tag-driven releases. CI includes a required live exact-v0.10.2 Kagent create shard; its official charts are external test preconditions that KMX does not install. |
| `.goreleaser.yaml` | **Scaffolding** | GoReleaser config the `release` workflow builds and renders the Homebrew formula with; publishing reuses that checked artifact set and never pushes the formula to the tap (`skip_upload: true`). |
| `.github/actions/classify-change/` | **Scaffolding** | Classifies docs-only changes for CI. |
| `staticcheck.conf` | **Scaffolding** | Lint configuration for both modules. |
| `go.mod`, `go.sum` | **Installed tooling** | Root module dependencies. |
| `.gitignore` | **Scaffolding** | Checkout exclusions. |
| `.dockerignore` | **Scaffolding** | Defensive exclusions for root Docker contexts; current image builds do not use a root context. |

## Open questions — one

1. **Cross-runtime lifecycle scope.** Whether the exact Kagent v0.10.2
   create-only adapter should ever grow into a general translation or lifecycle
   surface remains open and unsupported. The current adapter renders Kagent
   resources directly; it does not translate them to Orka or add Kagent
   installation, chat, inspection, lift, status, evaluation, or console support.

## Existing layout

Eight tracked files under `scripts/` contain the literal `k8s/`. Embedded scripts
remain at the paths named by `embed.go`; the separate plane module is fetched
at the CLI revision. Removed workflow/ERP directories are not kept as empty
packages or placeholder manifests. Model migration, secret custody, accounting
and default Orka authoring retain their tests; scoped Kagent creation adds tests
in `app`, `runtime` and `scaffold` without adding a package directory.
