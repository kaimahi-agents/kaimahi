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
literal-loopback `eval --development-host FILE` remain independent of deleted
bundle Sessions evaluation/replay: one fresh Session/config/Exec per declared run of each ordered
case, full-pass private proof and exact-S evaluated push, no cluster settings,
tools, auth, Lift/chat/status or remote lifecycle. Receipt diff refuses
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
No runtime/model bootstrap belongs in workspace create. Root eval registers
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
Lift Apply/Verify. Quickstart then sends a separate fresh answer Task; console
Ready means placement only. Chat `/lift` and console Lift only print
`Lift moved: kmx lift <ref>@<digest> --to-context … --evidence …`.
No release GUI/live-copy fallback. Wizard tools/host modes and live tools/inference
editing are retired; host Foundry/Copilot chat controls remain independent.
Standalone AKS up/down and internal App.Lift are independent operations.

Kagent eval requires author-owned bindings, unsecure auth, no trusted-proxy and
one fresh blocking A2A Task per declared case run without retry. Optional KMX
`runs`/`minPasses` supports Orka/kagent and development AgentSessions with unknown-veto thresholds and the
configured deadline independently per run, with summed budget disclosure;
AgentSessions uses fresh Session/config/Exec per run. Draft #497's JSON
case schema does not yet accept those fields. Evidence is full-passing and
runtime-specific. Digest Lift refuses every occupied kagent primary/child; Orka
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
python3 scripts/check-mutations.py
python3 scripts/check-readme-front-door.py
python3 scripts/check-readme-front-door-test.py
python3 scripts/check-brand-assets.py
python3 scripts/homebrew-formula.py --selftest
bash scripts/check-no-azure-ids-test.sh && bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
formatted=$(gofmt -l cmd internal pkg/kmx embed.go embed_test.go) && test -z "$formatted" && go vet ./... && go test ./...
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

One Go module contains the root CLI (`cmd/kmx`, `internal/kmx`) and application
interface (`pkg/kmx`). Portable AgentSuite validation is a separate dependency:
`agentsuite "github.com/agent-primitives/suite"`, pinned to the published minor release
`v0.4.0` in `go.mod` with verified module sums.
Public runtime-profile format acceptance enables offline packaging/selection, not
qualified image execution; see [runtime profiles](docs/suite-workspace.md#explicit-runtime-profiles-offline-only).
Root `go test ./...` covers kmx consumers, including first-class Orka paths and
exact-v0.10.3 Go packaged workspace evaluation/evaluated push and create-only
digest Lift; portable conformance tests run in the suite repository.

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
Lift or native A2A execution qualification; it is not an installed CLI installer.
Hygiene runs hermetic bootstrap, qualification, audit, shell, CI-routing and
loopback fixture tests without creating a cluster.

With no argument (or explicit `qualify`), the shell runs the full qualification.
Build the final Go state first with `go build -o bin/kmx ./cmd/kmx`, then run
`bash scripts/ci/kagent-lift.sh`. It performs raw upstream Tool smoke/direct single A2A MCP proof,
separately from tool-free packaged-suite qualification.
Native A2A execution is independently verified with exactly one MCP tool call,
two model calls and one terminal answer, with no resend. A separate packaged
workspace phase runs two tool-free cases, self-diff and evaluated push with
stale-evidence refusal, then source-free digest Lift into a fresh namespace with
GET-only planning, ordered creation and occupied repeat refusal without another
native execution. Only payload-free `qualification.json` and
`suite-qualification.json` survive under `${RUNNER_TEMP:-/tmp}/kmx-kagent-lift/`;
raw logs, manifests, credentials, prompts and answers are not artifacts.
Qualifier diagnostics expose only whitelisted subphase and reason codes through
saved shell stderr; bootstrap and tool output remain private.
The `e2e-kagent-lift` shard runs for relevant or unknown PR changes and always
on main pushes, nightly and manual runs. Its required aggregator accepts only
intentional skipped outcomes, never failed or cancelled shards.

The existing `e2e-orka-runtime` shard also runs
`python3 scripts/ci/suite-native.py kind-kmx-orka "$RUNNER_TEMP/kmx-suite-native"`
after its raw upstream Orka Tool/Task and model-answer proof. The checkout-only qualifier requires the
shard's exclusive cluster-creation flag and a matching `KIND_CLUSTER`, refuses
existing work/model fixtures, and uses a fresh private KMX home. It adds the pinned
CPU AIKit model inside that owned cluster, warms it, then attempts one native
full-workspace evaluation without retry. Historical API UID/spec/input snapshots,
exact registry/layout transfer, verified
candidate root/child absence, source-withheld explicit promotion and no-effect
refusal audits gate its metadata artifacts. Source answer/verdict/judge metadata
is product-observed after cleanup, not re-fetched from deleted Tasks. Only passing
metadata and scalar `qualification.json` are uploaded; raw captures are private.
Existing cluster deletion owns all runtime/model cleanup. The native-only
inventory has two text cases plus one open-ended rubric case, with exactly two
judge samples from the same pinned AIKit workload over an owned keyless loopback
forward. Independent receipt checks bind declared model/version, policy, threshold,
endpoint configuration digest and both scores; they do not attest model identity.
No job, case or subprocess timeout is increased. The existing 660s outer limit
bounds three sequential cases; it does not allow three full 5m waits. A successful
run qualifies this fixed three-case native workspace and explicit-promoted Lift
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
description or CHANGELOG instead.

## Pull requests

- Keep each pull request focused on a user problem.
- List exact verification commands and results.
- Label behavior as continuously tested, demonstrated once, schema-valid,
  proposed, or unbuilt.
- Update the capability documentation and status language with the code.
- Never include API keys, tokens, private endpoints, tenant/subscription IDs,
  registry names, cluster addresses, or unsanitized user data.

Every change lands through a pull request with required checks green.
