# Models and endpoints

Model selection is separate from runtime selection. Native Orka Agents use
Providers; interactive host inference can instead use detected local models,
Copilot or Foundry without rewriting the stored Orka Provider. See
[Orka installation](orka.md), [agent create](kmx.md#kmx-agent-create),
[Copilot inference](copilot-inference.md) and
[Foundry inference](local-foundry-inference.md).

The legacy model presets and plane overlay commands are removed. KMX does not
provide a replacement model proxy, budget service or automatic credential
migration. Hosted calls can incur charges; configure access and limits through
the selected provider and runtime.

## Storing an API key

Native Orka Providers reference an existing namespaced Kubernetes Secret.
Provision it separately; `agent create` accepts only its name and key reference,
not key bytes. Never put keys in command arguments, checked-in YAML, ConfigMaps
or logs. The metadata-only Secret skeleton in rendered output must not be applied.

The key must have no trailing newline: Orka can report a Provider Ready while
every Task fails because a line ending corrupts its Authorization header. In
Bash or Zsh, read a key file without exposing the value on the command line:

```bash
kubectl --context <ctx> -n <ns> create secret generic <name> \
  --from-file=api-key=<(tr -d '\r\n' < <path>)
```

See the [create contract](kmx.md#default-orka-contract) for the ordered
Provider → Agent → Task workflow and result-reader access.

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

## GitHub Models retirement and native Copilot inference

GitHub Models used to be the obvious keyed endpoint for anyone with a
GitHub account. That service no longer exists: **GitHub retired GitHub
Models entirely on 2026-07-30**, playground, model catalog, inference
API and BYOK, for all customers including existing ones
([changelog](https://github.blog/changelog/2026-07-30-github-models-is-now-retired/)).
Verified directly on 2026-08-31: `https://models.github.ai/inference/...`
returns HTTP 410 (`github_models_retirement_brownout`) even with a valid
`gh` OAuth token.

KMX's native Copilot path uses the installed, authenticated Copilot CLI, not a
plane-side token exchange. The interactive quickstart offers **Copilot · auto**;
`/inference-copilot` in chat discovers models and opens the model picker.
Credentials remain managed by the installed CLI and are not copied into the
cluster by this adapter. Read the [Copilot guide](copilot-inference.md) for
supported tools, per-turn limits and authentication boundaries.

## Swapping the local model

A full `kmx local up` first probes the host's loopback Ollama API. It offers reuse
only when `/api/tags` reports at least one installed model. Reuse is opt-in;
KMX's bundled model remains the default. Redirected sessions,
`kmx local up --step ...`, and an explicit `MODEL` never probe or prompt.

`kmx quickstart` never probes or prompts at all. It is deterministic and
non-interactive on purpose — there is no host-model picker on that path, so
it always deploys the in-cluster Ollama and the bundled model, and the same
command on the same machine produces the same fixed Agent bundle. It does
not read, write or replace the `local` Provider: the bundle has its own
in-cluster endpoint and only shares the placeholder `local-provider-key`
Secret. Choosing a host model while the runtime starts is
`kmx quickstart --interactive`.

Before reusing host Ollama, KMX verifies the selected tag through an endpoint
reachable from the kind node, trying the engine host alias and kind bridge
gateway. If neither works, setup installs the bundled model instead. Reuse
skips both the in-cluster Ollama deployment and model pull. Limit host Ollama's
exposure to the container network rather than publishing its unauthenticated
API to the LAN.

`kmx local up` writes the verified route into the **Orka** `local` Provider
(`defaultModel` and `baseURL` in `orka-system`), which is the only model
configuration that run creates. `kmx local up --step orka` refuses to replace an
existing `local` Provider whose endpoint differs; its error names an explicit
`kmx orka install --model-url` command that **replaces the host route** if you
choose to run it. Quickstart does not require that replacement. The follow-up
commands printed after `up` carry no explicit host endpoint — the Provider
holds it.

`MODEL=<tag> kmx local up --step model` pulls another Ollama model into the pod; a
full `kmx local up` then resolves that model through the Orka Provider it wires. Test
it with several fresh chats before trusting it: small models misfire a
runtime's built-in question tool, and small models that call a tool correctly
can still garble its output in the summary
([getting-started.md](getting-started.md#choices-and-caveats),
[FAQ](FAQ.md#the-tool-worked-but-the-answer-is-wrong)). The Ollama
pod stores models in an `emptyDir`, so a restart loses the cached model;
repeat the pull when needed.
