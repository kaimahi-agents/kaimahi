# Running on a managed cluster (AKS)

[Orka](orka.md) is the platform; Kaimahi helps applications get onto it.
For existing applications, start with [model-traffic migration](migrate.md).
This page documents the **current AKS implementation**, including `kmx lift`'s
legacy kagent/Copilot demo journey pending the code transition. It is not a
claim that Orka runs those demo agents or that kagent authoring has been ruled out.

AKS is **demonstrated, not maintained**: recorded clusters were short-lived and
torn down. CI exercises portability and ownership logic with keyless tests;
it has no Azure credential and does not re-prove live cloud behavior.

## Prerequisites

- An authenticated `az` CLI and subscription permissions for the intended
  resources. kmx does not install `az`. Created-cluster registry attachment also
  requires permission to create the pull-role assignment.
- Phase-specific tools: kubectl; Bash for cluster/boundary/plane; Python 3 for
  boundary and PyYAML for plane rendering; Helm for kagent; Go for the plane;
  curl for Azure telemetry verification. Script phases require Linux, macOS or
  WSL, not native Windows. `--plan` requires only the authenticated Azure CLI.
- A Copilot subscription for the **full legacy lift**, not for every possible
  Orka migration. The full lift deploys no Ollama. An independently provisioned
  model/Provider is the operator's responsibility on the Orka path.
- A checkout for standalone probes and model-key helpers, not for lift or
  Copilot capture. Images build in private ACR: no local Docker or push.

## One command: `kmx lift`

```bash
kmx lift --plan --resource-group <your-rg> --registry <registry> --cluster <cluster>
kmx lift --resource-group <your-rg> --registry <registry> --cluster <cluster>
```

The banner names the account, subscription name and destination. `--plan` stops
after preflight/account inspection, without creating cloud resources. A real
run requires the cluster name typed at a terminal or
`KAIMAHI_CONFIRM=<cluster>`; unattended and unconfirmed runs refuse.

For an existing cluster, add `--byo` explicitly:

```bash
kmx lift --byo --resource-group <your-rg> --registry <registry> --cluster <cluster>
```

**BYO never creates, deletes or adopts the cluster or resource group.** It checks
registry pull rights and refuses rather than granting itself `AcrPull`. It also
refuses to take over pre-existing Azure monitoring. Those are owner decisions.

### Targets and resume

| Phase | Current work |
|---|---|
| `cluster` | create tagged resource group, private ACR and AKS; obtain kubeconfig |
| `boundary` | check policy engine, deploy plane boundary/ledger bootstrap, run negative network proof |
| `kagent` | install the legacy runtime |
| `credential` | keep an existing Copilot Secret; otherwise run native device login and capture |
| `plane` | build in ACR, create/renew data certificate, render registry image/pull policy, deploy |
| `agents` | configure the retained legacy agents for governed Copilot; tool wiring stays direct kagent MCP or owner-selected, not gateway-governed |
| `observability` | Azure monitoring, scrape allowance/PodMonitor, workbook |
| `verify` | legacy agent answer and ledger; Azure metrics/logs when enabled |

`--step <phase>` runs **one phase only**, not that phase and all following ones.
Failures leave earlier work in place and print a target-preserving retry. Fix
the cause, rerun that phase, then run the remaining phases or the full lift:

```bash
kmx lift --step plane --resource-group <your-rg> --registry <registry> --cluster <cluster>
```

Retain the same identity/options, including `--byo` where used. Completing a
single phase proves nothing about phases not run by that invocation.

For an Orka migration, the reusable infrastructure phases are `cluster`,
`boundary`, `plane`, and optionally `observability`. Install/configure Orka and
its Provider separately, then run `kmx migrate`. The `kagent`, `agents`, and
`verify` phases are still kagent-shaped; `credential` requires Copilot even when
the migration does not. **Do not present a full lift as an Orka-native journey.**
The boundary phase also applies the legacy Copilot egress allowance; selected
phases are not yet a minimal Orka-specific provisioning profile.

### The credential handoff

The full lift keeps `kaimahi/kaimahi-copilot-token` if present; if absent, it
runs the same native device-login flow available explicitly as:

```bash
KAIMAHI_CONFIRM=<cluster> kmx --context <cluster> models credential copilot
```

No checkout or credential input flag/stdin is needed. Follow the printed GitHub
device-login instructions; a `gh` login is not a Copilot login. The OAuth login
is cached at `~/.config/kaimahi/copilot-oauth-token` with mode 0600; only the
exchanged short-lived token enters the plane Secret through kubectl stdin.
The command applies Copilot egress and **restarts an existing proxy**, waiting
for rollout; an absent proxy will start with the Secret later. Re-run this
command when the token expires. Lift's presence check does not establish that
an existing token is still valid. See [models](models.md).

### Defaults and escape hatches

| Choice | Default / override |
|---|---|
| Region | `westus3`; `--location` |
| Node | one `Standard_B4ms` (4 vCPU / 16 GiB); `--node-count`, `--node-size` |
| OS disk | lift: 64 GiB; standalone `aks-up.sh`: 32 GiB, configurable with `AKS_NODE_OSDISK_SIZE` |
| Network | Azure CNI Overlay + Cilium (`--network-policy cilium`); `azure` or `calico` accepted but not live-verified here |
| Control plane | Free tier, no SLA; not exposed as a lift flag |
| Registry | private ACR Basic, admin user disabled; `--registry` |
| Monitoring | enabled; `--observability=false` skips it and Azure telemetry verification |

The larger lift disk follows a recorded 32 GiB `DiskPressure`/eviction failure
with both monitoring add-ons. It is not a sizing guarantee for Orka, a larger
model, more applications or a production workload. Scheduling isolation is a
separate concern; see [isolation](isolation.md).

## Context and boundary safety

kmx pins the named cluster on kubectl/Helm calls. **Azure's
`get-credentials --overwrite-existing` still changes shared kubeconfig's
current-context**, including resumed invocations. Other tools may follow that
new current-context; use explicit contexts for manual checks too.

Ordinary cluster mutations use the [context guard](kmx.md#where-the-command-will-land):
local kind requires both a `kind-*` name and loopback API server; remote writes
require confirmation naming the context. Cloud provisioning/deletion have their
own confirmation because they do not act through a kube context.

A NetworkPolicy object is not evidence of enforcement. The boundary phase:

1. Refuses an absent, non-enforcing or unreadable policy engine.
2. Applies the boundary and ledger bootstrap, then runs
   [`netpol-probe.sh`](../scripts/netpol-probe.sh), with a reachable positive
   control and connections that must be denied.

A failed fresh boundary proof leaves bootstrap resources to clean up, not a
completed plane/agent deployment. The tooling does not migrate an existing
cluster to a different policy engine; a mismatch is refused. Changing a cluster's
CNI or reimaging node pools is not hidden behind a create operation.

## Remaining checkout helpers

Use native `kmx lift` and its phases for managed provisioning; the former
`make up`, `make ollama` and `make aks-down` shims are removed. The
[Makefile](../Makefile) retains model credential helpers and probes such as
`make netpol-verify`. For those helpers, set `TARGET=aks`,
`KUBE_CTX=<cluster>` and confirmation explicitly; do not infer the target from
kubectl's current-context. Read the model ledger and metrics through native kmx.

Lift carries its needed scripts/manifests. Registry rendering preserves the
committed kind `imagePullPolicy: Never`, and the plane uses the default
StorageClass. There is no separate plane Helm chart.

## Observability

Lift wires two independent paths: Managed Prometheus into an Azure Monitor
workspace and Container Insights logs into Log Analytics. A parameterized ARM
workbook joins them; committed templates carry no deployment identifiers.

The plane's unauthenticated ops port **9092 remains on no Service**. Pod discovery
reaches the named `ops` container port through
[`k8s/observability/network-policy.yaml`](../k8s/observability/network-policy.yaml),
which admits only `kube-system` pods labelled `rsName: ama-metrics`. This is the
custom-scrape replica, not the per-node DaemonSet. The allowance is access control,
not permission to expose the port generally.

### The scrape job is a PodMonitor, and yours can sit beside it

[`podmonitor.yaml`](../k8s/observability/podmonitor.yaml) lives in `kaimahi` and
selects only plane pods and their named ops port. For your own workload, author
a separate monitor and allowance; retain these load-bearing details:

- API group `azmonitoring.coreos.com/v1`, not the open-source operator's group.
- `port` is a **container port name**, not a number.
- Put it beside its pods or set `namespaceSelector.matchNames` explicitly.
- Retain `labelLimit: 63`, `labelNameLengthLimit: 511`, and
  `labelValueLengthLimit: 1023`; Azure may drop a job exceeding its limits.
- A default-deny namespace needs its own scraper ingress allowance.

Lift never reads, writes or deletes the cluster-wide
`ama-metrics-prometheus-config` ConfigMap. On an older add-on without the monitor
CRD, it prints the equivalent job for **the owner to merge**, continues the
workbook/log path, and leaves metrics unproven until data arrives.

**Disabling the metrics add-on can remove its CRD and every PodMonitor using it,
including yours.** BYO teardown warns before disabling an add-on this run enabled;
keep your manifests and reapply after re-enabling it. This consequence exists
even though lift never edits your shared scrape ConfigMap.

`verify` queries `kaimahi_build_info` and an actual plane log line, with waits for
both pipelines. Add-on enabled, scrape target allocated, data queryable and
workbook rendered are different claims. `--observability=false` checks none of
the Azure telemetry. The view covers calls crossing the seam, **not agent-internal
spans or reasoning**; keep your own OpenTelemetry instrumentation.

## Teardown

Cloud resources keep billing until cleaned up. For a lift-created cluster:

```bash
KAIMAHI_CONFIRM=<your-rg> kmx lift down --resource-group <your-rg> --cluster <cluster>
```

Confirmation names the **resource group**, not the cluster. Before recursive
`az group delete`, the group must carry `kaimahi-ephemeral`; an untagged group
is refused even with confirmation. The command waits and rechecks absence,
removes kubeconfig entries, and separately handles recorded resources outside
the group. Its conclusion covers that group and its record, not all billing.

### Teardown on a cluster you did not create

```bash
KAIMAHI_CONFIRM=<cluster> kmx lift down --byo \
  --resource-group <your-rg> --cluster <cluster>
```

BYO confirms the **cluster**. It removes recorded monitoring, **not the agents,
governance plane, cluster or resource group**. Those remaining workloads need
an owner's cleanup decision. It removes its in-cluster monitoring objects first,
disables only add-ons it enabled, then removes recorded Azure resources by ID,
never by a guessed name. Unknown/pre-existing ownership is left unchanged.

Both branches use a run record under `$KMX_HOME` or the user config directory,
not the checkout. It contains resource IDs: protect it and do not commit it.
Lost record, subscription/branch mismatch, or unresolvable ownership refuses
rather than adopting resources. Incomplete cleanup retains the record and names
what may still bill. Unknown prior monitoring ownership can retain the record
without an error exit: read the report, not only the status code.

The workspaces charge for ingestion/retention; add-ons route data into them.
Data-collection resources and rule groups may land in the AKS node group or
another recorded monitoring group. Check those too, along with the node resource
group, registry and kubeconfig. Deleting one named group is not a subscription
billing audit. Use current Azure prices; historical run estimates are not quotes.

## Retired public edge and concurrent checks

The gateway/MCP listener, public inbound edge, tool/workflow commands and
Slack/ERP/AP fixtures are removed. The plane retains **model 8080, admin 9091
and ops 9092**. Existing installations need the
[explicit retirement steps](operations.md#upgrading-after-approval-retirement):
review rejected tool overlays, old Services/network allowances, credentials and
owner-managed application references. **Applying the new manifests does not
prune them or safely repoint tools.** For older inbound installations also disable
external webhooks/Slack subscriptions before releasing their DNS name and remove
obsolete owned edge resources. This is not automatic cloud deletion, credential
revocation or database cleanup.

All custom approvals/grants and their APIs/CLI views are retired. Ordinary model
caps/accounting remain; historical requests/grants/audits remain in SQL/backups.
Upgrade CLI and plane together and verify every replica's new build: old replicas
can still consume grants during rollout, and rollback can reactivate them. The
original direct kagent MCP example remains, not the gateway-backed fixtures.

Chat allocates a free loopback port by default. Fixed-port helpers need distinct
`CHAT_PORT`, `ADMIN_PORT`, or `OPS_PORT` values when checking two clusters
concurrently. Occupied chat ports fail rather than silently selecting another
cluster's forward.

## What was verified, and what was not

Recorded single-node runs in September 2026 demonstrated private ACR builds,
default-storage PVC binding, Copilot model rows and budget denial, MCP audit,
Cilium enforcement, the now-retired opt-in Slack edge, and the AP fixture path.
Those gateway/AP measurements are historical: their fixtures and CI scenarios
are now retired, not current verification of the reduced model plane.

The September 6 lift runs exercised created and BYO clusters, refused a missing
engine and missing pull rights, and queried metrics/log data. Those runs used
the older ConfigMap scrape implementation; do not treat them as evidence for
later PodMonitor behavior. The September 10 [migration](migrate.md) run exercised
selected infrastructure phases and observed PodMonitor target allocation.

Not established: current end-to-end cloud correctness on every PR, workbook
panel rendering in a browser, Azure/Calico engine enforcement, multi-node
scheduling, durability, upgrades or node replacement. Historical edge runs also
did not establish certificate renewal; the edge is no longer shipped. The legacy
default is ephemeral, with default node SSH and no claim of production hardening;
the legacy agent namespaces remain outside the plane's default-deny boundary. Orka+Ollama was demonstrated in a separate migration, not
provisioned by the full Copilot lift. The checkout ACR plane build currently
omits the version build argument and can report `unknown`; inspect rather than
infer the running revision.

## No Azure identifiers in shared evidence

```bash
bash scripts/check-no-azure-ids.sh path/to/transcript.txt
```

The scanner catches identifier **shapes** such as GUIDs, resource IDs, hostnames
and public IPs, not bare resource-group, cluster, registry or workspace names.
Lift's banner omits the subscription ID, but echoed Azure commands can contain
full resource IDs. Scan **and manually redact names** before sharing transcripts,
attachments or PR evidence. Never commit live Azure or Slack identifiers.
