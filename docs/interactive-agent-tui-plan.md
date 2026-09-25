# Interactive agent console

`kmx console` displays one local kind environment and one remote Kubernetes
environment side by side. It requires an interactive terminal, at least 64×18.

## Try it

From this checkout:

```bash
make
bin/kmx console --demo
```

Demo mode uses sample agents, exercises navigation and command completion, and
performs no cluster or Azure operations. For live inventory:

```bash
bin/kmx console --local-context kind-dev --remote-context my-remote --namespace orka-system
```

Live reads need `kubectl`, configured credentials, and permission to discover API
resources and list Agents and Providers. Tool details additionally
read Orka Tools; ConfigMap-backed prompts read their referenced ConfigMap key.
The console reads `agents.core.orka.ai` in `--namespace` (default `orka-system`)
and nothing else. It is an Orka workspace: every operation it offers — list,
create, chat, inference, tools, lift — is an Orka operation.

Without context flags, each column remembers its last `/env` selection. The
remote column next prefers the last lift destination, then the configured kmx
context, then the first matching context. The local column prefers its saved
selection, then the configured context, then the first local context. Missing
saved preferences are skipped. The local column uses the same
kind-name plus loopback-server classification as kmx's context guard. Remote
contexts include saved lift locations with private kubeconfig snapshots. An
explicit missing/wrong-type context is reported instead of silently substituted.
When a saved lift context also exists in the default kubeconfig, its validated
private snapshot takes precedence. Newly provisioned lift environments inherit
the selected source context and kubeconfig, rather than the console's startup default.

When no remote environment is selected, its column collapses into a narrow setup
panel. Focus it and press `s` or Enter to select a remote context, or `L` to lift
a local agent; choose `new` in lift completion to create a remote environment.
The local inventory uses the remaining width. Selecting a remote restores equal
column widths. Native local Orka agents show `L lift` alongside their shortcuts
and offer Lift in the Enter actions popup.

## Reading the overview

Each agent shows its name, version, runtime, readiness, model and inference
provider, with indented fields and a horizontal separator after each entry.
Environment headings and connection status use darker, muted text without a
background. The selected agent in the focused column has a solid background
across its name and detail rows, with `c` chat and `i` inspect hints on the entry.
Enter opens a popup of available actions. `i` opens an opaque, centered details window
over the inventory with namespace, endpoint origin and inference readiness.
The inspector also shows the system prompt and configured tools, including
enabled/disabled state and Orka tool descriptions/transports. `p` jumps to the prompt, `t` to tools;
arrows or `j`/`k`, Page Up/Down, and Home/End scroll the content. Multiline prompts
retain indentation and wrap at word boundaries to the panel width. Labels are
highlighted separately from values, and wrapped descriptions/prompts maintain
their indentation on continuation lines. Unreadable ConfigMaps or tool
definitions are reported as unavailable.

Each agent entry shows enabled/configured tool counts and tool names.

Esc or Enter closes inspection and restores the same selection. External Orka runtimes
are identified and have inspection only.

Version is the `app.kubernetes.io/version` label. If absent, the dashboard says
`unversioned · gen N`: Kubernetes generation is not a release version. Provider
defaults resolve missing agent model overrides. Missing/unreadable inference
information is `unknown`. Errors stay visible alongside any successfully read
agents. Endpoint details omit credentials, paths and query parameters.
Missing/null Kubernetes list items are reported as errors, not empty inventory.
ConfigMap-backed prompt references are resolved; other prompt references are
identified without reading their contents.

Refresh with `r` or `/refresh`; reads run independently for each column. Selection
is retained by runtime/namespace/name when the selected agent still exists.

## Keys and slash commands

| Key | Action |
|---|---|
| `h` / `l`, left / right, Tab | Change column |
| `j` / `k`, down / up | Select agent, scrolling as needed |
| `g` / `G`, Home / End | First / last agent |
| Enter | Open available actions for the selected agent; arrows or `j`/`k` select, Enter runs, Esc closes |
| `i` | Inspect selected agent |
| `n` | Create a native Orka agent in the focused environment |
| `c` | Open selected agent's interactive chat |
| `L` | Start lift completion for selected local native Orka agent |
| `/` | Open bottom command bar |
| `r`, `?`, `q` | Refresh, help, quit |
| Esc | Close current input/details/help; quit from overview |

Hints change with the selected agent's available actions. While editing the
command bar, Vim letters type text. Up/down selects suggestions, Tab completes,
and Enter accepts a suggestion or runs a completed command. Left/right edits the
input. Completion itself never executes an operation.

```text
/inspect [agent]
/chat [agent]
/lift <local-agent> <remote-context>
/lift <local-agent> new
/env local|remote <context>
/refresh
/help
/quit
```

Chat and inspection resolve agents in the focused column. Lift always resolves
local native Orka agents, then suggests remote environments and `new`. Duplicate
names within a column use `runtime/namespace/name` completion to disambiguate.

The Enter actions menu also offers **Edit inference** (`f` in the menu) and
**Edit tools** (`t` in the menu). Inference opens a window
over the inventory. Select an existing Provider in the agent namespace plus an
optional model override. An empty
override uses the Provider default. Saving updates the agent reference, not
the shared Provider. The review names the agent, context and
configuration before saving. Saving and cancellation remain inside the overlay.
The agent's current cross-namespace Orka Provider is included with its namespace,
which is retained when selected and saved.

**Add inference source…** is the last item, even when no configurations exist:

The inference header uses a distinct title color, muted environment text and a
spaced divider above the choices. Every new source asks for its name last, with
an editable default derived from the source kind/provider and model settings.

| Source | Setup and execution |
|---|---|
| Azure (when `az` is installed) | Browse subscriptions, Foundry/OpenAI resources and ready deployments. For local kind, save a Foundry host connector. For remote Kubernetes, retrieve an API key during setup, create a cluster Secret and Provider, and test a small billed request from a cluster Job. |
| Foundry | Local kind: endpoint, deployment and optional tenant using host Azure login. Remote Kubernetes: endpoint, deployment and an existing cluster Secret/key reference; no local Azure CLI required. Remote setup probes inference from the cluster. |
| Ollama | Enter a connector name, cluster-reachable Ollama endpoint and installed model. Create an Orka Provider and select it. Orka uses a new, keyless dummy Secret required by its Provider schema. |
| Copilot | Local kind only. Select a model from an installed, authenticated Copilot CLI. Not offered or permitted for remote environments. |
| API key | Enter connector name, provider type, endpoint, model and an existing Kubernetes Secret/key reference. Create an Orka Provider. The form does not collect the credential value. OpenAI and Anthropic provider types are supported. |

These forms configure connectors to existing services; they do not provision a
Foundry resource/deployment, install Ollama/Copilot, or download model weights.
Missing logins and dependencies are reported in the result pane. New cluster
connectors are create-only; collisions stop setup. If a later step fails or is
cancelled, already-created resources remain available for inspection.

Host connectors are saved per context/runtime/namespace/agent under the private
`kmx/console-inference/` configuration directory. They contain routing metadata,
not tokens, and apply to subsequent chats launched from the console. They do not
change the cluster's Provider. Selecting a cluster source clears the host override.
The console labels host overrides explicitly and reports health as not checked.

Remote AKS and other Kubernetes environments always execute through cluster
configuration. Saved host overrides from earlier versions are ignored remotely;
chat also rejects host Foundry/Copilot selection and execution on non-local
contexts. Remote Foundry authenticates with an API key stored in Kubernetes,
not the user's Azure CLI session. Azure CLI is only a setup/discovery convenience.
Manual Secret-reference setup works without it. No workstation process is needed
for subsequent cluster inference. This is not managed/workload identity support:
Foundry accounts with API keys disabled need worker-side token refresh support
and are refused by the Azure key setup path.

Tools reuses the Orka multi-select picker: Space toggles tools, `/` searches,
and Enter saves. Automatic Orka tools remain locked. Both editors use
resource-version checks to reject concurrent agent edits and refresh inventory
on return (host-only source changes do not patch the cluster). Demo inference
forms can be completed and reviewed without writing. External Orka
runtimes remain inspection-only.

## Chat and lift

An empty, successfully loaded environment displays
`<no agents here, n to create a new one>`. Press `n` to open a creation overlay
above the inventory, pinned to that environment's context/kubeconfig and the
console's Orka namespace. It reuses quickstart's native creation form, validation,
compact field rendering and Apply/Cancel review. It collects the description,
name, provider, model and Secret reference. The context and API server remain
visible during review, and the normal create guard validates the reviewed target.
Creation runs in the overlay with a progress indicator; Esc cancels and waits
for work to stop. Completed writes are not rolled back. The result remains
visible until Enter/Esc closes it, and inventory refreshes after the operation.
`n` also works in populated environments; demo mode lets you fill and review the
form without creating anything. This flow does not run quickstart infrastructure
provisioning against the selected environment.

Chat opens the existing Orka interactive session; `/exit` returns
to the dashboard. Lift opens the existing target prerequisite, inference, tools,
deployment review and progress panes. Both return to a refreshed overview.

`/lift <agent> new` collects resource group, AKS cluster, globally unique registry
name and Azure region. It then restores the terminal for the existing
`kmx lift --payload orka --step cluster` provisioning flow, which reviews the
current Azure account and confirms the named cluster. Existing AKS defaults
apply (one `Standard_B4ms` node, Cilium, 64 GiB disk, monitoring enabled).
After provisioning it continues the same agent lift flow on that cluster,
including Orka installation and inference selection. Azure CLI login is required.
Provisioning records and retry/cleanup behavior follow [AKS](aks.md).

## Verification scope

Automated tests cover input modes, argument completion and identity resolution,
demo isolation, partial inventory failures, context-pinned reads, resize bounds,
terminal metadata escaping, and a Linux pseudo-terminal exit/restoration check.
Cloud provisioning and live agent deployment reuse existing operations; this
feature's tests do not create Azure resources or prove a live AKS deployment.
