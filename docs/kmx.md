# `kmx` — agent tooling with Orka as the default

[Orka](orka.md) remains the first-class platform and the default for every
existing workflow. Kaimahi's front door is `kmx orka` for installation/status
and `kmx agent create` for native Provider + Agent authoring. The selected
runtime owns execution, enforcement and model access; installing it alone does
not prove an Agent can answer.

The former broad Kagent integration is not restored. Its installer steps,
manifests, chat, list/show/status, lift/evaluate, console, quickstart, `up`, and
AKS payload remain absent or retired. The only current capability is explicit
`kmx agent create --runtime kagent <name>` against an already-installed exact
Kagent v0.10.2. Historical lift records remain readable for teardown only.
Plane administration, model overlays, migration, governance and preset
switching are removed, not redirected to replacement commands. Agent editing
remains retired; its hidden stub points to kubectl. `orka.harness.v2` is outside
the direction.

## Install

The stable release is available through the official Homebrew tap:

```bash
brew install kaimahi-agents/tap/kmx &&
  kmx_prefix="$(brew --prefix kaimahi-agents/tap/kmx)" &&
  "$kmx_prefix/bin/kmx" version
```

The fully qualified formula trusts only `kmx`. Homebrew installs the CLI, not
Docker, Podman or Go; local kind still needs a container engine. To build the
same release with Go 1.26+ instead:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.0
kmx version
kmx orka --help
```

Ensure Go's binary directory is on PATH. `@latest` follows the newest tagged
release; `@main` remains a moving development revision. From a checkout,
`make` builds `bin/kmx` and prints its path without changing a cluster; use
that binary to exercise edits.

Alternatively, use the checksum-verified release installer:

```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
```

That installer puts the selected release in `~/.local/bin`, without sudo;
`--version=v0.4.0` pins it, `--bin-dir=DIR` changes the destination, and
`--quickstart` continues into `kmx quickstart`. A new shell may need
`~/.local/bin` on `PATH`, or invoke `$HOME/.local/bin/kmx` directly. It checks the binary against
a checksum from the **same** GitHub release over TLS: corruption detection, not
an independent signature. See [releases](releases.md) for platforms and upgrades.

Local kind commands need Docker or Podman. kmx uses kind and kubectl from
PATH first, otherwise fetches pinned, checksum-verified tools, including
Helm 3 for Orka's v0.2.0 chart. Helm calls pin the selected Kubernetes context
with `--kube-context`. No Orka runtime CLI is fetched or cached. Cached digests are
rechecked before reuse. Set
`KMX_TOOLCHAIN=off` to refuse missing tools instead. No container engine or Azure
CLI is installed for you. AKS setup installs Orka directly.

Docker remains the default for compatibility. Podman is a first-class explicit
choice:

```bash
kmx --container-engine podman quickstart
```

The global flag works before or after a subcommand and overrides
`CONTAINER_ENGINE=...`. kmx deliberately does not fall back between live daemons:
Docker and Podman have separate kind inventories and can each own a same-named
cluster, so a silent switch can target the wrong one.

## Commands

Use `kmx --help` and `kmx <command> --help` for flags and defaults. The Cobra tree
also generates completion; this guide describes contracts rather than duplicating
every flag. Command definitions are in [`cmd/kmx`](../cmd/kmx).

**Unreleased entry-point alignment:** `context show/use` and `local up/down`
are the canonical grouped commands below. Tagged v0.4.1 uses `ctx [context]`
and root `up/down`; those routes remain callable in newer builds with notices
only on stderr. No removal release is scheduled. See the
[alignment record](command-semantics.md) for current versus proposed names.

### Current Orka path and explicit Kagent create

| Command | Contract / reference |
|---|---|
| `kmx targets [-o table\|json] [--detect] [--sessions <host:port>]` | offline compiled support by default; explicit read-only probes report present/absent/unreadable separately from qualified versions. No runtime selection, installation or deployment. [Target view and support matrix](runtime-adapters.md#read-only-target-view) |
| `kmx orka install` | verify the pinned v0.2.0 release chart; apply its CRDs; install harness-v2 with fullname `orka-api` on the selected context; keep the chart-generated snapshot key private; optionally create a keyless Provider. Refuses old v0.1.3 installs rather than upgrading. [Orka](orka.md) |
| `kmx orka status` | read running controller version, Deployments, CRDs and Providers; distinguish unreadable from absent and running version from pin |
| `kmx agent create [name]` | default, unchanged Orka authoring: native Provider + Agent and optional Task. Explicit `--runtime kagent <name>` is create-only for an already-installed exact Kagent v0.10.2. Retrieve a real answer only with `--task`. [Create contract](#kmx-agent-create) |
| `kmx suite build <directory> ...` | resolve one agent/platform composition and build an OCI image-layout tar through AgentKit using Docker buildx. The current implementation is explicitly experimental and non-conformant because it treats the selected harness image as a monolithic AgentKit adapter and does not compose the runtime-base image. |
| `kmx suite push <directory> <registry/repository:tag>` | deterministically archive and validate an extracted AgentSuite, then push it to an OCI registry. An existing tag with the same digest is a no-op; a different digest requires `--force`. Prints the pushed digest. See [registry safety](#registry-authentication). |
| `kmx suite push <directory> --to-layout <layout> <repository:tag>` | deterministically archive and validate an extracted AgentSuite, then add its referenced artifact to a local OCI image layout. New layouts are published atomically; existing layouts retain unrelated references. Output states whether the layout was created or updated. |
| `kmx suite pull <registry/repository:tag-or-digest> --output <directory>` | pull and validate one AgentSuite from an OCI registry, then atomically extract it into a new directory. Prints the resolved digest; tag pulls also print a digest-pinned reference for repeatable pulls. See [registry safety](#registry-authentication). |
| `kmx suite pull <repository:tag> --from-layout <layout> --output <directory>` | resolve and validate one AgentSuite reference from a local OCI image layout, then atomically extract its content into a new directory. Existing output is never replaced. |
| `kmx suite validate <path>` | validate an extracted AgentSuite content directory or OCI image layout offline, including strict JSON, canonical identities, closed references, tool locks, build profiles and archive safety. `-o json` emits the validated summary. [AgentSuite specification](agentsuite-spec.md) |
| `kmx agent lift <bundle-dir>` | reconcile an existing Orka portable bundle on a prepared destination; Kagent bundles are refused before target reads. `--plan` checks without writing. [Bundle lift](agent-lift.md) |
| `kmx agent retire <bundle-dir>` | delete or release bundle-owned Orka resources after UID, receipt and dependent checks; Kagent bundles are refused before target reads. `--plan` checks without writing. [Bundle retirement](agent-lift.md#retiring-a-bundle-from-a-target) |
| `kmx agent status <bundle-dir>` | compare an Orka portable Git revision with each recorded target's live Provider and Agent, readiness, drift and evaluation result; Kagent bundles are refused. `--to-context` selects one target, `-o json` emits structured facts. [Bundle status](agent-lift.md#checking-deployed-status) |
| `kmx agent evaluate <bundle-dir>` | run an Orka bundle's `eval/*.yaml` cases as one Task each against the deployed revision, only when it carries the bundle's current portable digest; Kagent bundles are refused. Print answers, write a receipt with answer digests (never text), exit non-zero unless every case passed. [Bundle evaluation](agent-lift.md#evaluating-a-deployed-revision) |
| `kmx agent run <bundle-dir> --prompt <text>` / `kmx agent run --agent <name> --prompt-file <path>` | execute one Task against an already-deployed Agent without changing the bundle; stdout is answer-only. Supports `--prompt-file -` for stdin and `--wait` (default 5m, maximum 9m). [Running an existing Agent](agent-lift.md#running-an-existing-agent) |
| `kmx task result <task> [--context <ctx>] [--namespace <ns>] [--wait 5m]` | inspect an AI Task's phase once by default and open a result session only for a readable terminal answer; `--wait <duration>` waits 10s–9m and reports phase changes. Pending or not-yet-readable results exit 2; failed or cancelled Tasks exit 1. [Running an existing Agent](agent-lift.md#running-an-existing-agent) |
| `kmx context show` / `kmx context use <context>` | read effective context/source/posture or remember KMX's guarded access selection; never change kubectl current-context |
| `kmx console` | two-column local/remote workspace for native Orka Agents, with Vim/arrow navigation, agent actions, inference details and slash-command completion; Kagent inventory and bundles are unsupported. `b` compares the selected Orka agent with its local bundle using the same report as `kmx agent status`; `--demo` uses sample data. [Console guide](interactive-agent-tui-plan.md) |

#### Experimental AgentSuite image build

The first image-build backend is intentionally narrow:

It requires Docker with the buildx plugin. Authenticate Docker to any private
registry containing the selected harness image before building.

With the classic Docker image store, the default `docker` builder can reject
`--output type=oci` with `OCI exporter is not supported for the docker driver.`,
even with attestations disabled. If you see this error, enable Docker's containerd
image store, or create a `docker-container` builder and pass its name to KMX:

```sh
docker buildx create --name kmx-builder --driver docker-container
# Add --builder kmx-builder to the kmx suite build command below.
```

`--builder` selects an existing Buildx builder for this build without changing
Docker's default builder. When omitted, Docker Buildx uses its existing builder
selection. Disabling attestations does not fix an unsupported OCI exporter.

```zsh
SUITE=./internal/kmx/agentsuite/testdata/incident-analyst

kmx suite validate "$SUITE"

read "AZURE_OPENAI_RESOURCE?Azure OpenAI resource name: "
AZURE_OPENAI_BASE_URL="https://${AZURE_OPENAI_RESOURCE}.openai.azure.com/openai/v1/"

kmx suite build "$SUITE" \
  --agent incident-analyst \
  --platform linux/amd64 \
  --model-base-url "$AZURE_OPENAI_BASE_URL" \
  --model-api-key-env AZURE_OPENAI_API_KEY \
  --builder kmx-builder \
  --require-attestations \
  --verbose \
  --output incident-analyst.oci.tar

tar -tf incident-analyst.oci.tar
```

`--verbose` exposes Docker buildx progress. Correctness warnings,
including the current non-conformance warning, are always written to stderr.
An existing output archive is replaced only after the new build completes
successfully; a failed or canceled build preserves the previous archive and
removes its staged partial output.

SBOM and maximum-mode provenance attestations are requested by default
(`--sbom=true --provenance=mode=max`). If the selected builder explicitly rejects
attestation support, KMX warns and retries without either attestation.
`--require-attestations` fails instead of downgrading and refuses an archive
missing the required statements. Use `--attestations=false` to opt out explicitly;
it cannot be combined with `--require-attestations`. Authentication, scanner,
network, cancellation, and unrelated build failures are not grounds to downgrade.

Attestations are stored inside the OCI archive: `index.json` can reference a
nested image index containing the runnable platform manifest and an
`unknown/unknown` attestation manifest. The latter contains in-toto SBOM and
provenance statements bound to the runnable image. KMX prints the runnable image
manifest digest as **the image digest** and reports the index digest separately
as including attestations and varying per build. `SOURCE_DATE_EPOCH` and
`rewrite-timestamp=true` normalize image timestamps; provenance still records
per-build timestamps, invocation identifiers, and BuildKit metadata. Do not
expect byte-identical attested archives. An unchanged runnable-image digest was
demonstrated across two cached builds of a scratch-image probe; this is not a
clean-room reproducibility guarantee for every agent.

When an SBOM is present, KMX prints a `tar -xOf` command with the archive path and
SBOM blob path. Run that command to read the complete in-toto SPDX statement
without a registry or network access. These BuildKit attestations are not signed
identity evidence; KMX does not sign user-built images.

Create an Azure OpenAI deployment named `gpt-5-mini` in Azure AI Foundry. Then
load the image and inject its API key only when the container starts:

```zsh
docker load -i incident-analyst.oci.tar

read -s "AZURE_OPENAI_API_KEY?Azure OpenAI API key: "
echo
export AZURE_OPENAI_API_KEY

docker run --rm \
  --platform linux/amd64 \
  --name incident-analyst \
  -p 127.0.0.1:8080:8080 \
  -e AGENTKIT_BIND=0.0.0.0 \
  -e AGENTKIT_AUTH_TOKEN=local-test-token \
  -e AZURE_OPENAI_API_KEY \
  incident-analyst:latest
```

The image is not build-locked to the OpenAI-compatible server surface.
AgentKit selects its protocol at runtime with `AGENTKIT_PROTOCOL`; the command
above uses the default `openai` protocol for local testing. Supported AgentKit
versions can instead select `foundry`, `orka`, or `acp` without rebuilding the
image.

From another terminal:

```bash
curl --fail http://127.0.0.1:8080/healthz

curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'authorization: Bearer local-test-token' \
  -H 'content-type: application/json' \
  -d '{
    "model": "gpt-5-mini",
    "messages": [{
      "role": "user",
      "content": "At 14:03 UTC checkout latency rose from 200 ms to 8 s. Pods report database connection pool exhaustion. Produce an incident triage report."
    }]
  }'
```

The checked-in incident analyst is a realistic, tool-free example: it turns
pasted alerts, logs and operator notes into an evidence-based triage report. Its
Linux amd64 Python runtime base and AgentKit v0.1.0 Pydantic AI harness are real,
platform-specific, digest-pinned OCI manifests. The example targets an Azure
OpenAI deployment named `gpt-5-mini` through the OpenAI-compatible v1 endpoint.
The build records only the environment variable name `AZURE_OPENAI_API_KEY`; it
never reads or embeds the key. `--model-api-key-env` accepts only names matching
`[A-Za-z_][A-Za-z0-9_]*`; invalid names are rejected before any build files are
written. Supply the key in that environment variable when running or deploying
the resulting image.

The command validates the suite, resolves one immutable sandbox build plan and
publishes a new output atomically. It supports only agents without ToolProviders,
invocation edges, endpoint environment bindings or model secret references.
AgentKit v0.1 requires a literal model `baseURL`, so this adapter currently
embeds `--model-base-url` and rejects `model.endpointEnv`; a future AgentKit
release needs runtime model-endpoint environment resolution before KMX can
preserve that portable binding.
KMX writes the current experimental `pydantic-ai` runtime into a generated
AgentKitfile and invokes `docker buildx build`. The AgentKit frontend is
digest-pinned, the selected harness image is passed as its adapter, and buildx
exports the OCI tar through KMX's staged output. Docker supplies registry
credentials for private harness images through its configured credential store.
KMX does not link the BuildKit client libraries or create a privileged BuildKit
daemon.

The provider-neutral build-plan and builder contracts do not import AgentKit.
The current adapter uses the selected harness image as AgentKit's
monolithic adapter. The experimental plan currently carries validated metadata,
not verified image blobs or ToolProvider payload bytes; a conformant offline
builder will require a provider-neutral verified content source.
It emits a warning because the runtime-base image, AgentSuite binding, final
inventory and provider payload composition are not yet represented. The harness
image must therefore currently be a complete AgentKit adapter image from the
`ghcr.io/orka-agents/agentkit/serve-pydantic-ai` repository, matching the fixed
`pydantic-ai` runtime; other harness repositories are refused. A future
conformant AgentKit adapter or another builder can replace it without changing
AgentSuite resolution.

#### Registry authentication

Registry push and pull do not add a Kaimahi login contract or accept credentials
on command arguments. They use the Docker-compatible config and credential
helpers selected by `DOCKER_CONFIG` or the standard `~/.docker/config.json`.
Authenticate before invoking `kmx`, for example:

```bash
ACR_NAME=<registry-name>
ACR_LOGIN_SERVER="${ACR_NAME}.azurecr.io"
az acr login --name "$ACR_NAME"
kmx suite push ./suite "$ACR_LOGIN_SERVER/agentsuites/team:v1"
kmx suite pull "$ACR_LOGIN_SERVER/agentsuites/team:v1" --output ./suite-copy
```

`docker login` and `oras login` work when they populate the same credential
store. `az acr login --expose-token` only returns a token; by itself it does not
persist credentials for `kmx` to read. Kaimahi never prints credentials and
does not expose username, password, or token flags. HTTPS is the default;
`--plain-http` must be supplied explicitly for a development registry and
cannot be combined with `--to-layout` or `--from-layout`. Credentials never go
over plain HTTP to a non-loopback host, including a token endpoint advertised
by a registry or a redirect destination. Only `localhost`, addresses in
`127.0.0.0/8`, and `::1` qualify as loopback; arbitrary hostnames resolving to
loopback do not. A non-loopback plain-HTTP registry is accessed anonymously,
without an `Authorization` header; if it requests authentication, `kmx` refuses
and requires HTTPS. Loopback development registries may use stored credentials.

Registry push checks the existing tag's digest before writing. A new tag is
created; re-pushing the same digest succeeds without writing; replacing a
different digest is refused unless `--force` is supplied. This is a preflight
check, **not atomic against concurrent pushers**: another writer can change the
tag between the check and the push. Use distinct tags or registry-enforced
immutability when concurrent writers must not overwrite each other. `--force`
is registry-only and cannot be combined with `--to-layout`; local-layout
reference replacement is unchanged.

Push prints the artifact digest on success. Pull prints the resolved digest,
and a pull by tag also prints a reference such as
`registry.example.com/agentsuites/team@sha256:…` to pin subsequent pulls to that
artifact rather than a mutable tag.

### Runtime commands

| Command | Current behavior |
|---|---|
| `kmx quickstart` | kind + keyless Ollama + pinned Orka v0.2.0 Helm chart + the fixed `hello-world-agent` Orka bundle + a fresh Task with a readable answer; no Kagent installation or plane/governance enabled. [Getting started](getting-started.md#one-command-and-an-agent-that-answers) |
| `kmx quickstart --interactive` (`-i`) | Experimental TUI: author an Orka agent while kind, Ollama/model, and Orka start in the background; then validate, apply, and optionally run its first Task. Flags for the other quickstart mode are refused before configuration loads. The retired `kmx quickstart-wizard` spelling refuses and names this command. |
| `kmx local up` | the runtime and no agent: cluster, ollama, model, orka. `--step` selects exactly one of those four; the three legacy steps are removed and are refused as unknown |
| `kmx aks up` / `kmx aks down` | Temporary compatibility route, hidden from root help and shell completion; not a first-class KMX domain. Setup provisions AKS and installs Orka only, with optional Azure monitoring add-ons; the owner configures the model Provider. It does not deploy a model plane or capture its credentials. Teardown preserves cleanup of recorded owned resources, including historical runs. `--payload` defaults to `orka` and is the only payload (no Provider is created); the legacy payload is refused as retired, and an existing legacy lift can still be inspected and torn down. The deprecated `kmx lift` / `kmx lift down` still work; `kmx lift` still requires `--payload`. [AKS](aks.md) |
| `kmx agent list` | Orka Agents in one namespace: readiness, Provider and resolved model. `--namespace <ns>` selects it and defaults to `orka-system`; table/JSON/YAML |
| `kmx agent show <name>` | one Orka Agent and the chain it depends on: Provider readiness, the Secret the Provider names (**presence only — the value is never read**), the model actually resolved, the tools including disabled ones, and recent Tasks. Requires `--namespace`, because Orka watches namespaces explicitly. An unread hop is reported `unknown`, never as absent (`--namespace`, `--output table\|json`, `--tasks`) |
| `kmx agent chat <name> [message]` | interactive Orka session (`--runtime auto\|orka`, `--namespace`, default `orka-system`). Chat is always a session: an optional message is its first turn, and the one-shot Task is `kmx agent run`. The former `--interactive` flag is removed; Kagent chat is not restored by its create capability |
| `kmx status` | The same Orka runtime report as `kmx orka status`: running version, Deployments, CRDs and Provider readiness on the pinned context. `-o table` only |
| `kmx local down` | delete the named kind cluster and all its data, including any historical database still present |
| `kmx completion bash\|zsh\|fish` / `kmx version` | shell completion / binary, Orka pin and bundled model versions |

`quickstart` reuses an **exact** live match of the Provider and Agent it would
write, and nothing else: a differing spec is somebody's deliberate change, so
it refuses rather than overwrite it, and a half-finished run resumes. Every run
creates a **fresh** Task — reporting an existing completed Task's answer would
turn "the agent answered" into "the agent answered once, some time ago". The
result must be non-blank after sanitisation. These are not read-only
operations: other setup steps still reconcile. Old gateway tool references on
a cluster require an explicit owner decision, not a silent switch to direct
access.

## Settings

| Setting | Meaning / default |
|---|---|
| `--context`, `KUBE_CTX`, `kmx context use` | explicit invocation, environment or remembered target; kmx does not follow changing kubectl current-context |
| `KIND_CLUSTER` | kind container cluster name, default `kaimahi-p1`; pick your own for isolated work |
| `--container-engine`, `CONTAINER_ENGINE` | `docker` (default) or `podman`; the flag overrides the environment; keep consistent for every operation on a cluster |
| `MODEL` | model the runtime pulls and resolves, default `qwen2.5:3b` |
| `KAIMAHI_CONFIRM` | explicit named-target consent, not a universal yes |
| `KMX_HOME` | portable override for all kmx state/cache; otherwise the native user config directory (`~/.config/kmx` on Linux, `~/Library/Application Support/kmx` on macOS) |

Use `KMX_HOME` for isolated runs and tests on both Linux and macOS. KMX follows
Go's native user-config lookup when it is unset: `XDG_CONFIG_HOME` participates
in that lookup on Linux, but it is not the macOS override. Changing `KMX_HOME`
selects a separate set of saved records; it does not migrate files from the
native location. Point it only at a trusted private directory: KMX state can
include kubeconfig snapshots and infrastructure resource identifiers.

### Where the command will land

Mutations print context, who chose it, server and namespaces on stderr. Local
kind requires both a kind-shaped name and loopback server; remote mutations
require typed confirmation or `KAIMAHI_CONFIRM=<context>`. Non-interactive runs
without consent refuse. Read-only reports do not prompt.

The implicit `kind-kaimahi-p1` fallback refuses when kubeconfig contains other
possible targets and nobody chose this one. An empty machine, or corroboration
that the fallback is already current, permits first setup. Current-context is
never substituted as kmx's target. Kind creation/image loading also require
context `kind-$KIND_CLUSTER`; confirmation cannot override that mismatch.

`kmx local down` checks the container engine because kind deletes by container name,
not kubeconfig. A listed kind cluster missing from kubeconfig requires explicit
named confirmation; no matching container cluster is a no-op. An absent context
is not proof there is nothing to delete.

Lift has cloud-specific consent: creation/BYO lift confirms cluster; created
teardown confirms **resource group**, BYO teardown confirms **cluster**. Consent
precedes deletion, including recorded resources outside the group. BYO removes
recorded monitoring, not agents/plane; unknown ownership is left alone. Read
[AKS ownership and teardown](aks.md#teardown), not just the exit status.

## Output contracts

- On capable terminals, reports use rich headings and responsive tables. Wide
  tables become labelled records. Full identifiers/digests and exact numbers
  remain available. `TERM=dumb` chooses plain; non-empty `NO_COLOR` removes ANSI
  but keeps static rich report layout. Chat additionally disables cursor effects
  and enhanced input under `NO_COLOR`.
- Progress/diagnostics go to stderr. JSON/YAML, manifests, completion and
  answer-only Task output bypass styling.
- Chat is a session and has no one-shot form: a message after the Agent name
  is the first turn of that session, and there is no `--json` chat output.
  `--json` and `--session` are refused before configuration loads. Use
  `kmx agent run` for answer-only stdout.
- Quickstart JSON has keys `ok`, `context`, `cluster`, `agent`, `manifest`,
  `question`, `answer`, `governed`, `tools`, `elapsed_seconds`, `next`. `tools` is
  null when none were provisioned. `governed: false` means **this invocation did
  not enable governance**, not that existing governance is absent. Success
  requires a completed task with a readable answer; stdout is one JSON document.
- Root `status` has no `-o json|yaml`, and refuses them by name rather than
  printing an empty document. Read the cluster directly for machine-readable
  runtime facts; `agent status` retains its separate bundle-aware JSON report.

Completion (`source <(kmx completion bash)`, similarly zsh; fish uses
`kmx completion fish | source`) performs bounded read-only lookups for contexts
and agents, no guard/download/forward/mutation. Static completion works offline.

## `kmx agent create`

Orka remains the default. Omitting `--runtime`, or passing `--runtime orka`,
retains the existing bytes, calls, wizard, flags, and behavior. Kagent is
explicit-only: `kmx agent create --runtime kagent <name>`. Runtime selection is
independent of `--provider-type`, which selects the model provider used by that
runtime.

### Default Orka contract

Every default Orka bundle contains a new, same-name Provider and referencing
Agent in `core.orka.ai/v1alpha1`, a metadata-only Secret skeleton, and optionally
a fresh Task. It does not install Orka or adopt the installer's shared Provider.
Start with the [first-Task guide](orka.md#author-an-orka-agent-and-get-an-answer)
for a context-pinned local run, separately provisioned result account, and the
release/main authorization and connection limits.

For offline preview, without tools, kubeconfig reads or cluster calls:

```bash
kmx agent create preview --namespace orka-system \
  --provider-type openai --model qwen2.5:3b --secret local-provider-key \
  --base-url http://ollama.ollama.svc.cluster.local:11434/v1 --out -
```

Namespace, Provider type (`openai|anthropic|azure-openai`), actual model ID and existing Secret
name are explicit inputs; `--secret-key` defaults to `api-key`. These are names,
not credential values. For native Azure OpenAI, set `--azure-deployment` to the
same value as `--model` and `--base-url` to the HTTPS resource root (for example,
`https://example.openai.azure.com`, **not** `/openai/v1`). Orka v0.2.0 validates
`Provider.spec.azure.deploymentName`, but its request client addresses the
Azure deployment using the effective model name. kmx therefore requires them
to match when authoring; a Task-level model override can still target another
deployment. Use `--provider-type openai` instead for the v1-compatible
`/openai/v1` endpoint. `--azure-api-version` is optional; if omitted, kmx leaves
the field out, but the Orka CRD can default it on apply. We recommend setting
`--azure-api-version` explicitly for predictable runtime behavior.

The key file must contain **only the key, with no trailing newline**. Orka
does not trim it: the Provider can be Ready while every Task fails because the
key contains a line ending. In Bash or Zsh, this context-pinned command removes
LF and CRLF line endings without putting the key on the command line or printing it:

```bash
kubectl --context <ctx> -n <ns> create secret generic <name> \
  --from-file=api-key=<(tr -d '\r\n' < <path>)
kmx --context <ctx> agent create my-azure-agent --namespace <ns> \
  --provider-type azure-openai --model <deployment> --azure-deployment <deployment> \
  --azure-api-version <version> --base-url https://example.openai.azure.com \
  --secret <name>
```

The key stays in the file and the existing namespaced Secret; kmx accepts only
the Secret name/key reference, not key bytes or a key-file flag.
`--instructions` reads a system-prompt file; `--tools` and `--skills` name Orka
references on this default Orka path, not Kagent `server:tool` selections or translated
MCP wiring. `--coordination` requires at least one repeatable `--allowed-agent
<name>`; both go in the portable Orka extension. Edit the bundle for optional
delegation limits ([bundle format](agent-lift.md#coordination-in-agentyaml)). Use
`kmx agent create --help` for all flags and defaults.

- `--out -` prints rendered YAML only and implies offline; it writes no files
  unless `--bundle-path` explicitly names a bundle directory. `--no-apply`
  writes an exclusive local rendered artifact. Default artifact file:
  `agents/<name>.yaml`. An existing rendered artifact is never overwritten:
  an applying create keeps one that is byte-identical to what it renders and
  refuses, naming the file, one that differs; offline and `--dry-run` refuse any
  existing file. Input and final YAML reject known credential shapes.
- When the rendered artifact is written to a file (online creation, offline
  `--no-apply`, or online `--dry-run`), kmx also writes a Git-friendly agent
  bundle at `agents/<name>/` by default, or at `--bundle-path <directory>`.
  `agent.yaml` contains the portable behavior revision (name, description,
  instructions, model, Orka tools, skills and rate limits); `bindings.yaml`
  records **only the creation target** (namespace, Provider type and endpoint,
  Azure deployment/optional API version when applicable, Secret name/key reference,
  never a value). A future lift will obtain other
  targets' bindings from its flags and kmx's local state, not additional bundle
  files. A new bundle also gets one example evaluation case,
  `eval/example.yaml`, for [`kmx agent evaluate`](agent-lift.md#evaluating-a-deployed-revision);
  cases are not part of the portable digest, and a rerun accepts any `eval/`
  directory.
  The portable digest covers the **exact bytes** of `agent.yaml`: even changing
  a comment or whitespace creates a new revision by design. Target bindings
  do not change it; rendered YAML and its digest do reflect them. A rerun can
  reuse an existing byte-identical bundle; if either file differs, creation
  refuses and names the differing file. A failed deploy leaves the bundle and
  the rendered artifact for retry, and **rerunning the same command is safe**:
  both are kept when identical, and the cluster side reconciles as below.
  With `--task` the rendered Task name is random, so the old artifact can never
  match: a `--task` rerun refuses it and needs a new `--out` path.
- Offline defaults to `--schema-target v0.2.0`. Explicit
  `--schema-target v0.1.3|main` retains [pinned CRD fixtures](../internal/kmx/orkaschema/README.md)
  for legacy reads/authoring and the immutable old main snapshot, **not**
  install targets or a network fetch. Unknown fields refuse; v0.2.0 and the
  pinned old main snapshot lack Agent/Provider rate limits and refuse those
  flags rather than dropping fields. Select `v0.1.3` explicitly to inspect
  old manifests; that does not make a v0.1.3 runtime installable by kmx.
  Offline schema validation is not CEL/admission, readiness or execution proof.
- Online uses installed CRDs with **no fixture fallback**, checks Secret/key
  presence and ownership, and strictly server-dry-runs each custom resource.
  `--dry-run` writes the local artifact but no cluster resources, token or forward;
  it tests neither result access nor execution and cannot be combined with offline modes.
- **Never write the Secret skeleton or bulk-apply the bundle.** Provision the
  referenced key separately through your secret-management path. kmx never creates,
  replaces or merges that Secret. Online creation never uses apply:
  Provider → current-generation Ready → Agent → current-generation Ready → optional
  Task. Provider and Agent are reconciled as `kmx agent lift` does
  ([ownership](agent-lift.md)): created with the bundle's ownership markers,
  reused or updated (under a resourceVersion precondition) when they carry them,
  adopted when unmarked but identical, and refused otherwise — both are
  inspected before the first write. The optional Task is always newly created,
  never reused or resubmitted. For manual creation split out only those custom
  resources and preserve that order and the UID/generation readiness checks,
  using an explicit context and namespace. Failures leave partial state and
  nothing is rolled back; the error says so, and a rerun reuses what it wrote.
- `--task` authorizes a model call and requires an existing
  `--result-service-account` in the selected namespace; `agent create` creates no
  account or RBAC. (`kmx local up --step orka` provisions `orka-result-reader`, whose
  grant is one verb on `tasks.core.orka.ai` in `orka-system`.) It creates the
  Task once and waits for Succeeded plus an actual nonblank
  answer. **Without `--task`, no model response was tested.**
- kmx requests a ten-minute token; **the API server determines its actual TTL**.
  It carries the account's full effective authority, not result-only scope;
  discarding it is not revocation. The historical v0.1.3 release did not
  enforce Task-read RBAC; the pinned old main snapshot requires namespaced
  Task-get. Do not use that history to infer v0.2.0 enforcement; keep the
  namespaced grant. Result bytes are not bound to a UID.
  The context-pinned loopback HTTP forward uses one TCP connection and stops on
  connection/forward loss, never redialing or resubmitting. This trades reconnect
  availability for protection against later local-port reuse; initial connection
  trust is still local. See the [full limits](orka.md#author-an-orka-agent-and-get-an-answer).

No-name terminal use offers a wizard for missing required inputs and explicit
Apply/Cancel; Escape/Ctrl-C cancel without writing. `TERM=dumb` uses linear
prompts. Non-interactive use requires a name. No agent name is reserved: the
embedded examples that occupied `hello-world` and `hello-tools` are gone.

`--image`, `--isolation` and `--run-as-user` are removed. There is no BYO image
scaffold, model-preset/MCP conversion, injected governance or copied legacy pod
hardening. Keep application image/placement/identity in the owner's Deployment.
This native implementation does not settle the open authoring-format decision or
promote the isolated conversion spike to a supported interface.

### Explicit Kagent v0.10.2 create

This is a narrow lifecycle adapter for **render and create only**. It targets an
already-installed exact Kagent v0.10.2; KMX never installs, upgrades, repairs,
or otherwise manages Kagent. It does not change the default runtime or restore
Kagent chat, list, show, status, lift, evaluate, console, interactive `/lift`,
quickstart, `up`, installer, or AKS payload support. Old AKS lift records remain
teardown-only.

An offline review needs no cluster, kubeconfig read, or tool on `PATH`:

```bash
kmx agent create reviewer --runtime kagent --namespace agents \
  --description 'Reviews changes' --provider-type openai \
  --model gpt-4o-mini --secret reviewer-key \
  --instructions instructions.txt --out -
```

`--out -` implies `--no-apply` and writes the review artifact to stdout. Add
`--bundle-path agents/reviewer` to retain the portable bundle when using stdout;
otherwise file output and `--no-apply` use `agents/<name>.yaml` and
`agents/<name>/` by default. The artifact contains, in review order, a
metadata-only Secret skeleton, ModelConfig, and Agent, including the Agent's
reviewed system instructions. Never apply the Secret
skeleton or bulk-apply the artifact. The bundle contains a strict portable
`agent.yaml` plus creation-target-only `bindings.yaml`; it does not scaffold
evaluation cases.

Supported Kagent inputs are:

- `--namespace`, required `--description`, `--provider-type openai|anthropic`,
  `--model`, `--secret`, optional `--secret-key` (default `api-key`), optional
  credential-free `--base-url`, and optional `--instructions` file;
- `--tools server:tool1,tool2` for at most one same-namespace
  `RemoteMCPServer` binding with an explicit nonempty tool allowlist;
- `--kagent-runtime go|python` (default `go`) for the declarative Agent runtime;
- optional applying `--task`, plus `--out`, `--bundle-path`, `--no-apply`, and
  `--dry-run`.

Kagent create refuses unsupported/Orka-only `--skills`,
`--agent-requests-per-minute`, `--agent-tokens-per-minute`,
`--provider-requests-per-minute`, `--provider-tokens-per-minute`,
`--schema-target`, `--result-service-account`, `--orka-api-service`, and
`--result-port`. `--task` is also refused with `--no-apply`, `--out -`, or
`--dry-run`.

Online create is deliberately create-only. Before the first cluster write, KMX
requires exactly one unambiguous labeled controller Service exposing named
`controller` TCP port 8083 and proves the
controller's exact v0.10.2 version and a release-reported Git prefix of pinned
commit `68df64f671800c37c4204d81ebe0dd66ec35d223`, published controller image
tag/digest identity, current rollout, image ConfigMap, selected Go/Python runtime
image configuration, exact Agent/ModelConfig CRD schemas, that `WATCH_NAMESPACES`
includes the target (or is empty for all namespaces), and the controller
ServiceAccount's required target-namespace RBAC. It also proves the separately
provisioned Secret/key and captures any configured RemoteMCPServer identity,
spec/Secret hash, and discovered tool set. It refuses existing same-name
ModelConfig or Agent objects and the same-name Secret, ServiceAccount,
Deployment, or Service children the controller would overwrite. The model
Secret must not share the Agent name. Both strict server-dry-run responses must
retain the exact reviewed spec and KMX markers, allowing only OpenAI's pinned
`apiFormat: chatCompletions` default. `--dry-run` stops after those online checks
and writes no cluster resource or message.

After those preflights KMX writes the review artifact, creates ModelConfig, and
waits for current-generation `Accepted`; then it rechecks child collisions and
captured dependencies, creates Agent, and waits for current-generation
`Accepted` and `Ready`. Every create response and later live read must retain
the admitted spec and KMX markers. Success also proves the Agent-owned
Deployment and Service, the complete current rollout, and the selected official
v0.10.2 runtime image tag/digest identity. Pre-create cluster checks have a
five-minute budget; each created resource then receives a fresh ten-minute
readiness budget. Before an optional task it additionally proves
the controller-proxied A2A card. KMX never adopts, updates, reconciles, or
deletes an existing Kagent object.

There is no transaction or rollback. A create failure stops later resources and
messages but leaves anything already created. Rerunning after a partial failure
will refuse the existing ModelConfig or Agent, so an operator must inspect the
named resources and deliberately clean up only what should be recreated.
Ambiguous creates are never retried, adopted, or deleted.

`--task` sends one A2A `message/send` after all readiness, workload, image, and
agent-card proof. Agent-card discovery and the send each receive a fresh
five-minute budget. It never retries an ambiguous send. KMX accepts only a strict
matching JSON-RPC response containing an `agent` text message or a completed
task whose answer message has a nonblank ID and the task's exact context ID,
strips unsafe terminal
controls, and prints the sanitized answer. KMX rechecks the Agent UID/generation
and its owned Deployment/Service before and after the send, but A2A itself does
not cryptographically bind returned bytes to that Kubernetes UID. KMX feeds the task prompt to kubectl
on stdin, never argv; it is not written to the artifact, bundle, or receipt.

Kagent persists the full prompt, history, and answer in its database. Upstream
default session retention is unlimited, and Kubernetes audit policy may capture
Service-proxy request and response bodies. A controller configured with
`AUTH_MODE=trusted-proxy` may still be used for creation without `--task`, but
the task path is refused before writes because KMX accepts no Kagent bearer
credential.

After every successful online create, KMX writes a mode-0600
`KagentCreateReceipt` under the bundle's `receipts/` directory. It records the
cluster and resource identities and portable/rendered digests, never prompt or
answer text or system instructions. This receipt does not make the bundle eligible for other lifecycle
commands: `kmx agent lift`, `status`, `evaluate`, console bundle operations, and
interactive `/lift` are currently Orka-only and intentionally refuse Kagent
bundles before target work.

### Editing an agent

`kmx agent edit` is retired; its hidden stub points to kubectl.
`kmx agent create` writes reviewable YAML you own, and a live Orka Agent
is edited as the Kubernetes resource it is:

```bash
kubectl --context <ctx> -n <namespace> edit agents.core.orka.ai <name>
```

`kmx agent show <name> --namespace <ns>` reads it back with the chain it
depends on.

## Interactive chat

```bash
kmx agent chat --namespace orka-system hello-world-agent
```

`/help`, `/agent`, `/tools`, `/lift`, `/inference`, `/inference-local`,
`/inference-copilot`, `/inference-foundry`, `/retry`, `/verbose-on`,
`/verbose-off`, `/exit` are local controls. Each Orka turn is a fresh Task, so
there is no resumable server-side session and no `--session`. Scanner input
remains supported when raw mode is unavailable; terminal slash completion is
local. Malformed history can be skipped and ordinary
verbose payloads are display-limited; this is not an audit export.

Trusted actor/action labels and indented payloads prevent tool/model prose from
impersonating controls. `[KAIMAHI ROUTE]` shows verified startup configuration,
not a per-call enforcement receipt. Model prose is not trusted control or
authorization evidence. Tool activity records remain visible with `/tools off`.

Orka chat offers no native approval submission. Tools requiring approval are
unavailable in this client; inspect and configure them through the runtime's
supported operator path instead. A disconnect does not resume or reinvoke a
working Task. Enhanced-input resize stops chat without submitting the current
message and restores terminal state; restart chat to continue. This is a safe
abort, not live reflow or undo of earlier actions. Renderers keep durable
response text and stop uncertain animation after resize.

### Retry limits

There is no one-shot Kagent **chat** transport: the former chat invoke, its
connection-refused/EOF/reset retries and resumable `--session` remain removed.
The separate create-only `--runtime kagent --task` path sends once and never
retries an ambiguous result. Interactive Orka `/retry` explicitly resends; that
does not promise exactly-once execution.

`kmx quickstart` is Orka and does not participate in that policy. It creates one
Task, polls that Task's result over a single context-pinned connection, and never
resubmits: connection or forward loss ends the wait rather than repeating the
model call. A missing or whitespace-only result remains unavailable and is
polled on that same Task; once a nonblank result arrives, output with no printable
content is refused. Neither case resubmits the Task.
