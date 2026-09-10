# Installing Orka, and what installing it does not do

`kmx orka install` puts [Orka](https://github.com/orka-agents/orka) —
"Cloud and AI-native multi-agent orchestration platform for Kubernetes" — on
the cluster kmx is pointed at, in one command, with no API key.

It exists because Orka assumes a cluster and this project starts without one.
Their documented path is four prerequisites, a `kubectl apply` from a
checkout, and a Secret an operator is told to create by hand. There is no
published CLI binary and no GitHub Release to fetch.

**Installing Orka governs nothing.** That sentence is printed by the command
itself. Authoring a native Orka Agent is now [`kmx agent create`](kmx.md#kmx-agent-create);
putting an existing application's model traffic on the governed seam is
[`kmx migrate`](migrate.md), one owned Deployment at a time. Migration does not
translate a kagent BYO definition or create Tasks; install, create and migrate
are deliberately separate commands.

## The command

```console
$ kmx orka install
```

Four phases, refusing at each rather than continuing past it:

| phase | what it does |
|---|---|
| Fetch the pinned installer | downloads `deploy/orka.yaml` at their tag and refuses bytes that do not hash to the digest kmx pins |
| Reconcile the wrapper credential | creates `orka-system` and the `harness-wrapper-auth` Secret, and never replaces one that exists |
| Apply the installer and wait | applies their manifest unmodified, then waits for both Deployments |
| Wire a keyless Provider | a `Provider` pointing at the in-cluster model server, so no key is needed anywhere |

Then:

```console
$ kmx orka status
version running        0.1.3
version pinned by kmx  v0.1.3
deployments            orka-agent-harness-wrapper=1/1 orka-controller-manager=1/1
crds                   10 in core.orka.ai
providers              local=true
```

**`version running` is read off the controller's image, not restated from the
pin.** The pin is what kmx *would* install; an Orka put there by their Helm
chart, by `kubectl apply` from a checkout, or by an older kmx is a different
version, and status says so:

```text
version running        0.1.2 (kmx pins v0.1.3 — this cluster was installed another way)
```

## Seeing it before doing it

Both flags the other writing commands carry, for the same reasons:

```console
$ kmx orka install --no-apply     # fetch, verify the digest, write nothing
--no-apply: nothing was written. 78 documents would be applied to namespace orka-system,
  after the harness-wrapper-auth Secret, which is created first because the wrapper mounts it at start.

$ kmx orka install --dry-run      # ask the API server whether it would take it
COMPLETE  Validated; nothing was written (2.1s total)
```

`--dry-run` is the only way to learn that *this* cluster would refuse the
installer — a Pod Security policy on the namespace, an API server without
`ValidatingAdmissionPolicy` — without finding out halfway through applying it.
It writes nothing, so it does not create the wrapper Secret, and it says so:
a dry run cannot show whether the wrapper would become **ready**, only whether
the objects would be **accepted**.

## The three things worth knowing

### The bytes are theirs, and the pin is ours

Orka publishes no GitHub Releases and no checksum file, so there is nothing
upstream to verify a download against. kmx therefore carries the sha256 of
`deploy/orka.yaml` at `v0.1.3` — the bytes that were read, installed and
tested here — and refuses anything else:

```text
Orka's installer at v0.1.3 does not hash to the digest kmx pins.
  expected 33bdd38bc4aff5d9ef0c32cd5a6c2810a186d2c3ab482b5fdc0f0673a5d734cd
  got      1f2e…
  Nothing was applied. Either the tag moved or the bytes were changed in transit;
  neither is something to install past.
```

That is a weaker claim than a publisher's signature and a stronger one than
trusting whatever the URL serves today. It is written down rather than
skipped so that the weakness is visible.

The installer manifest is **fetched, not vendored**. Offline agent creation
separately embeds three CRD schemas per pinned target; those fixtures do not
install anything. 525 kB of somebody else's
installer committed here would be a copy that silently ages, and the
repository map would have to classify it. Fetching keeps the bytes theirs
and keeps "install exactly these bytes" ours. The cost is that this one
command needs the internet.

There is no `--version` flag. A version flag would either carry no
verification or need a digest per version; moving the pin is an edit to this
repository that somebody reviews.

### The Secret must exist before the manifest

Orka's own getting-started says it plainly: raw manifests cannot safely
contain a shared bearer token, so the operator is asked to run
`openssl rand -hex 32` into a Secret **before** applying. Skip it and the
apply still succeeds — the wrapper Deployment simply never becomes ready.

That is the failure this command removes, and it is why the order is fixed
rather than convenient. An existing Secret is kept, never regenerated:
rotating it under a running wrapper would invalidate a token its callers
still hold. A cluster that cannot be read is refused rather than treated as
a cluster without the Secret, because the second reading mints a second
token under a running wrapper.

### The Provider is what removes the fourth prerequisite

Orka's prerequisites end with "an LLM API key". Its `Provider` type accepts
`baseURL` — "an optional custom API endpoint (for proxies or self-hosted)" —
so kmx creates one of `type: openai` pointing at the keyless in-cluster model
server `kmx up` already deployed:

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

`agent create` authors Orka resources; it does not install Orka. On the local
kind path, reuse the Ollama model from `quickstart` or `up`, then install the
pinned release in that **same** context:

```bash
kmx --context kind-kaimahi-p1 orka install
```

The installer provisions `local-provider-key` separately. Create will reference
it by name and key only, and will create a **new Provider named `orka-hello`**;
it never adopts or updates the installer's shared Provider `local`. This is a
new Agent/Task example, not a continuation command for an Agent you already
created. Choose unused names and output paths; creation refuses collisions.

Task execution requires an existing result account. An operator with RBAC
creation permission can provision this dedicated account separately; these
commands contain names only, not token values:

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
against pinned `v0.1.3` returned stdout `Orka says hello.` with exit 0, using
Ollama and no paid endpoint (US$0). That is a demonstrated release run, **not**
a live main-snapshot or AKS proof. CI's clone-free journey also asserts the
actual answer, rather than calling Ready an execution result.

The generated `agents/orka-hello.yaml` includes a **value-free Secret skeleton
that must never be written**. The command does not write it or provision RBAC.
Without `--task`, only Provider/Agent readiness is tested. Use `--out -` or
`--no-apply` for fully offline generation; [the canonical create guide](kmx.md#kmx-agent-create)
covers explicit inputs, pinned schemas, ordered manual creation and collisions.

**Authority is broader than the name "result reader" suggests.** kmx requests a
ten-minute token; the API server determines the actual granted lifetime. The
token has this account's full effective authority, and discarding it is not
revocation. Pinned main requires the namespaced Task-get permission
above, but release `v0.1.3` authenticates result reads without enforcing that
Task-read RBAC. Results travel via a loopback HTTP port-forward; UID checks do
not bind the returned bytes to a UID. kmx pins one TCP connection and stops if it
or the forward is lost, rather than reconnecting or resubmitting the Task. This
trades reconnect availability for protection against later local-port reuse;
the initial bind and connection are still local trust, not cryptographic process
authentication. Dry-run does not test access or execution.

`agent chat/edit/list` still operate on kagent, not this Orka Agent. No automatic
MCP translation, application image deployment or governance is added here.

## The whole journey, from nothing

```console
$ kmx up                                    # a cluster, a model, an agent runtime
$ kmx plane                                 # the governance plane
$ kmx orka install                          # Orka, and a Provider with no key
$ kmx migrate concierge --namespace demo --model local/qwen2.5:3b
```

The fourth command is the one that governs anything. The first three are the
front door.

## Authoring an agent for Orka

Installing Orka and authoring an agent are separate steps. `kmx agent create`
now emits native Orka resources; installing Orka does not convert existing
`kagent.dev/v1alpha2` files.

For a new agent that will run on Orka, **author Orka's native
`core.orka.ai/v1alpha1` `Agent` and `Provider` resources and invoke it with
an Orka `Task`.** This keeps the runtime configuration explicit: changing a
kagent resource's API group would not translate its referenced model
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
guide records the exercised behavior and its limits. A kagent BYO definition
is not an input to `kmx migrate`: the application must already have a
Deployment it owns. Model-traffic migration is a separate boundary, not BYO
Agent conversion.

## Limits, stated

- **One pinned version.** `v0.1.3`. A newer Orka means editing the constant
  and its digest together, and re-running the install against it.
- **The namespace is not configurable**, because it is not configurable in
  their installer: `orka-system` is hard-coded across its 77 documents.
- **This installs; it does not upgrade.** Orka's own docs are explicit that
  Helm does not update CRDs on upgrade and that they must be applied from the
  exact target chart first. Re-running this command applies the same pinned
  version again, which is idempotent and is not an upgrade path.
- **Uninstall is not implemented.** Their installer retains CRDs and custom
  resources by design, so removing it is a decision with data attached rather
  than a command this project should offer casually.
- **The model seam has no per-credential allowlist**, so every credential the
  plane has issued can reach the `orka` upstream — a property of the seam,
  described in [migrate.md](migrate.md#8-limits-stated).
- **Not run on AKS.** Measured on kind only.

## See also

- [kmx agent create](kmx.md#kmx-agent-create) — native Orka authoring and Task result contract
- [migrate.md](migrate.md) — putting an application's model traffic on the seam
- [the Orka composition report](reviews/2026-09-09-orka-composition.md) — what
  each project has, measured rather than compared
