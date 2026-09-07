# Operating the plane

What it takes to run the governance plane for real: how many of what
runs, what each probe means, how to back up and restore the one
stateful piece, what the metrics say, and what is still not highly
available. Everything here is proven on kind in CI on every PR; nothing
here has been run on a managed cluster since the AKS demonstrations in
[aks.md](aks.md).

Assumes the plane from [spend.md](spend.md) is deployed (`make plane`, or
`kmx plane` — on kind they are the same code, see [kmx.md](kmx.md)).

**Which path is which.** On kind, everything on this page is `kmx`'s and
`make` delegates to it: standing the plane up, governing an agent, the
read-only views, `backup`, `restore`, `plane-metrics`, budgets and
approvals (`kmx backup`, `kmx restore`, `kmx metrics`, `kmx budget`,
`kmx approvals`/`approve`/`deny`/`request`).

The managed-cluster path is `kmx lift`, which creates the cluster, builds
the plane's image in a private registry, renders the manifest for it and
wires Azure-managed monitoring ([aks.md](aks.md)); the Makefile's
`TARGET=aks` path still exists and does the same work step by step. The one
step neither of them does is capturing the **model** key: a managed cluster
runs a hosted model, and a provider token is not one of the three upstream
credentials `kmx credential capture` can check a value against, so
`kmx lift` stops and names `make plane-copilot-secret`, from a checkout.
That is the only step on the managed path that still needs one.

## Shape

| Component | Replicas | State |
|---|---|---|
| `kaimahi-proxy` (LLM proxy, MCP gateway, inbound bridge, admin, ops) | **2** | none that matters — every governance decision is taken in Postgres |
| `kaimahi-postgres` | **1** | the ledger, credentials (hashes), grants, audit trails — on a PVC |

The proxy is stateless in the sense that counts: budgets, grant uses,
replay dedupe, request dedupe and approval immutability are each
decided in one Postgres transaction under a lock on the credential's
row, so two replicas admit exactly what one replica admitting one call
at a time would. That is proven, not argued: the store's concurrency
tests race the real SQL 10–20 ways against a real Postgres in CI, and
the e2e job fires concurrent calls at *both* replicas and counts.

What IS per replica, deliberately:

- **The inbound pre-auth token bucket.** It is a flood guard, not a
  governance decision, and it runs before authentication so that an
  unauthenticated flood cannot become a database flood — a bucket in
  the database would be the amplification it exists to bound. The
  effective ceiling is therefore **replicas × `rate_per_minute`**
  (2 × 60/min by default). Everything after the bucket that decides
  anything is exact in Postgres.
- **The inbound job queue and the notifier queue**, bounded (16 and 32
  by default). An admitted event waits in the replica that admitted it;
  a graceful stop drains them, a crash loses them, and the audit trail
  shows an `admitted` row with no outcome row when that happens. The
  queue depths are metrics.
- **The fail-closed breakers** (a seam refusing everything after its
  ledger or audit write failed). Each replica trips and heals on its
  own; `kaimahi_seam_degraded` shows which.

`make plane` rolls the replicas one at a time (`maxUnavailable: 0`,
`maxSurge: 1`); a PodDisruptionBudget keeps one up through a node
drain. On a one-node kind cluster both replicas share the node (the
anti-affinity is a preference); on a real cluster they spread.

Nothing on the agent side changed for this: kagent reaches the proxy
through its Services, which now have two endpoints.

## Probes

Two probes, on the ops port (9092), and they mean different things:

- **Readiness** (`/readyz`) — "route traffic to me". It pings
  Postgres. A plane that cannot read credentials or write the ledger
  fails every call closed anyway, so a Postgres outage takes every
  replica out of the Services, and nothing else happens: when Postgres
  is back, they are back. CI restarts Postgres and asserts exactly
  that — readiness dips, the proxies' restart count stays at zero.
- **Liveness** (`/livez`) — "restart me". It reports only a **local,
  unrecoverable** fault: a data listener that no longer answers on
  loopback, or a connection pool fully checked out with no acquire
  completing for a minute (a leak or a deadlock — every query in the
  plane is bounded, so a slow database returns connections and cannot
  look like this). It never consults Postgres or an upstream. A
  database outage, a slow model, an unreachable tool server: none of
  them restart the proxy.

`/healthz` on the data listeners stays what it was — an unconditional
"ok" the port-forward scripts wait on.

## Startup

Migrations run at every replica's start, under a Postgres session-level
advisory lock (goose's session locker). Two replicas booting together
against an empty database — the first `make plane` on a fresh cluster —
take the lock in turn: one applies, the other waits and then finds
nothing to do. Both say which in their logs (`migrations: applied` /
`migrations: nothing to apply`). CI deletes both pods at once and
asserts they come back clean with one version row per migration.

## Backup and restore

Postgres is one replica on one PVC. `make down` destroys it. Between
those two facts sits the backup:

```bash
make backup                          # backups/kaimahi-<UTC timestamp>.sql
make backup FILE=/somewhere/safe.sql
make restore FILE=backups/kaimahi-20260902T120000Z.sql   # guarded
```

`pg_dump` runs inside the Postgres pod over its unix socket and streams
through `kubectl exec` into the local file: no password leaves the pod,
nothing is written to disk in the cluster, no local Postgres client is
needed. The dump is `--clean --if-exists`, so restoring **replaces**
the database — every table dropped and recreated — which is what makes
it work on a fresh cluster whose `make plane` already ran the
migrations. A restore is a short outage by design: the script scales
the proxies to zero first (in-flight calls drain), replaces the tables,
and scales them back — a proxy admitting calls during the reload could
write ledger rows the restore then discards, or decide a budget against
a half-loaded ledger. Nothing is re-migrated. Because credential hashes
come back, the agent-side Secrets issued against the backed-up database
work again.

What the file holds: credential names and token hashes (never a token
or an upstream key), caps, the ledger, the tool, inbound and approval
audit trails — including whatever ids those trails record, such as the
Slack user id on a decision — grants and open spend holds. Keep it as
you would the database. `backups/` is git-ignored.

CI proves the round trip: it backs up a cluster with ledger rows, wipes
the database (deletes the PVC and the Postgres pod), restores, and
asserts the rows are back and a governed call still works.

## Metrics

Prometheus text format on **`:9092/metrics`** — its own listener, no
auth, on no Service. Kubelet reaches the port for the probes; nothing
in the agent namespace and nothing behind the inbound edge can (the
proxy's NetworkPolicy opens only 8080 and 8081 to agents). A scraper
gets in through one explicit allowance, `kaimahi-proxy-metrics` in
`k8s/plane/network-policy.yaml`: pods labelled
`app.kubernetes.io/name: prometheus` in a namespace named `monitoring`,
on 9092. Neither exists on kind and nothing scrapes in CI, so the rule
matches nothing until you create them; a different scraper means
editing those two selectors. Because the port has no auth, that
allowance *is* the access control.

```bash
make plane-metrics              # one replica's exposition (port-forward to a pod)
make plane-metrics POD=<name>   # a specific replica
```

| Metric | Labels | What |
|---|---|---|
| `kaimahi_decisions_total` | `seam` (proxy, gateway, inbound), `decision` (allowed, granted, denied), `reason` | every governance decision, by why |
| `kaimahi_ledger_month_cents`, `kaimahi_ledger_month_tokens` | `credential` | month-to-date ledger per credential **name**, read from Postgres at scrape time |
| `kaimahi_live_grants` | `kind` (tool, budget, inbound) | grants live right now |
| `kaimahi_credential_expires_in_seconds` | `credential` | seconds until a credential stops authenticating, negative once it already has — how an expiry is seen coming rather than diagnosed at 3am ([identity.md](identity.md)) |
| `kaimahi_credentials_without_expiry` | — | credentials issued before expiry existed, which therefore never expire. A closed class: this gauge can only fall |
| `kaimahi_open_reservations` | — | calls admitted under a cap whose ledger row has not landed yet, across all replicas |
| `kaimahi_upstream_latency_seconds` | `seam`, `upstream` | histogram of time spent at the upstream |
| `kaimahi_queue_depth`, `kaimahi_queue_capacity` | `queue` (inbound_jobs, notifier) | the per-replica queues |
| `kaimahi_seam_degraded` | `seam` | 1 while the replica's seam is failing closed on a write failure |
| `kaimahi_store_up` | — | 0 when this scrape's Postgres reads failed (the store-derived series are then absent, never stale) |
| `kaimahi_build_info` | `version`, `go_version` | the binary's VCS revision |

plus the standard `go_*` and `process_*` collectors.

**No identifier is ever a label value.** Label values come from fixed
vocabularies (the seams, decisions, reasons, kinds, queues) or from two
operator-chosen names that are already public in the repo and printed
by every audit command: a credential's name (`hello-world`,
`kaimahi-plane` — never its token) and an upstream's name. A channel
id, a user id, a request id, a delivery id, a model string or any free
text never becomes a label; a test in the `metrics` package walks the
live registry and fails on any label outside the set or any value
outside its shape.

## What is still not highly available

- **Postgres.** One replica, one PVC. While it restarts, every proxy
  replica drops readiness and nothing is admitted (fail closed — no
  ledger, no egress); when it is back, so is the plane, with no proxy
  restart. Backup and restore are the durability story; a managed
  database or Postgres HA is a later lane.
- **A single kind node.** Both replicas share it in CI and on a laptop.
  Real spreading needs real nodes.
- **In-flight inbound work** on a crashed replica (above).

Not in scope, and not built: tracing, dashboards, alerting rules, a
shared rate limiter, horizontal autoscaling. AKS was demonstrated
before this shape existed ([aks.md](aks.md)); running two replicas
there is the same manifest and has not been re-run.
