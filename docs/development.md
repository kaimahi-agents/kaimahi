# Development

For work **on** Kaimahi; contribution/PR expectations are in
[CONTRIBUTING.md](../CONTRIBUTING.md). For use, start with
[getting started](getting-started.md) and [Orka](orka.md).

New contributors should read the [codebase overview](codebase-overview.md) for
package boundaries and data flows, then the [interface baseline](interface-baseline.md)
before changing commands or public contracts. The [repository map](repository-map.md)
describes directory responsibilities and boundaries between installed tooling,
checkout support, historical records and test scaffolding.

## Direction and current implementation

**Orka remains the first-class/default platform.** Kaimahi tooling helps
agents get onto it. Default `kmx create <name>` authors one native workspace
member offline, requiring an exact model and an instructions file; Orka is the
default, while `--runtime kagent` selects the exact v0.10.3 Go profile.
Create and push share workspace discovery; exact `--workspace` initializes
without prompting. Live create/previews/wizard and user bundle commands,
readers/writers/receipts are removed, not converted. Old receipts are unreadable;
no files/resources are deleted. Quickstart/console compose native authoring,
full eval, exact publication and reviewed Deploy. Quickstart verifies placement
before a separate fresh answer Task; console Ready means placement only.
Kagent uses exact v0.10.3 Go/linux-amd64 packaged eval/evaluated push/create-only
Deploy; no installer, discovery, standalone chat/run or suite status/retirement.
KMX installs Orka's chart in harness-v2 controller mode, not the upstream harness
ABI. Runtimes and harnesses own execution, enforcement and model access. Orka
already supplies OTLP telemetry using GenAI conventions; extend that upstream
capability instead of adding a parallel KMX exporter.

Plane CLI administration, model overlays, credential exchange and workload
migration are removed, together with the nested source module, image-build
helper, embedded plane/observability assets and plane-only scripts/fixtures.
Historical source, including checked-in SQL migrations, remains in Git history,
not this checkout. No databases, credentials, caches or deployed resources are
deleted. Cloud ownership/teardown records and their tests remain. The old
gateway, approvals/grants, workflow runner and connector fixtures remain retired.

Packaged native suites have runtime-specific private App preparation seams. Orka
observes an explicit target, Ready Provider/Secret binding and pinned runtime,
then validates one exact unmarked render from `workspace.Load`. Private canonical
placement retains same-owner create/update; public Orka eval uses fresh S/run
candidates, never adoption/update/reuse. Kagent snapshots
author-owned `KagentBindings`, verifies the pinned runtime and creates new
ModelConfig/Agent resources only; occupied unrecorded or changed targets refuse,
never update or adopt. Secret reads project metadata and key presence, never
values. Root workspace evaluation consumes the matching seam. Verification binds
current ownership, UID, generation, spec and readiness to live snapshots. A late
failure can leave resources for a later explicit invocation.
Native ownership annotations, including the full candidate run ID, reserve objects against legacy adoption, reuse,
copying, bundle authorization and retirement, even with malformed values. These
fences do not forbid direct `run --agent` or supported Orka chat on suite-owned
live Agents. Invocation by context/namespace/name is not workspace-local execution,
fresh suite evaluation or adoption/update/copy/retirement authority. Fake readiness
is not model execution or cluster qualification.

Private full-passing receipts project Task/answer and ordered case/assertion
identities only, never prompts, answers, operands, reasons or diagnostics. Their
constructor binds observed raw-case/input frames and successful placement to a
verified package; execution brackets observations with native verification. The private export preflight retains one private anchored directory
outside authored workspace/cache trees and publishes complete bytes with an
exclusive hard link, not history or replacement. The private receipt-file push
consumer parses before cache writes, compares exact S, and validates packaged
subject/render/inventory coherence before provenance or transfer. Source
fingerprint is authored spelling, not a durable publication lock. These unsigned
observations prove neither authenticity nor cross-object atomicity. Root `test`
prepares workspace/inventory/export offline before configuration, then consents
once to native pair placement and all ordered one-shot Tasks. Public Orka eval
also covers bounded UID/RV cleanup and verified owner-UID child closure before
full-pass export. Startup reports never delete or block a fresh eval. Explicit
`test cleanup [run-id]` lists or confirms a frozen run/UID snapshot, refusing
unattributable orphans; it creates no execution or passing-evidence authority.
No cleanup journal, lease or later-publication blockade is implemented. Root test
registers completed proof in private immutable per-workspace/per-S state, with optional
standalone --out. Root push selects the latest exact-S pass; --evaluation FILE
explicitly overrides lookup without fallback and --unevaluated explicitly retains
ordinary publication. Per-S registration capacity is checked before effects, without
blocking push of existing proof. OS-held workspace exclusion releases on process exit;
visible complete records survive post-link persistence/reporting errors with disclosure.
Bundle evaluation is removed. Hermetic tests cover
these paths. The existing Orka runtime shard now invokes the bounded
[suite qualifier](../scripts/ci/suite-native.py) after its raw upstream Tool/Task and model-answer proof.
It uses the [fixed CPU AIKit workload](../scripts/ci/native-suite-model.yaml),
existing model configuration and installer Secret reference in the same owned
cluster. One full three-case evaluation has historical API identity/spec/input
snapshots and independent UID/RV cleanup/child-absence checks. Source verdicts,
answer hashes and judge scores are product-observed after cleanup, not re-fetched
from deleted Tasks. Exact-S layout/registry transfer and source-withheld explicit
promotion qualify absent canonical destinations and fresh destination answers.
Private captures are not artifacts; only passing metadata and a scalar report
are uploaded. This fixed scenario does not qualify every creation, promotion,
orphan-recovery or partial-outcome path. Hermetic tests are not live execution. The kagent qualifier proves source-free Deploy into a
fresh namespace and occupied repeat refusal without additional native execution.
Standalone packaged evaluation is unavailable.

## Repository layout

Consult the [repository map](repository-map.md) for directory ownership and
classification. Descendants inherit an owned directory's role; ordinary files,
nested fixtures and new packages below `internal/kmx/` need no map entry. Only a
new root area or immediate child of `cmd/`, `internal/` or `pkg/` does. Package
behavior lives in the Go package doc comment.

| Path | Responsibility |
|---|---|
| `cmd/kmx/`, `internal/kmx/`, `embed.go` | CLI command tree, Orka and version-bounded kagent lifecycle adapter, portable authoring, shared plumbing and embedded manifests |
| `pkg/suiteview/` | concrete read-only selected-member projection from verified local AgentSuite OCI layouts/archives; no runtime/evidence authority |
| `pkg/kmx/` | experimental public setup-only environment plus suite/deployment contracts, not wired into the installed CLI |
| `k8s/ollama.yaml`, `k8s/orka-k8s-tool.yaml` | native local model and read-only Kubernetes Tool |
| `scripts/`, `Makefile` | checks, packaging and repository helpers |
| `docs/package.json`, `docs/package-lock.json`, `docs/.vitepress/` | private documentation toolchain, navigation/theme and generated-site tests; published from `main` by the Pages workflow |
| `.github/workflows/` | verification jobs and docs-only routing |

The root CLI is the only Go module. Plain `make` builds `bin/kmx` only. The
supported kagent surface is exact-v0.10.3 Go/linux-amd64 packaged workspace
evaluation/evaluated push/create-only digest Deploy through private App seams,
not a user-bundle conversion layer. Public alpha ports remain unwired.

For a narrative architecture view, including workspace preparation, native
evaluation, OCI publication and user-bundle retirement, use the
[codebase overview](codebase-overview.md). For the current visible and hidden
command tree plus `pkg/kmx` method sets, use the
[interface baseline](interface-baseline.md).

## Build and verify

Run from the repository root. Portable AgentSuite is a separate pinned Go module;
KMX's public packages are part of this root module and the commands below include
them without a nested workspace.
CI's Go hygiene checks remain unguarded, including on documentation-only changes;
only the live and clone-free operation steps use documentation-only guards.
Formatting checks must test `gofmt` output because
`gofmt -l` alone exits successfully even when it lists files.

Tests that read or write KMX state must set `KMX_HOME` to `t.TempDir()`. Do not
use `XDG_CONFIG_HOME` as the test boundary: Go uses it for the native Linux
config location, while macOS uses `~/Library/Application Support`. `KMX_HOME`
is the repository's explicit cross-platform isolation contract.

```bash
formatted=$(gofmt -l cmd internal pkg embed.go embed_test.go) && test -z "$formatted" && go vet ./... && go build ./... && go test ./...
npm --prefix docs ci --ignore-scripts && npm --prefix docs run docs:check
python3 scripts/check-doc-links.py --selftest
python3 scripts/check-doc-links.py
python3 scripts/check-agentsuite-fixtures.py --selftest
python3 scripts/check-agentsuite-fixtures.py
python3 scripts/check-secret-shapes.py --selftest
python3 scripts/check-secret-shapes.py
bash scripts/check-no-azure-ids-test.sh
bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
python3 scripts/test_model_fixtures.py -v
```

### AgentSuite dependency and consumer fixtures

Import only the focused packages a file needs from the separate module:

```go
import (
    suitecore "github.com/agent-primitives/suite/core"
    suiteevidence "github.com/agent-primitives/suite/evidence"
    suiteflow "github.com/agent-primitives/suite/experimental/flow"
    suiteoci "github.com/agent-primitives/suite/oci"
)
```

Use `suitecore` for portable models and JSON/identity helpers, `suiteoci` for
artifact validation and diff, `suiteevidence` for claims and both verifiers,
and `suiteflow` for explicit stored-flow types and validation. The standalone
`policy` import is unchanged. Do not import the deprecated module root.
The internal builder keeps `kmxsuite` and its ORAS adapter keeps `suiteoras`.
`go.mod` pins the published release `v0.7.0`, with its sums in `go.sum`.
Use the [complete release-pinned schema bundle](docs-site.md#current-agentsuite-schema-selection)
for editors and external validators; the historical hosted schemas are not the
current consumer contract.
Verify releases with `GOWORK=off`. For coordinated local development, select an
external absolute `go.work` containing both modules with
`GOWORK=/absolute/path/to/go.work`; do not commit workspace files, `replace`
directives or invented sums.

`testdata/agentsuite` holds owned consumer snapshots with frozen source and
per-file SHA256/mode provenance. The offline fixture checker above protects those
bytes; add `--upstream /absolute/path/to/suite` to compare a standalone checkout.
Portable conformance belongs upstream; internal incident-analyst fixtures remain
kmx-owned and separate.

### Documentation preview

Node.js 22.12 or newer is required only for the browser documentation tooling.
`make docs-preview` installs the exact lockfile and serves on `127.0.0.1`;
`make docs-build` performs the same build and generated-site assertions as CI.
The preview renders the root README and `docs/*.md` directly; specification
links lead to the [standalone AgentSuite contract](https://github.com/agent-primitives/suite/blob/main/spec.md).
Do not copy guide content into
VitePress configuration or commit `docs/.vitepress/dist`, caches, or
`node_modules`.

The current framework pin remains explicitly reviewable. Organization GitHub
Pages publishes the same build from `main`; pull requests only build and test it.
No repository publication secret is configured. See [documentation site](docs-site.md).

### App test timing

`internal/kmx/app` fixtures inject invocation-local polling intervals and phase
deadlines. Keep setup budgets intact and shorten only the wait under test;
assert that the intended readiness, admission, or result boundary was reached.
An unset timing hook must retain the production intervals and deadlines.

The fake kubectl executable lives in `internal/kmx/app/testdata/kubectl`. App
fixtures default to compiling this lightweight executable once per package run
and running it with the fixture's environment and call log. A `go test -race`
App run also compiles the helper with `-race`; its build directory is removed
when the package run finishes. Keep helper imports independent of App and UI.

The shared stateful protocol lives in `internal/kmx/app/kubectlfake` for explicitly
selected test captures and executable-boundary fixtures. It has no App/UI imports
and is not reachable from installed commands. Normal argv/environment/stdin,
guard/deadline/error boundaries remain observable. Retired bundle tests are not
proof of retained bundle commands/readers; surviving helper inventory follows the
Go retirement pass.

The workflow's hygiene job is authoritative for the full checker/self-test/
mutation set; do not replace it with this focused list. The retained native
Orka Tool fixture tests exercise actual HTTP requests and health/refusal
responses without a cluster. No plane-module or plane Postgres service job
remains.
[CI configuration](../.github/workflows/ci.yml) also checks its own guards and
aggregator membership. The retired-command scanner rejects concrete removed
commands/assets, not kagent vocabulary or version claims. It uses bounded literal
command tokenization and exact inert exceptions; production command tests own
runtime selection and supported-operation validation.

### Release retention boundary

Complete fresh Orka Deploy creates retain a shared UID-bound baseline and a new
explicit private ownership proof; reused/mixed placements never enroll. Private
observation slots `intent.json` through `3.json` alone remain non-authoritative.
Pre-reserved `4.json` binds an acknowledged shared completion, but is only a
candidate until the final exclusive `committed.json` link succeeds. Readers require
both bounded private files with byte-identical content; all unmarked proofs,
including unreleased #573 test baselines, refuse without migration or backfill.
Publication checks and output closes precede the commit point; the capacity lock
remains held through it. Post-commit handle cleanup cannot revoke commitment,
and crash loss of the final directory entry conservatively blocks. Deterministic
post-link sync-failure tests exercise real create/update Apply and next-prepare
refusal; retain the separate lost-shared-response regressions. Unknown writes/checkpoints cannot be
resolved by re-reading live objects or by an automatic resend/takeover. Only a
live writer whose object writes have ceased may persist a known partial and
release its reservation. Pending/partial shared proof never rolls; completed
history rolls at ten entries within 64 KiB. See
[retention and operator recovery](suite-workspace.md#release-retention-and-operator-recovery).
The public `deploy --update` route uses the retained Orka backend. Tests must
distinguish same-digest no-write, fresh-RV-without-new-authority, old/new
known partials, a separate client/private home, and lost final acknowledgments.
Pinned API characterization is necessary but does not substitute for complete
Deploy/retention qualification or prove a model answer. The existing native Python
qualifier now independently evaluates and publishes v2, then drives the public
CLI v1→v2→original-evidence v1→v2 and status-only dependency-RV no-op against its
owned pair. It checks current status, stable UIDs, fresh mutation IDs, original
evidence and no Task/evaluation/publication during digest updates. Offline audit
tests do not establish live qualification or job headroom. Workspace historical
skip invalidation and separate-client/known-partial cases also retain focused Go
coverage; do not describe those as live-qualified without corresponding evidence.
Keep existing CI budgets.

### Local loop

```bash
make
export KIND_CLUSTER=dev-local
export KUBE_CTX=kind-dev-local
bin/kmx quickstart
bin/kmx orka status
# Quickstart verifies placement before its separate fresh answer Task.
bin/kmx local down
```

A bare `bin/kmx local up` installs the runtime without an Agent: kind, Ollama, the
model and pinned Orka with its keyless `local` Provider. Author source with
`bin/kmx create NAME --workspace DIR --model MODEL --instructions FILE`, then
full eval, evaluated push and digest Deploy with private evidence; or use guided
`bin/kmx quickstart --interactive`. `orka status` is table-only runtime state;
root `status --to NAME` independently observes destination Agent placement and
unsigned Deploy provenance with GETs only. It does not read suite member content,
private history or Secrets, and does not imply retirement.

KMX never installs/upgrades kagent. Its exact-v0.10.3 Go/linux-amd64 packaged
profile uses author-owned `--bindings` for root test/Deploy, without runtime/model
overrides. Profiles never fall back between installed runtimes. Live create and
all bundle lifecycle/readers/receipts are removed.

Pick a distinct cluster name and explicit context. Keep
`CONTAINER_ENGINE=podman` consistent if selected; Docker and Podman inventories
are separate. `kmx local down` destroys all data in the named local cluster,
including any historical database still present; export needed data first.
Cloud cleanup has different [ownership rules](aks.md#teardown).

### What CI proves

The workflow emits `hygiene` and `e2e-hello-world` merge gates. The latter
fail-closed aggregates four retained shards: `e2e-kagent-lift`,
`e2e-orka-runtime`, `e2e-orka-helm` and `e2e-quickstart`, plus successful
classification and the independent `kagent-contract` hermetic check. Only intentional skips are accepted, never failed/cancelled
shards. Orka runtime proof may skip only an explicitly safe PR or a main push;
unknown changes and events default to full proof. Required contexts remain
`hygiene` and `e2e-hello-world`, without a ruleset change.

The read-only [Orka worker](../.github/workflows/orka-runtime.yml) runs the same
complete proof when invoked, including native evaluation, transfer, promotion,
and pinned-update checks. It has no internal Markdown shortcut. Unrecognized
Markdown and live instruction fixtures require qualification; root policy docs
not explicitly listed also run. Its raw check is `e2e-orka-runtime / qualify`.
Quickstart, stock Helm and clone-free qualification keep their existing routing;
not every Orka-bearing job is moved out of PR CI.

[Orka nightly](../.github/workflows/orka-nightly.yml) runs the full main proof
at 03:23 UTC and supports manual qualification after the workflow lands. That
schedule shares the existing CI cron but owns a separate full worker. Its summary
is red for failure, cancellation or missing qualification. Scheduled main failures
maintain one workflow-owned issue assigned to `gambtho`, containing only run link,
commit and safe status. Current success closes it; older success cannot close a
newer failure. Manual branch qualification does not alter main health.

Moving main-push runtime proof to nightly trades immediate post-merge detection
for detection on the next nightly (normally within 24 hours) or before release.
Individually passing PRs can still interact after merging. Check Actions when the
last completed main nightly is older than 30 hours: an issue reporter cannot
report a schedule that never started. The first scheduled run remains an
operational check after merge, not evidence supplied by local fixtures.

### Fast kagent PR coverage and full live qualification

All PRs, including runtime-relevant and unknown diffs, run **kagent contract —
not live qualification**. The fast check executes the existing hermetic Python
bootstrap, pinned-profile, fixture request-count, qualifier refusal and mutation
audit contracts plus suite-lift contract tests. Focused offline Go tests cover
kagent profile/version fences, evaluation parsing, ownership and drift,
placement/provenance and refusal paths. These use local fakes/fixtures, not a
live kagent cluster. All App shards and hygiene checks remain enabled.

Live upstream MCP/A2A (one tool call, two model calls and one terminal answer),
full packaged evaluation, evaluated push, source-free digest lift and private
placement helpers run through the unchanged read-only
[kagent worker](../.github/workflows/kagent-runtime.yml). It runs once in the
existing 03:23 UTC scheduled CI, on manual CI and before every release/dry run.
PRs and routine main pushes intentionally skip only that full worker, regardless
of `kagent_changed`; the fast check must still pass. `e2e-hello-world` always
evaluates both outcomes and refuses fast-check skips, failures, cancellations,
missing results or unintended full-worker skips. Required contexts stay the same.

The fast check does **not** qualify live A2A execution, upstream image/bootstrap
compatibility, readiness, actual suite execution or cluster cleanup. Moving these
checks delays detection until nightly (normally within 24 hours) or before release.
Tom watches scheduled CI in Actions; there is no new kagent issue reporter or
promise that an undispatched schedule can report itself. Manual CI remains the
way to request immediate full qualification. Nothing changes runtime pins,
assertions, retries or budgets, and no Helm/Quickstart cluster or image cache is shared.

Every release/dry run requires fresh full **Orka and kagent** worker success at
the exact built commit; no earlier nightly success is reused. The worker is read-only; only the
scheduled-main issue reporter has issue-write permission. Reporter errors remain
visible as workflow failures. Use `gh pr checks --required` for required contexts;
a green gate does not establish human approval or resolved review threads.

The v0.10.2 live-create shard `e2e-kagent-create` and clusterless bundle recorded
Sessions/judge/reference-verify shard `e2e-eval-loop` are removed, along with the
reusable eval action and its runner/tests. AIKit model YAML/warmup and native
instruction/two-case fixtures remain native-qualifier assets, not an old bundle
runner. `state-paths-macos` retains its independent state routing checks.

Orka runtime and stock Helm scenarios use raw upstream fixtures and independent
`run --agent` Task/model-answer proofs. The Orka suite qualifier separately
covers fresh candidate placement, full three-case eval, verified root/child cleanup,
evaluated push and source-withheld explicit promotion, with private captures and
metadata-only artifacts. Registry/layout transfer and absent-pair promotion
include fresh destination answers with UID/count attribution. Promotion retains source-only
evidence, GET-only plans, reviewed Apply/Verify, full placement readback and a
no-effect model-mismatch refusal. Its offline reference uses the shared native
scaffold, not removed live-create flags. Evaluation is not retried; broader
promotion, absent creation and partial paths remain unqualified.
Quickstart independently verifies placement before its separate fresh answer Task.

The Orka runtime shard also runs `TestReleaseUpdatePinnedRV` and
`TestReleaseStatePinnedCAS` with `KMX_RELEASE_UPDATE_LIVE=1` and
`KMX_LIVE_KIND_CONTEXT=kind-kmx-orka`. These opt-in tests require the exact
pinned chart/controller and a verified loopback API. They characterize status-only
resourceVersion churn, isolated generation/spec preconditions, current-generation
readiness and a shared ConfigMap reservation's UID/RV/data CAS. A synthetic
local-only prerequisite and unusable inference endpoint keep these checks free of
model execution; they do not authorize or enable user-facing Deploy updates.
For local qualification, use a separately owned `kind-kmx-update-proof` cluster
with the pinned installer, never a demo or shared destination. The tests leave
freshly named fixtures for the owned cluster's teardown; they never delete an
occupied record or take over a reservation.

The checkout-only [kagent qualification shell](../scripts/ci/kagent-lift.sh)
retains exact v0.10.3 bootstrap, raw upstream Tool smoke and a direct single A2A
MCP proof, separately from tool-free packaged-suite two-case evaluation,
evaluated push and source-withheld digest Deploy into a fresh namespace, with
occupied-repeat refusal. No legacy bundle receipts/status/judge proof is claimed.
A2A proof requires one Tool call, two model calls and one terminal answer without
resend. Suite Deploy sends no native execution calls. Python/RemoteMCP, managed
clusters and hosted providers remain unqualified.

Build the final Go state with `go build -o bin/kmx ./cmd/kmx`, then run
`bash scripts/ci/kagent-lift.sh` on Linux amd64 with Docker. Explicit `bootstrap`
stops after readiness/discovery, not execution qualification. The shell refuses
an existing cluster and retains private kubeconfig/cache/Helm/registry state.
Inherited Helm auth/connection overrides are cleared. Only owned temporary work
and a successfully created owned cluster are cleaned; ambiguous kind creation
is left for inspection. Raw payloads/logs/manifests/credentials are not artifacts.
Hygiene tests these boundaries without a cluster. Final retirement Go build/live
runs are required before claiming runtime qualification; docs/hermetic tests
alone are not execution evidence.

Clone-free main/manual CI authors an exact-answer assertion inside full eval,
checks completed candidate cleanup and independent Task absence, then performs
evaluated layout publication and explicit-promoted digest Deploy. It neither reads
a deleted evaluation Task nor resends it, and claims no separate answer Task.
No Azure credentials belong in fork-exposed CI. A docs-only skip is not a live rerun.

### CI registry mirrors

CI's Docker-using jobs configure the host daemon with
[`scripts/ci/registry-mirrors.py`](../scripts/ci/registry-mirrors.py) and report
pull endpoint evidence. Setup merges `registry-mirrors=["https://mirror.gcr.io"]`
and `debug=true` into existing daemon configuration, preserving unrelated
settings, engine version and running containers. It reloads Docker with SIGHUP
rather than restarting or replacing it. Only Docker Hub routing changes:
references, digest pins, manifests and source stay unchanged; GHCR and ECR are
unaffected.

Component/direct-kind jobs configure nodes after cluster creation and before
workloads. The pinned kind node uses containerd 2.3.4's default
`/etc/containerd/certs.d` path: `docker.io/hosts.toml` keeps
`server = "https://registry-1.docker.io"` as the origin fallback and adds
`https://mirror.gcr.io` with `pull` and `resolve` capabilities. Setup enables
containerd debug logging for the endpoint report.

Compound quickstart and clone-free journeys use a CI-only Docker-named symlink
wrapper. It forwards Docker calls unchanged, then hooks a successful kind-node
creation identified by reserved kind cluster and node-role labels, configuring
hosts and debug logging before kind returns. This keeps full `kmx local up`
and fetched-kind proof intact without changing KMX or kind. Clone-free CI
fetches only this helper at the workflow SHA into `RUNNER_TEMP`; it does not check out the repository.

Reports print only successful HTTP 200 endpoint responses: endpoint, image and
manifest/blob category, without headers, query strings or payloads. A cache hit
is not mirror-routing evidence; successful Docker Hub fallback is identified
separately. Hygiene runs
[`scripts/test_registry_mirrors.py`](../scripts/test_registry_mirrors.py) to test
configuration merging, wrapper/node selection and evidence filtering.

Check coverage against existing image references and digests, including
`kindest/node` and Ollama. A cache miss or mirror outage can fall back to Docker
Hub; reports do not call that a mirror success. Existing Postgres ECR pins stay
unchanged. Model-weight downloads from `registry.ollama.ai` are separate from
OCI pulls and do not use these mirrors.

## Operation contexts

Operational Cobra adapters use `operationApplication(cmd)` to bind
`cmd.Context()` to a Runner copy on the invocation's App. Subprocesses,
network requests and waits using `App.operationContext()` inherit the caller's
cancellation, deadline and values without mutating the original shared Runner.
The invocation's App identity and recovery metadata remain unchanged.
`targets --detect` uses the same adapter; offline capability listing remains
configuration-free. Help and completion do not construct an operational App.

Keep narrower operation/case deadlines and explicitly scoped cleanup contexts.
This binding does not add a global signal handler, change command-specific
signal ownership, or detach work from cancellation.

## Invariants to preserve

1. Use platform capabilities rather than rebuilding them. Inspect the relevant
   upstream version first; prefer configuration, integration or an upstream
   contribution. Legacy-shaped helpers do not authorize expanding Kaimahi into
   another agent runtime.
2. Fail closed on missing proof: HTML with 200 is not a valid endpoint answer,
   unreadable or ambiguous state is not absent, and scanner failure is not a
   clean scan. Admission and dry-run acceptance are not readiness or execution
   evidence.
3. Keep artifacts reviewable and user-owned. Generated resource YAML remains
   readable, diffable and usable with the underlying tools; KMX must not hide
   resource ownership inside an entry point.
4. Generated files contain Secret references, never values. Keys never enter
   argv, logs, manifests or ConfigMaps; do not add token flags or advise users to
   commit credentials. Native Provider Secrets are provisioned separately; host
   Copilot credentials remain managed by its installed CLI. Preserve each native
   input contract.
5. Billed work must be recorded even when the surrounding operation fails.
   Historical cloud ownership records remain necessary for conservative teardown.
6. Mutations name their exact target and retain context/cloud guards; name and
   loopback server are independent evidence. Confirmation is scoped consent, not
   a general bypass, and a friendly context name does not prove disposability.
7. Repository cleanup does not authorize deleting databases, caches, credentials
   or deployed resources. Historical source remains available in Git history.
8. Keep one implementation of an operation. Scripts and Make targets may
   delegate to the native command but must not maintain a competing path or
   preserve retired connectors as checkout-only alternatives.
9. Metrics labels must not leak tokens or channel/user/request/delivery IDs.
10. State evidence accurately: distinguish unit and fake-service tests,
    continuously exercised cluster paths, one-off cloud measurements, schema
    validation and proposals. Label evidence as continuously tested,
    demonstrated, schema-valid, proposed or unbuilt. A one-off measurement is
    not durable live coverage; successful admission is not downstream
    completion, readiness is not an answer, and an old run is not a current
    availability claim.
11. Optimize for fewer prerequisites, a useful first result, clear recovery and
    less code of our own, not command count or adoption of this repository.

## Troubleshooting traps

- A Provider can exist without resolving a model. Inspect Provider and Agent
  readiness, not only controller pod health; then prove a real Task answer.
- Setup does not adopt or redirect existing applications. Owners must deliberately
  remove or replace obsolete gateway references and external subscriptions.
- A Secret created after an optional mount may need a rollout restart.
- A Service port-forward selects one pod, not every replica. KMX result forwards
  let kubectl choose and retain a free IPv4 loopback port. Explicit `--result-port`
  values need distinct free ports across concurrent checks and never fall back.
- Ambiguous-disconnect retries can repeat effects; read the
  [retry limits](kmx.md#retry-limits) before resending work.
- A bare shell `wait` waits on long-running forwards too; collect worker PIDs.
- Podman machines need checkout mounts for image builds; restarted machines may
  leave kind nodes stopped. The cluster step recovers named nodes and checks API/DNS.
- Python scripts supporting macOS Python 3.9 need postponed annotations before
  PEP 604 annotations. Ignore rules such as `bin/` match at every depth; verify
  tracked membership and beware newly unignored files entering `stash -u`.

Use `kmx orka status`, Agent/Provider/Task conditions and
context-pinned controller logs. Never paste live infrastructure IDs into
evidence: scan shapes and manually redact names too.
