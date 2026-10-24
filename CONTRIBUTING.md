# Contributing to Kaimahi

Kaimahi is an incubation project. Focus contributions on fixes, documentation
corrections, tests, and small capability changes. Start with the organization
[contribution expectations](https://github.com/kaimahi-agents/.github/blob/main/CONTRIBUTING.md).

## Before building something new

For command changes, follow [AGENTS.md](AGENTS.md), the
[command conventions](docs/command-conventions.md) and the
[command alignment record](docs/command-semantics.md), including help,
completion, generated instructions and compatibility updates.

Orka remains the first-class/default platform; Kaimahi is tooling to help people
get agents onto it, not a second control plane. Inspect the relevant upstream
version and existing integrations before adding a component. In the pull request,
explain why configuration, integration or an upstream contribution cannot provide
the requested behavior. Prefer a path that can eventually remove KMX-specific
code. Runtimes and harnesses own execution, enforcement and model access; KMX
must not duplicate them as a model proxy or governance platform.

Keep ownership visible to the user. Generated resource YAML must remain
readable, diffable and usable with the underlying tools. Name the exact mutation
target, retain useful no-write inspection and require scoped consent for
non-local changes; a friendly context name is not proof that a cluster is
disposable.
The [development invariants](docs/development.md#invariants-to-preserve) define
the corresponding fail-closed, credential and evidence rules.

Implement an operation once. Checkout scripts and Make targets may delegate to
the native command, but must not become competing implementations or preserve a
retired component as an undocumented alternative. Distribution and naming remain
subject to [the naming constraints](docs/NAMING.md); claiming a package-manager,
organization or domain namespace requires maintainer approval for that exact
artifact.

Root `kmx create NAME` authors one tool-free native workspace member offline,
Orka by default, exact v0.10.3 Go/linux-amd64 kagent or pinned AgentSessions
chat explicitly. AgentSessions prepare/load/ordinary push and explicit private
literal-loopback `test --development-host FILE` remain independent of deleted
bundle Sessions evaluation/replay: one fresh Session/config/Exec per declared run of each ordered
case, a required `AgentSessionsBinding` serving the packaged model, full-pass
private proof and exact-S evaluated push. Development evaluation reads no cluster
settings and supports no tools, auth, chat or remote-host evaluation. Separate
explicit-reference AgentSessions Deploy creates an immutable Agent at a named
destination and status reads exact registration, not live availability or target
evaluation. The offline --harness-image --bindings --plan render remains available. Receipt diff refuses
AgentSessions proof. Runtime-core assertions/llmJudge and all their test groups
remain retained; native Orka/kagent and development AgentSessions eval supports authored llmJudge with an
explicit endpoint, two threshold-side samples and payload-free declared identity
proof. The fixed modern Chat Completions wire uses max_completion_tokens
(1024) without a temperature override; judge evidence/configuration binds the
request style and old judge receipts require fresh evaluation.
AgentSessions shares the same judge and repeat policy. Exact model and
file-valued instructions are required: terminal create prompts for omitted inputs,
while nonterminal use requires both flags. Core authoring stays strict;
create/push share workspace discovery.
No runtime/model bootstrap belongs in workspace create. Root test registers
completed exact-suite passing metadata in private managed state; --out is optional.
Root push requires that proof by default, or explicit --evaluation FILE without
fallback; --unevaluated retains ordinary publication. Explicit REPO:TAG is preserved,
while bare push requires saved publishRepository and derives only its tag from S.
Private Quickstart/console and extracted-directory transfer keep their contracts.

User `agent.yaml` readers/writers/receipts, `bundle` commands, `create --live`
including previews/wizard, run bundle mode and `--bundles` flags are removed
outright. Old Orka/kagent/AgentSessions bundle receipts are unreadable; no migration
or on-disk/resource deletion. Legacy bundle status and retirement have no
replacement. Root `status --to NAME` is separate GET-only destination inspection,
not suite-source evaluation or a retired-state reader. Preserve public AgentSuite/ToolProvider implementation-bundle graph,
build/pull/extracted-directory transfer and alpha `pkg/kmx` interfaces.

Quickstart/console use native source → full eval → exact publication → reviewed
Deploy Apply/Verify. Quickstart then sends a separate fresh answer Task; console
Ready means placement only. Chat `/lift` and console Deploy only print
`Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …`.
No release GUI/live-copy fallback. Wizard tools/host modes and live tools/inference
editing are retired; host Foundry/Copilot chat controls remain independent.
Standalone AKS up/down and internal App.Lift are independent operations.

Kagent eval requires author-owned bindings, unsecure auth, no trusted-proxy and
one fresh blocking A2A Task per declared case run without retry. Optional KMX
`runs`/`minPasses` supports Orka/kagent and development AgentSessions with unknown-veto thresholds and the
configured deadline independently per run, with summed budget disclosure;
AgentSessions uses fresh Session/config/Exec per run. Draft #497's JSON
case schema does not yet accept those fields. Evidence is full-passing and
runtime-specific. Digest Deploy refuses every occupied kagent primary/child; Orka
permits exact evaluated reuse. No Python/RemoteMCP or kagent standalone chat/run.
Keep privacy/current identity ResourceVersion/Secret metadata bracketing intact;
readiness is not execution. See the [workspace guide](docs/suite-workspace.md).

New to the codebase? Start with the
[`codebase overview`](docs/codebase-overview.md) for architecture and data flows,
then [`development`](docs/development.md) for the build and operational traps.
The [`interface baseline`](docs/interface-baseline.md) pins current commands and
alpha Go boundaries, while the [`repository map`](docs/repository-map.md) says
which parts of the tree are current tooling, historical compatibility data or
test support. Read them before changing something found by grepping.

## Local verification

Run the checks relevant to your change:

```bash
npm --prefix docs ci --ignore-scripts && npm --prefix docs run docs:check
python3 scripts/check-doc-links.py --selftest && python3 scripts/check-doc-links.py
python3 scripts/check-secret-shapes.py --selftest && python3 scripts/check-secret-shapes.py
python3 scripts/check-repository-map.py --selftest && python3 scripts/check-repository-map.py
python3 scripts/check-agentsuite-fixtures.py --selftest && python3 scripts/check-agentsuite-fixtures.py
python3 scripts/check-legacy-runtime.py --selftest && python3 scripts/check-legacy-runtime.py
python3 scripts/check-comment-history.py --selftest && python3 scripts/check-comment-history.py
python3 scripts/test_check_mutations.py
python3 scripts/test_orka_routing.py
go run scripts/ci/orka-boundary.go
python3 scripts/test_orka_callers.py
go run scripts/ci/orka-callers.go
python3 scripts/test_app_tests.py
python3 scripts/check-mutations.py
python3 scripts/check-readme-front-door.py
python3 scripts/check-readme-front-door-test.py
python3 scripts/check-brand-assets.py
python3 scripts/homebrew-formula.py --selftest
bash scripts/check-no-azure-ids-test.sh && bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
formatted=$(gofmt -l cmd internal pkg embed.go embed_test.go) && test -z "$formatted" && go vet ./... && go test ./...
python3 scripts/test_model_fixtures.py -v
python3 scripts/test_kagent_lift.py -v
python3 scripts/test_kagent_lift_shell.py -v
python3 scripts/test_kagent_lift_fixture.py -v
python3 scripts/test_kagent_lift_qualification.py -v
python3 scripts/test_kagent_lift_audit.py -v
python3 scripts/test_kagent_lift_qualify.py -v
python3 scripts/test_kagent_lift_ci.py -v
python3 scripts/test_suite_native.py -v
shellcheck scripts/ci/kagent-lift.sh
actionlint .github/workflows/ci.yml
```

For a browser preview of the maintained Markdown, run `make docs-preview` with
Node.js 22.12 or newer. The root README and `docs/*.md` are the source of truth;
AgentSuite specification links lead to the standalone repository. Generated
HTML and caches are ignored. See the
[documentation site](docs/docs-site.md) for versioning, publication and
removal boundaries.

The checkers above are the ones you can usefully run by hand. CI's hygiene
job runs each checker, each checker's self-test, and a set of inline
meta-checks over CI's own guards; it is the authority on what gates a
merge, not this list.

The Linux and macOS app tests each run in four process-isolated CI shards,
assigned by SHA-256 of each compiled top-level test name. Subtests stay with their parent. The
required `hygiene` check requires every shard to succeed and still runs the other
packages and focused app regressions. Each shard retains the 600-second package
limit and publishes payload-free per-test timings. To run one shard locally,
use `python3 scripts/ci/app-tests.py --shard 0 --shards 4`; ordinary
`go test ./...` still runs the complete suite without sharding.
Use `make test-app` locally to run all four CI shards; an unsharded local run can reach the 600s limit.
The App test harness resolves macOS's temporary root before creating fixtures,
so `/var` aliases do not trip the source loaders' symlink-ancestor refusals.
Each CI timing/failure artifact names its operating system and shard.

The retired-command scanner checks concrete removed commands and assets, not
kagent API/image identifiers or runtime-version support. Its small exact
path/text/reason exceptions cover inert CI controls, notices and refusals;
unused exceptions and unreadable or insufficient text coverage fail the gate.
Conventional tests/testdata, source comments and historical releases are records,
not current instructions. Runtime behavior remains the command tests' responsibility.

The mutation harness checks 44 representative mutants across eleven checkers,
not every failure mode. This bounded set keeps verification practical but
intentionally drops some unique mutation coverage; checker self-tests and
ordinary tree checks still run. Checkers run in parallel with a worker count
that defaults to the CPU count (or one if unavailable). Set
`KMX_MUTATION_JOBS` or pass `--jobs N` to limit it; `--jobs` takes precedence.
Keep the checkout unchanged while the harness runs.

On pull requests, CI runs the mutation harness when `scripts/` changes or
change classification is uncertain. It runs the harness in full on main pushes
and in the nightly hygiene job. Its inexpensive runner/routing tests and other
hygiene checks run regardless of that mutation gate.

Live Orka runtime qualification runs only nightly and on demand through
`orka-nightly.yml` and `runtime-qualification.yml`, never on pull requests or
pushes. The release workflow separately qualifies the shipped inner loop.
Quickstart and stock Helm schedules and guards are unchanged; kagent uses the
separate hermetic/live policy below.

The `safe_*` fields in `scripts/ci/orka-routing.json` retain the existing
init/caller guard selections; they do not route runtime qualification. The JSON
policy and generated `scripts/ci/orka-callers.json` manifest remain inputs to
mandatory hygiene checks on every PR. These static guards do not prove absence
of arbitrary runtime effects; integration detection relies on the full nightly.
Hygiene checks the guard-selected production Go files and App test files for `init()`
functions and active global initializers, across build tags. App test initializers
also run in the live-test binary, even when `-run` selects another test. Only
literal `errors.New` and `regexp.MustCompile` setup with verified imports, and
passive same-file pointer-to-nil type assertions, are permitted.

The separate typed caller guard compares source references with the audited
`scripts/ci/orka-callers.json` manifest. It catches a selected path calling a new
unselected object, a new caller of selected code, and new calling declarations
or closures even in a file already on the boundary. Multi-value initializer
expressions get separate identities. Named imports in analyzed safe files are
also pinned, including standard packages whose initialization runs even when a
preview handler is never called. Static graph walks also
catch selected-to-selected changes that extend an existing boundary reference in either
direction. Methods, function values, fields and same-package private references
are resolved with Go types, not names guessed from source text.

The guard targets the qualifier's Linux/amd64 build without race/custom tags,
includes App tests linked into that binary, and excludes other packages' tests.
It compiles export metadata but never executes initializers or tests. Missing,
stale or malformed manifests, unresolved definitions, unsupported owned CGO,
source-file aliases and nested modules fail closed. Owned source containing
`//line` or `/*line` directives is rejected so adjusted positions cannot relabel
callers or imported definitions as another routing path. Existing init checks still
inspect all build tags. This is a conservative source-reference tripwire, not a
proof of dynamic dispatch, value flow or arbitrary Go effects, and not a sandbox.

For an intentional boundary change, inspect the output of
`go run scripts/ci/orka-callers.go --audit` and review the affected callers before
updating the manifest;
hygiene never refreshes it automatically. Remove obsolete entries too.
Both boundary helpers
have dedicated negative/positive fixtures rather than top-level checker mutation
discovery. Expanding the guard-only selections requires reviewed evidence and
boundary tests.

One Go module contains the root CLI (`cmd/kmx`, `internal/kmx`), concrete
read-only suite member recipe view (`pkg/suiteview`) and alpha application
interfaces (`pkg/kmx`). Portable AgentSuite validation is a separate dependency,
`github.com/agent-primitives/suite`, pinned to published `v0.7.0` in `go.mod`
with verified module sums. Import its focused `core`, `oci`, `evidence` and
`experimental/flow` packages, not the deprecated root. Use the `suitecore`,
`suiteoci`, `suiteevidence` and `suiteflow` aliases described in the
[development guide](docs/development.md#agentsuite-dependency-and-consumer-fixtures).
Public runtime-profile format acceptance enables offline packaging/selection, not
qualified image execution; see [runtime profiles](docs/suite-workspace.md#explicit-runtime-profiles-offline-only).
Root `go test ./...` covers kmx consumers, including first-class Orka paths and
exact-v0.10.3 Go packaged workspace evaluation/evaluated push and create-only
digest Deploy; portable conformance tests run in the suite repository.

Run release verification with `GOWORK=off` against the published dependency
and its checked-in sums. For coordinated local development, select an external
absolute `go.work` containing both checkouts explicitly with `GOWORK`; never
commit a `replace`, a workspace file or manufactured module sums. Consumer tests
use owned snapshots under `testdata/agentsuite`, not module-cache paths. The fixture checker verifies
recorded bytes, modes and inventory offline; `--upstream /path/to/suite` optionally
compares an upstream checkout. Internal incident-analyst fixtures are independent.
Plane commands, administration, the source module (including checked-in SQL),
build helper, embedded assets and plane-only scripts/fixtures are removed.
Historical source remains in Git history. This cleanup deletes no deployed
databases, credentials, caches or resources; legacy AKS ownership records and
conservative teardown support remain.

For cluster changes, use the documented kind path and a dedicated `KIND_CLUSTER`
name to avoid changing another developer's cluster. See
[`docs/getting-started.md`](docs/getting-started.md) for prerequisites.

The checkout-only `bash scripts/ci/kagent-lift.sh bootstrap` prepares pinned
kagent v0.10.3 charts and a keyless model/MCP fixture in the dedicated
`kmx-kagent-lift` kind cluster (Linux amd64, Docker required). It refuses a
preexisting cluster, uses private kubeconfig/discovery cache and Helm state,
clears inherited Helm connection/auth overrides, and pulls charts without caller
Helm/Docker credentials or plugins. Docker daemon access is preserved. It removes
only its owned cluster and temporary work. A failed kind create is ambiguous and
left for inspection. Bootstrap checks readiness and MCP discovery, not suite
Deploy or native A2A execution qualification; it is not an installed CLI installer.
Hygiene runs hermetic bootstrap, qualification, audit, shell, CI-routing and
loopback fixture tests without creating a cluster.

Full runs emit closed `kagent timing:` markers on saved stderr, with a fixed span
(`bootstrap`, `qualify`, `suite-evaluation`, `suite-helpers` or `cleanup`),
start/end boundary, numeric UTC epoch seconds and integer end status.

With no argument (or explicit `qualify`), the shell runs the full qualification.
Build the final Go state first with `go build -o bin/kmx ./cmd/kmx`, then run
`bash scripts/ci/kagent-lift.sh`. It performs raw upstream Tool smoke/direct single A2A MCP proof,
separately from tool-free packaged-suite qualification.
Native A2A execution is independently verified with exactly one MCP tool call,
two model calls and one terminal answer, with no resend. A separate packaged
workspace phase runs two tool-free cases, self-diff and evaluated push with
stale-evidence refusal, then source-free digest Deploy into a fresh namespace with
GET-only planning, ordered creation and occupied repeat refusal without another
native execution. Only payload-free `qualification.json` and
`suite-qualification.json` survive under `${RUNNER_TEMP:-/tmp}/kmx-kagent-lift/`;
raw logs, manifests, credentials, prompts and answers are not artifacts.
Qualifier diagnostics expose only whitelisted subphase and reason codes through
saved shell stderr; bootstrap and tool output remain private.
Every PR and routine main push instead runs `kagent contract — not live qualification`:
hermetic Python bootstrap/qualifier/audit/fixture contracts, suite-lift contracts,
and focused offline Go kagent profile, evaluation, placement, provenance and
ownership/refusal tests. It creates no live cluster and proves no live A2A, image
pull, controller rollout or real suite execution. Existing hygiene and all App
shards remain; their overlapping tests are not removed.

Full `e2e-kagent-lift / qualify` runs in the existing 03:23 UTC scheduled CI,
manual CI and the on-demand `runtime-qualification.yml` workflow. Its unchanged
worker retains live one-shot A2A, packaged two-case evaluation, evaluated push,
source-free digest lift and private helper proof. `e2e-hello-world` always
evaluates the fast check and full-worker outcome; it permits full-proof skips
only on PRs or ordinary main pushes, never a failed, cancelled or missing proof.
The legacy classifier `kagent_changed` output no longer controls qualification.
Tom monitors scheduled CI failures; no kagent issue reporter is added. Integration
failures not caught by hermetic PR coverage may wait until nightly (normally up
to 24 hours). Before a runtime-related tag, run `runtime-qualification.yml` on
that exact ref and inspect both workers' `Qualify the exact caller commit` steps.
See [release qualification](docs/releases.md#release-qualification) for commands.
Release tags check the binary, plugin and local previews on embedded Sessions;
they do not certify cluster runtimes.

The read-only `orka-runtime.yml` worker runs the complete raw Orka Tool/Task,
model-answer and native suite proof. Its CI check appears as
`qualify / qualify` in the nightly; required contexts stay `hygiene` and
`e2e-hello-world`. Use the nightly or on-demand runtime qualification workflow
for fresh full proof; ordinary CI does not call the Orka worker.
Release builds and dry runs do not invoke the cluster runtime workers.
The worker has no internal path/Markdown shortcuts. See [CI qualification](docs/development.md#what-ci-proves).

Inside its two owned contexts the command is
`python3 scripts/ci/suite-native.py kind-kmx-orka kind-kmx-orka-promote "$RUNNER_TEMP/kmx-suite-native"`.
The checkout-only qualifier requires the
shard's exclusive cluster-creation flag and a matching `KIND_CLUSTER`, refuses
existing work/model fixtures, and uses a fresh private KMX home. It adds the pinned
CPU AIKit model inside that owned cluster, warms it, then attempts two independent
native full-workspace evaluations without retry. Historical API UID/spec/input snapshots,
exact registry/layout transfer, verified
candidate root/child absence, source-withheld explicit promotion and no-effect
refusal audits gate its metadata artifacts. Source answer/verdict/judge metadata
is product-observed after cleanup, not re-fetched from deleted Tasks. Only passing
metadata and scalar `qualification.json` are uploaded as qualification evidence;
raw captures are private. Separate `${RUNNER_TEMP}/native-suite-timings.json`
records closed phase codes, monotonic elapsed seconds and scalar outcomes, and is
uploaded even on qualification failure. Candidate-cleanup timing measures independent
absence checks; deletion stays inside evaluation. Abrupt process termination may
leave only the last closed phases with a `running` outcome, never passing evidence.
Existing cluster deletion owns all runtime/model cleanup.
The shard also runs `TestReleaseUpdatePinnedRV` and `TestReleaseStatePinnedCAS`
against the pinned controller after native qualification. The native qualifier
also uses its two real passing digests for public CLI update, original-evidence
rollback, forward update and status-only dependency-RV no-op. Stable UIDs, fresh
mutation IDs, shared completion and unchanged Task inventory gate the result.
Workspace stale-skip and known-partial cases retain focused Go coverage; do not
claim those are live-qualified without separate evidence. A full private updater
round trip remains available as a separate opt-in test.
The native-only
inventory has two text cases plus one open-ended rubric case, with exactly two
judge samples from the same pinned AIKit workload over an owned keyless loopback
forward. Independent receipt checks bind declared model/version, policy, threshold,
endpoint configuration digest and both scores; they do not attest model identity.
No job, case or subprocess timeout is increased. The existing 660s outer limit
bounds three sequential cases; it does not allow three full 5m waits. A successful
run qualifies this fixed three-case native workspace and explicit-promoted Deploy
into absent canonical pairs, including a second exclusively owned kind cluster.
The destination has its own Provider/Secret in orka-system, sharing the same
source AIKit workload through a fixture-only private-network NodePort, never a
host mapping or second model workload. GET-only planning, actual guarded
Apply/Verify, independent destination readback, a separate fresh Task and no-effect
model-mismatch refusal are checked. Source evidence is not destination evaluation;
partial outcomes remain unqualified.
Harness tests alone are not evidence of real execution.

## Comments

Comments describe what the code does and why, as it works now. Keep them tied
to behavior or constraints that remain true. Put what changed, which effort
made the change, and what it replaced in the commit message, pull request
description or release-note fragment instead.

## Pull requests

- Keep each pull request focused on a user problem.
- List exact verification commands and results.
- Label behavior as continuously tested, demonstrated once, schema-valid,
  proposed, or unbuilt.
- Update the capability documentation and status language with the code.
- Never include API keys, tokens, private endpoints, tenant/subscription IDs,
  registry names, cluster addresses, or unsanitized user data.

Every change lands through a pull request with required checks green.

### Release-note fragments

Add one `changes/<PR>.md` fragment rather than editing `CHANGELOG.md`'s shared
Unreleased section. Until the PR number is known, use a unique flat lowercase
slug, replacing branch-name `/` separators with `-` or choosing another valid name. See [the fragment format](changes/README.md). Keep existing Unreleased
entries and released history intact; maintainers combine notes at release time.

Documentation/test-only changes are automatically exempt. This means `docs/**`,
Markdown files, Go `_test.go` files, `testdata/` trees, and the test scripts named
`scripts/test_*.py` or `scripts/*-test.py` / `scripts/*-test.sh`. Other changes,
including CI, tooling, dependency and shipped-asset changes, need a new valid
fragment. Do not rename, edit or delete another pending fragment in a product
PR; the docs-only release-preparation PR removes the notes it includes.

Validate pending notes locally:

```bash
python3 scripts/check-changelog-fragments.py
python3 scripts/check-changelog-fragments.py --selftest
python3 scripts/test_changelog_ci.py
```

After committing, check the complete authored branch range, not just its last
commit:

```bash
base=$(git merge-base origin/main HEAD)
python3 scripts/check-changelog-fragments.py --base "$base" --head HEAD
```

CI uses the synthetic PR merge's actual first parent instead of a stale event
base. Exemption skips only the requirement for a new note, not validation of
pending notes. Rendering and release preparation are described in
[Cutting a release](docs/releases.md#cutting-a-release).

### Repository-map changes

Files inherit the responsibility and classification of their documented
directory. Adding a file below an existing responsibility does not require a
map edit. Update [the map](docs/repository-map.md) only when adding, removing or
moving a top-level responsibility directory: a root area or an immediate child
of `cmd/`, `internal/` or `pkg/`. Behavior changes go in the package's Go doc
comment (`doc.go` or the existing package comment), and every `internal/kmx/*`
package needs one. Do not add file totals or exhaustive file/caller inventories.
The checker enforces named paths, top-level directory coverage, package doc
comments and the retired-module boundary; editorial classifications remain
reviewable.

```bash
python3 scripts/check-repository-map.py --selftest
python3 scripts/check-repository-map.py
```
