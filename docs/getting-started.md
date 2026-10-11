# Getting started

Install the stable CLI, get a deterministic Orka answer, then **Create → Prove → Lift**
an agent of your own. Orka is the platform; Kaimahi provides installation and native
authoring tools. The selected runtime owns execution and enforcement.

## Current Orka path

Install the stable release with Homebrew:

```bash
brew install kaimahi-agents/tap/kmx
```

For an exact **v0.4.1** pin, use Go or the checksum-verified
[release installer](releases.md#install):

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.1
kmx version
```

Put Go's binary directory on PATH. All tutorial commands below use v0.4.1.
`@main` is a moving development option; checkout `make` builds `bin/kmx` without provisioning.
Newer builds use different spellings:

| v0.4.1 | Current main (unreleased) |
|---|---|
| `kmx ctx` / `kmx ctx <context>` | `kmx context show` / `kmx context use <context>` |
| `kmx up` / `kmx down` | `kmx local up` / `kmx local down` |
| `kmx quickstart-wizard` | `kmx quickstart --interactive` |
| `kmx agent chat --interactive <name>` | `kmx agent chat <name>` (no `--interactive` flag) |

See [command alignment](command-semantics.md) for compatibility and retirement details.

## Prerequisites

| Tool | Needed for |
|---|---|
| Go 1.26+ | `go install` and checkout builds; not needed for a downloaded CLI |
| Docker or Podman | creating local kind clusters |
| kind, kubectl, helm | kmx uses PATH copies first, otherwise fetches pinned/checksummed binaries |
| git, make | checkout builds and remaining scripts/helpers |
| authenticated Azure CLI | AKS workflows **and host-local Azure Foundry inference**, including with local kind; never installed by kmx |

Set `KMX_TOOLCHAIN=off` if missing tools should fail rather than download.
[kmx installation](kmx.md#install) describes cache verification and release trust.
Azure is not needed for the keyless local quickstart.

## One command, and an agent that answers

The local quickstart below is the **supported deterministic Orka first-answer
path**: it ends with a native Orka Agent answering a question. For guided
custom authoring and a first answer, use the wizard described below.
KMX does not install or upgrade kagent. Explicit exact-v0.10.2 create and
exact-v0.10.3 declarative bundle lift/status require an operator-prepared
installation; see [version-qualified placement](agent-lift.md#exact-v0103-declarative-bundle-placement).

Choose a local cluster name you own and pin its context:

```bash
export KIND_CLUSTER=kmx-local
export KUBE_CTX=kind-kmx-local
kmx quickstart
```

On a fresh cluster this creates kind, keyless Ollama, the pinned **Orka v0.2.0
Helm chart**, a placeholder Provider Secret, the Task result-reader account and
RBAC, and the fixed `hello-world-agent` Provider/Agent bundle. It asks a **fresh
Task** and requires a readable answer; readiness alone is not an answer.
The local model defaults to `qwen2.5:3b`; `MODEL` can override it.

Quickstart is non-interactive: no model picker or prompt. The same settings select
the same cluster, Provider and Agent. Reruns reuse an **exact** live-spec match
only and stop rather than overwrite a changed Provider or Agent. A half-finished
run resumes; other setup still reconciles, so this is not a read-only probe.
For model selection and custom authoring, use the wizard below.

Optional machine-readable output also runs a new Task:

```bash
kmx quickstart --output json --task 'Who are you?'
```

JSON stdout is one document; subprocess/progress output goes to stderr.
`governed: false` means **this invocation did not enable governance**; existing
governance may survive a rerun and is not assessed. See [output contracts](kmx.md#output-contracts).

## Create your own agent, then lift it from chat

Keep the cluster/context above, then run the experimental wizard shipped in v0.4.1:

```bash
kmx quickstart-wizard
```

1. Describe your agent and choose a model. The wizard shows setup progress,
   bundled and detected host models, and can continue with an existing local
   Orka Agent. Choose **Chat with agent**, send a message and wait for a local answer.
2. Prepare an existing **Ready destination Provider and its Secret**, then enter
   `/lift` and select the destination context or AKS cluster. Bundle-backed Foundry
   inference must be provisioned separately. Review the destination and inference
   choice before confirming. `/lift` can separately prepare missing Orka and the
   Kubernetes tool; it creates neither a cluster nor the destination inference.
   Source Secret values are never copied to the destination.
3. After chat connects to the lifted Agent, send a **new message** and check its
   answer. Lift, readiness and connection do not prove that answer.

Setup starts while the form is open. It creates the Task result-reader account
and RBAC and installs the default read-only Kubernetes inventory tool. Its
ClusterRole can list the documented resource kinds **across namespaces**.
Cancelling stops active work but leaves completed resources in place. Review the
[tool and RBAC boundary](orka-k8s-tool.md) before using a shared cluster.

See [interactive lift](interactive-lift.md) for target discovery and confirmation,
and [bundle lift](agent-lift.md) for `kmx agent lift` and `kmx agent evaluate`.
These describe current builds; use the stable spellings above with v0.4.1.

### The whole runtime

To prepare the runtime without creating an Agent, use `kmx up` instead of quickstart:

```bash
kmx up
kmx orka status
# After quickstart or agent create has created this Agent:
kmx agent chat --interactive --namespace orka-system hello-world-agent
```

A bare `up` brings up kind, keyless Ollama with the configured `MODEL`, and pinned
Orka with its Provider and Task result-reader account. It deploys **no Agent**.
Orka chat is a session; a message after the name is its first turn, not a one-shot.

For a cluster that already exists, pass its context explicitly so the exported
`KUBE_CTX` above cannot select the tutorial cluster:

```bash
kmx --context <context> orka install
kmx --context <context> orka status
```

Configure its model/Provider explicitly;
installation alone does not govern model traffic. The [Orka guide](orka.md)
covers the pin, existing-install refusal and lack of a supported version upgrade.
Orka setup does not install or upgrade other runtimes or silently repair conflicting installations.

The following compatibility notes describe current builds, not v0.4.1 tutorial steps.
KMX no longer installs kagent or its two demonstration agents. Its three old
`kmx local up --step` names are unknown steps, their manifests are no longer shipped
in the binary, and agent editing, governance and preset switching remain
retired. `kmx agent chat` and `kmx agent list` remain Orka-only. The scoped
exceptions are `kmx agent create --runtime kagent <name>` for preinstalled exact
v0.10.2 and explicit exact-v0.10.3 declarative bundle lift/status; neither alters
this setup or quickstart path.

A cluster that still carries kagent is untouched by Orka setup. Outside those
version-qualified operations, use upstream tools or kubectl.
Owners must deliberately remove or replace obsolete gateway references; setup
does not repoint existing tools. Interactive `/help` lists local controls.

## An agent of your own

For explicit native Orka authoring, preview a Provider + Agent bundle offline:

```bash
kmx agent create my-agent --namespace orka-system \
  --provider-type openai --model qwen2.5:3b --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 --no-apply
```

Namespace, Provider type, actual model ID and existing Secret name are required;
match the model to your setup. The metadata-only Secret skeleton is a reference:
**never write it or bulk-apply the bundle**. Online creation uses installed schemas
and ordered readiness waits. `--dry-run` checks admission, **not execution**.
Only optional `--task` with an existing result ServiceAccount tests a real answer;
create does not provision that account or RBAC. Follow the
[context-pinned first-Task guide](orka.md#author-an-orka-agent-and-get-an-answer)
and [create safety contract](kmx.md#kmx-agent-create), using v0.4.1 spellings.
No-name terminal invocation offers a wizard. `agent chat` and `agent list --namespace <ns>`
are Orka-only; edit a live Agent with `kubectl edit agents.core.orka.ai`.
BYO images, model-preset and MCP conversion are not provided.

For the separate, advanced kagent v0.10.2 create-only path in current builds, use the complete
[create contract](kmx.md#explicit-kagent-v0102-create). Offline rendering
requires neither an installed kagent nor a provisioned cluster Secret. Online
creation requires both an exact v0.10.2 installation and a separately
provisioned Secret. Neither v0.10.2 create mode adds kagent chat, list, show, status, lift,
evaluate, console, quickstart, `up`, or AKS support.

## Using Podman instead of Docker

```bash
kmx --container-engine podman quickstart
# Equivalently for automation:
CONTAINER_ENGINE=podman kmx quickstart
```

The flag overrides `CONTAINER_ENGINE`. Keep the same engine for **every** operation:
Docker cannot see Podman's kind nodes, and both engines can own the same cluster
name. kmx does not silently switch engines.
On macOS, ensure the Podman machine mounts the checkout before building; absent
mounts require deliberate machine recreation, not implicit destructive repair.
Restarted machines may leave nodes stopped; the cluster step starts the named
nodes and checks API/DNS. kmx supplies `KIND_EXPERIMENTAL_PROVIDER=podman` automatically.

## Choices and caveats

- Select your own cluster name and context; review [where commands land](kmx.md#where-the-command-will-land).
- The default local model is small and tool-capable, not a guarantee of reliable
  prose. Validate actual tool payloads; [FAQ](FAQ.md) covers small-model failures.
- Ollama models are in `emptyDir`; a pod restart requires another model pull.
- `kmx down` (current main: `kmx local down`) deletes the **whole local cluster
  and its data**, including any historical database. Export needed data first;
  see [Orka backup cautions](orka.md#preserve-the-charts-generated-snapshot-key).
  For AKS use [lift teardown](aks.md#teardown), not kind down. Azure resources can
  remain billable after a failed operation; verify cleanup rather than assuming cancellation deleted them.

Next: [CLI reference](kmx.md), [models and endpoints](models.md) and
[bundle lifecycle](agent-lift.md).
