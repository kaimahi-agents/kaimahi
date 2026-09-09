# Getting started

From an empty machine to a conversation with an agent that is defined
entirely in YAML. Everything runs on [kagent](https://kagent.dev):
kagent's controller provisions the agent, kagent's CLI talks to it.
Kaimahi adds no runtime code at this point, only the YAML, a values file,
and `kmx` — one command that glues `kind`, `helm`, `kubectl` and the kagent
CLI together. Nothing runs in your cluster that is not kagent's or
Kubernetes' own.

Every command here was run against a live cluster before it was written
down. Model replies vary run to run; where one is quoted, expect the same
substance and different wording.

## Prerequisites

**One**: a container engine.

| Tool | Needed for | Install |
|------|-----|---------|
| Docker **or** Podman | everything — kind runs Kubernetes in containers | <https://docs.docker.com/get-docker/> · <https://podman.io/docs/installation> |
| kind, kubectl, Helm | **fetched by `kmx`** when they are absent, pinned and checksum-verified into `~/.config/kmx`. A copy you already have on PATH is used instead, always | — |
| curl | the install script (already present on macOS and every mainstream Linux) | your package manager |
| Go 1.26+ | **only** the two commands that build the plane's image. `kmx plane` needs it just when it is run from outside a checkout, where it fetches the plane's source from the Go module proxy; `kmx lift` demands it whenever its plane phase runs, checkout or not. Also `go install`, as an alternative way to get `kmx` | <https://go.dev/dl/> |
| make, git | **only** the clone path at the bottom of this page | your package manager |

`kmx` acquiring its own tools is not new behaviour invented here: it has
always downloaded the pinned kagent CLI the same way, verifying the published
digest before executing it and re-verifying it on every later use. kind,
kubectl and Helm now follow the same rule. If you would rather your machine
only ran binaries your own package manager put there, set
`KMX_TOOLCHAIN=off` and a missing tool goes back to being an error that names
its install page.

### Using Podman instead of Docker

Pass `CONTAINER_ENGINE=podman` to any target. It is explicit rather than
auto-detected, so the engine that built an image is always visible in the
command:

```bash
make up   KIND_CLUSTER=<your-name> CONTAINER_ENGINE=podman
make plane KIND_CLUSTER=<your-name> CONTAINER_ENGINE=podman
make down KIND_CLUSTER=<your-name> CONTAINER_ENGINE=podman
```

Set it once per shell with `export CONTAINER_ENGINE=podman` if you never use
Docker. **Pass it to every target for a given cluster** — kind reaches a
podman cluster only with `KIND_EXPERIMENTAL_PROVIDER=podman`, which the
Makefile sets from this variable, so a cluster created with one engine is
invisible to the other and looks like "kind lost my cluster".

On macOS the podman machine must be able to read your checkout. Machines
created with the defaults mount `/Users`, `/private` and `/var/folders`; a
machine created without them fails the build with
`faccessat <path>: connection refused`. Check with:

```bash
podman machine inspect --format '{{.Name}}'
podman run --rm -v "$HOME":/h:ro alpine ls /h   # must list your home directory
```

Volumes can only be set when the machine is created, so a machine missing
them has to be recreated:

```bash
podman machine rm <name>
podman machine init --volume "$HOME:$HOME"
podman machine start
```

No API key is needed anywhere: the default model is an in-cluster
[Ollama](https://ollama.com) server running `qwen2.5:3b` (free, local,
keyless). Hosted models are in [models.md](models.md).

## One command, and an agent that answers

```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart
```

That downloads `kmx` for your platform, checks it against the release's
published sha256, installs it into `~/.local/bin` (no sudo, ever), and runs
`kmx quickstart`: a kind cluster, an in-cluster model, kagent, one agent, and
the agent's answer to a question. Measured end to end on a clean machine with
nothing but Docker installed: **178s**, just under three minutes. That is the
one recorded measurement of this path; the [CHANGELOG](../CHANGELOG.md) has
it against what the same journey cost before (246s).

Drop `--quickstart` to install `kmx` and stop there. Set `KMX_VERSION=v0.1.0`
to pin a version, or `KMX_BIN_DIR=/somewhere/else` to install elsewhere.

`kmx quickstart` is safe to run again. It marks its own first-answer installation
and reconciles that reduced profile; after `kmx up`, or against an unmarked custom kagent
installation, it preserves the existing Helm release rather than turning the
UI, tool server or MCP controller off. It reads that release state before any
Helm mutation. Any deployed kagent application release is kept unchanged,
including custom values and versions, while quickstart checks controller
readiness. Only a successful, valid empty release list permits a minimal `helm
install`, not an upgrade, so a concurrently created release is not overwritten.
Failed, pending, other non-deployed, unreadable, and unexpected states fail
closed and require deliberate repair. Other setup steps still reconcile their
resources. The second run finds the cluster and asks another question.

The release query uses explicit status flags supported by Helm 3 and 4, not
`helm list --all`. A new install waits for Helm-managed workloads and jobs.
`--output json` makes the result drivable by something other than a person:

```bash
kmx quickstart --output json --task 'Who are you?'
```

```json
{
  "ok": true,
  "context": "kind-kaimahi-p1",
  "cluster": "kaimahi-p1",
  "agent": "hello-world",
  "manifest": "k8s/hello-world.yaml (embedded in kmx; `kmx agent create` writes your own)",
  "question": "Who are you?",
  "answer": "I am a declarative kagent agent defined entirely in YAML...",
  "governed": false,
  "tools": null,
  "elapsed_seconds": 42.1,
  "next": [
    "kmx --context kind-kaimahi-p1 agent chat hello-world 'ask it something else'",
    "kmx --context kind-kaimahi-p1 agent create my-agent --description 'Describe your agent'",
    "KIND_CLUSTER=kaimahi-p1 CONTAINER_ENGINE=docker kmx --context kind-kaimahi-p1 up",
    "KIND_CLUSTER=kaimahi-p1 CONTAINER_ENGINE=docker kmx --context kind-kaimahi-p1 plane",
    "kmx --context kind-kaimahi-p1 govern hello-world"
  ]
}
```

This illustrative result uses `--task 'Who are you?'` and tools already on PATH;
answers, timing, and tool provenance vary. `tools` is null when this invocation
provisioned none. It shows the complete, unchanged top-level key set. Actual
`next` commands pin the selected context and relevant cluster/engine settings.

`"governed": false` means **this invocation did not enable governance**. It is
not an assertion that the cluster or agent has none: reruns can preserve
existing governance, which quickstart does not assess. On a fresh cluster the
path is ungoverned; see [governing that agent](#governing-that-agent) below.
Success requires a completed task with a readable answer, not merely a response
containing text. Progress and subprocess chatter stay off JSON stdout.

Human output uses rich grouping on capable terminals and plain formatting when
redirected or under `TERM=dumb`. `NO_COLOR=1` removes ANSI while retaining static
rich layout. Status, agent list, and admin reports have responsive terminal
tables; their redirected compatibility formats remain, with intentional safety
corrections to unknown states, readiness, and flow refusal counts. See
[output contracts](kmx.md#output-contracts).

### With a Go toolchain instead

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
```

`@latest` is the newest tagged release, and `kmx version` says which one you
have. Releases also carry checksum-verified binaries you can download by hand
— see [releases.md](releases.md).

### The whole runtime

`kmx quickstart` deliberately deploys only what a first question touches on a
new installation. When you want the rest — kagent's console and bundled tool
server, the second (tool-using) agent — that is `kmx up`, which reconciles the
same cluster. Unlike quickstart's preserve-or-install policy, `up`
upgrades/installs the full kagent application chart and waits for its workloads
and jobs; it is the explicit way to restore the full profile. A later
quickstart does not reverse that reconciliation:

```bash
kmx up      # kind cluster + Ollama + model pull + kagent + two agents (first run ~5-10 min)
kmx agent chat hello-world "Who are you and where are you running?"
kmx status  # grouped agents, models, runtime health, next actions
kmx down    # delete the kind cluster (and everything in it, ledger included)
```

`kmx` is the whole journey in one command; [kmx.md](kmx.md) is its
reference, including what it deliberately does *not* do. Budgets,
approvals, tool governance, backup/restore, metrics and the managed-cluster
path (`kmx lift`) are all `kmx`'s now. What is still the Makefile's: the
Slack and inbound connector families; the demo and first-user agents that
are wired from committed manifests — the release agent, the
accounts-payable demo and the hosted-GitHub agent (`make govern-release`,
`make release`, `make erp`/`make govern-ap`/`make ap-demo`,
`make govern-github`); capturing a model key; and the network probes.

### Governing that agent

On a fresh cluster the runtime above is not metered. Neither `quickstart` nor
`up` enables governance; rerunning them is not evidence that existing governance
is absent. The plane is a separate command, and on kind it is keyless too:

```bash
kmx plane               # metering proxy + Postgres ledger, in the cluster
kmx govern hello-world  # issue the credential, switch the agent onto it
kmx agent chat hello-world "Who are you and where are you running?"
kmx ledger              # what that answer cost, attributed to a credential
```

`kmx plane` needs no clone and no registry: it fetches the plane's source
from the public Go proxy **at kmx's own revision**, builds it, and
side-loads the image into kind. The agent is handed an opaque Kaimahi token,
never an upstream key. [spend.md](spend.md) is what the plane does;
[kmx.md](kmx.md#how-the-plane-gets-there-without-a-clone) is how it gets
there.

### The same journey from a clone

Everything below uses `make`, and every one of these targets now calls the
same `kmx` binary — the Makefile builds it from the checkout. Use whichever
you prefer; they run the same code.

```bash
make        # build bin/kmx and print its path; no cluster changes
make up     # kind cluster + Ollama + model pull + kagent + two agents (first run ~5-10 min)
make chat   # ask the default question
```

At a terminal `make chat` prints a readable view of the reply. Piped into
a file or a script — and with `--json` — it prints the raw A2A task JSON
instead, which is what CI asserts on. Buried in that JSON is the reply,
from a real run:

```text
"I am the hello_world agent, designed to greet users and provide
information about myself. I am running on Kubernetes via kagent."
```

The underscore in `hello_world` is real: kagent normalizes the agent name
internally, and that is the name the model sees and repeats.

Ask your own question, or talk to the second agent, which has a tool:

```bash
make chat TASK="What are you defined in?"
make chat AGENT=hello-tools TASK="What pods are running in the ollama namespace?"
```

Keep a back-and-forth session with streamed replies and visible tool activity:

```bash
INTERACTIVE=1 make chat AGENT=hello-tools
# or directly: kmx agent chat --interactive hello-tools
```

The header names the active agent, model/tool governance posture, and effective
selected tools. Messages are labelled `You` and replies with the active agent;
tool calls and completion state appear inline. Use `/tools verbose` for full
tool results, `/sessions`, `/history`, and `/resume <id>` for prior
conversations, `/new` for a fresh session, and `/exit` to leave. See
[kmx.md](kmx.md#interactive-chat).

`--interactive` selects a human transcript and supports scanner input when a
capable terminal/raw mode is unavailable. `NO_COLOR` also disables enhanced chat
input and cursor effects, without removing trusted actor/operation labels.
`--interactive --json` is refused; use one-shot `--json` for raw A2A output.
Resizing during enhanced input stops chat without submitting the current message
or approval; restart to continue. This is a safe abort, not live input reflow.
The broader one-shot transport retry policy remains unchanged and can repeat a
turn after an ambiguous disconnect; see [retry limits](kmx.md#retry-limits).

The tools agent is covered in [tools.md](tools.md), including why its
prose summary is less reliable than the tool call underneath it.

## An agent of your own

```bash
kmx agent create fleet-reporter \
  --description "Reports what is running in the cluster." \
  --instructions ./fleet.md \
  --tools kagent-tool-server:k8s_get_resources
```

That writes `agents/fleet-reporter.yaml` — the same kind of document as the
one below, which you own, review and commit — and applies it. The tool
allowlist is mandatory: naming a server alone would grant every tool it
offers, today and after its next release. `kmx agent create` accepts no
credential in any form, and refuses to write a manifest with anything
key-shaped in it. [kmx.md](kmx.md#kmx-agent-create) has the full list of
what it refuses and why.

There is exactly one path in `kmx` that takes a credential —
`kmx credential capture <upstream> <repository|organization>`, for the
three tool upstreams it can check a token against. The value is **typed at
a prompt with the echo off**: no flag, no environment variable, no file,
and a pipe or a redirect is refused rather than read, because a credential
that can arrive through a pipe can arrive from a shell history or a CI log.
Model keys, the Slack bot token and inbound signing keys are still captured
by their own scripts (`make model-secret`, `make copilot-secret`,
`make slack-secret`, `make inbound-secret`), which read from stdin.

```bash
make status   # grouped agents, models, runtime health, next actions
make down     # delete the kind cluster (and everything in it, ledger included)
```

`make status` groups the selected context, agent-to-model/tool wiring, runtime
health across kagent/Ollama/the optional plane, pod restarts, next actions, and
a **Governance** section that counts how much of the system is actually behind
the plane:

```text
Governance
  plane:        not installed — nothing is enforced in front of these seams (`kmx plane`)
  model seams:  0 of 2 agents governed, 2 direct
  tool seams:   0 of 3 tool servers governed, 3 direct
  credentials:  none — no governed seam names one
```

The three lines count three different populations, and the difference is
deliberate: **model seams** counts AGENTS by where their model calls go,
**tool seams** counts the RemoteMCPServers on the cluster, and
**credentials** counts the Secrets the governed seam objects NAME — so a
governed preset no agent is using still requires its token, and a token
that has gone missing is reported before the next call fails on it. Only
Secret names are read; `kmx status` never reads a Secret's value.

The counts are read from the cluster objects — a seam is governed when it
points at the plane's Service — so they work with no plane, no credential and
no network. Nothing there is a fault: the fast path is ungoverned by design,
and the counts are how you see how much of your system is still on it. A
population kmx could not read says `unknown` and why; it never reports a zero
it did not count.

Unknown Kubernetes conditions remain `unknown`, not `no`. An installed plane
with zero or insufficient ready replicas, missing required credentials, or
unreadable required governance prevents a human `ready` verdict even if cached
agent conditions still say Accepted. These are intentional safety corrections
in both rich and plain reports; they do not make the supported direct path a
fault or change the structured envelope.

For the machine-readable form use `kmx status -o json`, `kmx status -o yaml`,
or from Make: `make status STATUS_OUTPUT=yaml`. That document carries
`context`, the `governance` block, and the kubectl objects verbatim under
`items` — so `jq '.items[]'` reads exactly what it always did. **What
changed:** the top level is no longer a Kubernetes `List`, so the
`apiVersion` and `kind` fields are gone and the output can no longer be
piped back into `kubectl apply`. Each population's `state` is the field to
read first: an `unknown` population publishes no counts at all, so a script
that reads `governed` without checking `state` gets a missing key rather
than a zero nobody counted.

On the clone path the governed half is `make plane` and `make govern`, which
are the same kmx commands with the checkout passed as the plane's source —
so a change you make to `plane/` is what gets deployed.

Coming from the project's old name? The cluster is now `kaimahi-p1` and
the Copilot login cache moved. See
[the FAQ](FAQ.md#i-have-a-cluster-and-paths-from-the-tomte-era).

## The agent is a YAML file

There is no kaimahi runtime to install. The whole agent,
[`k8s/hello-world.yaml`](../k8s/hello-world.yaml), is a kagent `Agent`
plus the `ModelConfig` it thinks with, in one document you can read, diff,
and review:

```yaml
apiVersion: kagent.dev/v1alpha2
kind: Agent
metadata:
  name: hello-world
  namespace: kagent
spec:
  type: Declarative
  declarative:
    modelConfig: hello-world-model
    systemMessage: |
      You are Kaimahi's hello-world agent, ...
```

`kubectl apply -f` it and kagent's controller provisions a pod for it.
The topology grows the same way:
[`k8s/tools-agent.yaml`](../k8s/tools-agent.yaml) is the same shape plus
a `tools:` block. No make target ever mutates the committed file.
Switching models patches the live Agent resource, not the YAML.

## What `make up` does, step by step

The `up` target on kind runs `cluster`, `ollama`, `model`, `kagent`,
`agent`, `tools-agent`, then `status`. In plain commands:

```bash
# 1. Local Kubernetes cluster (skipped if it already exists)
kind create cluster --name kaimahi-p1

# 2. In-cluster Ollama model server (namespace, deployment, service)
kubectl apply -f k8s/ollama.yaml
kubectl -n ollama rollout status deploy/ollama

# 3. Pull the model into the Ollama pod
kubectl -n ollama exec deploy/ollama -- ollama pull qwen2.5:3b

# 4. Install kagent (CRDs chart, then the app chart), pinned to v0.9.12,
#    with Ollama as the default provider and its bundled tool server
#    switched on in locked-down form
helm upgrade --install kagent-crds \
  oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds \
  --version 0.9.12 --namespace kagent --create-namespace
helm upgrade --install kagent \
  oci://ghcr.io/kagent-dev/kagent/helm/kagent \
  --version 0.9.12 --namespace kagent -f k8s/kagent-values.yaml \
  --wait --wait-for-jobs --timeout 420s

# 5. The agents: hello-world, then hello-tools once kagent's tool server
#    is Accepted
kubectl apply -f k8s/hello-world.yaml
kubectl apply -f k8s/tools-agent.yaml
```

Every step that writes to a cluster runs a guard first. On a local kind
cluster it prints a banner and proceeds; on anything else it demands a
confirmation naming the context. See [aks.md](aks.md) for why.

Re-running `make up` is safe for a governed agent. The `agent` and
`tools-agent` steps read the live agent first and, if it is on a
non-default ModelConfig or wired through the governed tool gateway, they
re-apply the committed YAML and then restore that state, with a `NOTE:`
line saying so. `make use PRESET=ollama` and `make ungovern-tools` are
the explicit ways back. An earlier version of `make up` silently reset
the agent; if you see that symptom, the
[FAQ entry](FAQ.md#my-governed-agent-stopped-showing-up-in-the-ledger)
covers it.

Overridable: `KIND_CLUSTER`, `KAGENT_VERSION`, `MODEL`, `AGENT`, `TASK`,
and `TARGET` (`kind` by default, or `aks`).

## Talking to the agent

`make chat` lets kmx fetch/cache the pinned kagent CLI (checksum-verified),
checks that the agent answers through its Service, asks kubectl for a free
loopback port, and port-forwards the kagent controller there. Set `CHAT_PORT`
only when a fixed port is required.

```bash
kmx agent chat hello-world "Hello! Who are you and where are you running?"
```

An explicit occupied `CHAT_PORT` still fails rather than falling back: an
explicit value is a deterministic contract. With the default automatic port,
stale forwards and concurrent chats do not collide.

Other ways in, all shipped by kagent:

```bash
kubectl -n kagent get agents            # CRD status (Ready / Accepted)
bin/kagent get agent                    # via the CLI (needs the port-forward)
bin/kagent dashboard                    # kagent's web UI
```

## Choices and caveats

- **Model**: `qwen2.5:3b` (~2 GB). kagent's runtime gives every agent a
  built-in `ask_user` tool; smaller models (`llama3.2:1b`/`3b`) misfire it
  with malformed arguments and the invocation fails with
  `'str' object has no attribute 'get'`. Telling the model not to use the
  tool in the system message does not stop it. Qwen 2.5 answers plainly.
  Any tool-capable Ollama model works via `make model MODEL=<tag>` plus
  the `model:` field in the two agent YAML files, but invocation-test it
  with several fresh chats before trusting it. "It's a known model" is
  not a test. Both small-model failure modes are in the
  [FAQ](FAQ.md#the-agent-errors-with-str-object-has-no-attribute-get).
- **Models are pod-local**: the Ollama pod stores models in an
  `emptyDir`, so a pod restart re-pulls (`make model`). Deliberate: no
  PVC to manage in a demo.
- **Version pin**: kagent v0.9.12 (`KAGENT_VERSION`), the latest stable
  at the time; 0.10 was still in RC. The Agent CRD's `runtime: go`
  variant does not work out of the box at this version: the chart's
  default registry (`cr.kagent.dev`) does not carry `golang-adk:0.9.12`
  (ImagePullBackOff), though the image does exist on `ghcr.io`. If you
  need the go runtime, set `controller.agentImage.registry=ghcr.io` in
  [`k8s/kagent-values.yaml`](../k8s/kagent-values.yaml). The agents here
  stay on the default python runtime. (Verified at 0.9.12 when the pin
  was chosen; not re-verified in this restructure.)
- **`make down` deletes everything**, including the governance plane's
  Postgres and its ledger, if you have deployed them. Demo-durable, not
  backup-managed.

## Where next

- A hosted model instead of the local one: [models.md](models.md).
- What the tools agent can do and how a real tool call is proven:
  [tools.md](tools.md).
- Budgets, a ledger, and keeping real API keys away from the agent:
  [spend.md](spend.md).
- Running the plane for real — two replicas, probes, metrics, backup and
  restore, and what is still not highly available:
  [operations.md](operations.md).
