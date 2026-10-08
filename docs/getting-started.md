# Getting started

## Create your own agent, then lift it from chat

With `kmx` installed and Docker or Podman available, follow this path for your
own agent (not the fixed `kmx quickstart` demonstration):

```bash
kmx quickstart --interactive
```

On tagged v0.4.1 and earlier, run `kmx quickstart-wizard` instead; newer builds
retire that spelling and name the replacement.

1. Describe your agent, choose **Chat with agent**, and send a message. Wait for
   a local answer.
2. In that chat, enter `/lift`. Select an existing Kubernetes context or AKS
   cluster. Review the destination and inference choice at the final deployment
   review before confirming. `/lift` offers to prepare Orka and the referenced
   Kubernetes tool if they are missing; it does not create a cluster.
3. After chat connects to the lifted agent, send a **new message** and check its
   answer. Lift, readiness and connection do not prove an answer; the answer
   to that new message does.

See the [interactive lift guide](interactive-lift.md) for target discovery,
confirmation and connection behavior. See the [bundle lift guide](agent-lift.md)
for `kmx agent lift` and `kmx agent evaluate`.

**Orka is the platform.** Kaimahi provides tooling to install it, author native
Agents and get an existing application's model traffic onto it. Start with the
[Orka guide](orka.md), including [native creation and a first Task](orka.md#author-an-orka-agent-and-get-an-answer),
or [migration](migrate.md); a migrated application's Deployment stays owner-managed.
Tool traffic remains the application owner's responsibility; the Kaimahi tool
gateway is retired.

The local quickstart below is the **supported deterministic Orka first-answer
path**: it ends with a native Orka Agent answering a question. For guided
custom authoring and a first answer, use the wizard described below. The old
Kagent setup steps remain removed; invoking one is still an unknown step.
KMX does not install or upgrade Kagent; only explicit exact-v0.10.2 create is
available for an installation an operator already owns.
`orka.harness.v2` is outside the direction.

## Prerequisites

| Tool | Needed for |
|---|---|
| Go 1.26+ | `go install` builds and fetched plane builds; not needed to run the downloaded CLI |
| Docker or Podman | creating local kind clusters; not needed for ACR cloud builds |
| kind, kubectl, helm | kmx uses PATH copies first, otherwise fetches pinned/checksummed binaries |
| git, make | checkout-based development and remaining scripts/helpers |
| authenticated Azure CLI | AKS only; never installed by kmx |

Set `KMX_TOOLCHAIN=off` if missing tools should fail rather than download.
[kmx installation](kmx.md#install) describes cache verification and release trust.
Choose Podman directly on the command line with
`kmx --container-engine podman quickstart`; automation can continue to set
`CONTAINER_ENGINE=podman`.

## Current Orka path

The stable release includes these Orka commands. Install it with Homebrew:

```bash
brew install kaimahi-agents/tap/kmx
```

To pin Kaimahi v0.4.0 exactly, use Go or the checksum-verified
[release installer](releases.md#install):

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.0
kmx version
kmx ctx <context>
kmx orka --help
kmx orka install
kmx orka status
```

Put Go's binary directory on PATH. `@main` remains a moving development option.
From a checkout, `make` builds `bin/kmx` without provisioning.
The selected cluster must already exist; for a fresh local cluster the current
component commands are:

```bash
export KIND_CLUSTER=orka-local
export KUBE_CTX=kind-orka-local
kmx up --step cluster
kmx up --step ollama
kmx up --step model
```

These prepare kind and the keyless model server without installing Orka or the
legacy runtime.
Then install Orka on that selected cluster. Its default Provider points at this
Ollama server; for an existing cluster use your own model/Provider configuration
as described in [Orka](orka.md). Installation alone does not govern model traffic.
For a new native Agent, use [agent create](#an-agent-of-your-own).

For an existing application on kind, deploy the plane and follow the owner-reviewed
[migration procedure](migrate.md) (on AKS use the [lift phases](aks.md#targets-and-resume)):

```bash
kmx plane
kmx migrate <deployment> --namespace <namespace> --model <provider>/<model>
```

kmx writes the Deployment patch; **you apply it** and carry its configuration into
the application's Helm/GitOps release. Review API/continuation compatibility and
both credential deadlines before adopting it. A fresh answer plus model ledger
rows is evidence of that route, not blanket governance of the application.

## One command, and an agent that answers

This section is the **supported Orka first-answer path**. With a current kmx:

```bash
export KIND_CLUSTER=kmx-local
export KUBE_CTX=kind-kmx-local
kmx quickstart
kmx quickstart --output json --task 'Who are you?'
```

To author your own Orka agent instead of deploying the fixed demonstration,
run the experimental `kmx quickstart --interactive`. Its TUI keeps kind, model, and
Orka setup progress visible while you describe the agent. It offers bundled
and detected host models with their source and reported size, can continue
with an existing local Orka agent, and ends by offering a native Task chat.
Progress rows show approximate local image/model footprints; they are size
estimates, not byte counters from the underlying container tools.

Setup starts while the form is open. It creates the Task result-reader account
and RBAC and installs the default read-only Kubernetes inventory tool, whose
ClusterRole can list the documented resource kinds across namespaces. Cancelling
the wizard stops active work but leaves completed resources in place. Review the
[tool and RBAC boundary](orka-k8s-tool.md) before running it on a shared cluster.

`quickstart` is deterministic and non-interactive: no model picker and no
prompt, so the same command on the same machine produces the same cluster, the
same Provider and the same Agent. That is what lets an unattended caller rerun
it and compare. Choosing your own model is the wizard above.

On a fresh cluster it creates kind, Ollama with `qwen2.5:3b`, the pinned Orka
release with a placeholder Provider Secret and Task result-reader account,
and the fixed `hello-world-agent` Provider/Agent bundle; then it asks a **fresh** Task and
requires a readable answer. It deploys no plane; the Orka runtime is installed
from the pinned v0.2.0 Helm chart (Helm is found on PATH or fetched by kmx).
Rerunning reuses an **exact** match only: a Provider or Agent whose live spec
differs from the one quickstart would write is somebody's deliberate change, so
it stops rather than overwrite it. A half-finished run resumes. Other setup
steps still reconcile: this is not a read-only probe.

JSON stdout is one document; subprocess/progress output goes to stderr.
`governed: false` means **this invocation did not enable governance**; reruns may
preserve existing governance, which quickstart does not assess. The key set and
human/raw output contracts are in [kmx](kmx.md#output-contracts).

### The whole runtime

```bash
kmx up
kmx orka status
kmx agent chat --namespace orka-system hello-world-agent
```

A bare `up` brings up the **runtime**: kind, the keyless Ollama model server and
the pinned Orka release with its Provider and Task result-reader account. It
deploys no agent — `kmx quickstart` is the command that ends with one
answering, and `kmx agent create` is the one that authors your own. The chat
line above therefore needs an Agent from one of those two commands first. Orka
chat is always a session; a message after the Agent name is its first turn, not
a one-shot. v0.4.1 and earlier require `--interactive`, which newer builds
no longer accept.

KMX no longer installs Kagent or its two demonstration agents. Its three old
`kmx up --step` names are unknown steps, their manifests are no longer shipped
in the binary, and agent editing, governance and preset switching remain
retired. `kmx agent chat` and `kmx agent list` remain Orka-only. The one scoped
exception is `kmx agent create --runtime kagent <name>` for a preinstalled exact
v0.10.2; it does not alter this setup or quickstart path.

A cluster that still carries Kagent is untouched by Orka setup. Other than the
explicit exact-v0.10.2 create path, operate it with upstream tools or kubectl.
An old gateway reference needs
[explicit upgrade review](operations.md#upgrading-after-gateway-retirement).
Interactive `/help` lists local controls.

### Governing an application

```bash
kmx plane
kmx migrate <deployment> --namespace <ns> --model local/qwen2.5:3b
kmx ledger <deployment>
```

`kmx migrate` puts an owner-managed application behind the plane and gives it an
opaque plane token, never the real upstream key. It changes model routing only;
see [kmx](kmx.md#governing-model-traffic). Direct MCP wiring an application
already owns is untouched, and carries no Kaimahi tool policy, grants or audit.

## An agent of your own

This is the **native Orka path**, not a new legacy agent for the commands
above. Preview a Provider + Agent bundle offline:

```bash
kmx agent create my-agent --namespace orka-system \
  --provider-type openai --model qwen2.5:3b --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 --no-apply
```

Namespace, Provider type, model ID and existing Secret name are required.
The metadata-only Secret skeleton is a reference: **never write it or bulk-apply
the bundle**. Online creation uses installed schemas and ordered readiness waits;
`--dry-run` checks admission but not execution. Only optional `--task` plus an
existing result ServiceAccount tests a real model answer. Follow the
[context-pinned first-Task guide](orka.md#author-an-orka-agent-and-get-an-answer)
and [create safety contract](kmx.md#kmx-agent-create) before creating resources.
No-name terminal invocation offers a wizard. `agent chat` and
`agent list --namespace <ns>` are Orka-only; a live Agent is edited with
`kubectl edit agents.core.orka.ai`. BYO images, model-preset and MCP conversion
are not provided.

For the separate, advanced Kagent v0.10.2 create-only path, use the complete
[create contract](kmx.md#explicit-kagent-v0102-create). Offline rendering
requires neither an installed Kagent nor a provisioned cluster Secret. Online
creation requires both an exact v0.10.2 installation and a separately
provisioned Secret. Neither mode adds Kagent chat, list, show, status, lift,
evaluate, console, quickstart, `up`, or AKS support.

## Using Podman instead of Docker

Select Podman explicitly for an invocation:

```bash
kmx --container-engine podman quickstart
# or equivalently for automation:
CONTAINER_ENGINE=podman kmx quickstart
```

The flag can appear before or after the command and overrides
`CONTAINER_ENGINE`. Keep the same engine for **every** operation on a cluster;
Docker's kind inventory cannot see Podman's nodes, and both engines can own a
cluster with the same name. kmx therefore does not silently switch engines when
one daemon is unavailable.

On macOS, ensure the Podman machine mounts the checkout before building; absent
mounts require deliberate machine recreation, not an implicit destructive
repair. Restarted machines may leave nodes stopped; the cluster step starts the
named nodes and checks API/DNS. kmx supplies
`KIND_EXPERIMENTAL_PROVIDER=podman` to kind automatically.

## Choices and caveats

- Target selection and confirmation are explicit: [where commands land](kmx.md#where-the-command-will-land).
  Use your own cluster name; do not share the default across development lanes.
- The bundled local model is small and tool-capable, not a guarantee of reliable
  prose. Validate actual tool payloads; [FAQ](FAQ.md) covers small-model failures.
- Ollama models are in `emptyDir`; a pod restart requires another model pull.
- `kmx down` deletes the whole local cluster, **including Postgres/ledger**.
  Back up first if needed. For AKS use [lift teardown](aks.md#teardown), not kind down.

Next: [CLI reference](kmx.md), [migration](migrate.md) and
[operations](operations.md).
