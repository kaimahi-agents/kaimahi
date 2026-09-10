# Installing Orka, and what installing it does not do

`kmx orka install` puts [Orka](https://github.com/orka-agents/orka) —
"Cloud and AI-native multi-agent orchestration platform for Kubernetes" — on
the cluster kmx is pointed at, in one command, with no API key.

It exists because Orka assumes a cluster and this project starts without one.
Their documented path is four prerequisites, a `kubectl apply` from a
checkout, and a Secret an operator is told to create by hand. There is no
published CLI binary and no GitHub Release to fetch.

**Installing Orka governs nothing.** That sentence is printed by the command
itself, and it is the reason this page is short: putting an application's
model traffic on the governed seam is [`kmx migrate`](migrate.md), one
workload at a time, and the two are deliberately separate commands.

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

The manifest is **fetched, not vendored**. 525 kB of somebody else's
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

## The whole journey, from nothing

```console
$ kmx up                                    # a cluster, a model, an agent runtime
$ kmx plane                                 # the governance plane
$ kmx orka install                          # Orka, and a Provider with no key
$ kmx migrate concierge --namespace demo --model local/qwen2.5:3b
```

The fourth command is the one that governs anything. The first three are the
front door.

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

- [migrate.md](migrate.md) — putting an application's model traffic on the seam
- [the Orka composition report](reviews/2026-09-09-orka-composition.md) — what
  each project has, measured rather than compared
