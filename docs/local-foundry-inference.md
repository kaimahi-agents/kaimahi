# Foundry inference for local Agent development

Status: native Orka proposal plus implemented host-inference option, 2026-09-18.

## Interactive host inference (implemented)

On a local kind target, choose **Azure Foundry (Azure login)** in interactive
quickstart, or use `/inference` → **Azure Foundry** in chat
(`/inference-foundry` is the direct shortcut). Host inference is refused on remote
targets; configure a cluster Provider there instead.
Select a saved configuration, browse subscriptions/resources/deployments, or
enter an endpoint and deployment. The host uses the existing `az login` identity
with refreshable Entra tokens. No Azure API key or access token is copied into
Kubernetes or saved settings. Endpoint/deployment/optional tenant and scope are
stored privately at `$KMX_HOME/foundry-inference.json` when `KMX_HOME` is set,
or under the native KMX config directory (`~/.config/kmx` on Linux and
`~/Library/Application Support/kmx` on macOS). The manual form accepts the
Cognitive Services or AI token scope; Cognitive Services is the default. Azure
inference role assignment and endpoint access are still required.

Setup explicitly tests a model function call before switching. Azure discovery
is optional, read-only and cancellable; no account/deployment is created. Failed
setup preserves the previous chat mode. The inference label says **Foundry / host**:
this path uses native model function calls and the existing KMX registered HTTP
tool executor, not Orka worker Jobs. MCP/auth/policy/approval tools require native
Orka execution and remain unavailable. Tool calls are schema checked, bounded
to eight calls/steps, with no automatic HTTP/tool replay. The client reuses Azure
tokens, HTTP connections and tool tunnels across turns.

Selecting this option in quickstart skips Ollama installation/download/prewarm.
The authored Agent retains a deferred local Provider; native Provider mode will
need a working model endpoint before it can run. Existing Agent Providers are
not changed by choosing host Foundry. This mode is for fast local testing; use
native Orka with a configured Foundry Provider when testing Orka runtime parity.

**Live benchmark status:** host Entra requests to the previously benchmarked
`gpt-5.1` deployment returned HTTP 401 with both documented token audiences.
No successful local-host Foundry latency is claimed. Resolve tenant/audience,
endpoint authentication and inference-role access, then rerun:

```sh
KMX_PROFILE_MODE=foundry KMX_PROFILE_CONTEXT=kind-kaimahi-p1 \
  KMX_PROFILE_FOUNDRY_ENDPOINT=https://YOUR-RESOURCE.openai.azure.com \
  KMX_PROFILE_FOUNDRY_DEPLOYMENT=YOUR-DEPLOYMENT \
  go test ./internal/kmx/app -run '^TestLiveChatPerformance$' -v -count=1 -timeout=540s
```

For end-to-end speed, this route removes the measured Copilot repeated-process
lifecycle and Orka Job startup/completion waits, but model/network latency still
needs measurement. Successful AKS/Foundry turns measured 12.54–16.15 s including
6.20–9.10 s of model work; these are not measurements of the new host route.

The remaining sections describe the separate native-Orka integration proposal.

## Recommendation

Support **local Orka execution with Azure Foundry inference** as a peer to
Ollama and Copilot CLI in quickstart. Keep Agent resources, Tasks and tools in
the selected local Kubernetes cluster; configure their Provider to call Foundry.
This tests the native Orka execution path without downloading local model weights
or relying on Copilot CLI's prompt subprocess and KMX tool adapter.

This is technically feasible with the pinned Orka release and existing KMX
Provider scaffolding. Discovery, credential setup and endpoint probes already
exist inside `/lift`. The missing work is integration and runtime verification,
not a new agent executor. No AKS cluster, Foundry project or Foundry Agent Service
is required. Azure model inference remains billed and network-dependent.

| Mode | Agent/tool execution | Model execution | Primary tradeoff |
|---|---|---|---|
| Local Orka + Ollama | Local Orka worker Jobs | Local model runtime | Weight download and local compute |
| Copilot CLI | Host CLI plus restricted KMX tool adapter | Copilot-selected hosted model | Convenient existing login; CLI overhead and different tool path |
| Local Orka + Foundry (recommended) | Local Orka worker Jobs | Selected Azure deployment | Native Orka parity; Azure access and pod egress required |
| Host-direct Foundry (implemented) | Host model/tool loop using Azure CLI credentials | Selected Azure deployment | Avoids worker Job latency, but does not exercise Orka worker execution |

Host-direct inference is implemented as described above. The proposed native
Foundry quickstart would instead test Orka worker execution. Its Job scheduling
and result overhead would remain; compare latency on the same model/task before
claiming an improvement.

## Evidence and existing configuration

Microsoft documents using the regular OpenAI client with:

- `https://<resource>.openai.azure.com/openai/v1/`, or the supported
  `services.ai.azure.com/openai/v1/` endpoint.
- The **deployment name** as `model`, not necessarily the underlying model name.
- API-key or Microsoft Entra ID authentication; no dated `api-version` is needed
  for the v1 GA surface.

Sources: [v1 API](https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle),
[switching endpoints](https://learn.microsoft.com/en-us/azure/foundry-classic/openai/how-to/switching-endpoints).

Orka v0.1.3's
[OpenAI Provider](https://github.com/orka-agents/orka/blob/b07d42c0b9e52fe511b434827a342b4720f5d422/internal/llm/openai/provider.go)
accepts `baseURL` and an API key through the standard OpenAI Go client. It tries
Responses and falls back to Chat Completions for recognized unsupported-surface
errors. It supports native function-call messages on both surfaces. This is source
evidence, not proof that every Foundry deployment/model accepts its request shape.

The corresponding native Provider shape is already generated by lift:

```yaml
apiVersion: core.orka.ai/v1alpha1
kind: Provider
metadata:
  name: dev-foundry
  namespace: orka-system
spec:
  type: openai
  baseURL: https://example.openai.azure.com/openai/v1
  defaultModel: my-chat-deployment
  secretRef:
    name: dev-foundry-key
    key: api-key
```

The Secret must be provisioned separately. The `openai` configuration above is
still the route for the v1-compatible `/openai/v1` endpoint. Native Orka
`azure-openai` instead takes the resource root URL, an Azure deployment and an
optional API version through `kmx agent create`; see the
[create guide](kmx.md#kmx-agent-create). The `kmx quickstart --interactive` hosted Foundry
choice continues to use host inference with a local Provider fallback, not a
native hosted Provider. Its agent-create wizard can author a native Azure
Provider separately. An already-configured local Agent using the v1 Provider
can use normal Orka chat. Guided native Provider provisioning remains in the
bundle-less live-copy lift route. Host Foundry configuration is also available
in interactive quickstart and chat; it does not provision a native Foundry
Provider or copy the host's Azure credentials into the cluster.

## Reuse, then decouple, existing setup

| Code | Reusable behavior / required adjustment |
|---|---|
| `chat_lift_foundry.go` | Subscription, AIServices/OpenAI account and deployment selection; explicit provisioning; key retrieval; Provider construction. Extract from lift-specific renderer and target naming. |
| `chat_lift_endpoint.go` | Readiness polling, temporary namespace probe Job, Secret mount and cleanup. Add native Orka acceptance, not just HTTP probe acceptance. |
| Foundry quota/preflight helpers | Reuse only when creating resources; selecting an existing deployment does not require new account/model capacity planning. |
| `chat_lift_prerequisites.go` | Ready Provider picker and inference selection. Generalize for a local target and existing Agent, without entering `/lift`. |
| `quickstart_wizard.go` | Add an explicit Foundry branch; skip Ollama install/pull/prewarm; install Orka/tools and provision the selected Provider credential. |
| `copilot_prompt.go`, `chat_orka_controls.go` | Replace the assumption that non-Copilot means locally hosted model; expose Provider/deployment details and an inference picker. |

Do not route Foundry through the current non-bundled host-model branch. That
branch tests host runtimes from kind and may fall back to bundled Ollama; a
Foundry authentication/network failure should return to selection with its cause.
Do not append `/v1` to an already-normalized `/openai/v1` endpoint via
`chooseModel` or the setup-event handler. Use a structured inference selection
with executor, provider kind, exact base URL, deployment and credential reference.

Quickstart currently installs a placeholder `kickstart-provider-key` in its Orka
setup helper. Foundry needs a separate generated Secret name propagated through
the creation bundle; never overwrite it with that placeholder. The setup worker
must report results through wizard events and leave stdin to the visible pane.
Foundry picker/provisioning work needs a proper wizard phase or a between-program
handoff, not a nested Bubble Tea reader in a background setup command.

## Authentication and networking

**First supported path: an API-key-backed Kubernetes Secret.** Reuse the current
key-handling pattern: hold key bytes in memory, create a namespaced Secret through
stdin, omit keys from argv, logs, saved selections and version artifacts. Support
both a pre-existing Secret reference and explicit key input through a masked
prompt/stdin/environment source. Endpoint + deployment + Secret should work without
Azure CLI or permission to enumerate subscriptions/list account keys.

Azure discovery via `az login` is a convenience. Management-plane discovery or
`listKeys` permission is different from inference permission. A principal with
data-plane access may not be able to list accounts/keys; allow explicit endpoint
configuration instead of treating that as “no models available”.

**Entra login is desirable but not a free extension of our current worker path.**
The v1 service supports refreshable token credentials, and KMX already uses
`DefaultAzureCredential` for some host-side Azure discovery. The inspected Orka
v0.1.3 OpenAI constructor uses a static API key; it does not install a refreshable
Azure token credential. A host's `az login` does not authenticate a worker pod.
Do not copy a one-time access token into a Secret and present it as durable setup.
Accounts with `disableLocalAuth` need worker-side token-refresh/workload-identity
support, or an explicitly host-direct implementation, before they are supported.
Managed identity available on AKS cannot be presumed on a local kind cluster.

The model endpoint must be reachable from the **local worker pod**, not merely
the host's browser or Azure CLI. Private endpoints can require VPN routes and
private DNS accessible to Docker/Podman and kind. Keep the cluster probe and
separate auth, quota, model compatibility and networking diagnostics. Do not
change Foundry firewall/network settings automatically to make a test pass.

## User experience

Proposed setup choices:

```text
Copilot CLI — auto (detected)
Azure Foundry — hosted model, local Agent and tools
Local Orka Model — available to install
<detected host models>
```

Foundry opens `Existing Provider / Browse Azure deployments / Enter endpoint`.
Browse shows deployment name, model/version, resource group and region. Start
with existing deployments; keep create-account/deployment explicit as in lift.
Only offer chat/tool-capable models supported by the selected execution path:
the current lift filter (`Succeeded` and model format `OpenAI`) can also include
non-chat deployments and is insufficient by itself. Unknown capabilities need
validation, not an assumption of tool support.

At completion, show execution location and inference separately:

```text
agent helper · location local-kind
inference Foundry · my-chat-deployment · gpt-4.1-mini / <version>
execution Orka Task worker · tools on local-kind
```

Add an `/inference` picker (retain current shortcuts as aliases) or a dedicated
`/inference-foundry` entry. A provider switch configures a fresh/reusable matching
Provider, verifies it, and patches only the Agent binding with UID/resourceVersion
checks. Do not rewrite a shared Provider that other Agents are using. Preserve
the previous connection/selection on failure or cancellation. State clearly that
changing the Agent binding affects subsequent Tasks, not already-running ones.

The current wording `Local Orka Provider` and `fresh local Task` is ambiguous for
remote inference or remote clusters. Replace it with actual executor location
and Provider details. Azure credentials remain distinct from Copilot login.

## Interaction with Agent revisions

The separately developed definition hash excludes Provider/model/Secret bindings.
Testing the same prompt/tools with Foundry should therefore preserve its release
content identity while Kubernetes generation can advance for a binding edit.
Persist/display an additional inference observation: Provider UID/generation,
endpoint, Azure deployment name, discovered model version and check time. This
observation is not implemented in this Foundry change. Agent editing/versioning
is maintained on a separate branch and is not part of this implementation.

A deployment name is mutable: its model can be upgraded independently. Record
the selected/observed model version where available and surface changes rather
than interpreting “same deployment name” as exact model reproducibility. Keep
inference observations and Secret values out of portable definition hashes.
The separate Agent push design preserves remote inference bindings; lift makes
its target inference selection explicit.

## Delivery and proof

1. Extract target-neutral Foundry configuration returning an inference binding.
   Support existing Provider and explicit endpoint/Secret first; then reuse the
   Azure browse path. Preserve lift behavior through existing fixtures.
2. Add wizard branch and chat switching. Skip local model setup, retain Orka and
   tool installation, and show actual inference identity in the header/info pane.
3. Add capability checks and native-worker acceptance: a small plain response,
   followed by a read-only Tool call with arguments/result validated. The current
   Python probe calls only `/chat/completions`, while Orka first tries `/responses`.
   Check actual model controls too: Orka can send a nonzero temperature (the Agent
   schema defaults to 0.7), which some reasoning models reject. Do not silently
   advertise every Foundry model as supported or drop user-requested settings.
4. Test auth failures, expired/revoked key, disabled local auth, 429/quota,
   unsupported model/API, private DNS/egress failure, cancellation and partial
   setup. Verify no Ollama download, key leakage, shared Provider mutation or
   unexpected Task replay. Run the same edit → chat → compare → lift flow with
   Foundry selected and the original Copilot/Ollama paths.
5. Benchmark first/warm response and tool turns; report model latency separately
   from Kubernetes Job/result overhead. Evaluate refreshable Entra worker support
   as a follow-up with upstream Orka, rather than expanding the KMX tool executor
   merely to get a second authentication method.

The original native-Orka assessment used repository/upstream source and Microsoft
documentation. Subsequent host-inference validation used Azure CLI credentials
and attempted two requests, both rejected with HTTP 401 as recorded above.
No Azure resources were provisioned or API keys copied.
