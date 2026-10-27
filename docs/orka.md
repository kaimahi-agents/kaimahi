# Installing Orka, and what installing it does not do

`kmx cluster orka install` puts [Orka](https://github.com/orka-agents/orka) —
"Cloud and AI-native multi-agent orchestration platform for Kubernetes" — on
the cluster kmx is pointed at, in one command, with no API key.

It exists because Orka assumes a cluster and this project starts without one.
At [Orka v0.2.0](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md),
upstream installs a Helm chart and generates its own encryption key and webhook
certificate. kmx fetches the pinned release chart rather than requiring an
Orka checkout or a manually created wrapper credential.

Installation does not author an Agent or run a Task. Author a native Orka Agent with
[native workspace authoring](suite-workspace.md), full eval, publication and digest Deploy.
Installation and authoring are deliberately separate operations.

## The command

```console
$ kmx cluster orka install
```

The install checks each boundary before proceeding:

| phase | what it does |
|---|---|
| Fetch the pinned chart | downloads `orka-0.2.0.tgz` from the v0.2.0 release and refuses bytes that do not hash to kmx's pinned SHA-256 |
| Check for an existing installation | refuses an existing controller Deployment, Orka CRDs or a foreign/partial Helm release rather than attempting an upgrade; a pre-created `orka-system` namespace containing only Secrets or ServiceAccounts is allowed |
| Apply CRDs and install | applies CRDs extracted from the verified chart before `helm install orka` with `--kube-context`, `controller.mode=harness-v2`, `fullnameOverride=orka-api` and `--wait`; no `helm upgrade --force` |
| Provision result reader and keyless Provider | installs the namespace-scoped Task-get result account, then a `Provider` pointing at the in-cluster model server, so no model key is needed on the local path |

Check the selected cluster with `kmx cluster orka status` and
`kubectl --context <ctx> -n orka-system get deployments,pvc`. The chart's
controller Deployment is `orka-api-controller`; status lists the running
Deployments, CRDs and Provider readiness separately from kmx's v0.2.0 pin.
Do not mistake a pinned version for proof of a running version, or a
ready Deployment for a completed Task. Status is read-only and does not
authorize reinstallation over an older release.

## Seeing it before doing it

Both flags the other writing commands carry, for the same reasons:

```console
$ kmx cluster orka install --no-apply     # fetch, verify chart and count its CRDs; write nothing
$ kmx cluster orka install --dry-run      # verify chart, check existing install, server-dry-run its CRDs
COMPLETE  Validated; nothing was written
```

`--dry-run` validates the chart CRDs at the API server, not the entire Helm
render or chart hooks. It writes nothing and cannot prove chart-generated Secrets,
controller readiness or Task execution. `--no-apply` does not check cluster
compatibility.

## The three things worth knowing

### The bytes are theirs, and the pin is ours

kmx fetches the [v0.2.0 release chart](https://github.com/orka-agents/orka/releases/tag/v0.2.0)
(`orka-0.2.0.tgz`) and verifies its SHA-256 against the digest pinned in kmx
(`b7596c4e35d7189a3b2cf25921cd50e6c31328dbb83bdee50e20c757e0f03b79`)
before extracting CRDs or passing it to Helm. A changed or corrupted chart is
refused, not installed. This kmx-maintained digest is not an independent
publisher signature. Offline agent creation separately embeds CRD schemas;
those fixtures do not install Orka. Installing the chart requires a download.

There is no `--version` flag. A version flag would either carry no
verification or need a digest per version; moving the pin is an edit to this
repository that somebody reviews.

### Preserve the chart's generated snapshot key

The [v0.2.0 installation guide](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md#2-check-the-installation)
explains that the controller generates the snapshot encryption key and writes
it to a Secret. With kmx's `fullnameOverride=orka-api`, the Secret is
`orka-api-agent-execution-snapshot` (not the upstream default
`orka-agent-execution-snapshot`). Back up the Secret **with the controller's
persistent volumes and Orka resources** before any retirement, restoration or
cluster replacement. Never print or paste its value into a ticket, terminal
transcript or documentation. This key is needed to read saved execution
records after restoring the volumes. Orka also creates its webhook TLS Secret;
never run `helm upgrade --force`, which upstream warns can replace generated
Secrets with empty chart versions. kmx neither prints nor rotates the key.

### The Provider connects the local model without a key

Orka v0.2.0 does not require an LLM API key to install; model calls need a
Provider and an endpoint the cluster can reach. Its `Provider` type accepts
`baseURL` — "an optional custom API endpoint (for proxies or self-hosted)" —
so kmx creates one of `type: openai` pointing at the keyless in-cluster model
server `kmx cluster up` already deployed:

```yaml
spec:
  type: openai
  baseURL: http://ollama.ollama.svc.cluster.local:11434/v1
  defaultModel: qwen2.5:3b
  secretRef: { name: local-provider-key, key: api-key }
```

The `secretRef` is required by their schema even where the endpoint needs no
key, so a placeholder is written and named for what it is.

`--provider -` skips this entirely. If there is no in-cluster model server,
the command refuses rather than creating a Provider that resolves nothing —
an adopter should learn that here, not at their first model call.

## Author an Orka Agent and get an answer

[Root create](kmx.md#kmx-create) authors native workspace source offline. Prepare
the runtime/model/Ready openai Provider and result access separately; create
never bootstraps them. Review authored cases, run full native eval, publish the
exact graph with matching private evidence and Deploy its digest. See the
[workspace lifecycle](suite-workspace.md). Suite status/retirement are unavailable.

Quickstart composes setup and this native lifecycle, verifies placement and then
sends a separate fresh answer Task. Console Ready is placement only. Live Orka
Agents, including suite-owned Agents placed by Deploy, can use `run --agent` and
chat with the selected context/namespace/name. Neither is workspace-local
execution or fresh suite evaluation. Ownership fences still prevent legacy
adoption, copying and retirement. `task result` reads existing Task phase/answer
without resending.

### Runtime and result access

Result reads require a separately provisioned namespaced Task-get account.
KMX installation/local setup provisions `orka-result-reader`; upstream-installed
runtime owners must arrange it separately. Tokens carry the account's effective
authority and discarding one is not revocation. Orka v0.2.0 Task-read
enforcement is unverified; v0.1.3 authenticates result reads only. A namespaced
Task-get grant is therefore required but does not by itself prove server-side
enforcement for Task execution. Results use a context-pinned loopback
forward/connection and stop on connection loss, not resubmit. Ready or admission
alone does not prove model execution or result-read access. Normal successful
commands print only a short pointer to this boundary; authorization and
transport failures remain explicit errors.

## The whole journey, from nothing

```console
$ kmx cluster quickstart                            # a cluster, model, Orka Agent and fresh Task answer
```

Use `kmx cluster up` instead to install the runtime without creating an Agent, then
follow the [native create guide](#author-an-orka-agent-and-get-an-answer).
Both setup paths install Orka, so neither needs a separate `kmx cluster orka install`.

## Authoring an agent for Orka

Installing Orka and authoring an agent are separate steps. The
native workspace path prepares native Orka resources; installing Orka does not
convert existing files written for another runtime's API group.

For a new agent that will run on Orka, **author Orka's native
`core.orka.ai/v1alpha1` `Agent` and `Provider` resources and invoke it with
an Orka `Task`.** This keeps the runtime configuration explicit: changing a
legacy resource's API group would not translate its referenced model
credentials, MCP connections or workload settings. Dropping those settings
would not preserve the agent, and this project provides no supported
translation layer. Native authoring is the recommendation for that reason,
not because another authoring format could never target Orka.

Use the schemas and examples from the Orka version you install, rather than
assuming that its `main` branch describes the release pinned here. Include
the Provider and its named credential Secret, not just the Agent; an accepted
Agent manifest alone does not prove it can reach its model.

For an application image you already operate, keep its Deployment under your
own management. KMX does not convert it into an Orka Agent or redirect its model
traffic. Configure its runtime and model access through the upstream interfaces.

## Limits, stated

- **Existing v0.1.3 reads remain supported.** Deploy, status and the console
  discover the controller by its Deployment role labels (legacy
  `control-plane=controller-manager`, chart
  `app.kubernetes.io/component=controller`), not by a release-specific name.
  No or multiple matching controllers is an explicit refusal; this does not
  authorize an installation or an in-place upgrade. Evaluate and console result
  sessions discover the controller's API Service by role selector and port, so
  they also work with a stock v0.2.0 chart release named `orka` instead of
  kmx's `orka-api`. Result reads retain their own API and RBAC checks.
- **Rate limits are not supported by v0.2.0 CRDs.** Orka removed
  `Provider.spec.rateLimit` and `Agent.spec.rateLimit`; kmx does not silently
  drop requested limits. Native workspace profiles reject unsupported fields;
  live create schema-selection/previews and old bundle readers are removed.
  Historical limit-bearing resources need operator review before a fresh v0.2.0
  target; there is no equivalent rate-limit mapping in kmx yet.
- **One pinned install.** `v0.2.0` chart, harness-v2, release `orka`, namespace
  `orka-system`, fullname `orka-api`. A recognized matching kmx installation
  keeps its chart, key and data on rerun only when its image overrides match
  the pin and its controller is Ready. `kmx cluster orka install` also ensures the
  read-only Task result account. An existing namespace alone is not an Orka
  installation; kmx refuses a legacy controller/CRD or foreign Helm release,
  not a pre-created `orka-system` namespace containing only Secrets or ServiceAccounts.
  On an existing kind cluster, `kmx cluster up` performs this refusal and checks a
  recognized controller's readiness before starting the long model pull.
- **Partial installs require operator review.** Applying CRDs or creating a
  failed Helm release can leave cluster state even if installation times out.
  kmx refuses the next install rather than silently adopting, retrying or
  deleting it. Inspect the context-pinned Helm release, Pods and events. After
  resolving a transient failure, an operator can explicitly retry the same
  SHA-256-verified chart with
  `helm --kube-context <ctx> -n orka-system upgrade orka <verified-orka-0.2.0.tgz> --reuse-values --wait`
  (never `--force`), or clean the failed target before retrying kmx. On a
  disposable local kind cluster you created, `kmx cluster down` then `kmx cluster up` replaces
  it, losing its data. On AKS, stop and use the verified backup/recovery plan.
- **No supported version upgrade.** [Orka v0.2.0 explicitly supports only new
  installations](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md).
  Its conditional CRD/Helm upgrade instructions are for a *future target
  release that publishes a tested procedure*, not instructions to upgrade
  v0.1.3 to v0.2.0. Do not use `helm upgrade --force`.
- **Local kind replacement loses data.** If replacing an old local install,
  first export anything you need; `kmx cluster down` deletes the named kind cluster,
  including Orka Tasks, custom resources, Secrets, SQLite volumes, snapshots,
  model data and any historical database still present. Only then run `kmx cluster up`
  for a new v0.2.0 installation. This is not a migration and does not restore the deleted data.
- **AKS replacement is operator-managed.** Before retiring v0.1.3, make and
  verify backups of the existing controller data volumes, Orka resources/Secrets
  and any owner workloads. Preserve a snapshot-key Secret if the existing
  installation has one; v0.1.3's wrapper path did not create the new v0.2.0
  snapshot key. Back up the new chart-managed snapshot key with its controller
  volume after a fresh v0.2.0 install; do not print key values. Follow the [v0.2.0 installation](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md)
  and [upgrade limits](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md)
  for a fresh new install on a clean target, not an in-place upgrade or an
  assumption that old SQLite data or running Tasks can be restored into v0.2.0.
  `kmx cluster aks up` is not a backup/restore tool; never delete cloud resources
  without an independently verified recovery plan. Orka warns Helm uninstall
  can delete PVCs (and, with a Delete reclaim policy, their underlying data),
  while deleting CRDs deletes their custom resources.
- **Uninstall is not implemented in kmx.** Orka's chart manages persistent
  state; removal is an explicit data-retention decision.
- **Not run on AKS.** Measured on kind only.

## See also

- [Native workspace lifecycle](suite-workspace.md) — source, eval, evaluated push and digest Deploy
