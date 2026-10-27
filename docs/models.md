# Models and endpoints

Model selection is separate from runtime selection. Native Orka Agents use
Providers; interactive host inference can instead use detected local models,
Copilot or Foundry without rewriting the stored Orka Provider. See
[Orka installation](orka.md), [native workspace lifecycle](suite-workspace.md),
[Copilot inference](copilot-inference.md) and
[Foundry inference](local-foundry-inference.md).

The legacy model presets and plane overlay commands are removed. KMX does not
provide a replacement model proxy, budget service or automatic credential
migration. Hosted calls can incur charges; configure access and limits through
the selected provider and runtime.

For the single-node AKS CPU test template and its explicit-context runbook, see
[Azure CPU-model sample](azure/cpu-model/README.md#single-node-cpu-inference-test).

## Installed local model discovery

`kmx model list` reports Foundry Local and Ollama inventory without cluster
configuration, prompts, runtime startup, installation or model downloads. Use
`kmx model list -o json` for the same bounded reports and non-secret model routes.
It distinguishes `absent`, `empty`, `installed` and `failure`; an installed but
stopped runtime is not an empty inventory. A failure reports a safe reason, not
raw runtime output. Both table and JSON output print the complete report before
exiting nonzero if any provider reports `failure`; `absent` and `empty` remain
successful. Discovery does not select a model or change a saved endpoint.

Foundry Local CLI preview 0.11 supplies its dynamic endpoint through
`foundry server status`; KMX then reads cached `/openai/models`. It never guesses
a Foundry port or runs `model list`, which can download a catalog. Ollama uses
loopback port 11434 and reads `/api/tags`. Local discovery is separate from the
legacy kind-reuse checks below. The route includes provider, exact model,
OpenAI-compatible endpoint and a non-secret authentication selector; it does not
implement local agent execution or a runner.

Missing/empty/unavailable runtimes show official install links, platform
requirements and download estimates (unknown where not published). Foundry Local
is suggested on supported Windows. CLI 0.11.0 download estimates are dated
2026-10-07: Windows x64/ARM64 about 24 MB, macOS Apple Silicon about 48 MB, Linux
x64/ARM64 about 55–58 MB. Models and execution providers need additional space;
model memory requirements depend on the chosen model. Ollama on Windows needs
at least 4 GB installed binary space, not a 4 GB download. Nothing is installed
when listing or declining an install link.

Sources: [Foundry CLI reference](https://learn.microsoft.com/en-us/azure/foundry-local/reference/reference-cli),
[Foundry CLI downloads](https://github.com/microsoft/Foundry-Local/releases/tag/cli-preview-0.11.0),
[Ollama downloads](https://ollama.com/download),
[Windows requirements](https://docs.ollama.com/windows),
[macOS requirements](https://docs.ollama.com/macos) and
[Linux installation](https://docs.ollama.com/linux).

The independent model-access package has native Windows/macOS/Linux tests, but
the narrow [native Windows day-zero CLI](windows.md) does not expose model
inventory, keychain, provider or network routes. Package tests are not command
qualification. Use WSL for these operations.

## Canvas model choice

The current canvas uses a canvas-only picker override, then the authored model,
then the known current chat model when available, then `gpt-5.6-terra`.
Omitting Model does not rewrite the agent file. The picker reads
`kmx://models` on focus; a catalog failure does not block editing or offline tests.
Unknown models report the available choices without falling back. Trusted startup
provider/endpoint/auth selection is unchanged. See [MCP](kmx.md#local-stdio-mcp).

## Explicit Copilot model access

```bash
kmx model list --provider copilot -o json  # uses an existing gh sign-in
kmx model login                     # optional, without gh: GitHub device code
kmx model login --gh                 # optional explicit copy into kmx's keychain
kmx model logout                    # only kmx's credential; not gh/editor logout
```

These unreleased, config-free commands are independent of the installed-CLI
chat adapter. Device flow uses VS Code's public OAuth client
`Iv1.b507a08c87ecfe98` with `read:user`. The reusable GitHub credential is stored
in kmx's OS keychain item, never workspace/config files or a plaintext cache.
Only native Windows Credential Manager, macOS Keychain and Linux Secret
Service are allowed, via `github.com/zalando/go-keyring v0.2.8`, with no file/pass
fallback. macOS works with the release's `CGO_ENABLED=0`: explicit credential
operations invoke `/usr/bin/security`; writes send the secret only on stdin,
never argv, environment or files. macOS credentials over 2048 bytes are refused
before starting the utility. The macOS item uses the security utility's keychain
access controls, not a kmx-binary-specific application ACL; other processes
running as the same user may access it through that utility.

Linux requires an existing session bus and login/default keyring collection. Explicit
credential operations use no-autostart D-Bus discovery before native access;
unavailable keychains do not launch a bus or create a token file. Custom
sessions must supply a `unix:` `DBUS_SESSION_BUS_ADDRESS`; `XDG_RUNTIME_DIR`
alone does not direct upstream discovery to a custom socket. Autolaunch,
non-Unix and multi-address values are refused. The owned item uses service
`kmx-model-copilot` and account `github-reusable-token`, not a dedicated
collection. Logout deletes only that item. Access may show OS unlock UI;
native keychain calls have no context-cancellation guarantee. Initialization,
help, completion and default local model listing do not access the keychain or
session bus. Copilot listing never initiates OAuth, reads editor credentials
or selects a model.

At runtime, an explicit `COPILOT_GITHUB_TOKEN` takes precedence without opening
kmx's keychain. An empty or malformed value is an error, not a reason to switch
accounts. Otherwise kmx uses an existing github.com GitHub CLI sign-in through
`gh auth token --hostname github.com`, bounded to five seconds and 16 KiB of
private stdout, without opening kmx's keychain. If gh is unavailable, an existing
kmx keychain credential remains usable. No sign-in is started automatically. This runtime child excludes `GH_TOKEN` and `GITHUB_TOKEN`, so
those variables cannot override the stored login; explicit `login --gh` retains
normal gh environment precedence. A malformed gh capture or caller cancellation
is an error; gh's own capture timeout can use the existing keychain fallback.
Authentication, subscription, policy or quota service refusals never trigger
another credential source. Neither
runtime source is copied into kmx storage or a token file.

With an unavailable keychain, `kmx model login` (including `--gh`) prints these
options instead of starting device sign-in or failing. It does not sign in gh,
claim a credential was saved, or verify the runtime credential. When a token
variable is set, login warns that it takes precedence over stored credentials.
Logout cannot disable external credentials: unset the variable or end the gh
session separately to stop runtime access. To recover a revoked runtime gh
credential without a usable OS keychain, supply a fresh environment token.
`gh auth login --hostname github.com` can renew gh auth with secure storage,
but gh may write a plaintext token file when its credential store is unavailable;
kmx never invokes that login command automatically.

Copilot JSON output is an array of non-secret shared routes: provider `copilot`,
exact available model, endpoint `https://api.githubcopilot.com`, auth
selector `copilot`. Enabled Chat-type models supporting `/chat/completions` or
`/responses` are listed. Protocol selection follows that catalog metadata;
Responses-only models are translated behind the existing Chat-shaped caller,
while models offering both protocols keep Chat. Actual upstream model identity
and incomplete/refusal/tool outcomes are preserved. The default local list and its report shape are unchanged. There is no
persisted/global selection, automatic model substitution or proxy.
Markdown-file `kmx run FILE --provider copilot` and
`kmx test FILE --provider copilot` consume this route through the temporary
tool-free preview runner; set the file's `Model.Preferred` to an exact listed
model first. The endpoint defaults to the fixed Copilot endpoint, and execution
never initiates login. Instructions and inputs are sent to the hosted service.
Unbound MCP uses the same route by default for the effective non-fake model,
resolved from the runtime override, then the authored model, then the
`gpt-5.6-terra` fallback. Trusted startup provider overrides still take precedence. Legacy root-bound MCP
requires an explicit route for real models. This is not a new harness or native
workspace/deployment integration. See
[file preview](kmx.md#agent-file-local-preview-unreleased-integration) and
[direct Copilot transport](copilot-inference.md#direct-model-access)
for usage, refusal codes and authentication behavior.

## Storing an API key

Native Orka Providers reference an existing namespaced Kubernetes Secret.
Provision it separately; native evaluation/Deploy accepts only name/key references,
not key bytes. Never put keys in command arguments, checked-in YAML, ConfigMaps
or logs. Offline workspace create neither provisions credentials nor bootstraps a model.

The key must have no trailing newline: Orka can report a Provider Ready while
every Task fails because a line ending corrupts its Authorization header. In
Bash or Zsh, read a key file without exposing the value on the command line:

```bash
kubectl --context <ctx> -n <ns> create secret generic <name> \
  --from-file=api-key=<(tr -d '\r\n' < <path>)
```

See the [native workspace guide](suite-workspace.md) for Provider/model
prerequisites, full evaluation and private evidence; configure result access separately.

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
plane-side token exchange. Host Copilot is retired from the wizard;
`/inference-copilot` in chat discovers models and opens the model picker.
Credentials remain managed by the installed CLI and are not copied into the
cluster by this adapter. Read the [Copilot guide](copilot-inference.md) for
supported tools, per-turn limits and authentication boundaries.

## Swapping the local model

A full `kmx cluster up` first probes the host's loopback Ollama API. It offers reuse
only when `/api/tags` reports at least one installed model. Reuse is opt-in;
KMX's bundled model remains the default. Redirected sessions,
`kmx cluster up --step ...`, and an explicit `MODEL` never probe or prompt.

`kmx cluster quickstart` never probes or prompts at all. It is deterministic and
non-interactive on purpose — there is no host-model picker on that path, so
it always deploys the in-cluster Ollama and the bundled model, and the same
command on the same machine produces the same fixed native suite source. It does
not read, write or replace the `local` Provider: the suite has its own
in-cluster endpoint and only shares the placeholder `local-provider-key`
Secret. Guided quickstart uses the native tool-free path; host Foundry/Copilot
wizard modes are retired. Ordinary host-backed chat controls remain independent.

Before reusing host Ollama, KMX verifies the selected tag through an endpoint
reachable from the kind node, trying the engine host alias and kind bridge
gateway. If neither works, setup installs the bundled model instead. Reuse
skips both the in-cluster Ollama deployment and model pull. Limit host Ollama's
exposure to the container network rather than publishing its unauthenticated
API to the LAN.

`kmx cluster up` writes the verified route into the **Orka** `local` Provider
(`defaultModel` and `baseURL` in `orka-system`), which is the only model
configuration that run creates. `kmx cluster up --step orka` refuses to replace an
existing `local` Provider whose endpoint differs; its error names an explicit
`kmx cluster orka install --model-url` command that **replaces the host route** if you
choose to run it. Quickstart does not require that replacement. The follow-up
commands printed after `kmx cluster up` carry no explicit host endpoint — the Provider
holds it.

`MODEL=<tag> kmx cluster up --step model` pulls another Ollama model into the pod; a
full `kmx cluster up` then resolves that model through the Orka Provider it wires. Test
it with several fresh chats before trusting it: small models misfire a
runtime's built-in question tool, and small models that call a tool correctly
can still garble its output in the summary
([getting-started.md](getting-started.md#choices-and-caveats),
[FAQ](FAQ.md#the-tool-worked-but-the-answer-is-wrong)). The Ollama
pod stores models in an `emptyDir`, so a restart loses the cached model;
repeat the pull when needed.
