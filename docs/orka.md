# Installing Orka, and what installing it does not do

`kmx orka install` puts [Orka](https://github.com/orka-agents/orka) —
"Cloud and AI-native multi-agent orchestration platform for Kubernetes" — on
the cluster kmx is pointed at, in one command, with no API key.

It exists because Orka assumes a cluster and this project starts without one.
At [Orka v0.2.0](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md),
upstream installs a Helm chart and generates its own encryption key and webhook
certificate. kmx fetches the pinned release chart rather than requiring an
Orka checkout or a manually created wrapper credential.

**Installing Orka governs nothing.** That sentence is printed by the command
itself. Authoring a native Orka Agent is now [`kmx agent create`](kmx.md#kmx-agent-create);
putting an existing application's model traffic on the governed seam is
[`kmx migrate`](migrate.md), one owned Deployment at a time. Migration does not
translate a legacy BYO definition or create Tasks; install, create and migrate
are deliberately separate commands.

## The command

```console
$ kmx orka install
```

The install checks each boundary before proceeding:

| phase | what it does |
|---|---|
| Fetch the pinned chart | downloads `orka-0.2.0.tgz` from the v0.2.0 release and refuses bytes that do not hash to kmx's pinned SHA-256 |
| Check for an existing installation | refuses an existing controller Deployment, Orka CRDs or a foreign/partial Helm release rather than attempting an upgrade; a pre-created `orka-system` namespace containing only Secrets or ServiceAccounts is allowed |
| Apply CRDs and install | applies CRDs extracted from the verified chart before `helm install orka` with `--kube-context`, `controller.mode=harness-v2`, `fullnameOverride=orka-api` and `--wait`; no `helm upgrade --force` |
| Provision result reader and keyless Provider | installs the namespace-scoped Task-get result account, then a `Provider` pointing at the in-cluster model server, so no model key is needed on the local path |

Check the selected cluster with `kmx orka status` and
`kubectl --context <ctx> -n orka-system get deployments,pvc`. The chart's
controller Deployment is `orka-api-controller`; status lists the running
Deployments, CRDs and Provider readiness separately from kmx's v0.2.0 pin.
Do not mistake a pinned version for proof of a running version, or a
ready Deployment for a completed Task. Status is read-only and does not
authorize reinstallation over an older release.

## Seeing it before doing it

Both flags the other writing commands carry, for the same reasons:

```console
$ kmx orka install --no-apply     # fetch, verify chart and count its CRDs; write nothing
$ kmx orka install --dry-run      # verify chart, check existing install, server-dry-run its CRDs
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
server `kmx local up` already deployed:

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

`agent create` defaults to Orka resources; it does not install Orka. The
separate explicit Kagent create path is documented in the
[CLI contract](kmx.md#explicit-kagent-v0102-create) and does not change this
guide. On the local kind path the runtime is already there: `kmx local up` and `kmx quickstart` both
install the pinned release and leave the Ollama model in place, so author
against that **same** context. On a cluster kmx did not bring up, install it
first:

```bash
kmx --context kind-kaimahi-p1 orka install
```

The installer provisions `local-provider-key` separately. Create will reference
it by name and key only, and will create a **new Provider named `orka-hello`**;
it never adopts or updates the installer's shared Provider `local`. This is a
new Agent/Task example, not a continuation command for an Agent you already
created. Choose unused names and output paths; creation refuses collisions.

Task execution requires an existing result account, and the runtime step owns
it. `kmx local up --step orka` provisions exactly this account, Role and
RoleBinding, and so does every run that includes that step — a bare `kmx local up`
and `kmx quickstart`. A standalone `kmx orka install` does **not**: it
installs the release and wires the keyless Provider, and nothing more.
`agent create` never does either: it only NAMES an account, so authoring an
agent cannot mint a grant nobody read as a grant. On a cluster whose Orka
arrived another way — including one where only `kmx orka install` has run —
an operator with RBAC creation permission can provision the same account
separately; these commands contain names only, not token values:

```bash
kubectl --context kind-kaimahi-p1 -n orka-system create serviceaccount orka-result-reader
kubectl --context kind-kaimahi-p1 -n orka-system create role orka-result-reader \
  --verb=get --resource=tasks.core.orka.ai
kubectl --context kind-kaimahi-p1 -n orka-system create rolebinding orka-result-reader \
  --role=orka-result-reader --serviceaccount=orka-system:orka-result-reader

kmx --context kind-kaimahi-p1 agent create orka-hello \
  --namespace orka-system --provider-type openai --model qwen2.5:3b \
  --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 \
  --task 'Reply with exactly this text: Orka says hello.' \
  --result-service-account orka-result-reader
```

This creates Provider → waits for current-generation Ready → creates Agent →
waits → creates a fresh Task → retrieves its actual answer. A local kind run
in an earlier v0.1.3 run returned stdout `Orka says hello.` with exit 0, using
Ollama and no paid endpoint (US$0). That is a demonstrated release run, **not**
a v0.2.0 chart or AKS proof. The historical clone-free CI journey also
asserted the actual answer, rather than calling Ready an execution result.

The generated `agents/orka-hello.yaml` includes a **value-free Secret skeleton
that must never be written**. The command does not write it or provision RBAC.
Without `--task`, only Provider/Agent readiness is tested. Use `--out -` or
`--no-apply` for fully offline generation; [the canonical create guide](kmx.md#kmx-agent-create)
covers explicit inputs, pinned schemas, ordered manual creation and collisions.

**Authority is broader than the name "result reader" suggests.** kmx requests a
ten-minute token; the API server determines the actual granted lifetime. The
token has this account's full effective authority, and discarding it is not
revocation. An earlier v0.1.3 release authenticated result reads without enforcing
Task-read RBAC; the pinned old main snapshot requires namespaced Task-get. Do
not infer v0.2.0 authorization from the older result: retain the namespaced
grant. Results travel via a loopback HTTP port-forward; UID checks do not bind
the returned bytes to a UID. kmx pins one TCP connection and stops if it
or the forward is lost, rather than reconnecting or resubmitting the Task. This
trades reconnect availability for protection against later local-port reuse;
the initial bind and connection are still local trust, not cryptographic process
authentication. Dry-run does not test access or execution.

`kmx agent list --namespace <ns>` lists Orka Agents; an omitted namespace reads
`orka-system`, the namespace the pinned installer uses. Existing Orka Agents are
used through the interactive chat:

```bash
kmx agent chat --interactive --namespace <ns> <name>
```

Orka chat is a session, so a non-interactive invocation is refused. Use
`kmx agent run --agent <name> --prompt-file -` for a one-shot Task, and
`kmx task result <task> --wait 5m` to retrieve a later answer. A live Agent is
edited with `kubectl edit agents.core.orka.ai` and read back with
`kmx agent show`. No automatic MCP translation, application image deployment
or governance is added here.

## The whole journey, from nothing

```console
$ kmx local up                              # a cluster, a model, and the Orka runtime
$ kmx plane                                 # the model-traffic bridge
$ kmx migrate concierge --namespace demo --model local/qwen2.5:3b
```

The third command is the one that governs anything. The first two are the
front door — `kmx local up` installs Orka itself, so there is no separate
`kmx orka install` on this path.

## Authoring an agent for Orka

Installing Orka and authoring an agent are separate steps. The default
`kmx agent create` path emits native Orka resources; installing Orka does not
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
own management. [`kmx migrate`](migrate.md) describes the model-traffic path
for supported applications. That path does not register the application as
an Orka `Agent` or turn its requests into Orka `Task` resources. The migration
guide records the exercised behavior and its limits. A legacy BYO definition
is not an input to `kmx migrate`: the application must already have a
Deployment it owns. Model-traffic migration is a separate boundary, not BYO
Agent conversion.

## Limits, stated

- **Existing v0.1.3 reads remain supported.** Lift, status and the console
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
  drop requested limits. Offline `--schema-target v0.2.0` and online create/lift
  against that release refuse those fields. Explicit v0.1.3 schema selection
  and existing v0.1.3 installations still accept legacy limit-bearing bundles.
  Remove the limits deliberately before moving a bundle to a fresh v0.2.0
  target; there is no equivalent rate-limit mapping in kmx yet.
- **One pinned install.** `v0.2.0` chart, harness-v2, release `orka`, namespace
  `orka-system`, fullname `orka-api`. A recognized matching kmx installation
  keeps its chart, key and data on rerun only when its image overrides match
  the pin and its controller is Ready. `kmx orka install` also ensures the
  read-only Task result account. An existing namespace alone is not an Orka
  installation; kmx refuses a legacy controller/CRD or foreign Helm release,
  not a pre-created `orka-system` namespace containing only Secrets or ServiceAccounts.
  On an existing kind cluster, `kmx local up` performs this refusal and checks a
  recognized controller's readiness before starting the long model pull.
- **Partial installs require operator review.** Applying CRDs or creating a
  failed Helm release can leave cluster state even if installation times out.
  kmx refuses the next install rather than silently adopting, retrying or
  deleting it. Inspect the context-pinned Helm release, Pods and events. After
  resolving a transient failure, an operator can explicitly retry the same
  SHA-256-verified chart with
  `helm --kube-context <ctx> -n orka-system upgrade orka <verified-orka-0.2.0.tgz> --reuse-values --wait`
  (never `--force`), or clean the failed target before retrying kmx. On a
  disposable local kind cluster you created, `kmx local down` then `kmx local up` replaces
  it, losing its data. On AKS, stop and use the verified backup/recovery plan.
- **No supported version upgrade.** [Orka v0.2.0 explicitly supports only new
  installations](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md).
  Its conditional CRD/Helm upgrade instructions are for a *future target
  release that publishes a tested procedure*, not instructions to upgrade
  v0.1.3 to v0.2.0. Do not use `helm upgrade --force`.
- **Local kind replacement loses data.** If replacing an old local install,
  first export anything you need; `kmx local down` deletes the named kind cluster,
  including Orka Tasks, custom resources, Secrets, SQLite volumes, snapshots,
  model data and the plane ledger. Only then run `kmx local up` for a new v0.2.0
  installation. This is not a migration and does not restore the deleted data.
- **AKS replacement is operator-managed.** Before retiring v0.1.3, make and
  verify backups of the existing controller data volumes, Orka resources/Secrets
  and any owner workloads. Preserve a snapshot-key Secret if the existing
  installation has one; v0.1.3's wrapper path did not create the new v0.2.0
  snapshot key. Back up the new chart-managed snapshot key with its controller
  volume after a fresh v0.2.0 install; do not print key values. Follow the [v0.2.0 installation](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/installation.md)
  and [upgrade limits](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md)
  for a fresh new install on a clean target, not an in-place upgrade or an
  assumption that old SQLite data or running Tasks can be restored into v0.2.0.
  `kmx aks up` is not a backup/restore tool; never delete cloud resources
  without an independently verified recovery plan. Orka warns Helm uninstall
  can delete PVCs (and, with a Delete reclaim policy, their underlying data),
  while deleting CRDs deletes their custom resources.
- **Uninstall is not implemented in kmx.** Orka's chart manages persistent
  state; removal is an explicit data-retention decision.
- **The model seam has no per-credential allowlist**, so every credential the
  plane has issued can reach the `orka` upstream — a property of the seam,
  described in [migrate.md](migrate.md#8-limits-stated).
- **Not run on AKS.** Measured on kind only.

## See also

- [kmx agent create](kmx.md#kmx-agent-create) — native Orka authoring and Task result contract
- [migrate.md](migrate.md) — putting an application's model traffic on the seam
- [the Orka composition report](reviews/2026-09-09-orka-composition.md) — what
  each project has, measured rather than compared
