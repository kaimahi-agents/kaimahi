# Getting started

**Run / Test / Publish** are the developer operations: Run invokes an existing
live Orka Agent, Test checks workspace cases with `kmx package test`, and Publish transfers
the package with `kmx package push`. Run is not workspace-local execution.

Install kmx, then create and Deploy an agent of your own with two commands.
Deploy composes missing evaluation, publication and placement on real targets;
the selected runtime owns execution and enforcement. Need a local test environment
first? Use the [local quickstart](#one-command-and-an-agent-that-answers) below.

## Install kmx

This guide covers the full cluster/workspace lifecycle. On Windows, run these
install and workflow commands inside a Linux WSL distribution. Current `main`
also has an **unreleased, narrow** [native Windows CLI](windows.md):
version/help/completion, package-free fake `kmx run FILE` and `kmx test FILE`,
and offline `kmx test explain ... --file FILE`. It does not support the cluster,
workspace, model, publication or deployment steps below. Stable v0.8.1 and
`@latest` have no Windows `.exe`; Homebrew and the Go route shown here remain
macOS/Linux or WSL guidance as applicable.

Install the latest release with Homebrew:

```bash
brew install kaimahi-agents/tap/kmx
```

For a repeatable install, pin the release with Go or use the checksum-verified
[release installer](releases.md#install):

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.8.1
kmx version
```

Put Go's binary directory on PATH. Inspect the effective context with
`kmx cluster context show` or select one with `kmx cluster context use <context>`.
See [release upgrades](releases.md#upgrading-kmx) when moving from an older version,
and [command alignment](command-semantics.md) for compatibility and retirement details.
The root `create/chat/run/push` commands below are available in v0.8.1.
`kmx package create`, `kmx package test` and `kmx package deploy` are unreleased;
v0.8.1 uses `create`, `eval` and `lift` with the same arguments. Use a main
checkout build for the `package create`/`test`/`deploy` examples below, or the
v0.8.1 spellings with that release. Current main's root `create` writes agent
files instead of workspace source. See
[Test compatibility](kmx.md#test-compatibility-unreleased) and
[Deploy compatibility](kmx.md#deploy-compatibility-unreleased). When upgrading,
see the [migration guidance](../CHANGELOG.md#upgrading) for removed commands and
legacy bundle workflows.

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

## An agent of your own

Before either command, have a prepared runtime and inference (Orka today) in both
evaluation and destination environments, arrange registry authentication, and save
workspace-root `kmx.yaml` with `publishRepository`, named destination
context/namespace/inference selectors and the destination's `evaluationTarget`.
Create supplies none of these. Follow the [named configuration example](suite-workspace.md#named-destinations);
the local setup path below can prepare a test environment.

From that workspace root, provide an exact model name and an instructions file:

```bash
kmx package create my-agent --workspace . \
  --model qwen2.5:3b --instructions ./instructions.md
kmx package deploy --to prod
```

Create authors one tool-free workspace member offline. Deploy freezes the exact
suite and selectors, runs missing evaluation, publication and placement stages,
and discloses source-only promotion: passed on the evaluation target, not
evaluated on the destination. Review its frozen plan before confirming.
See [composed Deploy](suite-workspace.md#composed-workspace-lift) for stage output,
read-only skips and partial outcomes.

Provide an instructions **file**, not inline text; model is required and exact.
Omit `--workspace` to discover the nearest ancestor `workspace.yaml`. If none
exists, terminal use asks `Start one here? [Y/n]`; nonterminal or redirected use
requires `--workspace DIR`, which selects/initializes that exact root without a
prompt. Invalid nearest markers refuse without fallback. An existing one-member
workspace refuses a second member without changing files; select a new root.
See [workspace authoring, evaluation, publication and digest Deploy](suite-workspace.md).

For manual stages, packaged workspace evaluation is implemented for native Orka
with `--inference` and exact-v0.10.3 Go kagent with `--bindings`; matching
full-passing evidence can authorize evaluated push. Standalone package evaluation
remains unavailable; explicit-reference `kmx package deploy REF@sha256:DIGEST` instead
deploys an exact registry or local-layout native package using a separate matching
private evidence file and explicit target. Orka uses a Ready `openai` inference
Provider; its public evaluation produces candidate evidence, so manual reference
Deploy requires `--promote` even in the same environment. Kagent uses author-owned
bindings and refuses any occupied target.

### Retired live authoring

`create --live`, live-only/preview flags, its wizard and user bundle
commands/readers/writers are removed outright. No conversion/migration or saved
receipt/resource deletion occurs. Old bundle receipts are unreadable. Suite
status/retirement remain unavailable. Without FILE, `kmx run` requires `--agent` for a live Orka
Agent, including one placed by Deploy; chat also targets those live Agents. Neither is
workspace-local execution or suite evaluation. Task/result access remains
independent, without resend.

## One command, and an agent that answers

The local quickstart below is the **supported deterministic Orka first-answer
path**: it ends with a native Orka Agent answering a question. For guided
custom authoring and a first answer, use the wizard described below.
kmx does not install/upgrade kagent. Exact v0.10.3 Go/linux-amd64 packaged
workspaces support `test --bindings`, evaluated push and create-only digest Deploy
on operator-prepared installations; see [native profiles](suite-workspace.md).
Live create and bundle operations are removed.

Choose a local cluster name you own and pin its context:

```bash
export KIND_CLUSTER=kmx-local
export KUBE_CTX=kind-kmx-local
kmx cluster quickstart
```

On a fresh cluster this creates kind, keyless Ollama, the pinned **Orka v0.2.0
Helm chart**, a placeholder Provider Secret, the Task result-reader account and
RBAC, and the tool-free `hello-world-agent` native source → full eval → publication →
reviewed Deploy/placement Verify workflow. It then asks a separate **fresh Task** and requires a readable answer; readiness alone is not an answer.
The local model defaults to `qwen2.5:3b`; `MODEL` can override it.

On an uncached first run, the default model downloads roughly **1.9GB**.
Startup and first-answer latency depend on your network and host hardware.

New local kind nodes created by `kmx cluster quickstart` or `kmx cluster up` use a **5s
kubelet sync frequency** with either Docker or Podman. This default is intended
to reduce waits for Orka's webhook Secret file projection; it does not fix
Orka's upstream coupling to projection. The shorter node-wide cadence means
more periodic reconciliation work. It applies only to future KMX-created local
nodes: existing or recovered clusters and AKS nodes are not modified.

Quickstart is non-interactive: no model picker or prompt. The same settings select
the same cluster, Provider and Agent. Reruns reuse an **exact** live-spec match
only and stop rather than overwrite a changed Provider or Agent. A half-finished
run resumes; other setup still reconciles, so this is not a read-only probe.
For model selection and custom authoring, use the wizard below.

Optional machine-readable output also runs a new Task:

```bash
kmx cluster quickstart --output json --task 'Who are you?'
```

JSON stdout is one document; subprocess/progress output goes to stderr.
See [output contracts](kmx.md#output-contracts).

## Guided native authoring

`kmx cluster quickstart --interactive` guides native source/case review, full eval,
publication and reviewed Deploy before a separate fresh answer Task. Console
creation selects authored native source; Ready there is placement only.

Automatic tools and host Foundry/Copilot wizard modes are retired; ordinary
host-backed chat controls stay independent. Chat `/lift` and console Deploy only
print `Deploy moved: kmx package deploy <ref>@<digest> --to-context … --evidence …`.
Use [digest Deploy](suite-workspace.md#digest-lift) with exact package/private evidence
and explicit target/bindings, not a release GUI/live-copy fallback. Cancellation
can leave completed resources in place.

### The whole runtime

To prepare the runtime without creating an Agent, use `kmx cluster up` instead of quickstart:

```bash
kmx cluster up
kmx cluster orka status
# For an existing live Agent, including one placed by Deploy:
kmx chat --namespace orka-system <agent-name>
```

`kmx cluster up` brings up kind, keyless Ollama with the configured `MODEL`, and pinned
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

A cluster that still carries kagent is untouched by Orka setup. Outside those
version-qualified operations, use upstream tools or kubectl.
Owners must deliberately remove or replace obsolete gateway references; setup
does not repoint existing tools. Interactive `/help` lists local controls.

## Using Podman instead of Docker

```bash
kmx --container-engine podman quickstart
# Equivalently for automation:
CONTAINER_ENGINE=podman kmx cluster quickstart
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
- `kmx cluster down` deletes the **whole local cluster
  and its data**, including any historical database. Export needed data first;
  see [Orka backup cautions](orka.md#preserve-the-charts-generated-snapshot-key).
  For AKS use [`kmx cluster aks down`](aks.md#teardown), not kind down. Azure resources can
  remain billable after a failed operation; verify cleanup rather than assuming cancellation deleted them.

Next: [CLI reference](kmx.md), [models and endpoints](models.md),
[workspace lifecycle](suite-workspace.md).
