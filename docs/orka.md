# Installing Orka, and what installing it does not do

`kmx cluster orka install` puts [Orka](https://github.com/orka-agents/orka) —
"Cloud and AI-native multi-agent orchestration platform for Kubernetes" — on
the cluster kmx is pointed at, in one command, with no API key.

It exists because Orka assumes a cluster and this project starts without one.
At [Orka v0.3.0](https://github.com/orka-agents/orka/blob/v0.3.0/website/docs/operations/installation.md),
upstream installs a Helm chart and generates its own encryption key and webhook
certificate. kmx fetches the pinned release chart rather than requiring an
Orka checkout or a manually created wrapper credential.

Installation does not author an Agent or run a Task. Author a native Orka Agent with
[native workspace authoring](suite-workspace.md), full eval, publication and digest Deploy.
Installation and authoring are deliberately separate operations.

## Read-only tool availability

```sh
kmx cluster orka tools --context kind-dev --namespace orka-system
kmx cluster orka tools --context team --namespace agents -o json
```

Both selectors are required. The command performs bounded Kubernetes GETs only:
no TokenRequest, Secret read, provisioning, endpoint probe or Tool invocation.
Kubernetes credential helpers can still run through kubectl authentication.
Native Windows remains unsupported. Help and completion do not load configuration.

Built-ins are copied from the exact [Orka v0.3.0 API catalog](https://github.com/orka-agents/orka/blob/a533c3491d350263c0fa39bb7887dabd0d055c4e/internal/api/handlers.go#L56-L64).
They are offered only after observing the supported current-generation Ready
controller image/configuration and matching watch namespace. Their source and
reason are `pinned-catalog`, not live catalog discovery or successful execution.
An unqualified runtime or unwatched namespace produces incomplete inventory,
exit 1, no built-in choices, and `unknown` HTTP availability with reason
`unsupported_runtime`.

HTTP candidates are namespace-local Tool resources. A plain HTTP candidate requires
`spec.http` and no `spec.mcp`; MCP resources with HTTP transport settings are
`unsupported` with `backend_unsupported`. All runtime-supported methods remain
supported, including default POST. The command never invokes any method.
An `available` row reflects structural checks and the controller's last reported
`status.available: true`, not a fresh health check or credential validation.
False or absent status is `unknown` with `endpoint_unavailable`.

The shared App reader `ReadOrkaTools` returns target context/namespace and checked
UIDs, runtime/catalog provenance, ordered rows and `complete`. Each row includes
kind, name, availability, fixed reason, source and observed resource identity/spec
digest where present. It never includes URLs, headers, schemas, status errors,
Tool inputs/results or Secret values. Destination consumers call that reader
with explicit requirements, require complete inventory and available selected
rows, and reobserve before execution. Disabled requirements do not resolve an
exact resource dependency. Only an authoritative exact Tool NotFound, after the
namespace and Tool API are observed, gives `missing`/`resource_missing`.
A forbidden read is `unknown`/`read_denied`; unrecognized failures are
`unknown`/`read_unknown`, never absence.

Complete inventory exits 0, even when empty or containing unavailable candidates.
Incomplete inventory exits 1 and preserves fitting readable rows; never interpret
a failed list as empty. Fixed reasons additionally include `observed`, `disabled`,
`name_unsupported`, `authentication_required`, `resource_terminating`, `malformed`,
`inventory_limit`, `stale_input` and `cancelled`. Reads, pages, total bytes and
machine output are bounded; repeated cursors, duplicate identities and overflow
cannot produce a complete result. Availability grants no native evaluation,
publication or deployment authority. See [machine output](machine-output.md).

## The command

```console
$ kmx cluster orka install
```

The install checks each boundary before proceeding:

| phase | what it does |
|---|---|
| Fetch the pinned chart | downloads `orka-0.3.0.tgz` from the v0.3.0 release and refuses bytes that do not hash to kmx's pinned SHA-256 |
| Check for an existing installation | refuses an existing controller Deployment, Orka CRDs or a foreign/partial Helm release rather than attempting an upgrade; a pre-created `orka-system` namespace containing only Secrets or ServiceAccounts is allowed |
| Apply CRDs and install | applies each verified chart CRD with `--server-side --field-manager=kmx` (never `--force-conflicts`) before `helm install orka` with `--kube-context`, `controller.mode=harness-v2`, `fullnameOverride=orka-api` and `--wait`; no `helm upgrade --force` |
| Provision result reader and keyless Provider | installs the namespace-scoped Task-get result account, then a `Provider` pointing at the in-cluster model server, so no model key is needed on the local path |

Check the selected cluster with `kmx cluster orka status` and
`kubectl --context <ctx> -n orka-system get deployments,pvc`. The chart's
controller Deployment is `orka-api-controller`; status lists the running
Deployments, CRDs and Provider readiness separately from kmx's v0.3.0 pin.
Do not mistake a pinned version for proof of a running version, or a
ready Deployment for a completed Task. Status is read-only and does not
authorize reinstallation over an older release.

## Native installation from a local bundle

`kmx cluster orka install --provider - --offline-bundle DIR` uses the compiled
source-qualification native profile, not the online v0.3.0 pin. The guest supplies
the pinned chart, source descriptor, all OCI metadata, tools and pre-imported
images in every selected kind node. No tools, images, chart or models are fetched,
and no Provider is created. See the [bundle input and install contract](kmx.md#native-orka-install)
for exact cache/target checks, generated fixed values and disabled components.

Exact healthy reuse compares all custom values and live workload execution
settings, preserves chart data/keys and reconciles only the result reader.
Foreign, online, changed, partial and orphan state refuse without repair.
Bundle `--no-apply` is local-only and leaves target checks explicitly unchecked;
bundle `--dry-run` validates only CRDs server-side after full preflight.
A failed phase stops subsequent phases and retains effects for private inspection.

## Seeing it before doing it

Both flags the other writing commands carry, for the same reasons:

```console
$ kmx cluster orka install --no-apply     # fetch, verify chart and count its CRDs; write nothing
$ kmx cluster orka install --dry-run      # verify chart, check existing install, server-dry-run its CRDs
COMPLETE  Validated; nothing was written
```

`--dry-run` validates each chart CRD with the same server-side field manager
and `--dry-run=server`, not the entire Helm render or chart hooks. It writes
nothing and cannot prove chart-generated Secrets, controller readiness or Task
execution. `--no-apply` does not check cluster
compatibility.

## The three things worth knowing

### The bytes are theirs, and the pin is ours

kmx fetches the [v0.3.0 release chart](https://github.com/orka-agents/orka/releases/tag/v0.3.0)
(`orka-0.3.0.tgz`) and verifies its SHA-256 against the digest pinned in kmx
(`a83e374c76e223ae6a96d598d62fd0af3b081ea1c01787783a3b7294f3418bce`)
before extracting CRDs or passing it to Helm. A changed or corrupted chart is
refused, not installed. This kmx-maintained digest is not an independent
publisher signature. Offline agent creation separately embeds CRD schemas;
those fixtures do not install Orka. Installing the online release chart requires a download.

There is no `--version` flag. A version flag would either carry no
verification or need a digest per version; moving the pin is an edit to this
repository that somebody reviews.

### Preserve the chart's generated snapshot key

The [v0.3.0 installation guide](https://github.com/orka-agents/orka/blob/v0.3.0/website/docs/operations/installation.md#2-check-the-installation)
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

Orka v0.3.0 does not require an LLM API key to install; model calls need a
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

[Package create](kmx.md#kmx-package-create) authors native workspace source offline. Prepare
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
authority and discarding one is not revocation. Orka v0.3.0 Task-read
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
  they also work with a stock v0.3.0 chart release named `orka` instead of
  kmx's `orka-api`. Result reads retain their own API and RBAC checks.
- **Rate limits are not supported by v0.3.0 CRDs.** Orka removed
  `Provider.spec.rateLimit` and `Agent.spec.rateLimit`; kmx does not silently
  drop requested limits. Native workspace profiles reject unsupported fields;
  live create schema-selection/previews and old bundle readers are removed.
  Historical limit-bearing resources need operator review before a fresh v0.3.0
  target; there is no equivalent rate-limit mapping in kmx yet.
- **One pinned install.** `v0.3.0` chart, harness-v2, release `orka`, namespace
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
  A CRD field-manager conflict stops before later CRDs, waits or Helm; earlier
  CRDs may already exist. KMX names the failing CRD, retains kubectl's owner
  diagnostics and never forces ownership or rolls back earlier writes.
  kmx refuses the next install rather than silently adopting, retrying or
  deleting it. Inspect the context-pinned Helm release, Pods and events. If CRD
  application or waiting fails before Helm creates a release, use a clean target
  or the recovery steps below; `helm upgrade` cannot create that missing release.
  Only when a KMX-owned release of this pinned version already exists, after
  resolving its transient failure, an operator can explicitly retry the same
  SHA-256-verified chart with
  `helm --kube-context <ctx> -n orka-system upgrade orka <verified-orka-0.3.0.tgz> --reuse-values --wait`
  (never `--force`), or clean the failed target before retrying kmx. On a
  disposable local kind cluster you created, `kmx cluster down` then `kmx cluster up` replaces
  it, losing its data. On AKS, stop and use the verified backup/recovery plan.
- **KMX supports fresh installations only.** An existing v0.2.0 or older
  runtime is refused, not upgraded. [Orka v0.3.0's narrow database-layout
  compatibility](https://github.com/orka-agents/orka/blob/v0.3.0/website/docs/operations/upgrading.md)
  does not qualify a KMX runtime migration, CRD ownership transfer or restoration
  of old SQLite volumes. Do not use `helm upgrade --force`.
- **Local kind replacement loses data.** If replacing an old local install,
  first export anything you need; `kmx cluster down` deletes the named kind cluster,
  including Orka Tasks, custom resources, Secrets, SQLite volumes, snapshots,
  model data and any historical database still present. Only then run `kmx cluster up`
  for a new v0.3.0 installation. This is not a migration and does not restore the deleted data.
- **AKS replacement is operator-managed.** Before retiring an older runtime, make and
  verify backups of the existing controller data volumes, Orka resources/Secrets
  and any owner workloads. Preserve a snapshot-key Secret if the existing
  installation has one; v0.1.3's wrapper path did not create the new v0.2.0
  snapshot key. Back up the new chart-managed snapshot key with its controller
  volume after a fresh v0.3.0 install; do not print key values. Follow the [v0.3.0 installation](https://github.com/orka-agents/orka/blob/v0.3.0/website/docs/operations/installation.md)
  and [upgrade limits](https://github.com/orka-agents/orka/blob/v0.3.0/website/docs/operations/upgrading.md)
  for a fresh new install on a clean target, not an in-place upgrade or an
  assumption that old SQLite data or running Tasks can be restored into v0.3.0.
  `kmx cluster aks up` is not a backup/restore tool; never delete cloud resources
  without an independently verified recovery plan. Orka warns Helm uninstall
  can delete PVCs (and, with a Delete reclaim policy, their underlying data),
  while deleting CRDs deletes their custom resources.
- **Uninstall is not implemented in kmx.** Orka's chart manages persistent
  state; removal is an explicit data-retention decision.
- **Not run on AKS.** Measured on kind only.

## See also

- [Native workspace lifecycle](suite-workspace.md) — source, eval, evaluated push and digest Deploy
