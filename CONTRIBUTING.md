# Contributing to Kaimahi

Kaimahi is an incubation project. Focus contributions on fixes, documentation
corrections, tests, and small capability changes. Start with the organization
[contribution expectations](https://github.com/kaimahi-agents/.github/blob/main/CONTRIBUTING.md).

## Before building something new

Kaimahi builds on kagent, Kubernetes, and existing MCP servers. Check those
projects first. In the pull request, explain why configuration or integration
cannot provide the requested behavior.

New to the codebase? [`docs/development.md`](docs/development.md) covers the
architecture, the build, and the mistakes that are easy to make here, and
[`docs/repository-map.md`](docs/repository-map.md) says which parts of the
tree are the product and which are demonstrations or scaffolding — worth
reading before you change something you found by grepping.

## Local verification

Run the checks relevant to your change:

```bash
python3 scripts/check-doc-links.py --selftest && python3 scripts/check-doc-links.py
python3 scripts/check-secret-shapes.py --selftest && python3 scripts/check-secret-shapes.py
python3 scripts/check-repository-map.py --selftest && python3 scripts/check-repository-map.py
python3 scripts/check-board.py --selftest && python3 scripts/check-board.py
python3 scripts/check-mutations.py
python3 scripts/check-readme-front-door.py
python3 scripts/check-readme-front-door-test.py
python3 scripts/check-brand-assets.py
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
`plane/` is the governance plane's.

For cluster changes, use the documented kind path and a dedicated `KIND_CLUSTER`
name when another lane owns the shared cluster. See
[`docs/COORDINATION.md`](docs/COORDINATION.md) for the process and
[`docs/getting-started.md`](docs/getting-started.md) for prerequisites.

## Pull requests

- Keep each pull request focused on a user problem.
- List exact verification commands and results.
- Label behavior as continuously tested, demonstrated once, schema-valid,
  proposed, or unbuilt.
- Update the capability documentation and status language with the code.
- Never include API keys, tokens, private endpoints, tenant/subscription IDs,
  registry names, cluster addresses, or unsanitized user data.

Every change lands through a pull request with required checks green.
