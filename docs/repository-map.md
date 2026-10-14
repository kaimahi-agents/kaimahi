# What is actually in this repository

**Orka remains the first-class/default platform; Kaimahi provides tools to get
agents onto it.** The installed CLI creates and runs agents on Kubernetes.
Runtimes and harnesses own execution, enforcement and model access. Native
workspace create/eval/evaluated push/digest Lift remain: Orka by default, exact
v0.10.3 Go/linux-amd64 kagent explicitly. Pinned AgentSessions chat separately
retains offline create/prepare/load/push, explicit development-host full eval
and exact-S evaluated push, without Lift/chat/status/auth or remote lifecycle.
Public AgentSuite/ToolProvider
implementation-bundle graph/build/pull/extracted-directory transfer and alpha
Go interfaces remain. User agent.yaml readers/writers/receipts, bundle lifecycle,
live create/previews/wizard, run bundle mode and `--bundles` flags are removed.
Old bundle receipts are unreadable; no migration or on-disk/resource deletion.
Independent AKS setup/teardown, internal App.Lift, live Orka run/chat and Task/result
access remain; suite-owned objects refuse legacy invocation. Suite status and
retirement are unavailable. Root/Orka status remain runtime-only.

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
| `agentsuite/` | public specification, Go model, schemas and conformance validator | — | conformance fixtures |
| `internal/` | `kmx/` (17 packages), including schema fixtures | — | experimental lifecycle contracts and tests |
| `pkg/` | — | — | experimental KMX environment setup, AgentSuite and deployment contracts |
| `ax-harness/` | preview projector source only; no built image or kmx adapter | — | synthetic Python tests |
| `k8s/` | two embedded native manifests | — | native packaging checks |
| `scripts/` | 4 (4 embedded in the binary, 0 operator) | 0 | 59 (checkers, release packaging, native CI runners, qualification fixtures, mutation specs) |
| `docs/` | 34 tracked files; native guides, design and history | — | maintainer and process docs |
| `brand/` | 7 identity assets for repository and organization surfaces | — | its own checker |

## `cmd/` — installed CLI

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (44 files) | **Installed** | Named workspace destinations with resolve-once eval/Lift selectors, offline list-only `ctx` and optional bounded local-context/anonymous registry checks (not capability or execution proof). Root native workspace create/eval/push/digest Lift, Orka install/status/show/list, independent live `run --agent`/chat and Task results, quickstart/native console creation, context/local and hidden AKS setup/teardown, read-only targets, public suite validation/diff/build/push/pull. Eval diff reads only strict full-passing native Orka/kagent suite receipts; pinned AgentSessions native authoring/development eval and exact-S evaluated push remain independent of deleted bundle evaluation, with no cluster settings, auth or remote lifecycle; from-sessions exports private references only. Removed bundle/live-create routes are unknown; interactive Lift only prints the move notice. |

## `agentsuite/` — public specification package

The portable Go model, canonical JSON serializer, OCI/content safety checks,
and offline graph validator are importable from the root Go module.
`spec.md` is the normative specification and `README.md` introduces the package.
Builders, packing, transfer, credentials, and sandbox construction remain under
`internal/`, including the builder-owned incident-analyst fixture. Counts are
direct non-test files, including documentation and data; every file-bearing
directory has its own row.

| Package or data directory | Non-test files | Class | What it is |
|---|---|---|---|
| `.` | 11 | Checkout | Portable Go model, canonical JSON, OCI image-reference/content safety, semantic record and affected-agent diffing, and offline graph validation, plus package documentation and the normative specification. |
| `schema/` | 6 | Checkout | Closed JSON Schema 2020-12 reference documents for suite, agent, ToolProvider and provider composition, and build-profile records. |
| `testdata/minimal/` | 1 | Scaffolding | Root manifest for the minimal conformant suite. |
| `testdata/minimal/agents/` | 1 | Scaffolding | Minimal writer agent manifest. |
| `testdata/minimal/build-profiles/` | 1 | Scaffolding | Exact Linux build profile with pinned example descriptors. |
| `testdata/minimal/instructions/` | 1 | Scaffolding | Digest-bound writer instructions. |
| `testdata/minimal/compositions/` | 1 | Scaffolding | Empty agent composition for Linux amd64. |
| `testdata/minimal/tool-providers/` | 1 | Scaffolding | Empty closed ToolProvider catalog. |
| `testdata/coordinator-workers/` | 1 | Scaffolding | Root manifest for the coordinator, writer and reviewer suite. |
| `testdata/coordinator-workers/agents/` | 3 | Scaffolding | Coordinator, writer and reviewer manifests with directional invocation edges. |
| `testdata/coordinator-workers/build-profiles/` | 1 | Scaffolding | Shared exact Linux build profile. |
| `testdata/coordinator-workers/instructions/` | 3 | Scaffolding | Digest-bound coordinator, writer and reviewer instructions. |
| `testdata/coordinator-workers/compositions/` | 3 | Scaffolding | Empty per-agent Linux amd64 compositions. |
| `testdata/coordinator-workers/tool-providers/` | 1 | Scaffolding | Empty closed ToolProvider catalog. |
| `testdata/tool-providers/` | 4 | Scaffolding | `kubectl` and Azure CLI ToolProvider manifests plus the OPA provider and provider composition examples. |
| `testdata/remote-mcp/` | 1 | Scaffolding | Remote Streamable HTTP ToolProvider manifest. |
| `testdata/remote-mcp/schemas/` | 4 | Scaffolding | Digest-bound input and output schemas for the remote provider's classified, delegated-user-authorized callable Tools. |

## `internal/` — CLI packages and retained support

`internal/kmx/` is seventeen packages at the top level (twenty-two Go packages
including nested `runview/orka`, `agentsuite/agentkit`, `agentsuite/oras`, `agentsuite/workspace`, and
`app/kubectlfake`).
The short version counts top-level directories.
The plane module and image-build helper are absent.
There is no `require`, and no `plane/...` import anywhere in root `cmd/` or `internal/`.
Cluster-independent decisions live in packages; shell-out orchestration lives in
`app`. `lift` holds cloud-independent rules, while the six `lift*.go` files in
`app` run cloud orchestration, preferences and reuse checks. Interactive lift
panes use `chat_lift*.go`. Counts exclude Go test files but include non-Go data.
The test-only kubectl executable under `app/testdata/kubectl` is built separately
by App tests, never installed. Final surviving fixture/helper membership is
pending the Go retirement pass; historical data is not evidence of active readers.

| Package or data directory | Non-test files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 116 | Installed | Private AgentSessions host snapshots/full ordered packaged chat eval in `suite_agentsessions.go`, full-pass runtime proof in `suite_passing_agentsessions.go` and exact-S evaluated push, with no cluster configuration/auth/Lift/chat/status; Native source/case consent, full ordered Orka/exact-v0.10.3 Go kagent workspace eval, private passing evidence/export/push consumer and same-runtime offline diff, exact publication, digest GET-only release planning and guarded single-use Apply/Verify, Orka exact evaluated reuse and kagent absent-only placement, current identity/ResourceVersion/Secret metadata bracketing and partial outcome retention. Quickstart adds a separate fresh answer after opaque placement verification; console Ready is placement only. Bounded OpenAI-compatible judge transport in `evaluation_judge.go` and payload-free suite judge projection in `suite_judge.go` wire explicit declared identity/configuration and two threshold-side samples into native eval, with post-judge attribution checks and unchanged text receipt encoding. Independent Task/results, Orka chat/host routes, cloud setup/ownership teardown and private reference extraction remain. No user bundle persistence/normalized receipts/RecordedExecution/bundle judge wiring/replay/status/retirement/live create or live-copy GUI. Typed PortableAgent scaffolding feeds native preparation. |
| `kmx/agentsessions` | 7 | Installed | Strict native packaged chat invocation/config domain in `suite.go`: complete canonical five-record semantic proof, 256-frame/4-MiB stream budgets, pre-Exec repeated-Session refusal and one fresh Session/config/Exec per case. Read-only availability probes, verified remote TLS for private reference extraction and bounded journal/reference-chat reconstruction remain. Diagnostic read-only replay cannot authorize native candidate proof; no legacy bundle consumer or compatibility receipt reader remains. Correlation is not tool capability; no daemon startup, tool enablement, host/build/model attestation, registration discovery or lift-gate integration. |
| `kmx/agentsuite` | 8 | Installed | Builder-owned sandbox binding, bounded OCI image-archive metadata parsing, digest and attestation inspection, provider-neutral sandbox build planning, and provider-neutral packing, CAS, and artifact push/pull contracts; portable format validation remains in the public AgentSuite package. |
| `kmx/agentsuite/agentkit` | 2 | Installed | Experimental AgentKit adapter for the provider-neutral sandbox builder contract; generates input for a digest-pinned AgentKit frontend, invokes the selected Docker buildx builder with the monolithic harness adapter, requests SBOM/provenance attestations where supported, and streams an OCI image-layout tar to App for inspection before publication while explicitly reporting that the runtime base is not composed. |
| `kmx/agentsuite/oras` | 10 | Installed | Bounded exact-digest registry/read-only-layout artifact snapshots before validation, unsigned provenance referrers attached on registry push before tagging and listed on pull, cross-process local-layout writer exclusion and exact prepared-layout graph publication, ORAS-backed deterministic directory push, validated directory extraction, manifest construction, validation materialization, target-bound artifact push/pull, and remote repository binding through the Docker credential store; registry configuration, authentication, and transport policy remain outside the portable AgentSuite contracts. |
| `kmx/agentsuite/workspace` | 6 | Installed | Read-only nearest-ancestor workspace discovery with explicit-root override, closed one-member Orka/default, exact-v0.10.3 Go kagent or pinned AgentSessions chat workspace authoring (AgentSessions author/prepare/load/push; execution authority lives in the separate explicit development eval/proof path), bounded confined source/case snapshotting, existing public-format resolution and runtime-bound KMX-private native metadata, private atomic prepared OCI layouts, digest-verified reuse and internal packaged native-input loading, including source-free loading from verified artifact snapshots. Embedded real build-profile descriptors remain declared build inputs, not native member images. No builder, inference or cluster operations. |
| `kmx/agentsuite/schema` | 1 | Checkout | Builder-owned Agent sandbox-binding schema; published with the source checkout, not embedded in or loaded by the binary. |
| `kmx/agentsuite/testdata/incident-analyst` | 1 | Scaffolding | Internal builder-owned realistic, buildable incident-response example with real digest-pinned Linux amd64 image inputs; not a portable conformance fixture. |
| `kmx/agentsuite/testdata/incident-analyst/agents` | 1 | Scaffolding | Tool-free incident analyst targeting an Azure OpenAI deployment named gpt-5-mini. |
| `kmx/agentsuite/testdata/incident-analyst/build-profiles` | 1 | Scaffolding | Real digest-pinned Python runtime-base and AgentKit v0.1.0 Pydantic AI harness descriptors. |
| `kmx/agentsuite/testdata/incident-analyst/instructions` | 1 | Scaffolding | Evidence-bound incident triage, hypothesis, diagnostic and mitigation instructions. |
| `kmx/agentsuite/testdata/incident-analyst/compositions` | 1 | Scaffolding | Linux amd64 composition selecting the AgentKit v0.1.0 build profile. |
| `kmx/agentsuite/testdata/incident-analyst/tool-providers` | 1 | Scaffolding | Empty catalog reflecting the experimental backend's current no-ToolProvider boundary. |
| `kmx/app/testdata` | 2 | Scaffolding | Golden bytes pin the no-Task Orka artifact for both v0.1.3 and v0.2.0. |
| `kmx/app/testdata/kubectl` | 0 | Scaffolding | Test-only executable-boundary kubectl handlers and thin reconcile/kagent adapters, compiled once per App test run with matching race instrumentation; no App or UI dependencies. |
| `kmx/app/kubectlfake` | 4 | Scaffolding | Shared stateful reconcile/kagent handlers and pins for executable and explicitly selected in-process capture fixtures; request-scoped context, environment and I/O, synchronous cancellable delays and observed writer errors. App parity tests exercise both modes; unit tests run under `go test ./...` and CI. Not imported by installed code. |
| `kmx/app/testdata/live-eval-loop` | 1 | Scaffolding | Native instruction-text fixture; old user agent.yaml fixture removed. |
| `kmx/app/testdata/live-eval-loop/eval` | 2 | Scaffolding | Public capital and arithmetic cases requiring literal `Paris` and `4` substrings, plus a Go RE2 capital pattern and `notContains` check, not exact answers. |
| `kmx/runview` | 1 | Installed | In-memory, runtime-neutral run, Task, agent, hand-off and missing-evidence presentation types. |
| `kmx/runview/orka` | 1 | Installed | Bounded Orka Task lineage, event and trace reader with caller-scoped reads and safe projection. |
| `kmx/runtime` | 12 | Installed | Runtime/session identity, capability and event contracts, internal native typed scaffolding/bindings, strict authored evaluation assertions/RE2 and private reference manifests. Only Orka chat/discovery is registered. No persistent old user bundle/normalized receipt or RecordedExecution consumer. Assertion and judge primitives are retained independently; native Orka/kagent suite eval wires declared llmJudge without changing runtime-core policy. |
| `kmx/lifecycle` | 4 | Scaffolding | Experimental deployment runtime ports; runtime-native documents/bundles, deploy options, recovery interfaces and deployment/retirement receipt factories behind the public `pkg/kmx` workflow. Environment setup has no mirrored southbound SPI. Focused AgentSuite builder, packing, storage and transfer ports remain in `internal/kmx/agentsuite`. |
| `kmx/scaffold` | 6 | Installed | Native Orka authoring, exact Kagent v0.10.2 review scaffolding in `kagent.go`, shared name validators, YAML helpers and file writing. Model-overlay and migration generators are removed. |
| `kmx/orkaschema` | 3 | Installed | Structural schema validator, attribution and upstream licence. |
| `kmx/orkaschema/fixtures/v0.1.3` | 3 | Installed | Historical release Agent/Provider/Task CRDs for explicit offline validation, not installation. |
| `kmx/orkaschema/fixtures/v0.2.0` | 3 | Installed | Default offline validation CRDs; the verified chart, not these fixtures, installs Orka. |
| `kmx/orkaschema/fixtures/main` | 3 | Installed | Immutable old main-snapshot CRDs for explicit offline validation, not a runtime support claim. |
| `kmx/guard` | 2 | Installed | Context-safety checks and read-only target resolution. |
| `kmx/toolchain` | 2 | Installed | Pinned, checksum-verified kind, kubectl and Helm downloads. |
| `kmx/portforward` | 1 | Installed | Owned loopback kubectl forwards with bind proof and process lifetime, shared by native Orka results and host tools. |
| `kmx/lift` | 2 | Installed | Cloud-independent lift rules and persisted ownership records. |
| `kmx/config` | 2 | Installed | Native settings/state/cache paths and strict bounded credential-free kmx.yaml operational destination selectors, independent from authored suite identity. |
| `kmx/cliui` | 2 | Installed | Destination-aware CLI presentation. |
| `kmx/run` | 1 | Installed | Shell-out layer. |
| `kmx/secretshapes` | 2 | Installed | Shared credential-shape checks and data. |
| `kmx/version` | 1 | Installed | Version and upgrade answers. |

Historical compatibility fixtures, native asset tests and Orka tool names do
not establish a plane dependency in native operations. Shared authoring,
credential-shape and loopback-forwarding helpers retain their native callers.

## `pkg/` — experimental public contracts

`pkg/kmx` is alpha design evidence for a wider KMX interface. It defines a
setup-only logical `AgentEnvironment`, neutral agent revisions, staged AgentSuite
validate/build/push values and sandbox-image OCI identities, targets,
deployments, deployment/retirement receipts, and the northbound
`AgentSuites` and `AgentDeployments` workflows. Focused suite and deployment
runtime ports plus native build artifacts remain internal. It is not wired into
the installed CLI and makes no compatibility commitment.
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

## `scripts/` — 63 tracked files, native helpers and scaffolding

**Reference coverage:** 53 of the 63 are named by something outside themselves,
and 10 are named by nothing. The ten mutation specifications are discovered by
glob. Map and repository-map checker mentions are not caller evidence; textual
references, including script cross-references and test data, are not necessarily
invocations or active CI coverage.

**Unreferenced retained scaffolding (0):** None.

| Class | Count | Files |
|---|---|---|
| **Embedded packaging** — native provisioning, guard and Tool helpers | 4 | `aks-up.sh`, `aks-down.sh`, `kube-guard.sh`, `orka-k8s-tool.py` |
| **Scaffolding** — checkers, self-tests and release packaging | 33 | the eleven `check-*` files, `comment-history-go.go`, `kube-guard-test.sh`, `install-sh-test.sh`, `release-notes.py`, `homebrew-formula.py`, `test_model_fixtures.py`, `test_orka_k8s_tool.py`, `test_check_mutations.py`, `test_registry_mirrors.py`, `test_kagent_lift.py`, `test_kagent_lift_shell.py`, `test_kagent_lift_fixture.py`, `test_kagent_lift_qualification.py`, `test_kagent_lift_audit.py`, `test_kagent_lift_qualify.py`, `test_kagent_lift_ci.py`, `test_suite_native.py`, `test_suite_native_lift.py`, `test_suite_kagent.py`, `test_orka_runtime_fixture.py`, `test_bundle_retirement_ci.py`, `test_orka_native_reference.py` |
| **Scaffolding** — native/eval CI and qualification fixtures | 15 | `scripts/ci/`: `orka-tool-model.py`, `orka-tool-model.yaml`, `eval-loop-model.yaml`, `eval-loop-warmup.json`, `registry-mirrors.py`, `kagent-lift.sh`, `kagent-lift.py`, `kagent-lift-fixture.py`, `kagent-lift-audit.py`, `suite-native.py`, `suite-native-audit.py`, `native-suite-model.yaml`, `suite-kagent.py`, `orka-runtime-fixture.py`, `orka-native-reference.go` |
| **Scaffolding** — mutation specifications | 10 | `scripts/mutations/*.json` |
| **Scaffolding** — retired-command scanner's exact inert exceptions | 1 | `legacy-runtime-allowlist.json` |

`kube-guard.sh` is counted once as embedded, and is one of the ten checkers
the mutation harness breaks on purpose. `scripts/test_check_mutations.py`
checks the mutation runner and is invoked by CONTRIBUTING's local verification
commands and CI's hygiene job.

`test_model_fixtures.py` exercises four native Orka Tool HTTP fixture tests:
read-only deployment health, missing or mismatched live deployment identity,
unset identity and unexpected Tool results. The native fixture source and YAML
remain; plane-only fixture servers, clients, source guards and probes are removed.

The reusable bundle eval action, clusterless runner and their
self-tests are removed with the clusterless recorded Sessions/judge/replay shard.
AIKit model YAML/warmup and native instruction/two-case assets remain inputs to
native qualification, not a replacement user-bundle runner.

`scripts/ci/registry-mirrors.py` is called by `.github/workflows/ci.yml`
for CI-only host Docker and kind-node Docker Hub mirror setup and payload-free
endpoint reporting. The workflow's hygiene job calls
`scripts/test_registry_mirrors.py`, which also names the helper and tests daemon
configuration merging, kind-node selection, wrapper behavior and successful
endpoint evidence filtering. Neither file is embedded or installed for users;
see [CI registry mirrors](development.md#ci-registry-mirrors) for the boundaries
and pull-evidence contract.

`scripts/ci/kagent-lift.sh` retains exact v0.10.3 external bootstrap and calls
raw upstream Tool smoke/direct single A2A MCP proof, separately from tool-free
packaged-suite two-case eval, self-diff, evaluated push and source-withheld digest
Lift into a fresh namespace with occupied-repeat refusal. Lift sends no native
execution calls. Readiness/bootstrap is not evaluated execution; no legacy
bundle receipts/status/judge claims remain. Hermetic fixture/routing/privacy
tests exercise boundaries without a cluster; final build/live execution is
needed before claiming retirement qualification.

`scripts/ci/suite-native.py` runs in the exclusively owned Orka shard after
raw upstream Tool/Task/model-answer proof. It covers actual native create/full
two-case eval/evaluated publication/exact-S source-withheld digest Lift reuse,
private captures and payload-free artifacts. Exact-reuse and bounded absent-pair
registry promotion include fresh answer Tasks. Promotion uses source-only evidence,
GET-only plans, reviewed Apply/Verify and independent placement readback; its
reference rendering uses the shared native scaffold through the checkout-only
`orka-native-reference.go` helper. Evaluation is not retried; broader promotion,
creation and partial outcomes stay outside this fixed scope. Existing cluster
deletion owns cleanup.

`scripts/ci/suite-native.py` is called by the existing `e2e-orka-runtime`
shard after its legacy Ollama proof. It creates the fixed `native-suite-model.yaml`
AIKit workload in that exclusively owned cluster and installs the
`suite-native-audit.py` product command wrapper. Hygiene calls
`test_suite_native.py` and `test_suite_native_lift.py` for hermetic independent
readback, hash, intent/completion audit, refusal and privacy checks. A successful
scenario qualifies the fixed three-case workspace path (two text cases and one
open-ended two-sample rubric case), exact registry/local root
CLI lift reuse with source withheld, separate fresh answer Tasks and no-effect
lift refusals against the pinned model/runtime. It also qualifies registry
promotion into an absent pair on a second exclusively owned kind cluster, with
GET-only planning, ordered admission/creation, independent readback, one fresh
Task and no-effect model-mismatch refusal. Both pinned Orka installations use
orka-system; destination inference has its own Provider/Secret and shares the
source model through a fixture-only private-network NodePort with no host mapping.
Source evidence remains distinct from destination evaluation. Partial outcomes
are not qualified. Passing artifacts contain only
private receipt metadata and payload-free qualification evidence, including
independently bound declared judge identity/configuration and scalar sample counts.
The judge reuses the pinned AIKit workload; existing timeout budgets remain fixed.
Existing cluster deletion
owns runtime/model cleanup. These files are checkout-only scaffolding, not an
installer or new CLI/API capability.

## `docs/` — 34 tracked files, native guides and design/history

**Guides and index (14):** `README.md`, `getting-started.md`, `kmx.md`,
`aks.md`, `models.md`, `releases.md`, `FAQ.md`, `orka.md`,
`copilot-inference.md`, `interactive-chat.md`,
`orka-k8s-tool.md`, `bundle-format.md`, `runtime-adapters.md` and `suite-workspace.md`.

**Maintainer and process (20):** `development.md`, `codebase-overview.md`,
`interface-baseline.md`, `repository-map.md`,
`cli-ux-plan.md`, `command-conventions.md`, `charm-ux-followup-plan.md`,
`interactive-agent-tui-plan.md`, `NAMING.md`, `kmx-lifecycle-interfaces.md`,
`kmx-application-api.md`, `kmx-public-interface.md`,
`azure-discovery-performance.md`, `copilot-performance.md`, `orka-latency.md`,
`orka-startup-performance.md`, `local-foundry-inference.md`,
`chat-performance-profile.md`, `command-semantics.md` and
`suite-first-lifecycle.md`.

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
| `CODE_OF_CONDUCT.md` | **Documentation** | Microsoft Open Source Code of Conduct and moderation contacts. |
| `SECURITY.md` | **Documentation** | Microsoft security vulnerability reporting guidance. |
| `AGENTS.md` | **Documentation** | Repository guidance for consistent command changes and verification. |
| `embed.go` | **Packaging** | Root-module native manifest and helper embed declarations. |
| `embed_test.go` | **Scaffolding** | Verifies every embedded asset is readable. |
| `Makefile` | **Scaffolding** | Native build/check targets, context guard and AKS-credential helper. |
| `.github/workflows/ci.yml`, `release.yml` | **Scaffolding** | Native four-shard fail-closed required aggregator and unchanged 315s quickstart ceiling: kagent Lift, Orka runtime, stock Orka Helm, quickstart. Raw upstream Task/Tool/model-answer fixtures are separate from packaged native eval/push/source-withheld digest Lift and bounded Orka absent-pair registry promotion with source-only evidence. Clone-free authors/evaluates/publishes/Lifts a workspace then reads its existing evaluation Task result, without separate answer Task/raw suite run. Hermetic harness checks are separate from pinned live runtime qualification. |
| `.goreleaser.yaml` | **Scaffolding** | GoReleaser config the release workflow builds and renders the Homebrew formula with; publishing reuses that checked artifact set and never pushes the formula to the tap (`skip_upload: true`). |
| `.github/actions/classify-change/` | **Scaffolding** | Classifies docs-only, scripts and kagent/shared qualification paths for CI. |

| `staticcheck.conf` | **Scaffolding** | Root-module lint configuration. |
| `go.mod`, `go.sum` | **Installed tooling** | Root module dependencies. |
| `.gitignore` | **Scaffolding** | Checkout exclusions. |
| `.dockerignore` | **Scaffolding** | Defensive exclusions for root Docker contexts, including sensitive operator data. |

## Open questions — one

1. **Wider cross-runtime lifecycle scope.** Native exact-v0.10.3 Go kagent
   supports packaged eval/publication/create-only digest Lift, not standalone
   run/chat, image placement, updates, suite status/retirement, diff or rollback.
   Public alpha contracts are unwired, and retired bundle readers are not a
   compatibility layer. Final inventory counts are pending parent finalization.

## Existing layout

Two tracked files under `scripts/` contain the literal `k8s/`.
Embedded native helpers remain at the paths named by `embed.go`.
The repository has one Go module; no plane module, build helper or plane-only
assets/scripts remain.
Removed source/docs are not kept as empty packages or placeholder guides.
Native authoring, host inference, shared credential-shape and forwarding helpers,
Sessions evaluation/replay and conservative historical cloud teardown retain
source and tests.
