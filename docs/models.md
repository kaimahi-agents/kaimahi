# Legacy reference: kagent models and endpoints

This page describes the kagent presets still shipped by the current CLI,
not Orka Provider configuration. For Orka installation and model-traffic
migration, use [orka.md](orka.md) and [migrate.md](migrate.md). The future
authoring boundary remains open; these presets do not translate kagent
resources into Orka resources.

The hello-world agent thinks with an in-cluster Ollama model by default.
This doc is how to make the same agent think with a hosted endpoint
instead. Each endpoint is a kagent `ModelConfig` preset committed under
[`k8s/models/`](../k8s/models/), applied with kubectl to switch between
them. Nothing else changes: same cluster, same agent YAML, same `kmx agent
chat`.

> **A plain hosted preset is a live credit card.** Switching to one
> sends every conversation to a billed API with no budget, metering, or
> ledger in front of it. Kaimahi's model-traffic bridge puts exactly that in
> front of it: budgets that fail closed, a ledger of every call, and the
> real key held away from the agent. That is [spend.md](spend.md), and
> the `governed-*` presets below are its entry point. Either accept the
> ungoverned path knowingly or use a governed preset.

## The presets

| Preset (`k8s/models/`) | Endpoint | Key Secret expected | Live-verified? |
|---|---|---|---|
| `ollama` | in-cluster Ollama (keyless, free) | none | **yes**, keyless end to end in CI on every PR |
| `github-copilot` | Copilot subscription (OpenAI models via api.githubcopilot.com) | `github-copilot-token` (via checkout-only `make copilot-secret`) | **yes**, 2026-08-31, `gpt-5-mini`, A2A task completed |
| `anthropic` | Anthropic first-party API | `anthropic-api-key` | not live-verified |
| `openai` | OpenAI first-party API | `openai-api-key` | not live-verified |
| `openrouter` | OpenRouter gateway | `openrouter-api-key` | not live-verified |
| `azure-foundry` | Azure AI Foundry, v1 GA API (edit `baseUrl` + `model` first) | `azure-foundry-api-key` | not live-verified |
| `openai-compatible` | any OpenAI-compatible base URL (template, edit first) | `openai-compatible-api-key` | not live-verified |
| `governed-ollama` | Ollama through the kaimahi proxy | `kaimahi-governed-token` (via the lift agents phase) | **yes**, live and in CI. See [spend.md](spend.md) |
| `governed-copilot` | Copilot through the kaimahi proxy | `kaimahi-governed-token` (via the lift agents phase), plus `kmx models credential copilot` for the proxy | **yes**, once, on AKS. See [spend.md](spend.md) and [aks.md](aks.md) |

"Not live-verified" means exactly that. The preset is schema-valid
against the kagent 0.9.12 CRDs, which CI proves with a server-side
dry-run on every PR, so the YAML is well-formed and the fields exist. But
no real completion has been bought through it yet. A preset graduates to
live-verified only when an actual model call completes through the
endpoint, and nobody has paid to do that for those five. They should
work. "Should" is the honest word; schema validation does not prove provider
availability or successful inference.

At kagent 0.9.12 there is no OpenRouter or Copilot-specific provider in
the CRD. Every OpenAI-compatible endpoint rides `provider: OpenAI` plus
`openAI.baseUrl`, and that is all any of these presets do.

## "OpenAI-compatible" is two protocols, not one

Every preset above speaks **chat completions** — `POST
v1/chat/completions`, token counts reported as `prompt_tokens` and
`completion_tokens`. That is what kagent's client sends and what this
table has always meant by OpenAI-compatible.

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
argv, environment listings, or logs. For providers other than Copilot, the
repository currently has only a checkout setup helper, not a public `kmx`
capture command:

```bash
# Checkout-only repository setup:
make model-secret NAME=anthropic-api-key
# Paste the key, press Enter, then Ctrl-D.
```

The pipeline strips the trailing newline before the key reaches
`kubectl create secret --from-file=api-key=/dev/stdin`. A newline left
in the Secret would corrupt the Authorization header on every request.
To rotate, `kubectl -n kagent delete secret <name>` and re-run.

## Switching the agent

`kmx use` has been removed with the rest of the legacy operational CLI. The
presets below are still carried in `k8s/models/` and still apply with kubectl,
but kmx no longer switches a kagent Agent onto one:

```bash
kubectl --context <ctx> apply -f k8s/models/anthropic.yaml
kubectl --context <ctx> -n kagent patch agents.kagent.dev hello-world \
  --type merge -p '{"spec":{"declarative":{"modelConfig":"anthropic"}}}'
```

One thing still bites people: **create the preset's Secret before switching.**
An agent pointed at a ModelConfig whose Secret is missing never becomes Ready
([FAQ](FAQ.md#hosted-model-authentication-fails)).

For an owner-managed application, the supported route is `kmx migrate`, which
changes the model seam without any of this.

## Azure AI Foundry rides `provider: OpenAI`, deliberately

kagent 0.9.12 has an `azureOpenAI` provider, but its `apiVersion` field
is **required**, and that field belongs to Azure's legacy per-version
API surface. Kaimahi pins Foundry's **v1 GA** API, which is a plain
OpenAI-compatible endpoint with **no** api-version parameter. The two
are incompatible, so the `azure-foundry` preset uses `provider: OpenAI`
with the v1 base URL:

```yaml
openAI:
  baseUrl: https://YOUR-RESOURCE.openai.azure.com/openai/v1
```

Set `model` to your **deployment** name, not the upstream model name.
The same pattern covers OpenRouter and any other OpenAI-compatible
endpoint.

## GitHub: Models is retired, the Copilot subscription path replaces it

GitHub Models used to be the obvious keyed endpoint for anyone with a
GitHub account. That service no longer exists: **GitHub retired GitHub
Models entirely on 2026-07-30**, playground, model catalog, inference
API and BYOK, for all customers including existing ones
([changelog](https://github.blog/changelog/2026-07-30-github-models-is-now-retired/)).
Verified directly on 2026-08-31: `https://models.github.ai/inference/...`
returns HTTP 410 (`github_models_retirement_brownout`) even with a valid
`gh` OAuth token. No preset for it ships.

What a GitHub subscription still provides: **GitHub Copilot plans
include API access to OpenAI and other models** at
`api.githubcopilot.com`, an OpenAI-compatible endpoint. The
`github-copilot` preset targets it:

```bash
make copilot-secret               # checkout helper: kagent/github-copilot-token
```

The retained checkout helper, `make copilot-secret`, logs you in once via
GitHub's device flow (open the printed URL, enter
the code), caches that OAuth token 0600 under `~/.config/kaimahi/`
(override with `KAIMAHI_COPILOT_TOKEN_FILE`), exchanges it at GitHub's
Copilot token endpoint, and stores **only the short-lived Copilot token**
in-cluster. If you have only a cache under the former project name, log in
again or explicitly select that cache with `KAIMAHI_COPILOT_TOKEN_FILE`;
there is no automatic migration of that old path.

Custody properties worth knowing:

- **The gh CLI's own OAuth token is not Copilot-entitled.** The exchange
  returns 403 for it (verified 2026-08-31). The device flow authenticates
  as the Copilot-entitled VS Code OAuth client, which is what Copilot
  tooling itself does. Same terminal-login UX, one extra browser approval
  on first run, cached after that.
- **The device-flow OAuth token never enters the cluster.** Only the
  short-lived exchange token does. All token bytes travel through 0600
  temp files and pipes; nothing touches argv, env listings, YAML, or
  logs, and no keyed call follows redirects. Fail-closed: a failed or
  empty exchange stores nothing.
- **The exchanged token expires**, typically within hours. For the direct
  preset, re-run `make copilot-secret` so the rotated
  `kagent/github-copilot-token` Secret is in place.
  For the plane-side route, use `kmx models credential copilot`: it writes
  **only** `kaimahi/kaimahi-copilot-token` and restarts an existing plane.
  That native command does not populate the direct preset's Secret and uses
  the standard OAuth cache path without the script's environment override.
  Neither path has an in-cluster auto-refresher
  ([FAQ](FAQ.md#hosted-model-authentication-fails)).
- **`api.githubcopilot.com` is not part of GitHub's documented public API
  surface.** GitHub's documented programmatic paths are the Copilot
  CLI/SDK and BYOK. It is the endpoint GitHub's own clients and
  sanctioned third-party integrations use, but treat it as subject to
  change without notice, and mind your plan's premium-request
  accounting.
- **Model IDs are the Copilot catalog's** (e.g. `gpt-5-mini`,
  `gpt-4o-mini`, `claude-*`, `gemini-*`); the preset defaults to
  `gpt-5-mini`.

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

A bare `kmx up` writes the verified route into the **Orka** `local` Provider
(`defaultModel` and `baseURL` in `orka-system`), which is the only model
configuration that run creates. It renders no kagent `ModelConfig`: the bundled
preset is rendered only by the explicit `kmx up --step agent` (and `--step
tools-agent`), where a later run also preserves an already-verified host route.
The follow-up commands printed after a bare run carry no explicit host endpoint —
the Provider holds it — and the bundled plane preset is not offered because it
requires in-cluster Ollama.

`MODEL=<tag> kmx up --step model` pulls another Ollama model into the pod; a full
`kmx up` then resolves that model through the Orka Provider it wires, and
`kmx up --step agent` renders it into the bundled kagent ModelConfig. Test it with
several fresh chats before trusting it:
small models misfire kagent's built-in `ask_user` tool, and small models
that call a tool correctly can still garble its output in the summary
([getting-started.md](getting-started.md#choices-and-caveats),
[FAQ](FAQ.md#the-tool-worked-but-the-answer-is-wrong)). The Ollama
pod stores models in an `emptyDir`, so a restart loses the cached model;
repeat the pull when needed.
