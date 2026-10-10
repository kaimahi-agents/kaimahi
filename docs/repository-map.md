# What is actually in this repository

**Orka remains the first-class/default platform; Kaimahi provides tools to get
agents onto it.** The installed CLI creates and runs agents on Kubernetes.
Runtimes and harnesses own execution, enforcement and model access. Native
Orka authoring, Provider configuration, host inference, bundle lifecycle,
AgentSuite validation and evaluation remain. Explicit Kagent create targets an
already-installed exact v0.10.2, optionally followed by one A2A message in that
same invocation. Installation and later lifecycle are unsupported.
Root status delegates the Orka-only runtime report; bundle-aware agent status
is separate.

Plane commands, administration, model overlays, credential exchange and
workload migration are removed, along with their source module, image-build
helper, embedded assets and supporting scripts/fixtures. Historical source,
including SQL migrations, remains in Git history rather than this checkout.
This repository cleanup does not delete databases, credentials, caches or
deployed resources. Cloud ownership/teardown records remain readable.

Read [the documentation index](README.md) for current guides. This map describes
tracked files, packaging and actual references, not an endorsement of retained
legacy material.

- **Installed / checkout** describes packaging or reachability, not support.
  `embed.go` names what travels inside kmx, including three shell scripts and one
  Python tool server.
- **Scaffolding** describes build, test, CI and native synthetic model fixtures.
- The custom gateway, inbound/notification runtime, approvals/grants, tool
  workflows and connector demonstrations remain retired. Native runtime tools
  and host credential handling are distinct from those removed services.

**What is checked.** `scripts/check-repository-map.py` checks counts, paths,
membership, embedding, source-package coverage and the caller claims below.
It uses `git ls-files`: stage additions/deletions before checking the intended
tree. An empty checkout-only manifest set is valid when everything is embedded;
missing lists, miscounts and unclassified tracked files still fail. No dummy
examples or fabricated callers are needed to preserve mechanical coverage.

## The short version

| Area | Packaging / checkout | Demonstration | Scaffolding |
|---|---|---|---|
| `cmd/` | `kmx` | — | — |
| `internal/` | `kmx/` (17 packages), including schema fixtures | — | experimental lifecycle contracts and tests |
| `pkg/` | — | — | experimental KMX target and agent lifecycle contracts |
| `ax-harness/` | preview projector source only; no built image or kmx adapter | — | synthetic Python tests |
| `k8s/` | two embedded native manifests | — | native packaging checks |
| `scripts/` | 4 (4 embedded in the binary, 0 operator) | 0 | 40 (checkers, release packaging, native CI runners, mutation specs) |
| `docs/` | 35 tracked files; native guides, design and history | — | maintainer and process docs |
| `brand/` | 7 identity assets for repository and organization surfaces | — | its own checker |

## `cmd/` — installed CLI

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (36 files) | **Installed** | Native agent creation and execution, Orka install/status, quickstart and local setup, guarded AKS lifecycle, interactive console/chat, read-only runtime targets, offline AgentSuite validation and artifact operations, exact Kagent v0.10.2 create, bundle lift/status/evaluation and local-reference replay verification, safe retirement and Task results. Root help describes creating and running agents on Kubernetes; root status reports only Orka. |

## `internal/` — CLI packages and retained support

`internal/kmx/` is seventeen packages at the top level (twenty Go packages
including nested `runview/orka`, `agentsuite/agentkit`, and `agentsuite/oras`).
The short version counts top-level directories.
The plane module and image-build helper are absent.
There is no `require`, and no `plane/...` import anywhere in root `cmd/` or `internal/`.
Cluster-independent decisions live in packages; shell-out orchestration lives in
`app`. `lift` holds cloud-independent rules, while the seven `lift*.go` files in
`app` run cloud orchestration, preferences and reuse checks. Interactive lift
panes use `chat_lift*.go`. Counts exclude Go test files but include non-Go data.
The test-only kubectl executable under `app/testdata/kubectl` is built separately
by App tests, not installed with kmx.

| Package or data directory | Non-test files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 96 | Installed | Command orchestration, native Orka lifecycle and run views, exact Kagent v0.10.2 create-only adapter and online proof, agent bundle persistence, lift/evaluation gates, Sessions evidence and replay verification, Task execution/results, interactive console and shared chat UI, host inference, native platform operations, guarded cloud setup and historical ownership teardown. The three Kagent non-test files are `create_kagent.go`, `kagent_create_online.go` and `runtime_kagent_lifecycle.go`. |
| `kmx/agentsessions` | 4 | Installed | Read-only bounded Sessions availability probe, Sessions gRPC evaluation and read-only replay adapters, verified remote TLS transport and syntactic destination comparison, per-case chat execution, journal evidence projection and bounded offline reference-chat reconstruction. No daemon startup, host implementation attestation, descriptor discovery or lift-gate integration. |
| `kmx/agentsuite` | 13 | Installed | Strict JSON and JCS identities, OCI image-reference, image-layout, and content-layer validation, OCI image-archive digest and attestation inspection, closed agent/tool-provider/composition/build-profile graph validation, callable Tool contracts, sandbox binding validation, provider-neutral sandbox build planning, and provider-neutral packing, CAS, and artifact push/pull contracts. |
| `kmx/agentsuite/agentkit` | 2 | Installed | Experimental AgentKit adapter for the provider-neutral sandbox builder contract; generates input for a digest-pinned AgentKit frontend, invokes the selected Docker buildx builder with the monolithic harness adapter, requests SBOM/provenance attestations where supported, and streams an OCI image-layout tar to App for inspection before publication while explicitly reporting that the runtime base is not composed. |
| `kmx/agentsuite/oras` | 6 | Installed | ORAS-backed deterministic directory push, validated directory extraction, manifest construction, validation materialization, target-bound artifact push/pull, and remote repository binding through the Docker credential store; registry configuration, authentication, and transport policy remain outside the portable AgentSuite contracts. |
| `kmx/agentsuite/schema` | 7 | Checkout | Closed JSON Schema 2020-12 reference documents for suite, agent, ToolProvider and provider composition, build-profile, and Agent sandbox-binding records; published with the source checkout, not embedded in or loaded by the binary. |
| `kmx/agentsuite/testdata/minimal` | 1 | Scaffolding | Root manifest for the checked-in minimal conformant AgentSuite layout used by package and CLI validation tests. |
| `kmx/agentsuite/testdata/minimal/agents` | 1 | Scaffolding | Minimal writer agent manifest. |
| `kmx/agentsuite/testdata/minimal/build-profiles` | 1 | Scaffolding | Minimal exact Linux build profile with pinned example descriptors. |
| `kmx/agentsuite/testdata/minimal/instructions` | 1 | Scaffolding | Digest-bound instruction content for the minimal writer agent. |
| `kmx/agentsuite/testdata/minimal/compositions` | 1 | Scaffolding | Empty composition manifest for the minimal agent and platform. |
| `kmx/agentsuite/testdata/minimal/tool-providers` | 1 | Scaffolding | Empty closed tool provider catalog for the minimal suite. |
| `kmx/agentsuite/testdata/coordinator-workers` | 1 | Scaffolding | Root manifest for the conformant coordinator, writer and reviewer fixture. |
| `kmx/agentsuite/testdata/coordinator-workers/agents` | 3 | Scaffolding | Coordinator, writer and reviewer manifests; only the coordinator declares invocation edges. |
| `kmx/agentsuite/testdata/coordinator-workers/build-profiles` | 1 | Scaffolding | Shared exact Linux build profile for all three agents. |
| `kmx/agentsuite/testdata/coordinator-workers/instructions` | 3 | Scaffolding | Digest-bound coordinator, writer and reviewer instructions. |
| `kmx/agentsuite/testdata/coordinator-workers/compositions` | 3 | Scaffolding | Empty composition manifests for all three agents on Linux amd64. |
| `kmx/agentsuite/testdata/coordinator-workers/tool-providers` | 1 | Scaffolding | Empty closed tool provider catalog for the coordinator-workers suite. |
| `kmx/agentsuite/testdata/incident-analyst` | 1 | Scaffolding | Root manifest for the realistic, buildable incident-response example with real digest-pinned Linux amd64 image inputs. |
| `kmx/agentsuite/testdata/incident-analyst/agents` | 1 | Scaffolding | Tool-free incident analyst targeting an Azure OpenAI deployment named gpt-5-mini. |
| `kmx/agentsuite/testdata/incident-analyst/build-profiles` | 1 | Scaffolding | Real digest-pinned Python runtime-base and AgentKit v0.1.0 Pydantic AI harness descriptors. |
| `kmx/agentsuite/testdata/incident-analyst/instructions` | 1 | Scaffolding | Evidence-bound incident triage, hypothesis, diagnostic and mitigation instructions. |
| `kmx/agentsuite/testdata/incident-analyst/compositions` | 1 | Scaffolding | Linux amd64 composition selecting the AgentKit v0.1.0 build profile. |
| `kmx/agentsuite/testdata/incident-analyst/tool-providers` | 1 | Scaffolding | Empty catalog reflecting the experimental backend's current no-ToolProvider boundary. |
| `kmx/agentsuite/testdata/tool-providers` | 4 | Scaffolding | `kubectl` and Azure CLI ToolProvider manifests plus the OPA provider and provider composition examples. |
| `kmx/agentsuite/testdata/remote-mcp` | 1 | Scaffolding | Remote Streamable HTTP ToolProvider manifest used by schema and semantic validation tests. |
| `kmx/agentsuite/testdata/remote-mcp/schemas` | 2 | Scaffolding | Digest-bound input and output schemas for the remote provider's callable Tool. |
| `kmx/app/testdata` | 2 | Scaffolding | Golden bytes pin the no-Task Orka artifact for both v0.1.3 and v0.2.0. |
| `kmx/app/testdata/kubectl` | 0 | Scaffolding | Test-only executable-boundary kubectl handlers, compiled once per App test run with matching race instrumentation; no App or UI dependencies. |
| `kmx/app/testdata/live-eval-loop` | 1 | Scaffolding | Portable agent for the clusterless, secret-free real-inference CI fixture and App bundle-loading test. |
| `kmx/app/testdata/live-eval-loop/eval` | 2 | Scaffolding | Public capital and arithmetic cases requiring literal `Paris` and `4` substrings, not exact answers. |
| `kmx/app/testdata/bundle-format` | 2 | Scaffolding | Exact rendered documents for historical and current portable bundle fixtures. |
| `kmx/app/testdata/bundle-format/kagent` | 2 | Scaffolding | Kagent portable agent and creation bindings; the parser and portable digest are pinned in compatibility tests. |
| `kmx/app/testdata/bundle-format/main` | 2 | Scaffolding | Current-writer portable agent and creation bindings. |
| `kmx/app/testdata/bundle-format/main/eval` | 1 | Scaffolding | Current-writer evaluation case. |
| `kmx/app/testdata/bundle-format/v0.3.0` | 2 | Scaffolding | First bundle-writer portable agent and creation bindings. |
| `kmx/app/testdata/bundle-format/v0.3.0/eval` | 1 | Scaffolding | First bundle-writer evaluation case. |
| `kmx/runview` | 1 | Installed | In-memory, runtime-neutral run, Task, agent, hand-off and missing-evidence presentation types. |
| `kmx/runview/orka` | 1 | Installed | Bounded Orka Task lineage, event and trace reader with caller-scoped reads and safe projection. |
| `kmx/runtime` | 10 | Installed | Platform-neutral adapter/session and lifecycle contracts, identities, capabilities, events, bundle digests, registry, portable Orka/Kagent authoring union, target bindings and evaluation cases. `prepared.go` checks behavior before target-bound rendering; `kagent_bindings.go` adds closed creation-target bindings. Only Orka is registered for chat/discovery. |
| `kmx/lifecycle` | 4 | Scaffolding | Experimental internal platform, AgentSuite, OCI publication and runtime ports; runtime-native documents/bundles, deploy options, recovery interfaces and receipt factories behind the public `pkg/kmx` workflows. |
| `kmx/scaffold` | 6 | Installed | Native Orka authoring, exact Kagent v0.10.2 review scaffolding in `kagent.go`, shared name validators, YAML helpers and file writing. Model-overlay and migration generators are removed. |
| `kmx/orkaschema` | 3 | Installed | Structural schema validator, attribution and upstream licence. |
| `kmx/orkaschema/fixtures/v0.1.3` | 3 | Installed | Historical release Agent/Provider/Task CRDs for explicit offline validation, not installation. |
| `kmx/orkaschema/fixtures/v0.2.0` | 3 | Installed | Default offline validation CRDs; the verified chart, not these fixtures, installs Orka. |
| `kmx/orkaschema/fixtures/main` | 3 | Installed | Immutable old main-snapshot CRDs for explicit offline validation, not a runtime support claim. |
| `kmx/guard` | 2 | Installed | Context-safety checks and read-only target resolution. |
| `kmx/toolchain` | 2 | Installed | Pinned, checksum-verified kind, kubectl and Helm downloads. |
| `kmx/portforward` | 1 | Installed | Owned loopback kubectl forwards with bind proof and process lifetime, shared by native Orka results and host tools. |
| `kmx/lift` | 2 | Installed | Cloud-independent lift rules and persisted ownership records. |
| `kmx/config` | 1 | Installed | Native settings and state/cache path resolution. |
| `kmx/cliui` | 2 | Installed | Destination-aware CLI presentation. |
| `kmx/run` | 1 | Installed | Shell-out layer. |
| `kmx/secretshapes` | 2 | Installed | Shared credential-shape checks and data. |
| `kmx/version` | 1 | Installed | Version and upgrade answers. |

Historical compatibility fixtures, native asset tests and Orka tool names do
not establish a plane dependency in native operations. Shared authoring,
credential-shape and loopback-forwarding helpers retain their native callers.

## `pkg/` — experimental public contracts

`pkg/kmx` is alpha design evidence for a wider KMX interface. It defines neutral
agent revisions, AgentSuite and sandbox-image OCI identities, targets,
deployments, scoped receipts, and the northbound `AgentEnvironment`,
`AgentSuites`, and `AgentDeployments` workflows. Platform/suite/runtime ports and
native build artifacts remain internal. It is not wired into the installed CLI
and makes no compatibility commitment.
Architecture tests keep its production dependency closure in the Go standard
library and reject known concrete runtime, Kubernetes, cloud, subprocess, and
terminal names from exported names or serialized fields.

## `ax-harness/` — preview activity source

The stdlib Python projector (`activity.py`) is checkout-only, not embedded
in kmx or built into a release image. `test_activity.py` uses synthetic
native event and journal metadata. No Task command wrapper, AX target or
runtime read is enabled by these files alone; image packaging, verification
and publishing require separate reviewed changes.

## `k8s/` — embedded native manifests

Two of `k8s/`'s 2 files are embedded; zero are not embedded.

**Embedded in `kmx` (2):** `ollama.yaml`, `orka-k8s-tool.yaml`.

**Checkout only (0):** none.

The native local model and read-only Kubernetes Tool manifests remain active.
Packaging tests require every retained asset to be readable and reject retired
plane, egress and observability assets. Setup does not deploy a model plane,
custom scrape configuration or workbook.

## `scripts/` — 44 tracked files, native helpers and scaffolding

**Reference coverage:** 34 of the 44 are named by something outside themselves,
and 10 are named by nothing. The ten mutation specifications are discovered by
glob. Map and repository-map checker mentions are not caller evidence; textual
references, including script cross-references and test data, are not necessarily
invocations or active CI coverage.

**Unreferenced retained scaffolding (0):** None.

| Class | Count | Files |
|---|---|---|
| **Embedded packaging** — native provisioning, guard and Tool helpers | 4 | `aks-up.sh`, `aks-down.sh`, `kube-guard.sh`, `orka-k8s-tool.py` |
| **Scaffolding** — checkers, self-tests and release packaging | 22 | the eleven `check-*` files, `comment-history-go.go`, `kube-guard-test.sh`, `install-sh-test.sh`, `release-notes.py`, `homebrew-formula.py`, `test_model_fixtures.py`, `test_orka_k8s_tool.py`, `test_check_mutations.py`, `test_eval_loop.py`, `test_eval_runner.py`, `test_registry_mirrors.py` |
| **Scaffolding** — native/eval CI fixtures | 7 | `scripts/ci/`: `orka-tool-model.py`, `orka-tool-model.yaml`, `live-eval-loop.sh`, `eval-loop.py`, `eval-loop-model.yaml`, `eval-loop-warmup.json`, `registry-mirrors.py` |
| **Scaffolding** — mutation specifications | 10 | `scripts/mutations/*.json` |
| **Scaffolding** — legacy-runtime scanner's approved exemptions | 1 | `legacy-runtime-allowlist.json` |

`kube-guard.sh` is counted once as embedded, and is one of the ten checkers
the mutation harness breaks on purpose. `scripts/test_check_mutations.py`
checks the mutation runner and is invoked by CONTRIBUTING's local verification
commands and CI's hygiene job.

`test_model_fixtures.py` exercises four native Orka Tool HTTP fixture tests:
read-only deployment health, missing or mismatched live deployment identity,
unset identity and unexpected Tool results. The native fixture source and YAML
remain; plane-only fixture servers, clients, source guards and probes are removed.

`.github/actions/kmx-eval` calls `scripts/ci/live-eval-loop.sh` for CI's required
`e2e-eval-loop` shard and user CI. The runner loads `eval-loop-model.yaml` and
`eval-loop-warmup.json` in AIKit mode, then calls `eval-loop.py` to gate
checkout-bound evaluation and optional zero-call local-reference replay. Endpoint
mode accepts a key only through a named secret environment variable. CI's
hygiene job calls `scripts/test_eval_loop.py` to check evidence, provenance and
failure diagnostics, and `scripts/test_eval_runner.py` to check runtime cleanup
and credential handling. The evaluation shard installs no cluster and uses no
hosted credential.

`scripts/ci/registry-mirrors.py` is called by `.github/workflows/ci.yml`
for CI-only host Docker and kind-node Docker Hub mirror setup and payload-free
endpoint reporting. The workflow's hygiene job calls
`scripts/test_registry_mirrors.py`, which also names the helper and tests daemon
configuration merging, kind-node selection, wrapper behavior and successful
endpoint evidence filtering. Neither file is embedded or installed for users;
see [CI registry mirrors](development.md#ci-registry-mirrors) for the boundaries
and pull-evidence contract.

## `docs/` — 35 tracked files, native guides and design/history

**Guides and index (15):** `README.md`, `getting-started.md`, `kmx.md`,
`aks.md`, `models.md`, `releases.md`, `FAQ.md`, `orka.md`,
`copilot-inference.md`, `interactive-chat.md`, `interactive-lift.md`,
`orka-k8s-tool.md`, `bundle-format.md`, `agentsuite-spec.md` and
`runtime-adapters.md`.

**Maintainer and process (20):** `development.md`, `repository-map.md`,
`reviews/2026-09-09-orka-composition.md`,
`reviews/2026-09-10-substrate-evaluation.md`, `entry-point-principles.md`,
`cli-ux-plan.md`, `command-conventions.md`, `charm-ux-followup-plan.md`,
`interactive-agent-tui-plan.md`, `NAMING.md`, `kmx-lifecycle-interfaces.md`,
`kmx-application-api.md`, `kmx-public-interface.md`,
`azure-discovery-performance.md`, `copilot-performance.md`, `orka-latency.md`,
`orka-startup-performance.md`, `local-foundry-inference.md`,
`chat-performance-profile.md` and `agent-lift.md`.

## `brand/` — identity assets

Seven image files plus a README. Their repository and organization uses are
recorded in `brand/README.md`; the root README embeds the compact `ketu.svg` mark
and intentionally has no hero image.
`scripts/check-brand-assets.py` checks their dimensions/transparency/metadata.

## `.github/` and the root files

| Path | Class | Evidence |
|---|---|---|
| `install.sh` | **Installed tooling** | Release-binary installer with checksum verification. |
| `README.md` | **Documentation** | Repository entry point and native direction. |
| `CHANGELOG.md` | **Build input and history** | Release notes are extracted by the release-notes script; historical release prose remains. |
| `CONTRIBUTING.md`, `LICENSE` | **Documentation** | Contribution expectations and MIT licence. |
| `embed.go` | **Packaging** | Root-module native manifest and helper embed declarations. |
| `embed_test.go` | **Scaffolding** | Verifies every embedded asset is readable. |
| `Makefile` | **Scaffolding** | Native build/check targets, context guard and AKS-credential helper. |
| `.github/workflows/ci.yml`, `release.yml` | **Scaffolding** | Verification gates and tag-driven CLI releases. CI retains all five native e2e shards, their aggregator and native clone-free proof; no plane module or service job remains. Exact-v0.10.2 Kagent charts are external test preconditions that KMX does not install. Clone-free proves installation, bare native setup and an Orka Agent/Task answer. |
| `.goreleaser.yaml` | **Scaffolding** | GoReleaser config the release workflow builds and renders the Homebrew formula with; publishing reuses that checked artifact set and never pushes the formula to the tap (`skip_upload: true`). |
| `.github/actions/classify-change/` | **Scaffolding** | Classifies docs-only changes for CI. |
| `.github/actions/kmx-eval/` | **Checkout tooling** | Reusable GitHub Action for user bundle evaluation, optional replay and payload-free artifacts; also exercised by required repository CI. |
| `staticcheck.conf` | **Scaffolding** | Root-module lint configuration. |
| `go.mod`, `go.sum` | **Installed tooling** | Root module dependencies. |
| `.gitignore` | **Scaffolding** | Checkout exclusions. |
| `.dockerignore` | **Scaffolding** | Defensive exclusions for root Docker contexts, including sensitive operator data. |

## Open questions — one

1. **Cross-runtime lifecycle scope.** Whether the exact Kagent v0.10.2
   create-only adapter should ever grow into a general translation or lifecycle
   surface remains open and unsupported. The current adapter renders Kagent
   resources directly; it does not translate them to Orka or add Kagent
   installation, chat, inspection, lift, status, evaluation or console support.

## Existing layout

Three tracked files under `scripts/` contain the literal `k8s/`.
Embedded native helpers remain at the paths named by `embed.go`.
The repository has one Go module; no plane module, build helper or plane-only
assets/scripts remain.
Removed source/docs are not kept as empty packages or placeholder guides.
Native authoring, host inference, shared credential-shape and forwarding helpers,
Sessions evaluation/replay and conservative historical cloud teardown retain
source and tests.
