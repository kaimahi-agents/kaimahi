# Development

For work **on** Kaimahi; contribution/PR expectations are in
[CONTRIBUTING.md](../CONTRIBUTING.md). For use, start with [Orka](orka.md) and
[migration](migrate.md).

## Direction and current implementation

**Orka is the platform.** Kaimahi tooling helps agents/applications get onto it.
The current migration governs **model traffic only** and leaves the Deployment
owner-managed. Native-Orka-only authoring versus kagent YAML over Orka remains
open; a recommendation is not a ruling. `kmx agent create` currently authors
native Provider + Agent resources with an optional Task, not kagent conversion;
see its [safety contract](kmx.md#kmx-agent-create). `orka.harness.v2` is outside
the direction. The seam bridge shrinking as upstream capabilities arrive is a
successful outcome.

The tree retains kagent Agent/ModelConfig/direct RemoteMCPServer wiring and the
model proxy with its operator APIs, ordinary budgets and ledger. The custom MCP
gateway, all custom approvals/grants, workflow runner and connector fixtures are
removed, following inbound/notification retirement. Neither agent-authoring
path is removed; the original direct `hello-tools` example remains. Document
the bridge as present implementation, not the long-term platform boundary.

## Repository layout

Consult the [repository map](repository-map.md) for product/demo classification.

| Path | Responsibility |
|---|---|
| `cmd/kmx/`, `internal/kmx/`, `embed.go` | CLI command tree, orchestration, scaffolding, embedded manifests |
| `plane/` | separate Go module: model proxy, budgets, durable ledger |
| `plane/cmd/kaimahi-proxy/` | process/listener wiring |
| `plane/internal/` | proxy, meter/pricing, config, store/db, redaction, metrics/ops |
| `k8s/` | retained agents, presets, model plane and network policies |
| `scripts/`, `Makefile` | checks, model/network probes and model credential helpers |
| `.github/workflows/` | actual verification jobs and docs-only routing |

The root CLI and plane are separate modules because the plane builds independently.
The isolated `spikes/kagent-shim/` experiment has its own module and workflow;
it is not a root dependency or supported authoring interface. `go:embed` cannot
cross module boundaries; clone-free kmx fetches the plane at its own revision.
Use `kmx plane --source .` when exercising checkout changes. Plain `make` builds
`bin/kmx` only. Development Orka commands are not in the older `v0.1.0` release;
see [installation](kmx.md#install).

## Build and verify

Run from the repository root. Formatting checks must test `gofmt` output because
`gofmt -l` alone exits successfully even when it lists files.

```bash
test -z "$(gofmt -l cmd internal embed.go embed_test.go)" && go vet ./... && go build ./... && go test ./...
(cd plane && test -z "$(gofmt -l .)" && go vet ./... && go build ./... && go test ./...)
python3 scripts/check-doc-links.py --selftest
python3 scripts/check-doc-links.py
python3 scripts/check-secret-shapes.py --selftest
python3 scripts/check-secret-shapes.py
bash scripts/check-no-azure-ids-test.sh
bash scripts/check-no-azure-ids.sh
bash scripts/kube-guard-test.sh
```

**Store tests skip without PostgreSQL.** A green module test run without
`KAIMAHI_TEST_PG_DSN` does not verify durable concurrency/SQL behavior. Against a
throwaway database (never a valuable database), run:

```bash
(cd plane && KAIMAHI_TEST_PG_DSN='postgres://kaimahi:throwaway@127.0.0.1:5432/kaimahi?sslmode=disable' go test -count=1 ./...)
```

Bind a local test database to loopback only, not every interface. CI provides
Postgres and fails if those tests skip. The workflow's hygiene job is authoritative
for the full checker/self-test/mutation-test set; do not replace it with this
focused list. [CI configuration](../.github/workflows/ci.yml) also checks its own
guards and aggregator membership.

### Local loop

```bash
make
export KIND_CLUSTER=dev-local
export KUBE_CTX=kind-dev-local
bin/kmx up
bin/kmx plane --source .
bin/kmx govern hello-world
bin/kmx agent chat hello-world 'Who are you?'
bin/kmx ledger hello-world
bin/kmx down
```

This exercises the existing kagent plane path, not Orka authoring. For migration,
use [getting started](getting-started.md#current-orka-path) and an owner-managed
application. Pick a distinct cluster name and explicit context. Keep
`CONTAINER_ENGINE=podman` consistent if selected; Docker and Podman inventories
are separate. `down` destroys the local database too; [backup](kmx.md#backup-restore-and-metrics)
first if it matters. Cloud cleanup has different [ownership rules](aks.md#teardown).

### What CI proves

Required checks are `hygiene`, `go-plane`, and `e2e-hello-world`. The last is an
aggregator over the retained kind shards. Gateway/workflow/AP scenarios retire
with their runtime; the original direct kagent MCP test is a separate boundary.
Add probes to the shard owning their state lineage, or arrange independent setup.
Every cluster step needs the docs-only guard; the aggregator uses `always()` and
must depend on every shard. An unneeded failing shard would not gate a merge.

`plane-upgrade` tests schema/data preservation and failed migrations without a
cluster; it is not a shard. `kmx-clone-free` runs on main/manual dispatch, not as
a required PR shard. Its native Orka creation journey checks an actual Task answer,
separately from the retained kagent/plane journey. Tags trigger the separate
release workflow. None of these proves an AKS run: no Azure credentials belong
in fork-exposed CI. A docs-only shortcut is not an end-to-end rerun.

## How the existing plane works

One process, normally two replicas, one Postgres, three listeners:

| Port | Boundary |
|---|---|
| 8080 | model data, TLS under plane CA |
| 9091 | admin bearer API; no Service, reached by pod port-forward |
| 9092 | metrics/readiness/liveness, unauthenticated; no Service |

The model data seam authenticates opaque `kmh_` credentials; upstream keys remain
in proxy custody and only hashes of issued tokens are persisted. Do not confuse
agent identity, caller claims, observed source and acted-for attribution.
[Identity](identity.md) defines them; [operations](operations.md) defines the
per-replica breakers, single-database availability limit and
[retirement upgrade](operations.md#upgrading-after-approval-retirement).

Exact budget admission is a Postgres transaction under the credential-row lock;
never replace it with an unlocked Go read-then-act. `spend_reservation` holds
admitted spend until ledger settlement. Ordinary caps/accounting and credential
lifecycle remain live. Custom requests/grants/audits, retired allowlists/tool
audit, inbound replay/audit and agent-run attribution remain stored. There is
no custom approval API, automatic request filing or grant override; flow/watch
read only the model ledger. Historical data is accessible through SQL/backups,
not a new archive interface. The [migrations](../plane/internal/db/migrations)
are the schema source: retain all twelve applied SQL migrations; do not drop
tables, reset data, normalize pending requests or exhaust/rewrite grants.
Old replicas or a rollback can still consume grants: retirement becomes effective
only when every replica reports the new build. The migration app/scaffold
implementations remain byte-identical for generated-artifact and rerun
compatibility; old generated tool comments are
not evidence of surviving runtime tool governance.

The upstream table constrains destination **and exact forwarded path**; network
policy constrains reachable pods/namespaces/IPs/ports. Neither replaces the other.
An added destination needs both controls. Model translation and its strict
refusals are in [migration](migrate.md#responses-translation-and-refusals).

## Invariants to preserve

1. Use platform capabilities rather than rebuilding them. Existing kagent-shaped
   helpers do not authorize expanding Kaimahi into another agent runtime.
2. Fail closed on missing proof: HTML with 200 is not a valid endpoint answer,
   unreadable is not absent, and scanner failure is not a clean scan.
3. Keys never enter argv, logs, manifests or ConfigMaps. Tool credential capture
   is removed. Native `models credential copilot` keeps its device login and
   private OAuth cache, not credential input on stdin. Retained model helpers
   keep their own input contracts; do not move keys into unsafe make pipes.
4. Billed work must be recorded even when the surrounding operation fails. Free
   is an explicit upstream classification, never inferred from a URL or zero price.
5. Mutations need the appropriate context/cloud guard; name and loopback server
   are independent evidence. Confirmation is scoped consent, not a general bypass.
6. New durable limits need real concurrent Postgres tests, not a process-local
   argument. Two replicas agree through the database, not shared memory.
7. Metrics labels use fixed vocabularies or the explicitly permitted public
   credential/upstream names. Tokens, channel/user/request/delivery IDs are not labels.
8. State evidence accurately: continuously tested, demonstrated, schema-valid,
   proposed or unbuilt. Configuration and a fluent answer are not enforcement proof.

## Troubleshooting traps

- A missing ModelConfig can be admitted yet never reconcile. Inspect Agent
  Accepted/Ready conditions, not only a pod or cached readiness verdict.
- Fresh `up` does not enable governance. Rerunning it preserves non-default
  routing; `use` explicitly selects a model preset. There is no tool-ungovern
  repair path: old gateway references need an owner's deliberate decision.
- The image is distroless: no shell for exec-based readiness loops. Probe its
  behavior. A Secret created after an optional mount may need a rollout restart.
- A Service port-forward selects one pod. Use separate pod forwards for claims
  about both replicas; use distinct fixed admin/probe ports across clusters.
- One-shot ambiguous-disconnect retries can repeat effects; [retry limits](kmx.md#retry-limits).
  Check the ledger before assuming a timed-out model call never happened.
- A bare shell `wait` waits on long-running forwards too; collect worker PIDs.
- On macOS, fetched plane builds can fail on Go's persisted `GOBIN`; clear it
  deliberately with `go env -u GOBIN` or build from a checkout.
- Podman machines need checkout mounts for image builds; restarted machines may
  leave kind nodes stopped. The cluster step recovers named nodes and checks API/DNS.
- Python scripts supporting macOS Python 3.9 need postponed annotations before
  using PEP 604 annotations. Ignore rules such as `bin/` match at every depth;
  verify tracked membership, and beware newly unignored files entering `stash -u`.

Use `kmx ledger`, `kmx flow`, `kmx metrics`, Agent conditions and
proxy logs on the **explicit context**. A seam receipt is evidence for that seam,
not for all execution inside an agent. Never paste live infrastructure IDs into
evidence: scan shapes and manually redact names too.
