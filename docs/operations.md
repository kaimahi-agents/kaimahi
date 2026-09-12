# Legacy reference: operating the Kaimahi plane

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This runbook remains for the
> still-running proxy and database, not as a proposal to operate another
> platform indefinitely. Start at the [documentation index](README.md)
> for the current direction; retire state only through a separate,
> deliberate operational change.

## Shape and state

[proxy.yaml](../k8s/plane/proxy.yaml) runs two proxy replicas, containing
three listeners: model (8080), admin (9091) and ops (9092). The MCP listener
on 8081 is removed.
[postgres.yaml](../k8s/plane/postgres.yaml) runs one Postgres instance on
a PVC. Credentials, budgets, reservations and the model ledger live in Postgres;
it is **not highly available**. Historical requests, grants, tool/inbound audit,
replay and attribution records remain stored, not dropped by retirement.

Rollouts use `maxUnavailable: 0`, `maxSurge: 1`; pod anti-affinity is a
preference, so two replicas can still share a single node. Migrations run
under a database advisory lock at startup. A new replica with invalid
configuration cannot replace the healthy old replica automatically.

The model ledger-write breaker still trips and recovers independently on each
replica. Inspect individual replica metrics, not just a Service average.
Gateway, inbound and notifier workers are removed.

Shutdown drops readiness first and waits within a **20-second process
budget**; it is not a guarantee to finish every in-flight request or
post-response audit. Source: [main.go](../plane/cmd/kaimahi-proxy/main.go).

## Upgrading after approval retirement

Upgrade kmx and the plane together from the same revision. Admin contract **5**
marks deliberate API retirement, **not compatibility negotiation**. Old clients
accept higher numbers but still call removed routes. The model-overlay capability
floor remains 2; surviving capability checks do not restore retired APIs.

1. Back up the database. Stop automation relying on custom requests, approvals,
   denial, grants or approval audit. Those APIs/CLI commands are removed;
   `flow`/`watch` now read only the model ledger. Historical records remain
   accessible through SQL/backups; there is no replacement archive interface.
2. Review budgets deliberately: stored grants no longer add headroom on the new
   build. Monthly-cap refusal remains 429 without request filing or approval
   advice; recovery is an operator's budget change or the UTC month reset.
   Metering-unavailable 403 and the independent ledger-breaker 503 are unchanged.
   Keep ordinary caps/accounting, reservations, credential custody/expiry, model
   egress and TLS configuration. Update dashboards/alerts for removed grant
   metrics and the retired `granted` decision vocabulary.
3. Roll out and verify **every replica reports the new build**, using per-pod
   `kmx metrics --pod <proxy-pod>` and `kaimahi_build_info`. Old approval-capable
   replicas can still consume stored grants during a rolling update or a blocked
   rollout. Do not declare retirement effective until all have been replaced;
   apply success, one new replica or a reused image tag is insufficient.
4. **Rollback can reactivate stored grants.** All twelve SQL migrations and
   historical requests/grants/audits remain unchanged: no pending-request
   normalization, grant exhaustion/expiry rewriting or schema drop. Restoring
   a backup is not a retirement mechanism. Review the authority consequence
   before running an older binary against that preserved database.

Older installations also need the gateway and inbound cleanup below. Those
checklists do not authorize deleting model resources or application-owned tools.

## Upgrading after gateway retirement

Contract **4** marked the earlier gateway/tool API retirement; current upgrades
also require the contract-5 review above.

1. Back up the database and stop jobs relying on the gateway or workflow runner.
   Inventory owner-managed Agents, RemoteMCPServers, Deployment sidecars, URLs,
   Secret references and Helm/GitOps source before rollout. Decide with each
   application owner whether to stop or replace its tool integration. **Do not
   automatically repoint tools to direct access or widen their network reach.**
2. Review the committed table and ConfigMap `kaimahi-upstreams-extra` in
   `kaimahi`, including each saved/generated overlay. Remove retired
   `tool_upstreams` and `standing_constraints` entries deliberately, preserving
   model entries. Both keys are refused **even when empty or null**; they are
   not silently stripped. Existing `inbound_hooks` and `approval_notifier` keys
   are likewise rejected. Deploy reviewed config and plane together; invalid
   config can leave old replicas serving during a blocked rollout.
3. **`kubectl apply` does not prune omitted objects.** On the explicit context,
   review ownership and explicitly remove obsolete resources. Stop owned retired
   workloads before removing their protective policies. The names below
   come from the [pre-retirement manifests at `10c561d`](https://github.com/kaimahi-agents/kaimahi/tree/10c561d4a890244e240d9d223d20059b1464e957/k8s),
   not a wildcard deletion list:
   - In `kaimahi`: Service `kaimahi-mcp-gateway`; tool-only NetworkPolicies
     `kaimahi-proxy-egress-hosted`, `kaimahi-slack-mcp` and `kaimahi-erp`.
     Applying the updated **retained** `kaimahi-proxy` NetworkPolicy removes its
     8081 ingress and tool egress, but separately generated tool policies remain
     additive until reviewed/removed.
   - Retired fixtures in `kaimahi`: MCPServer `kaimahi-slack-mcp`, Deployment
     `kaimahi-erp`, Service `kaimahi-erp-mcp` and ConfigMap `kaimahi-erp-fixtures`.
     Review controller-owned children rather than assuming apply removed them.
   - In `kagent`: gateway RemoteMCPServers `kaimahi-tools`, `kaimahi-slack`,
     `kaimahi-github`, `kaimahi-erp`, `kaimahi-release-github` and
     `kaimahi-release-ado`; fixture Agents `hello-slack`, `hello-github`,
     `ap-agent` and `release-agent`. Review operator-created equivalents too.
     **Keep the direct `hello-tools` Agent and chart-managed `kagent-tool-server`**;
     if an owner repointed them at the old gateway, that owner must resolve it.
4. Review tool-only custody separately: plane Secrets `kaimahi-slack-bot`,
   `kaimahi-slack-mcp-key`, `kaimahi-github-pat`, `kaimahi-release-pat` and
   `kaimahi-ado-token`; old client Secrets such as `kaimahi-tools-token`,
   `kaimahi-slack-token`, `kaimahi-github-token`, `kaimahi-ap-token` and
   `kaimahi-release-token`. Remove only unused owned material after checking
   application references. External revocation is a separate owner action;
   deleting a Secret or mount does not revoke the issuer's token. Keep model/
   Copilot/Orka credentials, the plane CA/serving Secrets and Postgres state.
5. Check every replica is on the new build, all three surviving listeners,
   model authentication/ledger, credential expiry and ordinary budget caps. Verify
   the gateway is no longer served and old network allowances/references are
   resolved; apply success alone is not that proof. Update dashboards/alerts
   for removed tool metrics. `flow`/`watch` read **only the model ledger**;
   retired requests/grants/audits remain SQL/backup history, not API views.
   **All twelve SQL migrations and stored data remain intact**; no destructive
   database cleanup is required.

## Upgrading after inbound retirement

For installations predating the earlier inbound removal, these additional
public-edge steps still apply. Inbound webhooks, Slack approval commands and
notifications are removed; there is no Slack approver path or notification
fallback. Contract 3 marked that earlier removal; contracts 4 and 5 additionally
retire the gateway and remaining [custom approvals](approvals.md), respectively.

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
   certificate/ACME data. Do not delete `kaimahi-proxy`, its model Service,
   or the Postgres PVC. Verify the old endpoint is no longer exposed; a completed
   apply or proxy rollout alone is not that proof.
4. Review obsolete signing/approver/notifier Secrets and monitoring rules
   separately. Updating the plane alone does not revoke old tokens, remove
   cloud resources, or clean up the database. Keep historical SQL migrations and
   stored audit/attribution data; no destructive schema cleanup is required.

After rollout, verify the old public endpoint is no longer exposed. The inbound
audit API and CLI view are removed; old rows remain database history, not an
active delivery/replay interface. The checklists above cover current listener,
retained model/budget and all-replica retirement verification.

## Probes

On ops port 9092:

- `/readyz` checks Postgres and the draining flag. A database outage removes
  replicas from Service routing; recovery restores readiness.
- `/livez` checks the local model listener and a pool saturated without progress
  for a minute. It does not query an external database/upstream for health.
- `/healthz` on the model listener only says that listener answers; it is not
  proof that authenticated traffic, the ledger or an upstream works.

[ops.go](../plane/internal/ops/ops.go) defines the checks. TLS listener
probes verify the seam certificate too, so certificate failures can affect
local liveness; “database outage does not trigger restart” is not a promise
that every externally visible failure leaves the process running.

## The seam certificate

Model 8080 serves TLS. Existing custody is split:

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
  `kaimahi_open_reservations`;
- `kaimahi_credential_expires_in_seconds`,
  `kaimahi_credentials_without_expiry`,
  `kaimahi_seam_certificate_expires_in_seconds`;
- `kaimahi_seam_degraded`, `kaimahi_store_up`, `kaimahi_build_info`.

Gateway/tool and inbound/notifier series are removed, as are
`kaimahi_live_grants` and the `granted` decision value. Update dashboards and
alerts that expected them; ordinary model/accounting/expiry/build metrics remain.

A failed store scrape omits store-derived series rather than returning stale
values. Labels include credential/upstream **names**, not bearer tokens,
Slack IDs, delivery IDs or arbitrary request text. Names may still disclose
operator context. Alert on expiring credentials/certificates, degraded seams,
and missing usage; this page does not install alerting rules.

Tests and resilience probes remain in [scripts](../scripts/) and
[plane/internal](../plane/internal/). Historical demo runs are not a current
availability certification for kind, AKS or Orka.
