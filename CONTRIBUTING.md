# Contributing to Kaimahi

Kaimahi is an incubation project. Focus contributions on fixes, documentation
corrections, tests, and small capability changes. Start with the organization
[contribution expectations](https://github.com/kaimahi-agents/.github/blob/main/CONTRIBUTING.md).

## Before building something new

Orka remains the first-class/default platform; Kaimahi is tooling to help people
get agents onto it. Check Orka, Kubernetes and existing integrations first. In
the pull request, explain why configuration, integration or an upstream
contribution cannot provide the requested behavior. The migration bridge should
shrink as upstream capabilities cover it.

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
tree are current tooling, retained legacy implementation or test support — worth
reading before you change something you found by grepping.

## Local verification

Run the checks relevant to your change:

```bash
python3 scripts/check-doc-links.py --selftest && python3 scripts/check-doc-links.py
python3 scripts/check-secret-shapes.py --selftest && python3 scripts/check-secret-shapes.py
python3 scripts/check-repository-map.py --selftest && python3 scripts/check-repository-map.py
python3 scripts/check-comment-history.py --selftest && python3 scripts/check-comment-history.py
python3 scripts/check-mutations.py
python3 scripts/check-readme-front-door.py
python3 scripts/check-readme-front-door-test.py
python3 scripts/check-brand-assets.py
python3 scripts/homebrew-formula.py --selftest
bash scripts/check-no-azure-ids-test.sh && bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
test -z "$(gofmt -l cmd internal embed.go embed_test.go)" && go vet ./... && go test ./...
(cd plane && test -z "$(gofmt -l .)" && go vet ./... && go test ./...)
```

Without a database that last line still runs `gofmt`, `go vet` and every
non-Postgres package for real — but it covers the store not at all: every
`plane/internal/store` test skips unless `KAIMAHI_TEST_PG_DSN` points at a
Postgres. Stand a throwaway one up and set it if your change touches the
store — CI's `go-plane` job uses a service container and always runs them.

The checkers above are the ones you can usefully run by hand. CI's hygiene
job runs each checker, each checker's self-test, and a set of inline
meta-checks over CI's own guards; it is the authority on what gates a
merge, not this list.

Two Go modules: the root one is `kmx` (`cmd/kmx`, `internal/kmx`), and
`plane/` is the retained model seam's. The root includes the first-class Orka
paths and the narrowly scoped Kagent v0.10.2 create adapter. Gateway/tool
governance and custom approval/grant runtime are retired; ordinary model
caps/accounting remain. Historical SQL migrations, stored data, and legacy AKS
teardown records are not cleanup targets.

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
