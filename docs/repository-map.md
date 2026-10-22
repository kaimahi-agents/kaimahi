# What is actually in this repository

**Orka remains the first-class/default platform; Kaimahi provides tools to get
agents onto it.** The installed CLI creates and runs agents on Kubernetes.
Runtimes and harnesses own execution, enforcement and model access. Native
workspace create/test/evaluated push/digest Deploy remain: Orka by default, exact
v0.10.3 Go/linux-amd64 kagent explicitly. Pinned AgentSessions chat separately
retains offline create/prepare/load/push, explicit development-host full eval
and exact-S evaluated push with a required AgentSessionsBinding, plus a
plan-only Deploy render, without chat/status/auth, apply or remote lifecycle.
Public AgentSuite/ToolProvider implementation-bundle graph/build/pull/extracted-directory
transfer and alpha Go interfaces remain. User agent.yaml readers/writers/receipts,
bundle lifecycle, live create/previews/wizard, run bundle mode and the bundles
flag are removed. Old bundle receipts are unreadable; no migration or
on-disk/resource deletion. Independent AKS setup/teardown, internal App.Lift,
live Orka run/chat and Task/result access remain, including direct invocation
of suite-owned Orka Agents by live identity, not workspace-local execution or
suite evaluation. Legacy adoption, copying and retirement fences remain.
Suite status and retirement are unavailable. Root destination status is GET-only
observation, not lifecycle authority; Orka status remains runtime-only.

Plane commands, administration, model overlays, credential exchange and workload
migration are removed, along with their source module, image-build helper,
embedded assets and supporting scripts/fixtures. Historical source, including
SQL migrations, remains in Git history rather than this checkout. This cleanup
does not delete databases, credentials, caches or deployed resources. Cloud
ownership/teardown records remain readable.

Read [the documentation index](README.md) for current guides. This map describes
directory responsibilities and important boundaries, not an endorsement of
retained legacy material. **Installed / checkout** describes packaging or
reachability, not support. **Scaffolding** describes build, test, CI and native
synthetic model fixtures. The custom gateway, inbound/notification runtime,
approvals/grants, tool workflows and connector demonstrations remain retired;
native runtime tools and host credential handling are distinct.

**What is checked.** `scripts/check-repository-map.py` checks named paths,
local Markdown links, nonempty area sections, directory ownership coverage and
the retired-module boundary. It uses `git ls-files`: stage additions/deletions before checking the
intended tree. Descendants, including nested fixtures, inherit their nearest
owned directory's responsibility. Ordinary additions below an owned directory
need no map edit. New top-level areas and new ownership packages need a section
or responsibility row; an area heading alone cannot cover an unowned package.
Classifications are editorial, not checker verdicts. Examples are not exhaustive
file or caller lists. Actual embed declarations, including package-local ones,
remain build authority; this map does not mirror their inventory.

## `cmd/` — installed CLI

| Directory | Class | Responsibility |
|---|---|---|
| `cmd/kmx` | Installed | Command adapters, argument validation, help/completion and presentation. Native workspace create/test/push/Deploy, Orka operations, live run/chat and Task results, quickstart/console, context/local and hidden AKS setup/teardown, read-only targets, suite transfer and offline policy resolution. |

Stdio `mcp --root DIRECTORY` binds Create/Edit/Run/Test to one explicit root and
the shared snapshot result, without a separate runner or tool-selected endpoints.

Markdown-file Run and Test use detached snapshots and bounded safe preview
projection. A temporary direct no-journal runner uses the selected model-access
transport, a deterministic fake and one-sample preview judging until embedded
runner integration. This is not Squad registration or durable execution proof.

Create prompts for missing exact model/file inputs only at a terminal; scripts
must supply both. Composed Orka workspace Deploy has stage/verbose/JSON
presentation alongside explicit digest Deploy. Named destinations resolve once;
ctx is an offline list with optional bounded local-context/anonymous registry
checks, not execution proof. Offline test explain reads bounded owner-only
failure records with explicit content opt-in. Portable test export requires
fresh captured build provenance; test verify is a state-free gate over independently
reviewed expectations. Receipt diff accepts strict full-passing Orka/kagent
proof, not AgentSessions proof. Removed bundle/live-create routes stay unknown;
interactive Deploy only prints the move notice.

## `testdata/` — owned consumer snapshots (`testdata/agentsuite/`)

The portable Go model, canonical JSON, schemas, safety checks, offline graph
validator and conformance tests live in the separate
[agent-primitives/suite](https://github.com/agent-primitives/suite) module and its
[normative specification](https://github.com/agent-primitives/suite/blob/main/spec.md).
KMX imports that module explicitly as agentsuite.

`testdata/agentsuite/` owns byte-identical consumer-test snapshots and their
provenance without depending on a module-cache path. Its descendants inherit
this test-support responsibility, not a per-fixture map entry.
`scripts/check-agentsuite-fixtures.py` independently checks recorded bytes,
Git modes and provenance inventory offline, with an optional upstream comparison.
The internal builder's incident-analyst example is separate: realistic build
inputs are not portable conformance or evidence of runtime qualification.

## `internal/` — CLI packages and retained support

The plane module and image-build helper are absent.
Root Go module declarations and actual imports under `cmd/` and `internal/`
must not reacquire that dependency. Historical comments and compatibility
fixtures do not establish an active dependency. Cluster-independent decisions
live in packages; shell-out orchestration lives in App. Cloud-independent lift
rules and ownership records are separate from App's cloud orchestration,
preferences and reuse checks; interactive Deploy panes are presentation only.

Ownership roots are `internal/kmx/`'s immediate packages, or another immediate
area under `internal/`. Nested implementation packages, schemas and fixtures
inherit their owning root; they do not need individual rows.

Platform-specific Unix operations live under these ownership roots. Matching
Windows adapters are compile-only unsupported stubs, not native implementations.
The adapters are installed-library source; host-side tests check their inert
contract. The Windows CLI guard remains in place: library cross-builds do not
enable it.

| Directory | Class | Responsibility |
|---|---|---|
| `internal/kmx/mcp` | Installed | Stdio-only Create/Edit/Run/Test over canonical parsing and App snapshots, with exclusive atomic publication, revision checks and OS-held writer locks. Edit preserves mode bits, not ownership, ACLs or extended attributes; normal ownership and directory inheritance apply to replacement files. Intended for the user's own files. Runner availability comes from App, with no substitute runner or native Windows CLI qualification. |
| `internal/kmx/agentpreview` | Installed | Typed embedded-runner request/outcome contract; no execution engine, model adapter or production fake. |
| `internal/kmx/app` | Installed | Cluster-independent markdown-file preview orchestration and versioned safe projection, with temporary direct no-journal execution via selected model-access transport and one-sample preview judging until embedded runner integration. Native operation orchestration, frozen workspace eval→push→Deploy coordination, consent, private exact-S passing proof and completion observations, bounded release state/CAS, provenance and ownership-safe update/rollback. Public evidence export/verification stays separate from private execution and publication gates. Independent live Task/results, Orka chat/host routes and cloud ownership teardown remain. No legacy bundle persistence, replay/status/retirement, live create or live-copy GUI. |
| `internal/kmx/agentsessions` | Installed | Strict packaged chat invocation/config, read-only availability and bounded journal/reference extraction; offline ActorTemplate/HarnessRegistration rendering from bindings. Fresh Session/config/Exec per case run, bounded semantic journal proof; diagnostic replay cannot authorize candidate proof. No daemon startup, tool enablement, host/model attestation, registration discovery or legacy receipt reader. Golden renders are test support, not deployed assets. |
| `internal/kmx/agentfile` | Checkout | Bounded canonical charter parsing and regular-file loading with source locations, exact-byte revisions and ordered examples. No CLI, execution, model access, source writes, Squad registration or implicit suite loading. Nested charter fixtures are test support. |
| `internal/kmx/agentsuite` | Installed | Builder-owned sandbox binding and image metadata/attestation inspection, provider-neutral build/packing/CAS/transfer contracts. Nested AgentKit adapter, bounded local artifact ingestion, registry transport and workspace authoring own distinct seams, described below. Schemas are checkout assets; incident-analyst data is builder test support. |
| `internal/kmx/runview` | Installed | Runtime-neutral run, Task, agent, hand-off and missing-evidence presentation types; nested Orka readers provide bounded lineage/events/traces with caller-scoped projection. |
| `internal/kmx/runtime` | Installed | Runtime/session identity, capabilities, native typed scaffolding/bindings, strict authored assertions/RE2 and private references. Closed AgentSessionsBinding parsing, JCS identity and model matching. Only Orka chat/discovery is registered; assertion/judge primitives remain independent of removed bundle consumers. Nested binding examples are test support. |
| `internal/kmx/lifecycle` | Scaffolding | Experimental deployment runtime ports, runtime-native documents, recovery and receipt factories behind the public alpha workflow. Environment setup has no mirrored southbound SPI; focused builder/storage/transfer ports remain with the internal suite implementation. |
| `internal/kmx/scaffold` | Installed | Native Orka authoring, explicit review-only kagent scaffolding, shared name validation, YAML helpers and file writing. Model-overlay and migration generators are removed. |
| `internal/kmx/orkaschema` | Installed | Structural schema validation and attribution. Nested historical release/default/frozen-main CRDs support explicit offline validation, not installation or an additional runtime-support claim; the verified chart installs Orka. |
| `internal/kmx/guard` | Installed | Context-safety checks, context-bound pipeline confirmation and read-only target resolution. |
| `internal/kmx/modelaccess` | Installed | Non-secret exact model routing and bounded read-only Foundry Local/Ollama discovery. Config-free model list does not start/install runtimes, download catalogs or execute agents; legacy kind reuse adapts only Ollama inventory. |
| `internal/kmx/toolchain` | Installed | Pinned, checksum-verified kind, kubectl and Helm downloads. |
| `internal/kmx/portforward` | Installed | Owned loopback kubectl forwards with bind proof and process lifetime, shared by native Orka results and host tools. |
| `internal/kmx/lift` | Installed | Cloud-independent lift rules and persisted ownership records. |
| `internal/kmx/config` | Installed | Native settings/state/cache paths and strict bounded credential-free operational destination selectors, independent of authored suite identity. |
| `internal/kmx/cliui` | Installed | Destination-aware CLI presentation. |
| `internal/kmx/run` | Installed | Shell-out execution boundary. |
| `internal/kmx/secretshapes` | Installed | Shared credential-shape checks and data. |
| `internal/kmx/version` | Installed | Version and upgrade answers. |

App retains fresh candidate placement and verified cleanup for public Orka eval;
operator cleanup is a separately confirmed deletion-only UID snapshot, not
execution authority. Startup reports, never sweeps or blocks publication.
Managed passing proof is immutable and private, completion-finalized with
workspace exclusion; older placement observations and reuse/skips never enroll
or repair ownership. Public update is opt-in Orka-only with exact prepared
inputs and bounded recorded proof; rollback uses original retained evidence.
Only inference-Provider RV drift may pass between revisions. Non-secret release
provenance and acknowledged intent/outcomes remain distinct from evaluation and
GET-only skip authority. Failed evaluation details have bounded owner-only
retention and explicit content opt-in, never receipts or outcome JSON.

Native eval preserves full ordered runtime-specific proof, explicit judge
identity/configuration, threshold-side samples and unknown-veto repeat policy.
AgentSessions development eval loads no cluster config and shares judge/repeat
policy; its Deploy render applies nothing. Orka exact evaluated reuse and
kagent absent-only placement retain current-identity/RV/Secret bracketing and
partial outcomes. Quickstart sends a separate fresh answer after verified
placement; console Ready is placement only. App's nested kubectl executable and
shared fake handlers are test-only, with no installed App/UI dependency.

Within the internal suite implementation, the experimental AgentKit adapter
uses a digest-pinned frontend and selected Docker buildx builder, streams OCI
output and reports that a runtime base is not composed. Local artifact readers
perform bounded, confined, no-follow ingestion and exact selected-graph checks
without registry/auth/subprocess dependencies. Registry adapters add exact-digest
snapshots, unsigned provenance, writer exclusion and credential-store transport;
public recipe loading does not import that transport. Workspace authoring and
loading freeze source/cases and verify packaged identity, including source-free
snapshots. Profiles, multi-member inputs, flow and policy declarations have
explicit experimental loaders; default execution gates stay strict. Declared
Sessions associations are not image verification or execution/Deploy authority.
Embedded build descriptors are build inputs, not native member images.

## `pkg/` — public Go packages

| Directory | Class | Responsibility |
|---|---|---|
| `pkg/kmx` | Alpha design evidence | Standard-library-only setup, suite and deployment contracts; no concrete implementation or CLI wiring and no compatibility commitment. |
| `pkg/suiteview` | Concrete read-only library | Bounded local OCI layout/archive loading for an explicit verified member, or explicitly selected experimental flow with copied member recipes and flow identity. No runtime, evidence, registry, execution, publication or mutation authority. |

The alpha contracts describe setup-only logical environments, neutral revisions,
suite validate/build/push values, image identities, targets and deployment/retirement
receipts/workflows. Runtime ports and build artifacts remain internal. Architecture
tests keep the production dependency closure in the standard library and reject
concrete runtime/cloud/Kubernetes/subprocess/terminal leakage in exported names
or serialized fields. Public format ownership remains in the external suite
module; native loaders apply stricter private checks after public projection.

## `ax-harness/` — preview activity source

The stdlib Python activity projector is checkout-only, not embedded in kmx or
built into a release image. Its tests use synthetic native event and journal
metadata. These sources alone enable no Task wrapper, AX target or runtime
read; image packaging, verification and publishing require separate review.

## `k8s/` — native manifest assets

Native local model and read-only Kubernetes Tool manifests remain active.
`embed.go` declares root-module packaging; this directory's descendants inherit
its asset responsibility without mirroring those declarations here. Packaging
tests require retained assets to be readable and reject retired plane, egress
and observability assets. Setup does not deploy a model plane, custom scrape
configuration or workbook.

## `scripts/` — native helpers and scaffolding

This directory owns checkout automation, checkers/self-tests, release packaging,
mutation specifications and native CI qualification support. Descendants inherit
those roles; a new helper or mutation spec needs no file/caller inventory edit.
Some helpers travel in the binary; actual embed declarations determine which.
Textual references are not necessarily invocations or active CI coverage.
The mutation harness independently discovers gates and checks representative
mutants; checker self-tests and ordinary tree checks remain necessary.

Native synthetic model/Tool fixtures exercise health, identity and result
boundaries without establishing live qualification. Plane-only fixture servers,
clients, source guards and probes are removed. The reusable bundle eval action,
clusterless runner and recorded Sessions/judge/replay shard are removed;
retained AIKit model/warmup and native instruction/case assets are qualification
inputs, not a replacement user-bundle runner.

`scripts/ci/` owns checkout-only qualification, not an installed CLI capability.
Registry-mirror support configures CI host/kind Docker and emits payload-free
endpoint evidence; see [CI registry mirrors](development.md#ci-registry-mirrors).
Kagent qualification separates pinned external bootstrap/readiness and raw
upstream Tool/A2A proof from packaged-suite eval, self-diff, evaluated push and
source-withheld digest Deploy with occupied-repeat refusal. Deploy sends no
native execution calls. Hermetic routing/privacy tests are not live proof.

Orka qualification runs after raw upstream Tool/Task/model-answer proof in an
exclusively owned cluster. It evaluates the fixed native workspace against the
pinned AIKit workload, including declared judge configuration and scalar samples,
then checks exact publication, source-withheld registry/layout Deploy reuse,
separate fresh answer Tasks and no-effect refusals. Absent-pair promotion uses
source-only evidence, GET-only planning, guarded Apply/Verify and independent
readback on a separately owned destination; fixture-only private-network model
sharing is not destination evaluation. Partial outcomes stay unqualified.
Captures remain private and artifacts payload-free; existing cluster deletion
owns model/runtime cleanup. No timeout increase or hermetic harness result
substitutes for actual pinned live qualification.

## `docs/` — guides, site scaffolding and design/history

Descendants inherit documentation responsibility: current native guides,
maintainer/process guidance, design records, historical compatibility context,
site source and schema provenance. Historical plans are not current capability
claims. Frontend theme/navigation assets serve the documentation site, not an
agent runtime or product GUI; source Markdown remains authoritative and generated
HTML/caches are untracked. Frozen public schema assets retain upstream format
ownership and independent parity checks. Optional Azure CPU-inference templates
are checkout-only guidance/test assets, not embedded or invoked by KMX setup.

## `brand/` — identity assets

Repository and organization identity assets inherit this role. Uses are recorded
in `brand/README.md`; the root README embeds the compact mark and intentionally
has no hero image. `scripts/check-brand-assets.py` independently checks asset
dimensions, transparency and metadata.

## `plugins/` — Copilot plugin development package

`plugins/copilot/` contains Agent Plugins 1.0 manifest/MCP templates, one
chat skill, the offline Python developer packager and bundled-engine
integration tests. The packager binds one explicitly selected root before
installation and copies only caller-pinned engine bytes; it never executes or
downloads an engine. Source templates contain no executable and are not an
installable release. Generated bundles include no Python runtime.

Tests build the real CLI, relocate the generated bundle and exercise rooted
Create/Edit/Run/Test with its deterministic fake route on Linux/macOS. Windows
runs mechanical package tests only while main blocks native CLI builds. Both
hygiene and `.github/workflows/copilot-plugin.yml` run the package tests. See
[the package guide](../plugins/copilot/README.md) for the verification boundary,
macOS/Windows trust gates and deferred file-scoped Explain. No installed Copilot
chat, signed release or real-model qualification is implied.

## `.github/` — CI and repository automation

Workflows, reusable actions and review guidance inherit automation responsibility.
Hygiene checks the credential-free documentation site and repository guards;
the main-only Pages publisher uses job-scoped GitHub OIDC, not a repository
secret. Change classification routes bounded native qualification. Required
aggregators fail closed on failed/cancelled shards; hermetic checks are separate
from pinned runtime proof. Native qualification covers explicit scoped
publication/Deploy and ownership-safe update/rollback/RV-churn cases, not
universal lifecycle support. Clone-free proof does not retrieve deleted Tasks,
resend evaluation or imply a separate fresh answer. Release publishing reuses
reviewed artifacts; Homebrew rendering does not automatically push to the tap.

## Root files

Root files own build/dependency metadata, packaging, installation, repository
entry-point documentation and contribution/security guidance; they are not an
exhaustive file table. For example, `go.mod` pins dependencies, `Makefile`
delegates native build/check/preview operations, `install.sh` verifies release
checksums, and `embed.go` declares root-module assets. Package-local declarations
remain authoritative too. `CHANGELOG.md` is release input and historical record;
release preparation combines reviewed fragments without rewriting old releases.
Exclusion/configuration files support the checkout and build, not deployed state.

## `changes/` — per-PR release notes

Release-note fragments inherit this directory's release-input responsibility.
Each pull request owns its Markdown note; the README describes the format.
Release preparation combines pending notes without discarding existing
Unreleased entries or released history.

## Open questions

**Wider cross-runtime lifecycle scope.** Native exact-v0.10.3 Go kagent supports
packaged eval/publication/create-only digest Deploy, not standalone run/chat,
image placement, updates, retirement, diff or rollback. GET-only destination
status is observation, not another lifecycle authorization. Public alpha
contracts are unwired, and retired bundle readers are not a compatibility layer.
