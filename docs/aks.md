# Running on a managed cluster (AKS)

Assumes you know the kind path ([getting-started.md](getting-started.md)),
the governance plane ([spend.md](spend.md)) and the Copilot preset
([models.md](models.md)).

The README has named AKS as the managed target from early on. For a
while nothing had ever run there, and the tooling could not even *point*
at it: every kube context was hardcoded with a `kind-` prefix. That is
fixed. `TARGET=kind|aks` selects the environment, a private-registry
image path replaces the kind side-load, and real governed runs on real
AKS clusters have happened and were then **deleted**. This is a doc for
reproducing those runs, not a description of a maintained environment.

**If you just want the short version, it is [`kmx lift`](#one-command-kmx-lift):
one command from a working local agent to the same agent on AKS, with
Azure-managed monitoring already wired.** Everything below it is that
command's steps written out, which is worth reading once — the ordering
constraints in it were paid for.

> **Scope, honestly.** Six verified runs on six clusters, across
> 2026-09-01 (two), 09-02, 09-03 and 09-06 (two), each torn down the same
> day. They proved, in order: the governed Copilot path on a managed
> cluster; the NetworkPolicy boundary **enforced** there, which the first
> cluster, created without a policy engine, could not have; the Slack loop
> through a public edge; the accounts-payable demo through the registry
> path; and `kmx lift` end to end with Azure-managed monitoring, on a
> cluster it created and on one it did not. The list under [What was
> verified, and what was not](#what-was-verified-and-what-was-not) is the
> ledger this count comes from: add a run there and update this number
> with it.
> AKS is *demonstrated*, not *maintained*: there is no
> standing cluster, no scheduled job re-proving it, and no Azure
> credential in CI, ever.
> CI stays on kind and keyless. What re-runs on every PR is the
> portability *logic* (the context guard's decisions and the registry
> render), not the cloud.

## What is not built

The portability work adds **no new abstraction layer**. If you go
looking for a Kustomize overlay or a Helm chart for the plane, there is
none:

| Job | What already does it | Kaimahi's net-new |
|---|---|---|
| Target a different cluster | kubectl contexts | one variable: `KUBE_CTX` became overridable |
| Build an image without a local docker push | ACR Tasks (`az acr build`) | a make target that calls it |
| Grant a cluster pull rights | `az aks update --attach-acr` | a line in `scripts/aks-up.sh` |
| Environment-specific manifests | Kustomize, Helm, envsubst | **none of them**, see below |
| Provision AKS | `az aks create` | a parameterised, tagged wrapper |
| Enforce NetworkPolicy | the cluster's CNI (Cilium, here) | a flag the wrapper always passes, and a probe that proves it took |
| Scrape metrics into a managed store | Azure Monitor's metrics add-on | a scrape job, and the one NetworkPolicy allowance it needs |
| Collect container logs | Container Insights | the add-on, enabled with a workspace |
| A dashboard | Azure workbooks | one workbook, parameterised so it carries no identifiers |
| Package the install | Helm | **none**, deliberately, see below |

**There is no Helm chart, and that is a decision rather than an
omission.** The objection is real and worth stating: "you cannot `helm
install` it" is a thing to hear in an enterprise review. The answer is
that there is meant to be exactly *one* implementation of this journey —
a chart would be a second one to keep in step with `kmx` — and that
`kmx` already emits reviewable YAML a platform team can commit or wrap
in a chart of their own. If that ever costs a real adopter, it is worth
revisiting.

The one place a tool would have been the obvious reach is the
environment-dependent `imagePullPolicy`. Kustomize's `images:`
transformer only takes *static* values, so the registry name would have
had to be committed, and the registry name is precisely the identifier
this repo must not commit. Instead `scripts/plane-deploy.sh` transforms
the parsed manifest at deploy time and verifies the result before
applying it.

## The two guardrails

### 1. No Azure identifiers, ever

This repo is public. A subscription or tenant GUID fingerprints the
owner; a resource-group name, an ACR login server or a cluster FQDN names
live infrastructure and invites squatting on the registry name. So every
identifier is an operator-supplied parameter, and
`scripts/check-no-azure-ids.sh`, which CI runs on every PR, refuses
GUIDs, `*.azmk8s.io` FQDNs, any literal `<name>.azurecr.io` or
`<label>.<region>.cloudapp.azure.com` that is not built from a variable
or an obvious `<placeholder>`, public IPv4 addresses, and — since
monitoring arrived — Azure Monitor and Log Analytics **workspace
resource ids** and any literal `*.monitor.azure.com` endpoint.

Run it yourself before pasting terminal output anywhere:

```bash
bash scripts/check-no-azure-ids.sh
```

**What it cannot catch, so you have to.** These are *shape* rules. A bare
name has no shape: a resource-group name, a cluster name, a registry
name and — new with monitoring — a **workspace name** are all just
strings. The scanner sees a workspace name only when it appears inside a
resource id or a hostname. Written on its own, in a log line or a pasted
`az` command, it is invisible to the gate and it is yours to redact.

That applies to **anything you attach to a pull request**, not only to
files in the tree: a terminal transcript is exactly where these leak.
`kmx lift`'s **banner** helps a little by never printing your
subscription id — it names the subscription and the signed-in account
instead, which is what a human actually checks against — but it prints
resource group, cluster, registry and workspace names, because an
operator has to see where the thing is about to land.

**The command echo is a different matter, and it is the bigger risk.**
Every `az` and `kubectl` command is echoed as it runs, the way `make`
echoes a recipe line, so that what happened is copy-pasteable and
checkable. Some of those commands carry a full ARM resource id — which
begins with your subscription id. That is right for a terminal and wrong
for a pull request. **Scan and redact before pasting a lift transcript
anywhere**; the scanner will catch the GUID and the workspace resource
id, and you have to catch the names yourself.

### 2. Context safety, the net that replaced the hardcoding

`KUBE_CTX := kind-...` was an accidental safety feature: a mistyped
cluster name produced *"context not found"*, never a write to somebody's
production. Making it overridable removes that, and this repo's own
[CLI-PROPOSAL](CLI-PROPOSAL.md) already names the resulting foot-gun
(*"--apply on a production context by accident"*). `make down` is now a
command that can, in principle, delete a real cluster.

So every target that **writes to a cluster** depends on `guard`
(`scripts/kube-guard.sh`), which:

- **always prints** the context, the API-server host, and the namespaces
  it is about to touch;
- lets a **local kind** cluster through with no prompt, so the kind path
  and CI are unchanged;
- **demands explicit confirmation naming the context** for anything else;
- **fails closed**: no TTY and no `KAIMAHI_CONFIRM` means nothing happens.

Three targets deliberately sit outside it, because the guard checks a
*kube context* and these do not act through one: `aks-cluster` and
`aks-down` operate on Azure (and `aks-down` has its own two gates, below),
and `plane-image` on `TARGET=aks` only runs `az acr build`. `up` does not
guard either: its first step is what creates the context; every step
after that is guarded.

"Local kind" is deliberately **two independent checks**, the context
name *and* a loopback API server, because a name proves nothing. Anyone
can call an AKS context `kind-prod`; the guard is not fooled:

```console
$ KUBE_CTX=kind-sneaky bash scripts/kube-guard.sh 'apply'
----------------------------------------------------------------
  about to: apply
  context:  kind-sneaky
  server:   example.invalid
  namespace(s): kagent, kaimahi
  posture:  REMOTE / non-kind
----------------------------------------------------------------
kube-guard: 'kind-sneaky' is not a local kind cluster and there is no TTY to ask.
  to proceed:  KAIMAHI_CONFIRM=kind-sneaky make <target>
```

Read-only targets (`chat`, `status`, `ledger`, `tool-audit`,
`approvals`, `grants`) deliberately do **not** prompt: they cannot change
a cluster, and a prompt there would train you to type past it.

Confirm non-interactively, in a script or for a whole session:

```bash
export KAIMAHI_CONFIRM=$AKS_CLUSTER
```

## One command: `kmx lift`

The rest of this page is the long form — every step, and why each one is
where it is. You do not have to run it that way. `kmx lift` is the same
journey as one command, and it works from a released binary with no
checkout, because the scripts and manifests it needs travel inside it.

```bash
kmx lift --resource-group <your-rg> --registry <globally-unique-name> --cluster <name>
```

It creates the resource group, a private registry and a cluster with a
policy engine; **proves the network boundary is enforced before putting
anything behind it**; installs the runtime, the governance plane and the
same two agents you ran locally; wires Azure-managed monitoring; and
finishes by asking the agent a question and checking that the answer's
metrics and logs actually arrived in Azure.

Onto a cluster you already have, which is where most adopters are:

```bash
kmx lift --byo --resource-group <their-rg> --registry <a-registry-it-can-pull-from> --cluster <name>
```

Three things are worth knowing before you run either.

**It says what it will do and where, first.** The banner names the
subscription, the signed-in account, the resource group, the cluster and
every phase, and then refuses to continue without confirmation naming
the cluster (`KAIMAHI_CONFIRM=<cluster>`, or type it when asked). With
no terminal and no confirmation it stops: a cloud subscription is not
somewhere to act on an unanswered question. `--plan` prints the banner
and stops.

**It is resumable.** Every phase is re-runnable and idempotent, so a
failure is resumed by naming the phase that failed rather than by
unpicking the ones before it. The error tells you which:

```bash
kmx lift --step plane --resource-group <rg> --cluster <name> --registry <reg>
```

**The credential phase is direct too.** A managed cluster runs a hosted
model, so the plane needs a real provider token. If its Secret is absent,
the lift runs the same operation exposed independently as:

```bash
kmx --context <cluster> models credential copilot
```

It starts GitHub's device login when the cached OAuth login is absent, then
exchanges that login for a short-lived Copilot token. Only the short-lived
token enters `kaimahi/kaimahi-copilot-token`; the OAuth token is cached as a
`0600` local file. Neither token enters argv, an environment variable, a
manifest on disk, or output, and stdin is not a credential channel. The
operation also applies the plane's Copilot egress rule before continuing.

### The opinions, and how to override each one

"Opinionated" means choices were made so you do not have to. Here is
every one, what it is for, and the flag that changes it. An opinion
nobody can find is just a default, and one nobody can override is a
cage.

| Choice | Default | Why | Escape hatch |
|---|---|---|---|
| Node size | `Standard_B4ms` | 4 vCPU / 16 GiB, burstable. The plane, its ledger and two agents fit with room; the cheapest size that does not make the first chat feel broken. | `--node-size` |
| Node count | 1 | An ephemeral demonstration cluster. The plane is stateless and runs its two replicas on one node happily. | `--node-count` |
| Region | `westus3` | Has the capacity and the price this path was measured at. | `--location` |
| Node OS disk | 64 GiB | Twice what the plane and its agents need, because the monitoring add-ons carry their own images and buffers on the same disk. **Measured, not chosen**: at 32 GiB — which `make aks-cluster` still defaults to, since it enables no add-ons — a cluster with the plane, two agents and both add-ons went into `DiskPressure` and evicted the tools agent repeatedly. | `AKS_NODE_OSDISK_SIZE` on `scripts/aks-up.sh` |
| Policy engine | `cilium` | Azure CNI Overlay powered by Cilium: Microsoft's recommendation for new clusters, and the only engine this repository has watched enforce the plane's whole boundary matrix. | `--network-policy azure\|calico` |
| Control-plane tier | Free | No control-plane charge and no SLA, which is right for a cluster that exists for an afternoon. | not exposed; edit `scripts/aks-up.sh` |
| Where the image lives | a **private** ACR, built by `az acr build` | Built in Azure, so no local Docker and no `docker push`; private, so nothing is published and no public name is claimed. | `--registry` names it; the privacy is not optional |
| Model | governed Copilot | AKS is Copilot-only — no Ollama is deployed there. The keyless path is already proven on kind every PR; this cluster's job is proving the plane runs on a managed one with a real model. | none today |
| Monitoring | on | An agent that arrives on a managed cluster with nothing to look at is the gap this path exists to close. | `--observability=false` |
| Node placement | none | The plane and the agents are ordinary workloads and go wherever the scheduler puts them. Application placement belongs in the application's Deployment/chart; Orka `agent create` no longer exposes BYO image/isolation flags. | application Deployment/chart |

**Not** an opinion, and not overridable: the cluster must have a
NetworkPolicy engine. See below.

### The boundary is proven before anything is put behind it

On a cluster this path creates, `aks-up.sh` always passes
`--network-policy` and refuses a value that would not enforce. On **your**
cluster there is no such guarantee, and a cluster without an engine
accepts every NetworkPolicy and enforces none — the plane's manifests
would be present and inert, which reads as protection and is worse than
none: you would believe the ledger, the gateway and the fixture servers
were unreachable when every pod in the cluster can reach them.

So the `boundary` phase runs two gates before the plane exists:

1. **Ask the control plane which engine the cluster has.** Cheap, runs
   before anything is written, and catches the case that actually
   happens — a cluster created with no engine at all. An unreadable
   answer is a refusal too, not a pass.
2. **Deploy the boundary and prove it.** The NetworkPolicies and the
   ledger go on, and then `scripts/netpol-probe.sh` runs unchanged: it
   asserts connections that must **time out**, against a control that
   must succeed, so "blocked" cannot be a dead target or a runner with
   no internet.

Gate 2 writes before it proves, which is deliberate rather than
conceded: what it writes *is* the boundary, plus an empty ledger. If the
proof fails, no governance plane, no credential and no agent has been
put behind a boundary that does not hold, and the message tells you
exactly what exists and how to remove it.

## Prerequisites

| Prerequisite | Why |
|---|---|
| `az` CLI, logged in (`az login`) | provisioning; the tooling assumes an already-authenticated CLI, same pattern as `gh`. **`kmx` never downloads it**, unlike kind, kubectl and Helm: it is a Python distribution rather than one binary, it has a real package story on every platform, it must be signed in interactively anyway — and a tool that quietly installs the thing that then holds your cloud credentials is a different kind of surprise from one that drops a `kubectl` in a cache. |
| A subscription that can create resource groups, an ACR, and an AKS cluster | `--attach-acr` also needs permission to create a role assignment |
| `kubectl`, `helm`, `make`, `python3` | as for kind |
| `python3` with **PyYAML** | AKS-only, and the one prerequisite kind does not share: `scripts/plane-deploy.sh` parses `proxy.yaml` to render the registry image and pull policy. The kind branch returns before that import, so a kind-only user never needs it. `pip install pyyaml` |
| A GitHub Copilot subscription | AKS is Copilot-only: no Ollama is deployed there |

No Docker is needed for the AKS path: `az acr build` uploads the build
context and builds **in Azure**.

## From an empty subscription to a governed chat

Everything below is parameterised. Pick your own names. The ACR name
must be globally unique and alphanumeric. `AKS_CLUSTER` defaults to
`kaimahi` if you leave it unset.

```bash
export AKS_RESOURCE_GROUP=<your-rg>        # created by the script, deleted by it
export ACR_NAME=<globally-unique-name>     # 5-50 chars, alphanumeric
export AKS_CLUSTER=kaimahi-demo            # also the kube-context name
export AKS_LOCATION=westus3                # see "What this costs"
export TARGET=aks
# export AKS_NETWORK_POLICY=cilium         # the default; azure | calico also accepted
```

### 1. Provision: resource group + private ACR + AKS

```bash
kmx lift --step cluster \
  --resource-group "$AKS_RESOURCE_GROUP" --cluster "$AKS_CLUSTER" \
  --registry "$ACR_NAME" --location "$AKS_LOCATION"
```

This creates the group **tagged** `kaimahi-ephemeral`, a **private** ACR
(Basic, admin user disabled; never a public image), and a one-node AKS
cluster on the **Free** control-plane tier **with a NetworkPolicy
engine**, then grants the cluster's kubelet identity `AcrPull` and
writes the kubeconfig context.

It refuses to build inside a resource group it did not create, so a
mistyped group name cannot quietly scatter resources through someone
else's environment.

#### The policy engine is not optional

A bare `az aks create` builds a cluster whose CNI **ignores
NetworkPolicy**. The objects apply, `kubectl` lists them, and they block
nothing, which is the "worse than none" case [egress.md](egress.md)
warns about, and it is exactly what the first AKS run here had. The
script now always passes `--network-policy`, and refuses `none` or an
empty value outright rather than treating it as the operator's choice.

| `AKS_NETWORK_POLICY` | What it is | Why |
|---|---|---|
| `cilium` (**default**) | Azure CNI Overlay powered by Cilium: eBPF dataplane, `--network-dataplane cilium --network-policy cilium` | Microsoft's recommendation for new clusters, and the engine the other two are being retired in favour of. **Verified** enforcing the plane's whole matrix (below). |
| `azure` | Azure Network Policy Manager, iptables | Accepted for clusters that need it. Retiring: end of support on Linux is 2028-09-30. Not exercised here. |
| `calico` | Azure-managed Calico | Accepted. Not exercised here. |

All three ride Azure CNI Overlay (`--network-plugin azure
--network-plugin-mode overlay`): kubenet is retiring too (2028-03-31)
and Azure NPM never supported it. The script reads the engine back from
the control plane after the create and fails if it does not match, but
that only proves the flag took. Enforcement is a property of the CNI,
which the API server cannot vouch for, so `kmx lift` proves the boundary
before it deploys the plane.

**Existing clusters are not migrated.** If the cluster already exists
on a different engine, or on none, the script refuses and says what
the cluster actually has. `az aks update --network-policy` exists for
`azure` and `calico` but reimages every node pool at once, and moving
to Cilium is a dataplane upgrade with its own prerequisites; neither
belongs behind a script whose contract is "create". The cluster is
ephemeral, so the honest fix is `kmx lift down` and a fresh create.

Everything after this point acts on a **remote** context, so confirm once
for the session:

```bash
export KAIMAHI_CONFIRM=$AKS_CLUSTER
```

### 2. kagent

```bash
kmx --context "$AKS_CLUSTER" lift --step kagent \
  --resource-group "$AKS_RESOURCE_GROUP" --cluster "$AKS_CLUSTER" --registry "$ACR_NAME"
```

Identical to kind: the chart, the pins, and `k8s/kagent-values.yaml` are
the same. This is the portability claim in its plainest form.

### 3. The Copilot credential, **before** the plane, not after

```bash
kmx --context "$AKS_CLUSTER" models credential copilot
```

> **Order matters, and this is the one thing that bit us.** The proxy
> mounts `kaimahi-copilot-token` as an **optional** Secret volume. A proxy
> pod that starts before the Secret exists comes up with an empty mount,
> and every governed Copilot call then fails closed with *"upstream
> credential unavailable"* until kubelet gets around to projecting the
> new Secret, which on the verified run took minutes, long enough to look
> like a broken deployment rather than a race. Minting first means the pod
> mounts it at start and the first chat works.
>
> kind never hits this: its governed demo path is Ollama, which needs no
> upstream credential at all. If you *do* mint after deploying, don't wait:
> `kubectl -n kaimahi rollout restart deploy/kaimahi-proxy`. Rotation of
> an already-mounted token still needs no restart, as [spend.md](spend.md)
> says.

Custody is unchanged: the **real** Copilot token exists only as a Secret
mounted into the proxy pod, in the `kaimahi` namespace. The agent gets an
opaque `kmh_` token and never holds a provider key.

### 4. The governance plane, from the private registry

```bash
kmx --context "$AKS_CLUSTER" lift --step plane \
  --resource-group "$AKS_RESOURCE_GROUP" --cluster "$AKS_CLUSTER" --registry "$ACR_NAME"
kmx --context "$AKS_CLUSTER" govern hello-world
```

`plane-image` runs `az acr build` (built in Azure; no local docker build,
no `docker push`, no registry login on your machine), and
`scripts/plane-deploy.sh` renders `k8s/plane/proxy.yaml` with the ACR
image reference and a real pull policy. The committed manifest keeps
`imagePullPolicy: Never`, which is correct for kind and never edited here.

### 5. The agents

```bash
kmx --context "$AKS_CLUSTER" lift --step agents \
  --resource-group "$AKS_RESOURCE_GROUP" --cluster "$AKS_CLUSTER" --registry "$ACR_NAME"
kmx --context "$AKS_CLUSTER" tools govern --tools k8s_get_resources
```

On kind the agents start on the keyless Ollama preset and are switched
later. On AKS there is no Ollama, so they are created **on the governed
Copilot preset from the start**: governance is stood up before the
agents, not bolted on after.

### 6. Prove it

```bash
kmx --context "$AKS_CLUSTER" agent chat hello-world
kmx --context "$AKS_CLUSTER" ledger
kmx --context "$AKS_CLUSTER" budget hello-world --tokens 1
kmx --context "$AKS_CLUSTER" agent chat hello-world   # fails closed
kmx --context "$AKS_CLUSTER" agent chat hello-tools 'List the configmaps in the default namespace.'
kmx --context "$AKS_CLUSTER" audit tool hello-tools
```

`netpol-verify` is the step that makes the policy engine above a fact
rather than a flag. It runs the probe from [egress.md](egress.md): a
control pod that must reach everything, then an unlabeled pod in the
plane's namespace that must reach **nothing**. On `TARGET=aks` the probe
expects the proxy to reach the internet on 443, because the Copilot
allowance from step 3 is always applied there.

### 6b. Optional: the Slack loop, through a public edge

The one internet-reachable thing this repository demo can put on a cluster is the
inbound edge for the Slack Events hook: a Caddy pod with a Let's Encrypt
certificate on a load balancer whose public IP carries a DNS label you
choose. It is opt-in, AKS-only, and documented in
[inbound.md](inbound.md#putting-it-on-the-internet). This is checkout-only
repository orchestration with no binary equivalent; the Slack side
(`make slack-secret`, `make slack-mcp`, `make govern-slack`) is
[slack.md](slack.md). In short:

```bash
make slack-secret SLACK_CHANNEL=C0XXXXXXXXX && make slack-mcp && make govern-slack
make inbound-credential CRED_INBOUND=inbound-slack
make inbound-secret HOOK=slack-events                  # the app's Signing Secret, stdin
make slack-approvers && make notify-slack              # who may approve from Slack; the plane's own posting credential
make inbound-expose KAIMAHI_DNS_LABEL=<unique-label>   # prints the Request URL
make exposure-scan                                     # one IP, one port: 443
```

`KAIMAHI_DNS_LABEL` becomes `<label>.<region>.cloudapp.azure.com`. It
and the public IP are Azure identifiers like the others: never commit
them, redact them from evidence (`scripts/check-no-azure-ids.sh` now
refuses both shapes). When the cluster goes, the label is free for
anyone to claim: remove the Request URL from the Slack app when you
tear down.

### 6c. Optional: the accounts-payable demo

The [accounts-payable exception demo](ap-demo.md) is also checkout-only
repository orchestration. It runs here, and this is
where it is worth running: a real model doing the investigating, and a
real person approving the payment in Slack.

```bash
make erp          # az acr build the fixture ERP; project the corpus; roll it out
make govern-ap    # the AP agent, its credential, and its place in the policy
make ap-demo      SLACK_USER=<your Slack user id> AP_HUMAN=1
make ap-injection SLACK_USER=<your Slack user id> AP_HUMAN=1
```

Three things differ from kind, and only these three:

- **The ERP image is built by the registry.** `make erp` runs `az acr
  build` — the source is uploaded and built *in Azure*, so no image is
  built locally, pushed, or logged into a registry from your machine —
  and `scripts/erp-deploy.sh` renders `k8s/erp-mcp.yaml`'s image
  reference and pull policy for a registry target. The committed manifest
  keeps `imagePullPolicy: Never`, which is correct for kind and is never
  edited. Nothing is published: the image never leaves the private ACR.
- **The agent runs on Copilot.** `k8s/ap-agent.yaml` commits
  `governed-ollama` so the kind demo and CI stay keyless; `make govern-ap`
  patches the agent onto `$(GOVERNED_PRESET)`, which is `governed-ollama`
  on kind (a no-op) and `governed-copilot` here. Investigation of this
  kind is beyond `qwen2.5:3b`, so this is the first time the demo's
  narrative — read the invoice, the PO, the receiving record and the
  contract, then reconcile them — is carried by a model that can do it.
- **`AP_HUMAN=1` means a person really decides.** Without it, `SLACK_USER`
  synthesises a correctly signed `app_mention` as that id — right for CI,
  and a forgery against a real workspace. With it, the scenario prints
  each approval line, waits for that person to type it in the channel,
  and verifies the plane recorded *their* decision. See
  [ap-demo.md](ap-demo.md#approvals-in-slack).

Needs step 6b: the Request URL has to point at this cluster's edge for a
Slack message to reach the plane at all.

```bash
make ap-down      # the agent, the gateway seam, the ERP and its corpus
```

The installed-command path is the whole journey in one command:

```bash
kmx lift --resource-group "$AKS_RESOURCE_GROUP" --cluster "$AKS_CLUSTER" \
  --registry "$ACR_NAME" --location "$AKS_LOCATION"
```

`kmx lift` runs the phases in order. The credential comes
**before** the plane, for the reason in step 3.

### 7. Tear it down. This is not optional

```bash
KAIMAHI_CONFIRM="$AKS_RESOURCE_GROUP" kmx lift down
```

> **The confirmation names the RESOURCE GROUP, not the cluster.** The
> session-wide `export KAIMAHI_CONFIRM=$AKS_CLUSTER` from step 1 satisfies
> the *context* guard, and teardown deliberately does **not** accept it:
> deleting a whole resource group is a bigger act than applying to a
> context, and a standing "yes" to one cluster is not consent to destroy
> everything around it. If you forget, it refuses and prints the exact line
> to run, which is what happened on the verified run.

Deletes the resource group and everything in it, then removes the
kubeconfig entries so a dead context cannot be targeted later. Two gates
stand in front of `az group delete`, which is recursive and irreversible:

1. **Tag proof**: the group must carry the `kaimahi-ephemeral` tag that
   `aks-up.sh` sets. A group this tooling did not create **cannot** be
   deleted by it at all. This is what makes a typo'd group name harmless
   rather than catastrophic.
2. **Explicit confirmation** naming the group.

It waits for completion and then re-checks that the group is gone, because
*"I asked Azure to delete it"* is not the same claim as *"it is gone"*.

`kmx lift down` is the same thing with the same two gates:

```bash
KAIMAHI_CONFIRM=<your-rg> kmx lift down --resource-group <your-rg> --cluster <name>
```

### Teardown on a cluster you did not create

**The rule is the opposite, and it is absolute: your cluster and your
resource group are never deleted and never adopted.** `kmx lift down
--byo` removes only what the lift added — the two monitoring workspaces,
the data-collection rules the add-ons created, the workbook, and the two
objects it put in the `kaimahi` namespace (the scraper's NetworkPolicy
allowance and the plane's `PodMonitor`) — and it removes the Azure ones
**by the resource id it recorded when it created them**, never by name.

The distinction is not pedantry. Name matching on a subscription you do
not own is how a demo deletes a stranger's production monitoring: names
collide and get re-used, resource ids do not. So every resource the lift
creates gets a **run-scoped unique name** *and* has its id recorded, and
teardown:

- deletes a resource only when its recorded id still resolves to that
  same resource;
- **fails closed** when the id cannot be re-resolved, or when the name
  now resolves to something with a different id — it leaves the resource
  alone, names it, prints its id and what it costs, and exits non-zero;
- keeps the run record when anything is left behind, so it can be
  re-run once you have looked.

The record lives in `kmx`'s own state directory (`$KMX_HOME`, else your
user config directory), never in a checkout — it contains resource ids,
which contain a subscription id, and a file written into a working tree
is a file that eventually gets committed. **If you lose the record, `kmx
lift down` refuses rather than going looking by name.** Remove the
resources by hand from the portal in that case.

Three more refusals worth knowing, all on the bring-your-own branch:

- **Monitoring you already had is not taken over.** If Managed Prometheus or
  Container Insights is already enabled when the lift arrives, the
  observability phase stops rather than proceeding. It will not repoint your
  telemetry into a workspace this run owns and later deletes — and it cannot
  point the dashboard at the workspace you already use, because the cluster
  does not report which one that is for metrics. Proceeding would create
  workspaces nothing sends to, wire a dashboard to them, and then report the
  scrape as broken. Skip the phase with `--observability=false`, or disable the
  add-on first if you meant this run to own it. Teardown follows the same rule
  from the other end: an add-on that was on before the run is left on.

- **The cluster-wide scrape ConfigMap is not touched at all.**
  `ama-metrics-prometheus-config` is cluster-wide and singular, so it
  holds *your* scrape jobs as well as anyone else's. The plane's job is a
  `PodMonitor` in the `kaimahi` namespace instead, and the lift neither
  reads, writes nor deletes that ConfigMap. Your own jobs go in your own
  `PodMonitor`, in your own namespace — see [the scrape
  job](#the-scrape-job-is-a-podmonitor-and-yours-can-sit-beside-it).
- **`AcrPull` is not granted.** The lift checks whether your cluster can
  pull from the registry you named and refuses if it cannot. Granting a
  role assignment on your subscription is a change to your cluster's
  identity, and a demo has no business making it silently.

### What keeps billing after the demo ends

On a cluster **this path created**, nothing: it is all inside one
resource group, and `az group exists` returning `false` is a complete
proof.

On **your** cluster, an empty-resource-group check proves nothing,
because the group is not ours to delete. These are what the lift adds
that continue to cost you until they are removed:

| Resource | What it charges for |
|---|---|
| Azure Monitor workspace (Managed Prometheus) | per sample ingested and per query; retains samples for 18 months |
| Log Analytics workspace (Container Insights) | per GB ingested, plus retention beyond the included period — this is the larger of the two, and it keeps costing while anything still sends to it |
| Data collection rules and endpoints | no standing charge of their own; they are what routes data to the workspaces, which do charge |
| The workbook | nothing. A workbook is a saved set of queries. |

The two add-ons themselves are free; what they collect is not. Turning
them off (which `kmx lift down --byo` does first, before deleting
anything they point at) stops the ingestion charge immediately;
deleting the workspaces stops the retention charge.

## What this costs

Measured choices, not guesses (Azure retail prices API, 2026-09-01):

| Item | Choice | Why |
|---|---|---|
| Control plane | **Free tier** | $0, no SLA. Right for an ephemeral demo |
| Node | **1 × `Standard_B4ms`**, $0.166/hr | The live kind cluster's non-Ollama workload measures ~695m CPU of requests. A 2-vCPU AKS node has only ~1.2 CPU left after system overhead, so `B2ms` ($0.0832/hr) fits but leaves no room for a rollout surge. One scheduling stall costs more than the 8¢/hr saved. Both are `AKS_NODE_SIZE`. |
| Region | **`westus3`** | Ties the cheapest US price for this SKU (westus2 is identical; southcentralus is ~20% more) and had the most regional-vCPU headroom in the subscription used. |
| Registry | **ACR Basic** | ~$0.167/day; supports ACR Tasks, which is what `az acr build` needs |
| Load balancer | AKS default (Standard) | ~$0.025/hr; created for egress even with no `LoadBalancer` Service |
| Public IP (edge, optional) | Standard static, with a DNS label | ~$0.004/hr; only while `make inbound-expose` is up |
| Edge certificate volume (optional) | 1 GiB PVC | provisioned on the default StorageClass and billed at the smallest disk tier (E1, 4 GiB, a few cents a day); deleted with the edge |
| Disks | 32 GiB OS disk + the 1 Gi Postgres PVC | rounded up to Azure's minimum billable sizes |

A run of a few hours is **well under US$2**. The first verified run
existed for about 29 minutes (17:52–18:22 UTC) and cost roughly
**US$0.10**; the NetworkPolicy run existed for about 26 minutes
(22:39–23:05 UTC) and cost about the same. Cilium adds no line item.
The dominant risk to the bill is not the rate. It is forgetting step 7.

## Observability, and the one thing it must not trade away

`kmx lift` wires two Azure-managed data paths by default. They are
separate on purpose, because they fail separately and an operator needs
to be able to tell which one is broken:

- **Managed Prometheus** scrapes the plane's own `/metrics` into an
  Azure Monitor workspace.
- **Container Insights** collects the plane's stdout and stderr into a
  Log Analytics workspace.

Both workspaces are created **inside the same resource group as the
cluster**, so on the branch that creates that group, deleting it
accounts for them too.

### The ops port stays on no Service

The plane's operations port (9092 — metrics, readiness, liveness) is on
**no Service**, deliberately: reaching it takes either kubelet
(node-originated, which NetworkPolicy does not govern) or an explicit
NetworkPolicy allowance. The port carries **no authentication**, so that
allowance *is* the access control.

The obvious shortcut — put `/metrics` on a Service so a scraper can find
it — trades a security property for a dashboard, and it is not taken
here. Instead the scrape job uses **pod** service discovery, which
reaches the port at the pod's own address exactly the way the design
intends, and `k8s/observability/network-policy.yaml` opens 9092 to one
namespace, one pod label and one port:

```yaml
from:
  - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: kube-system } }
    podSelector:       { matchLabels: { rsName: ama-metrics } }
ports: [{ protocol: TCP, port: 9092 }]
```

`rsName: ama-metrics` is the add-on's **replica** pod, which is what runs
custom scrape jobs; its per-node DaemonSet runs only the default targets
and is deliberately not allowed, since that would open 9092 on every
node for a scrape that never comes.

This file lives outside `k8s/plane/` for the same reason the Copilot and
hosted-upstream egress allowances do: an allowance arrives when the
thing it is for is enabled, and not before. kind never applies it, which
is also what makes "the local path is unchanged" a fact rather than a
claim.

### The scrape job is a PodMonitor, and yours can sit beside it

`k8s/observability/podmonitor.yaml` is the job. It keeps only pods
labelled `app: kaimahi-proxy`, and only the container port **named
`ops`** — without that, pod discovery would also try the two data ports,
the inbound port and the **admin** port, which should never be dialled
by anything but a port-forward.

It is a `PodMonitor` in the `kaimahi` namespace rather than a job in the
`ama-metrics-prometheus-config` ConfigMap, and the difference is the
whole reason this section changed. That ConfigMap is **cluster-wide and
singular**: every custom scrape job on the cluster shares one document.
Anything that writes it either overwrites jobs it did not make or stops
and asks you to merge by hand, and a teardown that deletes it removes
jobs it never made. `kmx lift` now **does not read, write or delete that
ConfigMap at all** — a test asserts it, since the failure mode is
somebody else's monitoring going quiet.

So adding your own pods takes nothing from this repository. Write your
own `PodMonitor` in your own namespace:

```yaml
apiVersion: azmonitoring.coreos.com/v1   # Azure's group, not monitoring.coreos.com
kind: PodMonitor
metadata:
  name: my-agent
  namespace: my-app
spec:
  selector:
    matchLabels:
      app: my-agent
  podMetricsEndpoints:
    - port: metrics       # a NAMED container port on your pod
      path: /metrics
      interval: 30s
  labelLimit: 63
  labelNameLengthLimit: 511
  labelValueLengthLimit: 1023
```

Five things that are easy to get wrong, and each fails silently:

- **The API group is `azmonitoring.coreos.com`, not
  `monitoring.coreos.com`.** Azure's add-on ships its own copies of the
  operator CRDs under its own group precisely so a cluster already
  running the open-source Prometheus operator keeps two separate sets of
  jobs. Under the wrong group the object is still valid and still
  applies; it is simply never scraped.
- **`port` names a port, it does not number one.** Give the container
  port a `name` in your Deployment.
- **Keep the three limits.** Azure's collector drops an entire job whose
  series exceed them, and the symptom is an empty panel rather than an
  error.
- **A `PodMonitor` sees only its own namespace unless you say
  otherwise.** With no `spec.namespaceSelector` the generated discovery
  is scoped to the namespace the CR is in, so one written in a shared
  `monitoring` namespace and pointed at pods in `my-app` applies cleanly
  and scrapes nothing. Put it beside the pods, or set
  `namespaceSelector.matchNames`.
- **A default-deny NetworkPolicy in your namespace will block the
  scrape.** The allowance above is scoped to the plane's pods; yours
  needs its own, admitting `rsName: ama-metrics` from `kube-system` to
  your metrics port. That is the same trade the plane makes and the
  reason it is explicit.

Custom resources are read from **every** namespace and are scraped by
the same `ama-metrics` replica pods the plane's allowance already names,
so nothing about the boundary changes when you add one.

**One thing to know before you rely on it, measured rather than
inferred.** The `PodMonitor` *kind* belongs to the metrics add-on: the
add-on installs the custom resource definition, and disabling the add-on
takes that definition away — which takes every `PodMonitor` on the
cluster with it, whoever wrote them. After
`az aks update --disable-azure-monitor-metrics`, `kubectl get
podmonitors.azmonitoring.coreos.com` answers *"the server doesn't have a
resource type"*, and the objects are gone. This is Kubernetes collecting
custom resources whose definition has been removed; nothing can disable
the add-on without it. `kmx lift down --byo` therefore **names your
PodMonitors before it turns the add-on off**, so that "my scrape jobs
disappeared" is never something you have to work out afterwards. Your
manifests are untouched — re-apply them once the add-on is back.

If your cluster's metrics add-on is old enough to have no PodMonitor
CRD, the observability phase does **not** stop — the workbook and the log
path are unaffected by this, and stopping would cost you both. It prints
the same job in ConfigMap form and carries on, and the `verify` step then
reports that the metrics half is not arriving. Merging that job is left
to you on purpose: the ConfigMap is your document and holds everyone
else's jobs.

### The dashboard

An Azure **workbook** is deployed into the resource group, named
`Kaimahi governance plane (<run id>)` and pinned to the cluster: find it
under **Monitoring → Workbooks** on the cluster in the portal. It reads
both data paths, so an empty metric panel and an empty log panel mean
different things.

It is committed as an ARM **template**, not as an exported workbook,
because exporting one bakes the subscription id, the resource group and
the workspace ids into its serialized data as literals — which this
repository refuses to carry. Every identifying value is a parameter, and
the workbook's own name (which must be a GUID) is derived with `guid()`
at deploy time rather than committed.

The panels: decisions per second by outcome and reason; what was refused
in the last hour and why; **seam degraded**, which is the alarm — 1
while a seam refuses everything because its audit or ledger write last
failed; upstream latency; queue occupancy; which build is answering; and
the plane's recent log lines and pod restarts from Container Insights.

### Enabled and arriving are different claims

The `verify` phase does not stop at "the add-on is on". It queries
Managed Prometheus for `kaimahi_build_info` — a series the plane sets at
startup, so it exists whether or not anyone has used the system yet —
and it queries Log Analytics for an actual log line from a plane pod.
Each waits, because the two have very different latencies (a scrape is
30 seconds; Container Insights routinely takes several minutes to become
queryable), and each fails with the specific things to check.

A panel that is empty because nobody has used the system is otherwise
indistinguishable from a scrape that is not landing, and that confusion
is the whole reason this check exists.

### What this view covers, and what it does not

Say it plainly, because the gap is easy to walk into and expensive to
discover late.

**What you get here is what crossed the governance plane.** Every model
call and every tool call that went through a seam: what was allowed,
what was refused and for which reason, what a human approved, and what
it spent. That is the audit trail, the ledger and the panels above, and
it is true of an agent this project has never seen — it needs no library,
no exporter and no line of instrumentation in your code, because the
plane is in the path.

**What you do not get is what happened inside your agent.** No spans, no
per-step timings, no prompt-level traces, no view of the reasoning
between one governed call and the next. Nothing here can see them; the
plane observes a boundary, not a process.

**OpenTelemetry is the answer to that half, and this does not replace
it.** If your agent already exports traces to an OTLP endpoint, keep
doing exactly that — the two do not conflict and do not need to know
about each other. Container Insights is namespace-agnostic, so your
pods' logs land in the same Log Analytics workspace as the plane's
whether or not you add a scrape job, which is often enough to correlate
the two by timestamp and pod.

### Observability belongs to the lift

Observability is part of `kmx lift`. Running individual repository scripts
does not configure it and leaves `/metrics` on a cluster-internal port with
nothing reading it.

## What differs from kind

| | kind | AKS |
|---|---|---|
| **Model** | Ollama `qwen2.5:3b`, keyless, in-cluster | **Copilot only.** No Ollama is deployed; `kmx lift` does not half-deploy it. |
| **Plane image** | `docker build` + `kind load`, `imagePullPolicy: Never` | `az acr build` into a **private** ACR, pulled via the kubelet identity's `AcrPull` |
| **Demo ERP image** | the same: `docker build` + `kind load`, `imagePullPolicy: Never` | the same as the plane's: `az acr build` into that private ACR, pulled by the same identity. Never published either way |
| **Agent's initial model** | starts on the keyless preset, governed later | created **on** `governed-copilot`; governance precedes the agents |
| **Storage** | the kind default `standard` provisioner | the cluster's default StorageClass, which on AKS 1.35.7 is one literally **named `default`** (`disk.csi.azure.com`), *not* `managed-csi`, which also exists but is not marked default. The PVC deliberately sets **no** `storageClassName`, so it takes whichever class the cluster defaults to; it bound `1Gi RWO` first try. Verified, not assumed: the assumption going in was `managed-csi`. |
| **NetworkPolicy** | enforced by kindnetd (kube-network-policies), nothing to configure | enforced **only** because the lift provisions and proves a policy engine (Cilium by default). A cluster created without one applies the same manifests and blocks nothing. |
| **Mutating commands** | proceed with a banner | require confirmation naming the context |
| **Teardown** | `kmx down` deletes the kind cluster | `kmx lift down` deletes the whole tagged resource group |
| **Slack** | demonstrated ([slack.md](slack.md)) | **opt-in** (step 6b). It is the only way an approval can come from a real person, so the accounts-payable run needs it; a run that does not need it should leave it off, because a real workspace token in a temporary cloud cluster is credential exposure for no added proof. |
| **Approvals** | admin bearer, or a **synthetic** signed `app_mention` | a person typing in Slack (`AP_HUMAN=1`). The synthetic path is a forgery here and the scenarios refuse to pretend otherwise |
| **Cost** | free | see above |
| **CI** | every PR | never. No Azure credential belongs in a public, fork-exposed repo. |

Two smaller carry-overs, recorded rather than hidden:

- The `ollama` entry stays in the committed upstream table on AKS,
  pointing at a Service that does not exist there. Nothing calls it (the
  agents are on `governed-copilot`), and a governed-ollama request would
  fail closed at the proxy. It is left in place because the upstream
  table is a committed, environment-independent artifact.
- Node SSH access is left at the AKS default. `--ssh-access disabled` is
  the hardening step; it is not taken here because the cluster is
  short-lived and the flag's availability varies by CLI version. Worth
  taking for anything longer-lived.

## Working two clusters at once: move the local ports

`kmx agent chat` asks kubectl for a free loopback port, so concurrent chats do
not collide. Action-oriented `make slack-post` still uses fixed `8083`
(`CHAT_PORT`), kmx admin commands use `19091` (`ADMIN_PORT`), and each probe
has its own `GATEWAY_PORT` default:
`tool-denial-probe.sh` `18081`, `tool-call-probe.sh` `18082`,
`tool-admit-probe.sh` `18083`. Running a kind and an AKS verification
concurrently makes the second bind lose, and its requests land on the
*other* cluster's forward. Override per cluster:

```bash
CHAT_PORT=8183 kmx --context kind-kaimahi-p1 agent chat hello-world
CHAT_PORT=8283 make slack-post                      # fixed action helper
ADMIN_PORT=19291 kmx --context "$AKS_CLUSTER" approvals
GATEWAY_PORT=18281 bash scripts/tool-denial-probe.sh k8s_get_events
```

`ADMIN_PORT` is what kmx's admin commands read; `GATEWAY_PORT` is read only by the probe
scripts, which are run directly rather than through a target; `CHAT_PORT` is
optional for chat and still required to move the legacy action helper.

**The two collisions behave differently, and one used to be silent.** An
`ADMIN_PORT` clash fails closed with a flat `HTTP 401 unauthorized` (the
other cluster's admin token does not match): safe, though the message
does not name the cause. A `CHAT_PORT` clash had no such protection: the
kagent controller on that forward is unauthenticated, so the task quietly
ran on the wrong cluster and returned a plausible reply. `kmx agent chat`
waits for its own forward and **refuses** if it did not come up, naming
the port. `--context` cannot help here; the aiming happens at the
socket, not at kubectl.

## What was verified, and what was not

### The lift, verified live on 2026-09-06 (two clusters, both torn down)

This section is the record of that run and is not re-edited to match later
changes. It predates two of them: the scrape job was still a job inside the
`ama-metrics-prometheus-config` ConfigMap rather than a `PodMonitor`, and the
observability phase had a defect — a `kubectl` call with no verb — that was
introduced in a review follow-up on the same pull request, after this run,
and that blocked the phase in every build that shipped it until it was fixed.

On a cluster **`kmx lift` created** (1 × `Standard_B4ms`, westus3, Cilium):

- `netpol-probe` reported **"boundary enforced as written"** — the
  existing negative matrix, run unchanged, against a live boundary;
- the plane's image built by `az acr build` **in Azure**, pulled from the
  private registry, and the agent answered through governed Copilot, with
  a ledger row (`331` in, `213` out, status `200`);
- **Managed Prometheus returned 12 series of `kaimahi_decisions_total`**,
  both proxy replicas distinguished by the `pod` label, and
  `sum by (seam, decision, reason) (…) > 0` gave `{proxy, allowed, ok} = 1`
  — the chat above, as a metric;
- **Container Insights returned real plane log lines**, including
  `gateway: projected tools/list credential=hello-tools`;
- the scraper's own reachability, checked directly: `curl` from the
  `ama-metrics` pod to the plane's ops port returned 170 `kaimahi_`
  metric lines, which is the NetworkPolicy allowance working;
- the whole one-command run took **14m22s** with the cluster already
  there (first cluster creation is a further ~4m; the longest phase by
  far is enabling the two monitoring add-ons).

On a cluster **`kmx lift` did NOT create**, in a resource group it did
not create:

- a cluster with **no policy engine was REFUSED in 2.6s**, before
  anything was written — verified afterwards: the cluster still had only
  its four default namespaces, no `kaimahi`, no `kagent`;
- **`AcrPull` was refused, not granted**, naming the exact
  `az aks update --attach-acr` for the owner to run;
- with an enforcing engine and pull rights granted by hand, the same
  path ran, and `netpol-probe` again reported the boundary enforced.

**Not verified**: that the workbook's Prometheus panels render in the
portal. Every query in it was run against the live workspaces through
the API and returned data, and the workbook resource deployed cleanly,
but nobody opened it in a browser. The `azure` and `calico` policy
engines remain unexercised, as does more than one node.

### Earlier runs

Verified live on a real AKS cluster on 2026-09-01 (Kubernetes 1.35.7,
1 × `Standard_B4ms`, westus3; evidence in the PR that shipped it, with
Azure identifiers redacted):

- the proxy image built by `az acr build` **in Azure** and pulled from the
  private ACR, with `imagePullPolicy: IfNotPresent` rendered at deploy time
  while the committed manifest still says `Never`;
- the Postgres PVC binding `1Gi RWO` on the cluster's default StorageClass;
- a governed **Copilot** chat completing, and its ledger row:
  `hello-world copilot gpt-5-mini 335 357 0 unpriced 200`;
- a budget denial failing closed: `CAP_TOKENS=1`, the task does **not**
  complete, three `denied 429` rows ledgered, month-to-date unchanged;
- a real tool call through the enforcing MCP gateway
  (`k8s_get_resources allowed 200`), proven with the probe-ConfigMap
  pattern from [tools.md](tools.md) so the answer can only come from a
  live invocation;
- custody intact: the agent-side Secret matches `^kmh_[0-9a-f]{64}$` while
  the real Copilot token stays in the `kaimahi` namespace;
- teardown: resource group deleted, and re-checked gone. 0 clusters, 0
  registries, 0 kubeconfig contexts left.

Verified live on a second AKS cluster the same day (Kubernetes 1.35.7,
Azure CNI Overlay, **Cilium 1.18** as dataplane and policy engine, 1 ×
`Standard_B4ms`, westus3; the full redacted probe output is in the PR
that shipped it):

- `aks-up.sh` provisioning with `--network-policy cilium` and reading
  the engine back from the control plane, then, on a re-run, taking the
  existing-cluster path and accepting the cluster because its engine
  matched;
- the whole journey, then driven by the repository's Make orchestration:
  kagent, the Copilot Secret, the plane from the private ACR, governance,
  both agents;
- `TARGET=aks make netpol-verify`: **boundary enforced as written**. The
  unlabeled pod in the plane's namespace, which is the enforcement check
  itself, was blocked on DNS, Postgres, 443 and 80; the proxy-shaped
  pod reached DNS, Postgres and 443 (the Copilot allowance) and was
  blocked on 80; the Slack-shaped pod reached DNS and 443 only; the
  real Postgres pod reached its own loopback and nothing else. The
  ollama column is skipped on this target, with a note, because no
  ollama Service exists there;
- a governed Copilot chat completing **through** that boundary
  afterwards, and its ledger row (`hello-world copilot gpt-5-mini 335
  239 0 unpriced 200`);
- teardown, re-checked gone.

Verified live on a third AKS cluster on 2026-09-02 (Kubernetes 1.35.7,
Cilium 1.18, 1 × `Standard_B4ms`, westus3, about three hours, roughly
US$0.70), the Slack loop through the public edge
([inbound.md](inbound.md#slack-events-the-loop)):

- `make inbound-expose`: a Let's Encrypt certificate by TLS-ALPN-01 on a
  DNS-labelled public IP; `make exposure-scan`: exactly one open port
  (443) on one public IP, none on the cluster's other public IP, one
  LoadBalancer Service cluster-wide;
- Slack's challenge answered; a real `app_mention` refused 403 and
  filed; approved bounded; the next mention admitted, the agent's reply
  posted in the thread through the gateway under a tool grant; every
  step in the inbound audit, the ledger, the tool audit and the approval
  audit;
- `make netpol-verify` with the edge's policies present: boundary
  enforced as written;
- the Slack app un-pointed (Request URL and subscription removed) before
  the edge and the resource group were deleted, re-checked gone.

Verified live on a fourth AKS cluster on 2026-09-03 (single node,
westus3, roughly US$0.35), the accounts-payable demo through the
registry path ([ap-demo.md](ap-demo.md)): the fixture ERP built by
`az acr build` and deployed from the private registry, the agent denied
on the exception and approved by a named person for that transaction,
and the resource group deleted afterwards and re-checked gone.

Two things about that run are recorded rather than smoothed over. The
lane found a defect only a managed cluster could surface — the
accounts-payable agent's manifest pinned a preset that does not exist on
a Copilot-only cluster, so its governance step would have waited for
Ready until it timed out — and `make ap-injection` did not complete
verbatim: one attempt elapsed its 30-minute approval window and **failed
closed, claiming nothing**, and by the next a live grant for the
legitimate call existed, which makes the script's opening assertion (that
the call is denied) impossible to reach. The scenario's substantive half
was driven by hand with that script's own probes and arguments, and every
assertion still reachable was checked. The opening denial assertion was
not among them — a live grant for the legitimate call had made it
unreachable, which is what the third attempt ran into — so that one is
covered by CI on kind rather than by this run. The script is unchanged and
CI runs it end to end on every PR.


The multi-node caveat in [egress.md](egress.md) was not exercised: all
runs were single-node, which is the script's default.

Also verified: `aks-down` **refuses** a resource group that lacks the tag
`aks-up.sh` sets, even when given a correct confirmation. Tested against a
throwaway untagged group, which survived.

**Not** verified on AKS: Ollama (deliberate), the `azure` and `calico`
policy engines (accepted by the script, never run), certificate renewal
(the edge lived hours; Let's Encrypt renews at day 60), and anything
about durability, upgrades, node replacement or multi-node scheduling.
Each cluster existed for well under a day and was deleted.

## Limitations

The full governed-vs-ungoverned table is in
[README.md](README.md#what-is-governed-today-and-what-is-not). Specific
to this path:

- **Demonstrated, not maintained.** Nothing re-proves the cloud
  run; only the portability logic runs in CI.
- **Copilot only.** No keyless model on AKS, so no free tier there.
- **Slack on AKS is the inbound-loop demo only**, on a cluster deleted
  the same day; the workspace token is not meant to live in a cloud
  cluster longer than that.
- **The edge is the only public surface, and it is opt-in.** Neither `kmx
  lift` nor `kmx plane` creates a LoadBalancer. The checkout-only repository
  probe `make exposure-scan` checks that this stayed true after deploying the
  inbound demo.
- **The AKS cluster is not hardened** beyond a private registry, a
  tagged, ephemeral resource group, and the plane's NetworkPolicy
  boundary: default node SSH access, no durability story, and the
  `kagent` and `ollama` namespaces are as unpoliced as on kind. It is a
  demo that should be torn down the same day.
- **Only Cilium is verified.** `azure` and `calico` are accepted by the
  script because the flag is the same shape, but no run here has proven
  either enforces the matrix. Run `make netpol-verify` before trusting
  one.
