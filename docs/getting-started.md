# Getting started

**Orka is the platform.** Kaimahi provides tooling to install it, author native
Agents and get an existing application's model traffic onto it. Start with the
[Orka guide](orka.md), including [native creation and a first Task](orka.md#author-an-orka-agent-and-get-an-answer),
or [migration](migrate.md); a migrated application's Deployment stays owner-managed.
Tool traffic remains the application owner's responsibility; the Kaimahi tool
gateway is retired.

The local kagent quickstart below is the **existing legacy implementation**,
pending the code transition, not an Orka-native authoring tutorial. Native Orka
only versus kagent YAML over Orka remains open. Keeping this runnable path does
not settle that choice, and `orka.harness.v2` is outside the direction.

## Prerequisites

| Tool | Needed for |
|---|---|
| Go 1.26+ | current development `kmx` with Orka commands; also fetched plane builds |
| Docker or Podman | creating local kind clusters; not needed for ACR cloud builds |
| kind, kubectl, Helm | kmx uses PATH copies first, otherwise fetches pinned/checksummed binaries |
| git, make | checkout-based development and remaining scripts/helpers |
| authenticated Azure CLI | AKS only; never installed by kmx |

Set `KMX_TOOLCHAIN=off` if missing tools should fail rather than download.
[kmx installation](kmx.md#install) describes cache verification and release trust.

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

These prepare kind and the keyless model server without installing kagent.
Then install Orka on that selected cluster. Its default Provider points at this
Ollama server; for an existing cluster use your own model/Provider configuration
as described in [Orka](orka.md). Installation alone does not govern model traffic.
For a new native Agent, use [agent create](#an-agent-of-your-own); the kagent
quickstart/chat/model-governance sections below are a separate legacy path.

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

This section is the **legacy kagent first-answer path**. With a current kmx:

```bash
export KIND_CLUSTER=kagent-local
export KUBE_CTX=kind-kagent-local
kmx quickstart
kmx quickstart --output json --task 'Who are you?'
```

On a fresh cluster it creates kind, Ollama with `qwen2.5:3b`, a reduced kagent
profile and hello-world, then requires a completed task with a readable answer.
It deploys no plane. It reconciles its recognized minimal profile, preserves
deployed full/custom kagent releases and refuses unreadable/malformed release
state. Other setup steps still reconcile: this is not a read-only probe.

JSON stdout is one document; subprocess/progress output goes to stderr.
`governed: false` means **this invocation did not enable governance**; reruns may
preserve existing governance, which quickstart does not assess. The key set and
human/raw output contracts are in [kmx](kmx.md#output-contracts).

### The whole runtime

```bash
kmx up
kmx agent chat hello-world 'Who are you?'
kmx agent chat --interactive hello-tools
kmx status
```

`up` explicitly upgrades/installs the full kagent application profile, including
the tool server and original `hello-tools` agent from
[`k8s/tools-agent.yaml`](../k8s/tools-agent.yaml). Its MCP wiring is direct to the
kagent tool server, not the retired Kaimahi gateway. Both setup paths preserve
existing non-default routing rather than silently repointing an owner's tools.
An old gateway reference needs [explicit upgrade review](operations.md#upgrading-after-gateway-retirement).
Interactive `/help` lists local controls; [retry limits](kmx.md#retry-limits)
explain why an ambiguous one-shot disconnect can repeat effects or spend.

### Governing that agent

```bash
kmx plane
kmx govern hello-world
kmx agent chat hello-world 'Who are you?'
kmx ledger hello-world
```

This existing model-seam path switches the kagent Agent's preset and gives it an
opaque plane token, never the real upstream key. It changes model routing only;
see [kmx](kmx.md#governing-an-agent). The [direct MCP example](tools.md) remains
available without Kaimahi tool policy, grants or tool audit.

## An agent of your own

This is the **native Orka path**, not a new kagent agent for the legacy commands
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
No-name terminal invocation offers a wizard. `agent chat/edit/list` remain
kagent-specific; BYO images, ModelConfig and MCP conversion are not provided.

## Using Podman instead of Docker

Set `CONTAINER_ENGINE=podman` consistently for **every** operation on the cluster;
Docker's kind inventory cannot see Podman's nodes. On macOS, ensure the Podman
machine mounts the checkout before building; absent mounts require deliberate
machine recreation, not an implicit destructive repair. Restarted machines may
leave nodes stopped; the cluster step starts the named nodes and checks API/DNS.

## Choices and caveats

- Target selection and confirmation are explicit: [where commands land](kmx.md#where-the-command-will-land).
  Use your own cluster name; do not share the default across development lanes.
- The legacy model is small and tool-capable, not a guarantee of reliable prose.
  Validate actual tool payloads; [FAQ](FAQ.md) covers small-model failures.
- Ollama models are in `emptyDir`; a pod restart requires another model pull.
- The legacy pin is kagent 0.9.12. Its default Python runtime is used. The
  recorded Go-runtime image gap required `controller.agentImage.registry=ghcr.io`
  in `k8s/kagent-values.yaml`; this is version-scoped, not a current upstream survey.
- `kmx down` deletes the whole local cluster, **including Postgres/ledger**.
  Back up first if needed. For AKS use [lift teardown](aks.md#teardown), not kind down.

Next: [CLI reference](kmx.md), [migration](migrate.md), [operations](operations.md),
and the [direct MCP example](tools.md).
