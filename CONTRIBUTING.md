# Contributing to Kaimahi

Kaimahi is an incubation project. Focus contributions on fixes, documentation
corrections, tests, and small capability changes. Start with the organization
[contribution expectations](https://github.com/kaimahi-agents/.github/blob/main/CONTRIBUTING.md).

## Before building something new

Orka remains the first-class/default platform; Kaimahi is tooling to help people
get agents onto it. Check Orka, Kubernetes and existing integrations first. In
the pull request, explain why configuration, integration or an upstream
contribution cannot provide the requested behavior. Runtimes and harnesses own
execution, enforcement and model access; KMX should not duplicate them.

Kagent support is intentionally bounded to explicit
`kmx agent create --runtime kagent <name>` against an already-installed exact
v0.10.2. Do not infer or add an installer, upgrade path, auto-detection, chat,
list/show/status, lift/evaluate, console, quickstart, `up`, or AKS payload from
that adapter. Preserve Orka defaults and keep runtime choice distinct from the
model-provider choice. A wider cross-runtime authoring/lifecycle surface remains
an open, unsupported question.

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
python3 scripts/check-comment-history.py --selftest && python3 scripts/check-comment-history.py
python3 scripts/test_check_mutations.py
python3 scripts/check-mutations.py
python3 scripts/check-readme-front-door.py
python3 scripts/check-readme-front-door-test.py
python3 scripts/check-brand-assets.py
python3 scripts/homebrew-formula.py --selftest
bash scripts/check-no-azure-ids-test.sh && bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
test -z "$(gofmt -l cmd internal embed.go embed_test.go)" && go vet ./... && go test ./...
python3 scripts/test_model_fixtures.py -v
```

The checkers above are the ones you can usefully run by hand. CI's hygiene
job runs each checker, each checker's self-test, and a set of inline
meta-checks over CI's own guards; it is the authority on what gates a
merge, not this list.

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

One Go module remains: the root CLI (`cmd/kmx`, `internal/kmx`), including
first-class Orka paths and the narrowly scoped Kagent v0.10.2 create adapter.
Plane commands, administration, the source module (including checked-in SQL),
build helper, embedded assets and plane-only scripts/fixtures are removed.
Historical source remains in Git history. This cleanup deletes no deployed
databases, credentials, caches or resources; legacy AKS ownership records and
conservative teardown support remain.

For cluster changes, use the documented kind path and a dedicated `KIND_CLUSTER`
name to avoid changing another developer's cluster. See
[`docs/getting-started.md`](docs/getting-started.md) for prerequisites.

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
