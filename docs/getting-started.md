# Getting started

**Orka is the platform.** Kaimahi provides tooling to install it, author native
Agents and get an existing application's model traffic onto it. Start with the
[Orka guide](orka.md), including [native creation and a first Task](orka.md#author-an-orka-agent-and-get-an-answer),
or [migration](migrate.md); a migrated application's Deployment stays owner-managed.
Tool traffic remains the application owner's responsibility; the Kaimahi tool
gateway is retired.

The local quickstart below is the **supported Orka first-answer path**: it
ends with a native Orka Agent answering a question. It is now the only
first-answer path kmx has: the three legacy install steps
installers have been removed, and naming one is an unknown step. A cluster
that still runs the legacy runtime is operated with kubectl.
`orka.harness.v2` is outside the direction.

## Prerequisites

| Tool | Needed for |
|---|---|
| Go 1.26+ | current development `kmx` with Orka commands; also fetched plane builds |
| Docker or Podman | creating local kind clusters; not needed for ACR cloud builds |
| kind, kubectl | kmx uses PATH copies first, otherwise fetches pinned/checksummed binaries |
| git, make | checkout-based development and remaining scripts/helpers |
| authenticated Azure CLI | AKS only; never installed by kmx |

Set `KMX_TOOLCHAIN=off` if missing tools should fail rather than download.
[kmx installation](kmx.md#install) describes cache verification and release trust.
Choose Podman directly on the command line with
`kmx --container-engine podman quickstart`; automation can continue to set
`CONTAINER_ENGINE=podman`.

## Current Orka path

The published `v0.1.0` release predates these commands. Build current main rather
than assuming `@latest` or the release installer contains them:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx version
kmx ctx <context>
kmx orka --help
kmx orka install
kmx orka status
```

Put Go's binary directory on PATH. `@main` is a development revision, not a
release pin. From a checkout, `make` builds `bin/kmx` without provisioning.
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
For a new native Agent, use [agent create](#an-agent-of-your-own); the legacy
chat/model-governance sections below are a separate legacy path.

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
run the experimental `kmx quickstart-wizard`. Its TUI keeps kind, model, and
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
release with its keyless Provider and Task result-reader account, and the fixed
`hello-world-agent` Provider/Agent bundle; then it asks a **fresh** Task and
requires a readable answer. It deploys no plane and installs no Helm chart.
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
kmx agent chat --interactive --namespace orka-system hello-world-agent
```

A bare `up` brings up the **runtime**: kind, the keyless Ollama model server and
the pinned Orka release with its Provider and Task result-reader account. It
deploys no agent — `kmx quickstart` is the command that ends with one
answering, and `kmx agent create` is the one that authors your own. The chat
line above therefore needs an Agent from one of those two commands first. Orka
chat is a session: `--interactive` is required, and a one-shot invocation is
refused with the command that works.

kmx no longer installs the legacy runtime or its two demonstration
agents. Its three `kmx up --step` names are
unknown steps, their manifests are no longer shipped in the binary, and
`kmx agent edit`, `kmx govern` and `kmx use` went with the runtime adapter.
`kmx agent chat` and `kmx agent list` remain and are Orka-only.

A cluster that still carries that runtime is untouched by any of this, and is
operated with kubectl. An old gateway reference needs
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
No-name terminal invocation offers a wizard. `agent chat --interactive` and
`agent list --namespace <ns>` are Orka-only; a live Agent is edited with
`kubectl edit agents.core.orka.ai`. BYO images, model-preset and MCP conversion
are not provided.

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
