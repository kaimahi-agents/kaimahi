# Legacy reference: operating the Kaimahi plane

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This runbook remains for the
> still-running proxy and database, not as a proposal to operate another
> platform indefinitely. Start at the [documentation index](README.md)
> for the current direction; retire state only through a separate,
> deliberate operational change.

## Shape and state

[proxy.yaml](../k8s/plane/proxy.yaml) runs two proxy replicas, containing
four listeners: model (8080), MCP (8081), admin (9091) and ops (9092).
[postgres.yaml](../k8s/plane/postgres.yaml) runs one Postgres instance on
a PVC. Credentials, budgets, grants, request deduplication and audit
history live in Postgres; it is **not highly available**. Historical inbound
replay/audit and attribution records remain stored, not dropped by retirement.

Rollouts use `maxUnavailable: 0`, `maxSurge: 1`; pod anti-affinity is a
preference, so two replicas can still share a single node. Migrations run
under a database advisory lock at startup. A new replica with invalid
configuration cannot replace the healthy old replica automatically.

Ledger/audit-write breakers still trip and recover independently on each
replica. Inspect individual replica metrics, not just a Service average.
Inbound and notifier queues/workers are removed.

Shutdown drops readiness first and waits within a **20-second process
budget**; it is not a guarantee to finish every in-flight request or
post-response audit. Source: [main.go](../plane/cmd/kaimahi-proxy/main.go).

## Upgrading after inbound retirement

This slice removes inbound webhooks, Slack approval commands and notifications,
not the MCP gateway or tool/budget approvals. Use `kmx approvals`, `kmx approve`
and `kmx deny`; there is no Slack approver path or notification fallback.

Upgrade kmx and the plane from the same revision. Admin contract **3** marks
this intentional removal; it is not backward-compatibility negotiation. Old
kmx binaries accept higher contract numbers and may still promise compatibility,
but their inbound audit and four-trail flow/watch calls will fail. The current
CLI keeps lower capability checks for surviving operations, so a model-only
operation does not require a plane upgrade merely to reach its existing API.

1. Back up the database. Disable external webhook producers and Slack event
   subscriptions/Request URLs **before releasing the old public DNS name**;
   someone else can claim that name. Stop relying on inbound delivery during
   the upgrade; historical `admitted` rows do not prove work completed.
2. Remove `inbound_hooks` and `approval_notifier` from operator-maintained
   configuration, including empty or null entries. They are now rejected as
   unknown fields, **not ignored**. Deploy the updated configuration and plane
   together; old replicas may remain until the new ones become Ready.
3. **`kubectl apply` does not prune resources omitted from the new manifests.**
   On the explicit context, review ownership and explicitly delete obsolete
   resources in namespace `kaimahi`: Service `kaimahi-inbound`; Deployment,
   Service, ConfigMap and NetworkPolicy `kaimahi-inbound-edge`; and NetworkPolicy
   `kaimahi-proxy-ingress-edge`. These names come from the pre-retirement
   manifests. They defined no Ingress object: inspect any operator-added ingress
   separately and remove only the route owned by this retired integration.
   Review PVC `kaimahi-inbound-edge-data` separately before deleting its stored
   certificate/ACME data. Do not delete `kaimahi-proxy`, its model/MCP Services,
   or the Postgres PVC. Verify the old endpoint is no longer exposed; a completed
   apply or proxy rollout alone is not that proof.
4. Review obsolete signing/approver/notifier Secrets and monitoring rules
   separately. Updating the plane alone does not revoke old tokens, remove
   cloud resources, or clean up the database. Keep historical SQL migrations and
   stored audit/attribution data; no destructive schema cleanup is required.

After rollout, check all replicas, the four listeners, model ledger and retained
MCP/approval behavior. `kmx flow` and `kmx watch` now read three trails:
model, tool and approval. The inbound audit API and CLI view are removed;
old rows remain database history, not an active delivery/replay interface.

## Probes

On ops port 9092:

- `/readyz` checks Postgres and the draining flag. A database outage removes
  replicas from Service routing; recovery restores readiness.
- `/livez` checks local data listeners and a pool saturated without progress
  for a minute. It does not query an external database/upstream for health.
- `/healthz` on data listeners only says that listener answers; it is not
  proof that authenticated traffic, the ledger or an upstream works.

[ops.go](../plane/internal/ops/ops.go) defines the checks. TLS listener
probes verify the seam certificate too, so certificate failures can affect
local liveness; “database outage does not trigger restart” is not a promise
that every externally visible failure leaves the process running.

## The seam certificate

Model 8080 and MCP 8081 serve TLS. Existing custody is split:

| Secret | Namespace | Material |
|---|---|---|
| `kaimahi-plane-authority` | `kaimahi` | CA certificate and private key; mounted nowhere |
| `kaimahi-plane-seam-tls` | `kaimahi` | serving certificate/key and CA; proxy mount |
| `kaimahi-plane-ca` | client namespace | CA certificate only; client trust |

The serving certificate lasts 398 days. `kmx plane` renews within its
last 30 days under the existing CA; clients need no new trust distribution
for that re-sign. Readiness of a workload does not prove its client verifies
TLS. Never work around expiry by disabling verification.

```sh
kmx status
kmx plane --step certificate
```

The certificate step renews as needed, republishes trust and restarts the
proxy to load material read at startup. An expired certificate makes
verifying clients fail; kagent may report only a generic connection error.
CA/private-key loss is not fixed by a normal serving-certificate re-sign.
See [certificate.go](../internal/kmx/app/certificate.go) before replacing
trust material across a running installation.

## Backup and restore

```sh
kmx backup
kmx backup /safe/location/plane.sql
kmx restore /safe/location/plane.sql
```

**Restore replaces the database and causes an outage.** It validates a
complete dump, scales proxies to zero, restores transactionally, then
returns them to their prior replica count. It is not a merge of old and
new audit history. Inspect the context and preserve a current backup before
running it. If recovery fails, inspect both the restore and scale errors.

[backup.go](../internal/kmx/app/backup.go) streams `pg_dump` through
`kubectl exec` without exporting the database password. A completed backup
replaces the requested local file; an incomplete dump is refused. Dumps
contain credential hashes, caps, grants, ledger/audit rows, actor IDs,
caller observations and spend holds—not usable opaque tokens or provider
keys. Protect them as database material. Kubernetes Secrets and CA private
keys are **not** backed up by this command; retain an independent custody
recovery plan. Existing client tokens work only against matching restored
hashes. Cluster/PVC deletion destroys state unless backed up elsewhere.

## Metrics

```sh
kmx metrics
kmx metrics --pod <proxy-pod>
```

`:9092/metrics` is Prometheus text, without auth or a Service. Access is
limited by [network policy](egress.md), including selected monitoring pods;
port-forward uses Kubernetes permissions instead. Managed monitoring is a
separate opt-in configuration, not absence of an observability path.

Key series in [metrics.go](../plane/internal/metrics/metrics.go):

- `kaimahi_decisions_total`, `kaimahi_upstream_latency_seconds`;
- `kaimahi_ledger_month_cents`, `kaimahi_ledger_month_tokens`,
  `kaimahi_live_grants`, `kaimahi_open_reservations`;
- `kaimahi_credential_expires_in_seconds`,
  `kaimahi_credentials_without_expiry`,
  `kaimahi_seam_certificate_expires_in_seconds`;
- `kaimahi_seam_degraded`, `kaimahi_store_up`, `kaimahi_build_info`.

Inbound/notifier queue series are removed; update dashboards and alerts that
expected them.

A failed store scrape omits store-derived series rather than returning stale
values. Labels include credential/upstream **names**, not bearer tokens,
Slack IDs, delivery IDs or arbitrary request text. Names may still disclose
operator context. Alert on expiring credentials/certificates, degraded seams,
and missing usage; this page does not install alerting rules.

Tests and resilience probes remain in [scripts](../scripts/) and
[plane/internal](../plane/internal/). Historical demo runs are not a current
availability certification for kind, AKS or Orka.
