# Running on a managed cluster (AKS)

[Orka](orka.md) is the platform; Kaimahi helps developers get agents onto it.
Agents reach models through owner-configured Orka Providers directly.
This page documents the **current AKS implementation**, which lands Orka on a
cluster it provisions. The former Kagent Copilot demo journey and its lift
payload remain retired. Explicit Kagent v0.10.2 create can target a separately
installed compatible cluster. KMX does not manage that installation, and the
alternate-runtime AKS payload remains retired.

`kmx aks up` and `kmx aks down` are **temporary compatibility routes**,
not a first-class KMX domain. They are hidden from root help and shell
completion. Provisioning installs Orka and optionally enables Azure monitoring;
it does not deploy the model plane. Historical records, recovery and safe teardown
remain available. Cloud-provider
extraction remains tracked in [#223](https://github.com/kaimahi-agents/kaimahi/issues/223);
this visibility change does not move or redesign Azure implementation.

AKS is **demonstrated, not maintained**: recorded clusters were short-lived and
torn down. CI exercises portability and ownership logic with keyless tests;
it has no Azure credential and does not re-prove live cloud behavior.

## Prerequisites

- An authenticated `az` CLI and subscription permissions for the intended
  resources. kmx does not install `az`. Created-cluster registry attachment also
  requires permission to create the pull-role assignment.
- Phase-specific tools: kubectl and Bash for cluster provisioning. The Orka
  phase uses Helm from PATH or the pinned, checksum-verified kmx toolchain. Script phases
  require Linux, macOS or WSL, not native Windows. `--plan` requires only the
  authenticated Azure CLI.
- An independently provisioned model/Provider is the operator's responsibility:
  the lift deploys no Ollama and creates no Provider.
- No checkout, local Docker engine, plane build or Copilot login is needed for AKS setup.

## One command: `kmx aks up`

```bash
kmx aks up --plan --resource-group <your-rg> --registry <registry> --cluster <cluster>
kmx aks up --resource-group <your-rg> --registry <registry> --cluster <cluster>
```

### `--payload` defaults to `orka`, and is the only payload

`kmx aks up` bills money and installs a platform, so the flag states what:

| payload | what lands | phases |
|---|---|---|
| `orka` | Orka v0.2.0 chart — the same verified chart `kmx orka install` puts on a local cluster | cluster, **orka**, observability, verify |

The legacy payload is **refused as retired**, by name rather than as an unknown
value: a script that still names it asked for a platform this command installed
until the legacy runtime was removed, and a typo message would send its author
looking for a spelling instead of a replacement. An existing legacy lift can
still be inspected and torn down with `kmx aks down`; what is refused is
creating or resuming one.

The deprecated `kmx lift` still requires `--payload` so existing scripts cannot
silently change platform. Both names use the same run records and teardown.

**This is a fresh-install path, not a version upgrade.** An existing v0.1.3
manifest installation or foreign Helm release is refused. On an existing AKS
cluster, do not simply remove the old release and rerun lift: first back up
the existing controller PVC and other data volumes, resources and Secrets,
including a snapshot key if this installation has one, **without printing key
values**; verify your
recovery plan, then arrange a clean new target per [Orka's v0.2.0 upgrade
limits](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md)
and [installation guide](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md).
The new kmx installation generates `orka-api-agent-execution-snapshot` under
`fullnameOverride=orka-api`; retain that key with its new volume. Neither
`kmx aks up` nor `kmx aks down` migrates Orka's SQLite data, Tasks or key.
Avoid `helm upgrade --force`; Orka does not support version upgrades here.

**The lift creates no Provider.** A managed cluster has no
in-cluster model server — this path deploys no Ollama — and kmx holds no
credential for a hosted one. Orka refuses every model call until a Provider
exists, so the phase installs the platform and names the step that is yours:

```bash
printf %s "$ORKA_API_KEY" | kubectl --context <cluster> -n orka-system \
  create secret generic <name> --from-file=api-key=/dev/stdin
kmx --context <cluster> agent create <agent> --namespace orka-system \
  --provider-type openai --model <model> --secret <name> --base-url <endpoint>
```

Both commands **name the cluster**. `kmx aks up` moves only its own process's
context, so if your current-context is still a local kind cluster, an unpinned
pair sends the Secret one way and the Agent the other — and the half that lands
locally looks like success. The key is read from **stdin** rather than passed as
an argument, because an argument is visible in the process list and is usually
written to shell history; `printf` keeps the trailing newline out of the Secret,
and a newline there corrupts the Authorization header on every request. See
[models](models.md) for the same approach.

The endpoint is your model backend, not the Kaimahi model seam. To lift a
bundle onto an existing Ready Provider, use [`kmx agent lift`](agent-lift.md)
with `--inference provider:<name>`.

The banner names the account, subscription name and destination. `--plan` stops
after preflight/account inspection, without creating cloud resources. A real
run requires the cluster name typed at a terminal or
`KAIMAHI_CONFIRM=<cluster>`; unattended and unconfirmed runs refuse.

For an existing cluster, add `--byo` explicitly:

```bash
kmx aks up --byo --resource-group <your-rg> --registry <registry> --cluster <cluster>
```

**BYO never creates, deletes or adopts the cluster or resource group.** It
refuses to take over pre-existing Azure monitoring. Provider configuration and
workload image pull permissions remain owner decisions.

### Targets and resume

| Phase | Current work |
|---|---|
| `cluster` | create tagged resource group, private ACR and AKS; obtain kubeconfig |
| `orka` | install the pinned v0.2.0 Helm chart in harness-v2 mode on the selected context; creates **no** Provider — that stays yours |
| `observability` | enable Azure monitoring and record the workspaces/add-on resources it creates; no plane scrape or workbook |
| `verify` | Orka's controller is installed and ready, strictly — no model call or telemetry-arrival check is made |

`--step <phase>` runs **one phase only**, not that phase and all following ones.
Failures leave earlier work in place and print a target-preserving retry. Fix
the cause, rerun that phase, then run the remaining phases or the full lift:

```bash
kmx aks up --step orka --resource-group <your-rg> --registry <registry> --cluster <cluster>
```

Retain the same identity/options and `--byo` where used. A run is recorded with
the payload it landed, and resuming with a different one is refused rather than
reconciled: installing two platforms on one cluster is the outcome the record
exists to prevent. A record that names the legacy payload, or one written before the
payload split and therefore carrying none, refuses to **resume** with an
explicit retirement message — the phases that served it are gone — while
remaining fully readable so the cluster can still be inspected and torn down.
Completing a single phase proves nothing about phases not run by that
invocation.

`--step` is validated against the phases the lift has, so a retired phase such
as `--step agents` is refused and names the phases that exist.

The former `boundary`, `credential` and `plane` phases are not accepted.
Standalone model-plane commands remain separate from the agent setup lifecycle.

### The credential handoff

Model credentials stay in the owner-created Provider's Secret. AKS setup does
not capture a Copilot token, mint plane credentials or route traffic through a
model proxy. See [models](models.md) for native Provider configuration.

### Defaults and escape hatches

| Choice | Default / override |
|---|---|
| Region | `westus3`; `--location` |
| Node | one `Standard_B4ms` (4 vCPU / 16 GiB); `--node-count`, `--node-size` |
| OS disk | lift: 64 GiB; standalone `aks-up.sh`: 32 GiB, configurable with `AKS_NODE_OSDISK_SIZE` |
| Network | Azure CNI Overlay + Cilium (`--network-policy cilium`); `azure` or `calico` accepted but not live-verified here |
| Control plane | Free tier, no SLA; not exposed as a lift flag |
| Registry | private ACR Basic, admin user disabled; `--registry` |
| Monitoring | enabled; `--observability=false` skips new Azure monitoring setup |

The larger lift disk follows a recorded 32 GiB `DiskPressure`/eviction failure
with both monitoring add-ons. It is not a sizing guarantee for Orka, a larger
model, more applications or a production workload. Scheduling isolation is a
separate concern, and this repository makes no scheduling-isolation claim.

## Context and boundary safety

kmx pins the named cluster on kubectl/Helm calls. **Azure's
`get-credentials --overwrite-existing` still changes shared kubeconfig's
current-context**, including resumed invocations. Other tools may follow that
new current-context; use explicit contexts for manual checks too.

Ordinary cluster mutations use the [context guard](kmx.md#where-the-command-will-land):
local kind requires both a `kind-*` name and loopback API server; remote writes
require confirmation naming the context. Cloud provisioning/deletion have their
own confirmation because they do not act through a kube context.

A NetworkPolicy object is not evidence of enforcement. Created clusters retain
the selected policy engine, but setup does not deploy or prove a model-plane
boundary. The tooling does not migrate an existing cluster's CNI or reimage its
node pools; workload network policies remain the owner's responsibility.

## Remaining checkout helpers

Use native `kmx aks up` and its phases for managed provisioning; the former
`make up`, `make ollama` and `make aks-down` shims are removed. The
[Makefile](../Makefile) retains model credential helpers and probes such as
`make netpol-verify`. For those helpers, set `TARGET=aks`,
`KUBE_CTX=<cluster>` and confirmation explicitly; do not infer the target from
kubectl's current-context. Read the model ledger and metrics through native kmx.

Lift carries its provisioning scripts. It needs no plane image, manifest,
certificate or database to install Orka.

## Observability

Lift enables Managed Prometheus into an Azure Monitor workspace and Container
Insights logs into Log Analytics. It records workspaces and add-on resources as
they are created. It does not create a plane scrape allowance, PodMonitor or
workbook, and it never edits the cluster-wide scrape ConfigMap. Configure
workload-specific scrape jobs and dashboards yourself.

**Disabling the metrics add-on can remove its CRD and every PodMonitor using it,
including yours.** BYO teardown warns before disabling an add-on this run enabled;
keep your manifests and reapply after re-enabling it. This consequence exists
even though lift never edits your shared scrape ConfigMap.

`verify` checks Orka readiness, not telemetry arrival or model inference.
Enabling an add-on does not prove that your workload's metrics or logs arrive.
Keep your own OpenTelemetry instrumentation and verify its data path separately.

## Teardown

Cloud resources keep billing until cleaned up. For a lift-created cluster:

```bash
KAIMAHI_CONFIRM=<your-rg> kmx aks down --resource-group <your-rg> --cluster <cluster>
```

Confirmation names the **resource group**, not the cluster. Before recursive
`az group delete`, the group must carry `kaimahi-ephemeral`; an untagged group
is refused even with confirmation. The command waits and rechecks absence,
removes kubeconfig entries, and separately handles recorded resources outside
the group. Its conclusion covers that group and its record, not all billing.

### Teardown on a cluster you did not create

```bash
KAIMAHI_CONFIRM=<cluster> kmx aks down --byo \
  --resource-group <your-rg> --cluster <cluster>
```

BYO confirms the **cluster**. It removes recorded monitoring, **not the agents,
model-traffic bridge, cluster or resource group**. Those remaining workloads need
an owner's cleanup decision. It removes its in-cluster monitoring objects first,
disables only add-ons it enabled, then removes recorded Azure resources by ID,
never by a guessed name. Unknown/pre-existing ownership is left unchanged.

Both branches use a run record under `$KMX_HOME/lift` when `KMX_HOME` is set,
or under the native KMX config directory (`~/.config/kmx/lift` on Linux,
`~/Library/Application Support/kmx/lift` on macOS), not the checkout. It
contains resource IDs: protect it and do not commit it.
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
gateway-backed fixtures are gone with the gateway.

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
