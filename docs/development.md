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

`e2e-orka-runtime` is the Orka boundary and runs on every pull request. It brings
up kind, Ollama and the model with `kmx up --step` component steps only, installs
the pinned Orka and its keyless Provider, and requires a Provider → Agent → Task
round trip to return an exact, non-empty local-model answer. It then applies the
committed native Orka [Kubernetes Tool](orka-k8s-tool.md) and proves its boundary
directly over HTTP: an allowed ConfigMap listing that contains a ConfigMap created
seconds earlier, and refusal of Secret reads and pod mutation at both the tool's
own validation and the cluster's RBAC. It installs no kagent and creates no Helm
release, and fails closed if it ever does.

What that shard does **not** prove: that the local model chose to call the tool
(small-model tool selection is a known CI flake class, so model-driven invocation
is left unasserted rather than asserted flakily), and nothing about governance —
Orka traffic is not on the plane seam there.

`e2e-resilience` is the governance boundary, and it uses no kagent either. It
brings up kind, Ollama, the model and the pinned Orka with component steps,
creates an **owner-managed** Deployment (`owner-ci`) in its own namespace
before the plane exists, deploys the plane, and runs
[`kmx migrate`](migrate.md). What it proves is the boundary that command
claims: the owner's Deployment is byte-identical across the migration — uid,
generation and whole spec — and changes only when the **owner** applies the
generated patch. After that it requires a real model turn through the TLS seam
to Orka with its `unpriced` ledger row and the upstream's own token counts, a
429 the application itself reports once its token budget is exhausted, and the
plane surviving a replica killed mid-call — the in-flight call drained, the
survivor answering 200, exactly two ledger rows gained, 2/2 ready again — and
a Postgres outage, where every replica's readiness drops and returns with no
replica's restart count changing. The owner's application is separately
asserted to answer again after a simultaneous restart of both replicas and
after a backup/wipe/restore. Absence of the `kagent` namespace is asserted after
bring-up and again at the end.

What that shard does **not** prove: native Orka Agent governance. The pinned
Orka Provider schema has no field naming a private certificate authority, so an
Orka Agent cannot be told to trust the plane's seam; the governed caller is the
owner's own application, which is the supported path.

`e2e-spend` is the spend-control boundary, on the same owner-managed path and
with no kagent either. It reaches the same starting point as `e2e-resilience` —
component bring-up, pinned Orka, an ungoverned `owner-ci` Deployment, the plane,
then [`kmx migrate`](migrate.md) and a patch the **owner** applies — and then
asserts what the plane *charges and refuses*: the migration's NetworkPolicy
admits exactly one namespace on exactly TCP 8080 (the tool port stays shut); a
real turn writes an `unpriced` Orka ledger row attributed to `none`, which is a
complete answer and a different word from `unknown` or `legacy`; an expired
credential earns a 403 that names the credential and the renewing command, and
renewal restores service while leaving the mounted Secret's uid, resourceVersion
and bytes — and the pod holding them — untouched; a credential with no expiry at
all still authenticates; an exhausted token budget is a 429 the application
itself reports, and lifting the cap restores service. It ends with
`make netpol-verify` and the same `kagent`-namespace tripwire.

The ledger patterns it greps are pinned in
`internal/kmx/admin/ledger_format_test.go` against the real renderer, because a
`grep` that stops matching is a red shard but a *negative* assertion that stops
matching is a green one. Those pins compile with `(?m)`: Go's `$` is end of
text and `grep`'s is end of line, so an end-anchored expression copied in
verbatim would match nothing in Go while matching perfectly in the shard.

What that shard does **not** prove: pricing. The committed `orka` upstream has
no price row, so every row it writes is `unpriced` with honestly zero cents. A
cents-denominated cap is not exercised by any cluster shard — it is covered by
the meter and proxy unit tests in `plane/`.

`e2e-models` is the model-seam boundary, and it uses no kagent and no agent
runtime at all. It brings up kind, Ollama and the model with component steps,
creates the model client's own namespace, deploys the plane, and issues every
credential into that namespace **by name** — no command there inherits a
destination. Its governed caller is a direct authenticated TLS call to the
seam ([`model-seam-probe.sh`](../scripts/model-seam-probe.sh),
[`spend-race-probe.sh`](../scripts/spend-race-probe.sh)), which is also the
only way to exercise a protocol the committed upstreams do not speak: an
agent's OpenAI client sends one shape and retries a 429 on its own. It proves
a metered `free` ollama row with its caller fields intact, eight concurrent
calls against a one-token cap admitting exactly one across both replicas, a
cap denial and ordinary recovery that files no approval request, the ops-port
metrics, and model onboarding end to end — a Responses-API endpoint that is
not one of ours, dry-run, overlay precedence and stale-apply refusal, refused
overlay custody of the admin bearer, metering from the upstream's own token
fields, the onboarded endpoint reachable only by the proxy, an unmeterable
answer refused rather than relayed, a protocol contradicting its own path
refused at load, and the entry surviving the next `kmx plane`.

What that shard no longer proves: the combined `kmx status` counts and the raw
MCP inventory, which were counts of legacy objects, and the cannot-tell status
branch on a real cluster, whose probe minted a reader for kagent CRDs. Those
were deleted rather than rewritten against surviving objects, which would have
asserted less while looking the same. The rule that branch renders — `unknown`
is not a zero, it carries kubectl's own reason, and it publishes no counts —
keeps its unit coverage in `internal/kmx/app/governance_test.go`; what went is
the proof that a genuinely RBAC-denied reader reaches it. Status is rebuilt on
its own evidence separately.

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
