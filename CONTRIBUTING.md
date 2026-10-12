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
get agents onto it. Check Orka, Kubernetes and existing integrations first. In
the pull request, explain why configuration, integration or an upstream
contribution cannot provide the requested behavior. Runtimes and harnesses own
execution, enforcement and model access; KMX should not duplicate them.

kagent support is intentionally version-bounded: explicit
`kmx create --runtime kagent <name>` targets preinstalled exact v0.10.2;
explicit Go bundle lift/status/evaluate with `--runtime kagent --runtime-version v0.10.3`
targets preinstalled exact v0.10.3. Lift creates new Go declarative resources or
reuses a complete matching UID-bound deployment; it does not adopt or update.
Evaluation requires a complete matching saved placement, desired bindings and
explicit `--to-context`; unsecure only, trusted-proxy refused before any send.
Deployment receipts prove readiness, not execution success. Dedicated CI now
continuously qualifies Go v0.10.3 plan/lift/repeat/status/conflicts, receipt-bound
evaluation and a separate one-shot native A2A answer using a keyless model/MCP
fixture. Python remains render-only; promotion gates are unsupported. Do not
infer or add an installer, upgrade path, auto-detection, standalone chat/run,
list/show, retirement, console, quickstart, `up`, or AKS payload from those
capabilities. Preserve Orka defaults and v0.10.2 create; runtime choice remains
distinct from model-provider choice.
Wider cross-runtime lifecycle remains unsupported.

New to the codebase? [`docs/development.md`](docs/development.md) covers the
architecture, the build, and the mistakes that are easy to make here, and
[`docs/repository-map.md`](docs/repository-map.md) says which parts of the
tree are current tooling, historical compatibility data or test support — worth
reading before you change something you found by grepping.

## Local verification

Run the checks relevant to your change:

```bash
python3 scripts/check-doc-links.py --selftest && python3 scripts/check-doc-links.py
python3 scripts/check-secret-shapes.py --selftest && python3 scripts/check-secret-shapes.py
python3 scripts/check-repository-map.py --selftest && python3 scripts/check-repository-map.py
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
formatted=$(gofmt -l agentsuite cmd internal pkg/kmx embed.go embed_test.go) && test -z "$formatted" && go vet ./... && go test ./...
python3 scripts/test_model_fixtures.py -v
python3 scripts/test_kagent_lift.py -v
python3 scripts/test_kagent_lift_shell.py -v
python3 scripts/test_kagent_lift_fixture.py -v
python3 scripts/test_kagent_lift_qualification.py -v
python3 scripts/test_kagent_lift_audit.py -v
python3 scripts/test_kagent_lift_qualify.py -v
python3 scripts/test_kagent_lift_ci.py -v
shellcheck scripts/ci/kagent-lift.sh
actionlint .github/workflows/ci.yml
```

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

The mutation harness checks 40 representative mutants across ten checkers,
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

One Go module contains the root CLI (`cmd/kmx`, `internal/kmx`), the public
AgentSuite format package (`agentsuite`), and the application interface
(`pkg/kmx`). AgentSuite does not have a separate module or version pin; root
`go test ./...` tests it alongside the CLI, including first-class Orka paths
and bounded kagent v0.10.2 create/v0.10.3 Go bundle lift/status/evaluate.
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
left for inspection. Bootstrap checks readiness and MCP discovery, not bundle
lift or native A2A execution qualification; it is not an installed CLI installer.
Hygiene runs hermetic bootstrap, qualification, audit, shell, CI-routing and
loopback fixture tests without creating a cluster.

With no argument (or explicit `qualify`), the shell runs the full qualification.
Build the final Go state first with `go build -o bin/kmx ./cmd/kmx`, then run
`bash scripts/ci/kagent-lift.sh`. It audits complete ordered admission/create
metadata, enforces specific exit-1 refusals and checks receipt-bound status.
Native A2A execution is independently verified with exactly one MCP tool call,
two model calls and one terminal answer, with no resend. Only the payload-free
`qualification.json` survives under `${RUNNER_TEMP:-/tmp}/kmx-kagent-lift/`;
raw logs, manifests, credentials, prompts and answers are not artifacts.
Qualifier diagnostics expose only whitelisted subphase and reason codes through
saved shell stderr; bootstrap and tool output remain private.
The `e2e-kagent-lift` shard runs for relevant or unknown PR changes and always
on main pushes, nightly and manual runs. Its required aggregator accepts only
intentional skipped outcomes, never failed or cancelled shards.

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
