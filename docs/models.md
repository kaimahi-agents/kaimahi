# Models and endpoints

The hello-world agent thinks with an in-cluster Ollama model by default.
This doc is how to make the same agent think with a hosted endpoint
instead. Each endpoint is a kagent `ModelConfig` preset committed under
[`k8s/models/`](../k8s/models/), and one make target switches the agent
between them. Nothing else changes: same cluster, same agent YAML, same
`make chat`.

> **A plain hosted preset is a live credit card.** Switching to one
> sends every conversation to a billed API with no budget, metering, or
> ledger in front of it. Kaimahi's governance plane puts exactly that in
> front of it: budgets that fail closed, a ledger of every call, and the
> real key held away from the agent. That is [spend.md](spend.md), and
> the `governed-*` presets below are its entry point. Either accept the
> ungoverned path knowingly or use a governed preset.

## The presets

| Preset (`k8s/models/`) | Endpoint | Key Secret expected | Live-verified? |
|---|---|---|---|
| `ollama` | in-cluster Ollama (keyless, free) | none | **yes**, keyless end to end in CI on every PR |
| `github-copilot` | Copilot subscription (OpenAI models via api.githubcopilot.com) | `github-copilot-token` (via `make copilot-secret`) | **yes**, 2026-08-31, `gpt-5-mini`, A2A task completed |
| `anthropic` | Anthropic first-party API | `anthropic-api-key` | not live-verified |
| `openai` | OpenAI first-party API | `openai-api-key` | not live-verified |
| `openrouter` | OpenRouter gateway | `openrouter-api-key` | not live-verified |
| `azure-foundry` | Azure AI Foundry, v1 GA API (edit `baseUrl` + `model` first) | `azure-foundry-api-key` | not live-verified |
| `openai-compatible` | any OpenAI-compatible base URL (template, edit first) | `openai-compatible-api-key` | not live-verified |
| `governed-ollama` | Ollama through the kaimahi proxy | `kaimahi-governed-token` (via `make govern`) | **yes**, live and in CI. See [spend.md](spend.md) |
| `governed-copilot` | Copilot through the kaimahi proxy | `kaimahi-governed-token` (via `make govern`), plus `make plane-copilot-secret` for the proxy | **yes**, once, on AKS. See [spend.md](spend.md) and [aks.md](aks.md) |

"Not live-verified" means exactly that. The preset is schema-valid
against the kagent 0.9.12 CRDs, which CI proves with a server-side
dry-run on every PR, so the YAML is well-formed and the fields exist. But
no real completion has been bought through it yet. A preset graduates to
live-verified only when an actual `make chat` completes through the
endpoint, and nobody has paid to do that for those five. They should
work. "Should" is the honest word. (More in the
[FAQ](FAQ.md#what-schema-valid-only-means).)

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
[spend.md](spend.md#the-two-protocols); adding an upstream that speaks
either is [`kmx models add`](kmx.md#kmx-models-add).

Your own model endpoint — in-cluster, keyless, either protocol — is
onboarded rather than committed: it goes into the operator overlay,
which the next `kmx plane` does not discard. A hosted endpoint holds a
real API key and stays a reviewed entry in
[`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml).

## Storing an API key

Keys go in Kubernetes Secrets and nowhere else: never in YAML,
ConfigMaps, argv, environment listings, or logs. The make target reads
the key from stdin only:

```bash
make model-secret NAME=anthropic-api-key
# Paste the key, press Enter, then Ctrl-D.
```

The pipeline strips the trailing newline before the key reaches
`kubectl create secret --from-file=api-key=/dev/stdin`. A newline left
in the Secret would corrupt the Authorization header on every request.
To rotate, `kubectl -n kagent delete secret <name>` and re-run.

## Switching the agent

```bash
make use PRESET=anthropic      # apply the preset, point the agent at it, wait Ready
make chat                      # same conversation, different brain
make use-ollama                # back to the keyless local model
```

`make use` applies `k8s/models/$(PRESET).yaml` and patches the one field
that matters on the Agent, `spec.declarative.modelConfig`. The kagent
controller rolls the agent deployment; the target waits for both the
rollout and the Agent's Ready condition. The committed
`k8s/hello-world.yaml` is never edited.

Two things bite people here:

- **Create the preset's Secret before switching.** An agent pointed at a
  ModelConfig whose Secret is missing never becomes Ready, and `make use`
  hangs waiting for it
  ([FAQ](FAQ.md#make-use-hangs-at-waiting-for-ready)).
- **`make use` only touches `hello-world`.** The tools agent keeps its
  own `modelConfig`; point it at a preset by patching that field
  yourself if you want it on a hosted model.

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
make copilot-secret                 # GitHub device login -> Copilot token -> K8s Secret
make use PRESET=github-copilot
make chat
```

`make copilot-secret` (script: [`scripts/copilot-secret.sh`](../scripts/copilot-secret.sh))
logs you in once via GitHub's device flow (open the printed URL, enter
the code), caches that OAuth token 0600 under `~/.config/kaimahi/`
(override with `KAIMAHI_COPILOT_TOKEN_FILE`), exchanges it at GitHub's
Copilot token endpoint, and stores **only the short-lived Copilot token**
in-cluster. If you logged in under the old tomte name, the
[FAQ](FAQ.md#i-have-a-cluster-and-paths-from-the-tomte-era) has the
one-line cache migration.

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
- **The exchanged token expires**, typically within hours. When the agent
  starts failing auth, re-run `make copilot-secret` and then
  `make use PRESET=github-copilot` (the pod must restart to pick up the
  rotated Secret). On the governed path it is `make plane-copilot-secret`
  instead, and no restart: the proxy reads the Secret-mounted file per
  request. An in-cluster auto-refresher was deliberately not built; token
  lifecycle is governance-plane territory
  ([FAQ](FAQ.md#the-copilot-preset-worked-yesterday-and-fails-today)).
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

`make model MODEL=<tag>` pulls another Ollama model into the pod; then
edit `model:` in the ModelConfig of [`k8s/hello-world.yaml`](../k8s/hello-world.yaml)
and re-apply. Test it with several fresh chats before trusting it:
small models misfire kagent's built-in `ask_user` tool, and small models
that call a tool correctly can still garble its output in the summary
([getting-started.md](getting-started.md#choices-and-caveats),
[FAQ](FAQ.md#the-tool-call-worked-but-the-answer-is-wrong)). The Ollama
pod stores models in an `emptyDir`, so a restart re-pulls
([FAQ](FAQ.md#the-model-i-pulled-disappeared-after-a-pod-restart)).
