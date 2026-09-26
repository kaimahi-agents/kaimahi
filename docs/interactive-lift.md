# Interactive Orka lift

In quickstart's Orka chat, enter `/lift` to open the target picker:

- **Kubeconfig context:** search the contexts already configured locally.
- **Azure AKS:** select a subscription, then search all its AKS clusters. Results
  include resource group and region; both are searchable. The selected cluster's
  resource group is carried into credential retrieval and the Foundry default.

For faster AKS listing, start with `kmx quickstart-wizard --azure-discovery sdk`.
It uses the Go ARM SDK with `DefaultAzureCredential` (including Azure CLI login).
The `cli` default remains selectable; see `azure-discovery-performance.md` for
the measured comparison and scope of the alternative implementation.

Catalog pickers start with search active. Matching is case-insensitive and fuzzy: `akpr`
matches `AKS-production`, and multiple words can match name and detail fields.
Type normally (including j/k), use arrows to navigate, and Enter to select.
Escape switches to Vim navigation: **j/k**, **g/G** for first/last, and **/**
to return to search. Escape again cancels the pane; Ctrl-C exits chat.
Action prompts (Create/Cancel, install confirmation, deployment review, and
quota refresh/back) disable search entirely. Their choices cannot be filtered
away, and Escape cancels immediately. Search availability and initial mode are
separate picker options; a lone creation entry also has search disabled.
No Azure default subscription is changed. Every scoped Azure request explicitly
names the selected subscription; AKS credentials are initially written to a
temporary kubeconfig. After successful deployment, a private flattened kubeconfig
snapshot and Agent location are saved under `~/.config/kmx/agentconfigs/`
(`$XDG_CONFIG_HOME/kmx/agentconfigs/` when configured). The shell's kubeconfig
and default context are unchanged.

Last-selected subscription, kubeconfig context and AKS cluster identities are
stored in `~/.config/kmx/lift-selections.json` (respecting `XDG_CONFIG_HOME`).
Discovery still fetches current resources; a matching remembered choice is moved
to the top and marked “last selected”. Cluster preferences are scoped by
subscription and resource group. Missing old selections are not added to results.
Interactive Azure discovery and provisioning waits use a centered loading box
below the lift header. Escape/Ctrl-C cancels the fetch and joins it before returning.

Selecting a target starts read-only prerequisite discovery immediately; there is
no separate Target review. Installations and resource creation retain their own
explicit confirmations. One final deployment review defaults to Cancel and shows
the Agent, destination, model, endpoint and create/reuse behavior.
Deploy creates the selected Agent and its Provider on that target, using the
existing installed-schema, collision, server-admission and readiness checks.
Server-managed metadata/status are dropped; Agent specification (tools, skills,
instructions and other settings) and Provider specification are preserved.

The prerequisite stage checks required Orka CRDs and controller readiness and
offers the pinned Orka install/repair on the destination. Permission/connectivity
failures are reported with the target context, not treated as a missing install.
It also offers installation of the KMX read-only Kubernetes tool when referenced
and absent. Other Tool/Skill resources must be provisioned separately.

Inference selection lists ready Providers in the destination namespace, plus
Azure Foundry and an explicit keep-source-configuration option. Choosing a remote
Provider copies its configuration into the new Agent's Provider and uses its
existing target Secret reference.

Foundry selection browses AIServices/OpenAI accounts and existing model
deployments, or creates an AIServices account and OpenAI chat deployment. New
accounts default to the AKS resource group and region; the resource group picker
allows changing that choice. For kubeconfig-only targets, Azure subscription and
resource group are selected explicitly. Model versions and SKUs come from the
account's model catalog. Model/SKU choices are matched to regional usage by Azure's
`usageName`; only entries with confirmed quota for the minimum capacity (at least
one) are offered. The default creation choice prefers standard pay-as-you-go SKUs.
Quota is checked before creating a new account and again immediately before model
deployment. When no quota is confirmed, the pane offers refresh or return to
account selection instead of an empty list. Quota availability does not reserve
capacity or guarantee deployment success. Creation is confirmed with
scope/model/SKU shown; quota or regional failures
stop without automatically retrying or deleting completed Azure resources.

Preflight distinguishes an empty regional OpenAI chat catalog from exhausted
quota. When the cluster region has no eligible model, it checks westus3, eastus2
and eastus concurrently and offers verified region/model/SKU combinations before
account creation. The resource group stays the same. Existing accounts in an
unsupported region can offer creation of a separate account in a verified region;
their location is not changed and the original account is retained.
Quota scope (regional/global/data-zone) is shown from Azure's usage response.
Only Standard, GlobalStandard and DataZoneStandard chat SKUs are recommended;
batch/provisioned SKUs are excluded. Capacity respects minimum, step and allowed
values. Catalog duplicates are removed and small chat models/default versions
are preferred among quota-qualified choices.

Live read-only investigation on 2026-09-16 found no OpenAI models in westus2 for
the tested subscription despite global quota being available. The alternative
preflight recommended westus3 / gpt-4.1-mini / 2025-04-14 / GlobalStandard, capacity
1 against 14,900 remaining global quota units. The three-region check took 6.7 s.
No accounts or deployments were created during this investigation.

The selected Foundry account key is fetched into memory and written to a fresh
Secret on the target through stdin, never command arguments. The Provider uses
the OpenAI-compatible `/openai/v1` endpoint and deployment name. Accounts disabling
local key authentication require a separate identity-auth integration and are
reported as unsupported. This provisions inference account/deployment resources,
not a Foundry project or agent service. Secret values from the source are not copied.

Account and model deployment provisioning are polled until Azure reports
`Succeeded`; failed/cancelled provisioning stops the flow. After creating the
target Secret, a connection pane starts a temporary Job in the destination
namespace. It mounts that Secret and makes one short chat-completions request
over verified HTTPS. Only a response containing text completes endpoint setup.
Neither key nor response text is printed. The probe Job is cleaned up afterward
(TTL is a fallback); the Secret is retained on failure for diagnosis/retry.
This tests namespace DNS, egress, authentication and model invocation, though
workload-specific NetworkPolicies can affect other pods differently. Final
deployment creates the Provider referencing this endpoint and Secret, then the
Agent, waiting for each to become Ready.

Existing matching Agent and Provider configurations are reused during lift.
Comparison uses a server-side dry-run replace to account for API defaults; no
replacement is applied. Different specifications and terminating resources are
reported as conflicts. Ordinary `agent create` retains its strict collision policy.
The operation
does not create AKS clusters or install the plane lift stack.
`kmx aks up` is the separate provisioning workflow. The deprecated `kmx lift`
still works and requires `--payload`.

After success chat connects to the lifted Agent on the destination, resets retry
history and uses its remote Provider. The source location is also saved, so
`/agent` can switch back. Saved Agents are combined with live discovery in the
current namespace; identical names on different contexts remain separate entries.
Before connecting, KMX reads the destination Agent and waits for its current
generation to be Ready. A failed connection leaves the previous connection intact.
Saved entries remain accessible in `/agent` when the current cluster is offline.
A partial deployment reports an error without deleting resources.

Agent registry records contain names, namespace, context and optional AKS metadata.
Kubeconfig snapshots are separate files with mode 0600 in a private directory;
they can contain kubeconfig authentication material, including exec-plugin config.
Model API keys are not included in the registry. Azure authentication can still
expire and require login before a saved Agent can be reached.

## Deployment progress

A persistent KMX header is rendered above every lift picker and deployment pane,
and restored between Azure fetches. It shows the source Agent/context, destination,
and the six-step timeline **Target → Orka → Inference → Tools → Deploy → Connect**.
The active step is highlighted; narrow windows use a compact numbered bar.

Approved Orka installs/repairs, Kubernetes tool installation and final deployment
open a bordered **LIFT deployment pane** showing the Agent, destination, elapsed
time and real stage states. Only the active stage animates; no percentages are
estimated. Orka installation reports installer fetch, wrapper credential
reconciliation, and installer application/readiness. Final deployment reports
schema/prerequisite checks, server admission, Provider creation and Ready wait,
then Agent creation and Ready wait.

Completion or failure stays visible until Enter returns to the lift/chat flow.
On failure, **r** retries the current operation without repeating target selection.
Final deployment rechecks existing components on each attempt, so a completed
Provider is reused while an absent Agent is created. No Task is replayed by this
declarative retry path. Reused creation stages are marked `↪ (reused)` and still
followed by current-generation readiness waits.
Ctrl-C or Escape during work cancels the operation, waits for its worker to stop,
and restores the terminal before returning. Completed resources remain in place.
The pane is independent of chat verbose mode. Interactive inference selection
still uses its own picker between deployment phases.
