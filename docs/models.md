# Models and endpoints

This page is about the model endpoints Kaimahi governs and the local model
the runtime pulls. For Orka installation and model-traffic migration, use
[orka.md](orka.md) and [migrate.md](migrate.md).

**The committed legacy model presets are gone.** `k8s/models/` held
nine of them — `ollama`, `github-copilot`, `anthropic`, `openai`,
`openrouter`, `azure-foundry`, `openai-compatible`, `governed-ollama` and
`governed-copilot`. Every one was an object of a runtime kmx no
longer installs, and the commands that applied or switched between them
(preset switching, governance and the old agent install step) were removed before them.
Nothing in kmx reads, renders or applies a preset any more.

What replaces them is not another list of files. An endpoint becomes
governed by being **onboarded** as a plane upstream and then **migrated**
onto:

- [`kmx models add`](kmx.md#kmx-models-add) onboards an OpenAI-compatible
  endpoint into the operator overlay, with its protocol declared and its
  credential in plane custody.
- [`kmx migrate`](migrate.md) points an owner-managed Deployment's model
  traffic at the plane's seam, one workload at a time. The Deployment stays
  the owner's.

> **An ungoverned hosted endpoint is a live credit card.** Calling one
> directly sends every conversation to a billed API with no budget, metering
> or ledger in front of it. The plane puts exactly that in front of it:
> budgets that fail closed, a ledger of every call, and the real key held
> away from the caller. That is [spend.md](spend.md).

## "OpenAI-compatible" is two protocols, not one

The common shape is **chat completions** — `POST v1/chat/completions`, token
counts reported as `prompt_tokens` and `completion_tokens`. That is what most
clients send and what "OpenAI-compatible" has usually meant here.

It is not the only shape. The **Responses API** — `POST v1/responses`,
token counts reported as `input_tokens` and `output_tokens` — is what one
current agent framework speaks by default: its model client *is* the
Responses client, and it offers no switch. Both are OpenAI-compatible;
they disagree about the field names the meter reads.

That matters only on the governed path, and there it matters a lot: a
governed upstream declares which protocol it speaks, and a call the plane
cannot meter is refused rather than recorded as costing nothing. The
declaration, the two shapes and what happens to a third are in
[spend.md](spend.md#protocols-and-missing-usage); adding an upstream that speaks
either is [`kmx models add`](kmx.md#kmx-models-add).

Your own model endpoint — in-cluster, keyless, either protocol — is
onboarded rather than committed: it goes into the operator overlay,
which the next `kmx plane` does not discard. A hosted endpoint holds a
real API key and stays a reviewed entry in
[`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml).

## Storing an API key

Keys go in Kubernetes Secrets and nowhere else: never in YAML, ConfigMaps,
argv, environment listings, or logs.

For an endpoint the plane governs, the credential is captured by
[`kmx models add`](kmx.md#kmx-models-add) into plane custody, so the caller
never holds it. Copilot's plane-side token is `kmx models credential copilot`.
A key for something kmx does not govern is yours to place with
`kubectl create secret`; strip the trailing newline first, because one left in
the Secret corrupts the Authorization header on every request.

## Azure AI Foundry rides the v1 GA surface

Azure's `azureOpenAI`-style configuration requires an `apiVersion` field, and
that field belongs to Azure's legacy per-version API surface. Kaimahi targets
Foundry's **v1 GA** API, which is a plain OpenAI-compatible endpoint with
**no** api-version parameter:

```
https://YOUR-RESOURCE.openai.azure.com/openai/v1
```

Set the model to your **deployment** name, not the upstream model name. The
same pattern covers OpenRouter and any other OpenAI-compatible endpoint.

## GitHub: Models is retired, the Copilot subscription path replaces it

GitHub Models used to be the obvious keyed endpoint for anyone with a
GitHub account. That service no longer exists: **GitHub retired GitHub
Models entirely on 2026-07-30**, playground, model catalog, inference
API and BYOK, for all customers including existing ones
([changelog](https://github.blog/changelog/2026-07-30-github-models-is-now-retired/)).
Verified directly on 2026-08-31: `https://models.github.ai/inference/...`
returns HTTP 410 (`github_models_retirement_brownout`) even with a valid
`gh` OAuth token.

What a GitHub subscription still provides: **GitHub Copilot plans
include API access to OpenAI and other models** at
`api.githubcopilot.com`, an OpenAI-compatible endpoint.

The plane-side route is `kmx models credential copilot`. The checkout-only
`make copilot-secret` helper belonged to the deleted direct-to-Copilot kagent
preset and is no longer available. Use the plane-side command instead.

Custody properties worth knowing:

- **The gh CLI's own OAuth token is not Copilot-entitled.** The exchange
  returns 403 for it (verified 2026-08-31). The device flow authenticates
  as the Copilot-entitled VS Code OAuth client, which is what Copilot
  tooling itself does. Same terminal-login UX, one extra browser approval
  on first run, cached after that.
- **The device-flow OAuth token never enters the cluster.** Only the
  short-lived exchange token does. The plane command handles credential
  custody; do not place tokens in command arguments or checked-in manifests.
- **The exchanged token expires**, typically within hours. For the plane-side
  route, re-run `kmx models credential copilot`: it writes **only**
  `kaimahi/kaimahi-copilot-token` and restarts an existing plane. This path
  has no in-cluster auto-refresher ([FAQ](FAQ.md#hosted-model-authentication-fails)).
- **`api.githubcopilot.com` is not part of GitHub's documented public API
  surface.** GitHub's documented programmatic paths are the Copilot
  CLI/SDK and BYOK. It is the endpoint GitHub's own clients and
  sanctioned third-party integrations use, but treat it as subject to
  change without notice, and mind your plan's premium-request
  accounting.
- **Model IDs are the Copilot catalog's** (e.g. `gpt-5-mini`,
  `gpt-4o-mini`, `claude-*`, `gemini-*`).

## Swapping the local model

A full `kmx up` first probes the host's loopback Ollama API. It offers reuse
only when `/api/tags` reports at least one installed model. Reuse is opt-in;
KMX's bundled model remains the default. `--output json`, redirected sessions,
`kmx up --step ...`, and an explicit `MODEL` never probe or prompt.

`kmx quickstart` never probes or prompts at all. It is deterministic and
non-interactive on purpose — there is no host-model picker on that path, so
it always deploys the in-cluster Ollama and the bundled model, and the same
command on the same machine produces the same cluster, Provider and Agent.
Choosing a host model while the runtime starts is `kmx quickstart-wizard`.

Before reusing host Ollama, KMX verifies the selected tag through an endpoint
reachable from the kind node, trying the engine host alias and kind bridge
gateway. If neither works, setup installs the bundled model instead. Reuse
skips both the in-cluster Ollama deployment and model pull. Limit host Ollama's
exposure to the container network rather than publishing its unauthenticated
API to the LAN.

`kmx up` writes the verified route into the **Orka** `local` Provider
(`defaultModel` and `baseURL` in `orka-system`), which is the only model
configuration that run creates. The follow-up commands printed after it carry
no explicit host endpoint — the Provider holds it.

`MODEL=<tag> kmx up --step model` pulls another Ollama model into the pod; a
full `kmx up` then resolves that model through the Orka Provider it wires. Test
it with several fresh chats before trusting it: small models misfire a
runtime's built-in question tool, and small models that call a tool correctly
can still garble its output in the summary
([getting-started.md](getting-started.md#choices-and-caveats),
[FAQ](FAQ.md#the-tool-worked-but-the-answer-is-wrong)). The Ollama
pod stores models in an `emptyDir`, so a restart loses the cached model;
repeat the pull when needed.
