# `kmx` — agent tooling with Orka as the default

[Orka](orka.md) remains the first-class platform and the default for every
existing workflow. Kaimahi's front door is `kmx orka` for installation/status
and root `kmx create` for offline workspace authoring. Explicit
`kmx create --live` retains native Provider + Agent and PortableAgent bundle
creation. The selected runtime owns execution, enforcement and model access;
installing it alone does
not prove an Agent can answer.

The former broad kagent integration is not restored. Its installer steps,
fixed manifests, chat, list/show, console, quickstart, `local up` and
AKS payload remain absent or retired. Explicit create targets preinstalled
exact v0.10.2; explicit declarative bundle lift/status and receipt-bound Go
bundle evaluation target preinstalled exact v0.10.3. Evaluation is unsecure-only;
trusted-proxy is refused. Python execution, promotion gates and standalone
chat/run remain unsupported. [Version-qualified placement](agent-lift.md#exact-v0103-declarative-bundle-placement)
does not adopt or update resources. Historical AKS lift records remain
readable for teardown only.
Plane administration, model overlays, migration, governance and preset
switching are removed, not redirected to replacement commands. The entire
`kmx agent` group, including its edit stub, is removed without aliases. Edit
live Orka resources with kubectl. `orka.harness.v2` is outside
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
v0.5.1 release with Go 1.26+ instead:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.5.1
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
`--version=v0.5.1` pins it, `--bin-dir=DIR` changes the destination, and
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

**Released in v0.5.0:** `context show/use` and `local up/down` are the canonical
grouped commands below. Historically, v0.4.1 used `ctx [context]` and root
`up/down`. Current development builds remove root `ctx`, `up`, `down`, `lift`
and `lift down` outright, without retirement stubs. Use `context show/use`,
`local up/down` and `aks up/down` for those operations.
Everyday operations are now root `create/chat/run`, live Orka inspection is
`orka show/list`, and legacy lifecycle operations are under `bundle`.
These routes and root workspace `push` require a current development build;
tagged v0.5.1 still uses the `agent` routes. Workspace authoring/publication
are unreleased.
Root `eval` evaluates a full native Orka workspace and retains offline `diff` and
`from-sessions` helpers. Standalone packaged evaluation and root digest `lift`
are not implemented. See the
[alignment record](command-semantics.md) for current versus proposed names.

### Current Orka path and explicit version-qualified operations

| Command | Contract / reference |
|---|---|
| `kmx targets [-o table\|json] [--detect] [--sessions <host:port>]` | offline compiled support by default; explicit read-only probes report present/absent/unreadable separately from qualified versions. No runtime selection, installation or deployment. [Target view and support matrix](runtime-adapters.md#read-only-target-view) |
| `kmx orka install` | verify the pinned v0.2.0 release chart; apply its CRDs; install harness-v2 with fullname `orka-api` on the selected context; keep the chart-generated snapshot key private; optionally create a keyless Provider. Refuses old v0.1.3 installs rather than upgrading. [Orka](orka.md) |
| `kmx orka status` | same table-only Orka runtime report as root `status`: running controller version, Deployments, CRDs and Providers; distinguish unreadable from absent and running version from pin. `-o table` only |
| `kmx create <name> --model <model> --instructions <file> [--workspace <directory>]` | default offline authoring of one tool-free native suite member: Orka by default, or exact kagent v0.10.3 Go/linux-amd64 with `--runtime kagent`. Private metadata only; no suite kagent deployment/evaluation. Discover the nearest ancestor workspace, or select/initialize an exact root with `--workspace`. Optional `--description`; live-only flags and a second member refuse. No cluster configuration or image construction. [Workspace guide](suite-workspace.md) |
| `kmx create --live [name]` | unchanged live Orka resource/bundle creation, or its no-name terminal wizard. Explicit `--live --runtime kagent <name>` is create-only for preinstalled exact Kagent v0.10.2. `--live` is also required for `--no-apply`, `--dry-run` and `--out -` previews. Retrieve a real answer only with `--task`. [Live contract](#default-orka-contract) |
| `kmx push [--workspace <directory>] <reference>` | discover the nearest ancestor `workspace.yaml`, or select an exact root without fallback with `--workspace`; prepare its validated one-member native suite and authored cases, then copy its exact OCI graph to a registry or `--to-layout` target. Print the absolute workspace and OCI manifest digest on stderr. Required member images are empty. Does not evaluate or deploy. Existing registry policy applies. [Workspace guide](suite-workspace.md) |
| `kmx eval [--workspace <directory>] --to-context <ctx> --inference provider:NAME --out <private.json>` | evaluate every ordered native Orka case through one fresh Task per case (kagent suite evaluation is unavailable), without retry or answer/operand output; defaults: namespace `orka-system`, case timeout 5m (max 9m), loopback result port `19180`. Requires preinstalled runtime/inference/result access. Offline unsupported/budget/output checks precede configuration; one guard covers owned pair placement and execution. Only full passing metadata is exported once, privately outside workspace/cache. Hermetic tests, not real native CLI qualification. [Workspace evaluation](suite-workspace.md#evaluate-the-whole-workspace) |
| `kmx push --evaluation <private.json> [--workspace <directory>] <reference>` | consume a strict private full-passing Orka receipt (kagent suite evaluation is unavailable) and require exact suite manifest identity plus complete offline coherence before transfer/provenance. Ordinary push remains unevaluated; unsigned observations are not an authentication proof or policy gate. [Workspace guide](suite-workspace.md) |
| `kmx suite build <directory> ...` | resolve one agent/platform composition and build an OCI image-layout tar through AgentKit using Docker buildx. The current implementation is explicitly experimental and non-conformant because it treats the selected harness image as a monolithic AgentKit adapter and does not compose the runtime-base image. |
| `kmx suite push <directory> <registry/repository:tag>` | deterministically archive and validate an extracted AgentSuite, then push it to an OCI registry. An existing tag with the same digest is a no-op; a different digest requires `--force`. It also attaches unsigned [suite provenance](#suite-provenance) before moving the tag; `--provenance=false` skips it and `--require-provenance` fails without moving the tag if it cannot be attached. Prints the pushed and provenance digests. See [registry safety](#registry-authentication). |
| `kmx suite push <directory> --to-layout <layout> <repository:tag>` | deterministically archive and validate an extracted AgentSuite, then add its referenced artifact to a local OCI image layout. New layouts are published atomically; existing layouts retain unrelated references. Output states whether the layout was created or updated. No provenance is attached, since a conformant AgentSuite layout holds exactly one manifest. |
| `kmx suite pull <registry/repository:tag-or-digest> --output <directory>` | pull and validate one AgentSuite from an OCI registry, then atomically extract it into a new directory. Prints the resolved digest and any [suite provenance](#suite-provenance) that names it, for information only; tag pulls also print a digest-pinned reference for repeatable pulls. See [registry safety](#registry-authentication). |
| `kmx suite pull <repository:tag> --from-layout <layout> --output <directory>` | resolve and validate one AgentSuite reference from a local OCI image layout, then atomically extract its content into a new directory. Existing output is never replaced. |
| `kmx suite validate <path>` | validate an extracted AgentSuite content directory or OCI image layout offline, including strict JSON, canonical identities, closed references, tool locks, build profiles and archive safety. `-o json` emits the validated summary. [AgentSuite specification](../agentsuite/spec.md) |
| `kmx suite diff <before> <after>` | validate and semantically compare two extracted AgentSuites or local OCI image layouts, including supplemental packaged content and artifact identities. The default unified view uses `-` and `+` field lines; `--view split` renders before and after values side by side, and `-o json` emits the versioned change and affected-agent report. [Suite artifact diff](#suite-artifact-diff). |
| `kmx bundle lift <bundle-dir>` | default Orka reconciliation; explicit `--runtime kagent --runtime-version v0.10.3` is new-object declarative placement or complete receipt-bound reuse. No adoption/update in that profile. `--plan` checks without writing. [Bundle lift](agent-lift.md) |
| `kmx bundle retire <bundle-dir>` | delete or release bundle-owned Orka resources after UID, receipt and dependent checks; Kagent bundles are refused before target reads. `--plan` checks without writing. [Bundle retirement](agent-lift.md#retiring-a-bundle-from-a-target) |
| `kmx bundle status <bundle-dir>` | default Orka revision/readiness/drift/evaluation report; explicit `--runtime kagent --runtime-version v0.10.3` reports absent, unreadable, failed readiness, ready or drift against desired bindings and saved identity. `--to-context` selects the target, `-o json` emits facts. [Bundle status](agent-lift.md#checking-deployed-status) |
| `kmx bundle evaluate <bundle-dir>` | run a bundle's `eval/*.yaml` cases against the deployed revision. Orka remains default; explicit `--runtime kagent --runtime-version v0.10.3 --to-context <ctx>` evaluates a receipt-bound Go placement with desired `--bindings`, one fresh blocking A2A Task per case, no retries or promotion gates. auth: unsecure only, trusted-proxy refused; Python is refused. Print answers, evaluate legacy/typed assertions, write private v2 latest and immutable run receipts with digests (never payloads), and exit non-zero unless every case passed. `--sessions <host:port>` selects tool-disabled chat without Kubernetes; all three paths report tool assertions unknown. `llmJudge` explicitly authors rubric/threshold/model/version and uses `--judge-base-url` plus optional `--judge-key-env` (name only), with no defaults or agent-model fallback. Two temperature-zero scores must agree on the threshold side; split/malformed/unavailable is unknown. Input/answer/rubric go to the judge provider; safe terminal reasoning may contain answer text but is not stored in receipts. [Bundle evaluation](agent-lift.md#evaluating-a-deployed-revision) |
| `kmx eval from-sessions <session-uid>... --sessions <source> --assertions <case.yaml> --out <manifest.json>` | bounded, read-only extraction of private single-turn UID/prefix references and authored assertions, without recorded inputs/answers. Existing `--out` appends atomically; duplicates refuse the whole add. [Recorded cases](agent-lift.md#recorded-sessions-as-reference-cases) |
| `kmx bundle evaluate <bundle-dir> --from-sessions <manifest.json> --source-sessions <source> --sessions <candidate>` | resolve recorded inputs in memory, then execute fresh sessions using existing assertion receipts/diff. Requires `KAIMAHI_CONFIRM=<candidate>` even on loopback and separate `KAIMAHI_CONFIRM_JUDGE=<canonical-url>` when judging. Verdicts/IDs/digests only by default; `--show-content` opts into answers and reasoning. Missing/expired sources are unknown, never skipped; cases missing runtime identity get their own unknown diff rows while comparable cases still compare. [Recorded cases](agent-lift.md#recorded-sessions-as-reference-cases) |
| `kmx eval diff <before.json> <after.json>` | offline comparison of explicit v2 full-run receipts: align inputs and assertion IDs/definition digests under compatible runtime identities, allowing different portable revisions. Judge model/version, policy/configuration digests and threshold must match before transitions; comparable rows report both scores. Report changed sets, regressions/fixes and evidence gains/losses without payloads. Missing Sessions case identity is row-local unknown; observed identities must agree and each receipt needs a complete observed case identity. Incompatible, changed/incomplete, regression or lost/unknown evidence exits nonzero. Zero is not an evaluation pass. [Receipt comparison](agent-lift.md#comparing-saved-evaluations-offline) |
| `kmx bundle verify <sessions-receipt.json> --sessions <host:port>` | zero-call local reference reconstruction of agent journal/answer evidence, never judge verdict verification. Valid agent evidence may replay despite unknown judge outcomes; reports count `judgeAssertionsNotVerified` and print a notice when judges exist. No lift gate or host attestation. [Replay verification](agent-lift.md#verifying-a-sessions-receipt) |
| `kmx run <bundle-dir> --prompt <text>` / `kmx run --agent <name> --prompt-file <path>` | execute one Task against an already-deployed Agent without changing the bundle; stdout is answer-only. Supports `--prompt-file -` for stdin and `--wait` (default 5m, maximum 9m). [Running an existing Agent](agent-lift.md#running-an-existing-agent) |
| `kmx task result <task> [--context <ctx>] [--namespace <ns>] [--wait 5m]` | inspect an AI Task's phase once by default and open a result session only for a readable terminal answer; `--wait <duration>` waits 10s–9m and reports phase changes. Pending or not-yet-readable results exit 2; failed or cancelled Tasks exit 1. [Running an existing Agent](agent-lift.md#running-an-existing-agent) |
| `kmx context show` / `kmx context use <context>` | read effective context/source/posture or remember KMX's guarded access selection; never change kubectl current-context |
| `kmx console` | two-column local/remote workspace for native Orka Agents, with Vim/arrow navigation, agent actions, inference details and slash-command completion; Kagent inventory and bundles are unsupported. `b` compares the selected Orka agent with its local bundle using the same report as `kmx bundle status`; `--demo` uses sample data. [Console guide](interactive-agent-tui-plan.md) |

#### Experimental AgentSuite image build

The first image-build backend is intentionally narrow:

It requires Docker with the buildx plugin. Authenticate Docker to any private
registry containing the selected harness image before building.

A downloaded kmx binary does not include the example files. With Git installed,
get the tool-free incident-analyst fixture from the released source into a new
`kmx-examples` directory:

```sh
git clone --depth 1 --branch v0.5.1 https://github.com/kaimahi-agents/kaimahi.git kmx-examples
SUITE="$PWD/kmx-examples/internal/kmx/agentsuite/testdata/incident-analyst"
```

The checkout supplies the fixture only; keep using your installed kmx binary.
No Go build is needed. Run the remaining commands in the same shell and working
directory so `SUITE` still selects these files.

With the classic Docker image store, the default `docker` builder can reject
`--output type=oci` with `OCI exporter is not supported for the docker driver.`,
even with attestations disabled. The example below explicitly uses a named
`docker-container` builder. Create it before running that build:

```sh
docker buildx create --name kmx-builder --driver docker-container
docker buildx inspect kmx-builder
```

If `kmx-builder` already exists, inspect it and confirm its driver is
`docker-container` instead of recreating it. To use another builder, replace
`kmx-builder` in both the setup and the build's `--builder` flag.

`--builder` selects an existing Buildx builder for this build without changing
Docker's default builder. When omitted, Docker Buildx uses its existing builder
selection. Disabling attestations does not fix an unsupported OCI exporter.
A `docker-container` builder can export an OCI archive on a classic-store host,
but it does not change that host's image store. The load/run example below
requires Docker's containerd image store.

```zsh
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

Before loading the archive, check the selected Docker daemon's image store:

```sh
docker info -f '{{ .DriverStatus }}'
```

The output must include `driver-type io.containerd.snapshotter.v1`. Docker's
classic image store cannot load this attested OCI archive; `docker load` can
fail with `invalid archive: does not contain a manifest.json`. An alternate
buildx builder fixes export, not import. Do not disable attestations to work
around the image-store requirement.

If the daemon still uses the classic store, stop before `docker load`. Follow
Docker's [Engine](https://docs.docker.com/engine/storage/containerd/) or
[Desktop](https://docs.docker.com/desktop/features/containerd/) guidance to
choose the containerd image store. Switching stores hides the previous store's
images and containers until you switch back; review that impact before changing
a shared daemon. kmx does not change the daemon's settings.

With the containerd image store selected, create an Azure OpenAI deployment
named `gpt-5-mini` in Azure AI Foundry. Then load the image and inject its API
key only when the container starts:

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
created; re-pushing the same digest succeeds without writing, except that
[suite provenance](#suite-provenance) is attached if pull cannot see any; replacing a
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

#### Suite artifact diff

`kmx suite diff <before> <after>` validates and compares two extracted content
directories or local OCI layouts without registry or cluster access. Logical
records are matched by AgentSuite identity. Identity-keyed collections such as
agent invocation edges and ToolProvider selections are compared as sets;
behaviorally ordered values such as process arguments remain positional.
Added or removed identity-keyed elements are reported as one field containing
the complete element, and added or removed supplemental content records use one
aggregate `$` field, so large valid additions do not exhaust field limits by
expanding every leaf.
Supplemental packaged files, including `metadata/kmx/`, are compared by path,
mode, size and digest. A raw packaged-content identity change is still reported
when all modeled records remain semantically equal.

The report identifies each side by suite name, source type and a
representation-neutral content digest. OCI-layout inputs also include their
authoritative manifest digest. The content digest is SHA-256 over the sorted inventory of non-directory path,
filesystem-equivalent type, mode, size, file digest and symbolic-link target.
Archive hardlinks inherit their target file metadata and explicit directory
headers are excluded, so equivalent extracted and OCI-layout content has the
same identity. A standalone
ToolProvider composition and its build profile remain record changes but do not
affect an agent without an explicit AgentSuite relationship.

An OCI manifest-digest change is reported even when both manifests reference
identical packaged content. Differences are a successful comparison and exit
with status 0; validation, loading, usage and output failures return a nonzero
status.

`-o json` emits the pinned `kmx.agentsuite.diff/v1` envelope. `changes` and
`affectedAgents` are always arrays, including for an unchanged comparison.
The report refuses more than 10,000 changed fields in one record, 20,000
changed fields in total, 10,000 changed records, or an indented JSON
representation larger than 8 MiB; unchanged fields do not consume these
limits, and automation output is never silently truncated.

Human output strips terminal controls and Unicode format/bidirectional controls,
and renders artifact-derived labels on one line.
With a measured terminal width, split output constrains every report line and
falls back to unified rows when two useful columns do not fit. When output width
is unknown, split rows remain unwrapped and unpadded rather than assuming a
terminal size.

#### Suite provenance

A registry `kmx suite push` attaches an unsigned
[in-toto](https://github.com/in-toto/attestation) statement with an
[SLSA provenance v1](https://slsa.dev/spec/v1.0/provenance) predicate as an OCI
referrer of the suite manifest (`artifactType: application/vnd.in-toto+json`,
`subject`: the suite manifest). It is attached before the tag moves. This
section documents its build type, which the statement names as
`https://github.com/kaimahi-agents/kaimahi/blob/main/docs/kmx.md#suite-provenance`:

| Field | Value |
|---|---|
| `subject` | the suite name and its OCI manifest digest |
| `runDetails.builder.id` | `https://github.com/kaimahi-agents/kaimahi/cmd/kmx`; `version.kmx` is the kmx version when known |
| `resolvedDependencies` | one `gitCommit`, with a `git+https://` URI when `origin` is a credential-free HTTPS or `git@host:` remote; absent otherwise |
| `externalParameters.sourcePath` | the extracted suite or authored workspace directory relative to the repository root, when a commit is recorded |

For extracted directories, kmx records the commit only when every file it packs matches `HEAD` byte for
byte, with the same executable bit, and nothing else is in the directory:
untracked, ignored, edited, deleted and skip-worktree files, empty
directories, and `eol`, `filter`, `ident` or `working-tree-encoding` Git
attributes all prevent it, and push prints why. Packing is deterministic, so
anyone can check the claim: check out the commit with
`-c core.autocrlf=false`, run `kmx suite push` of that path to a local layout
with the recorded kmx version, and compare the suite digest. The origin URL
and path are published with the suite; use `--provenance=false` if they are
private.

Registry root `push --workspace` uses the same unsigned provenance policy and
flags. Its commit check compares the fingerprint-bound raw authored inputs with
HEAD, including executable bits, rewriting attributes and the complete case and
one-member inventory. Unrelated files and Git internals are not selected inputs.
Generated cache content is not the Git source. Workspace commit detection requires
Git 2.40+ for pinned-HEAD attribute checks; older Git safely omits the commit with
an explanatory warning. To reproduce a workspace claim,
check out the recorded commit with `-c core.autocrlf=false`, then run
`kmx push --workspace SOURCE_PATH --to-layout NEW_LAYOUT v1` with the recorded
kmx version and compare the suite digest. Dirty/non-Git source still gets a
source-less claim; `--require-provenance` requires attachment, not a source commit.

The statement has no timestamps, so the same suite, source and kmx version
always produce the same referrer. It is a claim, **not a signature**: anyone
with push access to the repository can attach one, and `kmx suite pull` shows
it for information only. Sign the suite digest where you push, for example
with keyless signing in CI. An attach failure is a warning unless
`--require-provenance` is set, in which case the push fails and the tag does
not move. Rerunning a push whose tag already names the digest writes nothing
if pull can already see kmx provenance for it, and otherwise attaches it. Registries without the OCI referrers API also receive a
`sha256-<digest>` index tag, which kmx updates but never deletes. Local OCI
layouts get none, because a conformant AgentSuite layout holds exactly one
manifest.

Pull checks at most 64 in-toto referrers from at most four API pages. Referrer
indexes, manifests and statements are limited to 1 MiB each. It checks each
referrer that uses this build type: its subject and statement must name the pulled digest, its builder
must be kmx, and every value it prints must be printable text without control
or format characters. Malformed
ones are warnings; other producers' attestations are skipped. A listing failure
prints `Provenance: not checked` and does not block the pull. The 64-referrer
limit applies to pull verification, not index updates: push preserves other
producers' descriptors in a bounded fallback index. An attached claim beyond
pull's limit may not be displayed, even with `--require-provenance`; that flag
requires attachment, not visibility or trust.

### Runtime commands

| Command | Current behavior |
|---|---|
| `kmx quickstart` | kind + keyless Ollama + pinned Orka v0.2.0 Helm chart + the fixed `hello-world-agent` Orka bundle + a fresh Task with a readable answer; no Kagent installation or plane/governance enabled. [Getting started](getting-started.md#one-command-and-an-agent-that-answers) |
| `kmx quickstart --interactive` (`-i`) | Experimental TUI: author an Orka agent while kind, Ollama/model, and Orka start in the background; then validate, apply, and optionally run its first Task. Flags for the other quickstart mode are refused before configuration loads. The retired `kmx quickstart-wizard` spelling refuses and names this command. |
| `kmx local up` | the runtime and no agent: cluster, ollama, model, orka. `--step` selects exactly one of those four; the three legacy steps are removed and are refused as unknown |
| `kmx aks up` / `kmx aks down` | Temporary compatibility route, hidden from root help and shell completion; not a first-class KMX domain. Setup provisions AKS and installs Orka only, with optional Azure monitoring add-ons; the owner configures the model Provider. It does not deploy a model plane or capture its credentials. Teardown preserves cleanup of recorded owned resources, including historical runs. `--payload` defaults to `orka` and is the only payload (no Provider is created); the legacy payload is refused as retired, and an existing legacy lift can still be inspected and torn down. The root AKS aliases are removed; use `kmx aks up` / `kmx aks down`. [AKS](aks.md) |
| `kmx orka list` | live Orka Agent inventory in one namespace; table/JSON/YAML. [Inventory contract](#kmx-orka-list) |
| `kmx orka show <name>` | live Orka Agent detail and dependencies; requires `--namespace`, supports `--output table\|json` and `--tasks`. [Detail contract](#kmx-orka-show) |
| `kmx chat <name> [message]` | interactive Orka session (`--runtime auto\|orka`, `--namespace`, default `orka-system`). Chat is always a session: an optional message is its first turn, and the one-shot Task is `kmx run`. The former `--interactive` flag is removed; Kagent chat is not restored by its create capability |
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

### `kmx orka list`

Lists live Orka Agents in one namespace, including readiness, Provider and
resolved model. `--namespace <ns>` defaults to `orka-system`; output is table,
JSON or YAML. This is runtime inventory, not workspace listing.

### `kmx orka show`

Reads one live Orka Agent and its dependency chain: Provider readiness, the
Secret it names (**presence only — the value is never read**), resolved model,
Tools including disabled ones, and recent Tasks. `--namespace` is required
because Orka watches namespaces explicitly. An unread hop is `unknown`, never
absent. Root `show/list` are not implemented.

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
  `kmx run` for answer-only stdout.
- Quickstart JSON has keys `ok`, `context`, `cluster`, `agent`, `manifest`,
  `question`, `answer`, `governed`, `tools`, `elapsed_seconds`, `next`. `tools` is
  null when none were provisioned. `governed: false` means **this invocation did
  not enable governance**, not that existing governance is absent. Success
  requires a completed task with a readable answer; stdout is one JSON document.
- Root `status` and `orka status` refuse `-o json|yaml` by name rather than
  printing an empty document. Read the cluster directly for machine-readable
  runtime facts; `bundle status <bundle-dir>` retains its separate bundle-aware
  JSON report. Root status is not a bundle or suite observer.

Completion (`source <(kmx completion bash)`, similarly zsh; fish uses
`kmx completion fish | source`) performs bounded read-only lookups for contexts
and agents, no guard/download/forward/mutation. Static completion works offline.

## `kmx create`

Default `kmx create <name> --model <model> --instructions <file>` authors one
tool-free native-Orka workspace member offline. Model and file-valued
instructions are required; description is optional. Create and root `push`
share nearest-ancestor `workspace.yaml` discovery. `--workspace DIR` selects an
exact root without fallback and initializes a missing workspace there without
prompting. Without a workspace, terminal create offers `Start one here? [Y/n]`;
nonterminal or redirected use refuses with a `--workspace` hint. Invalid nearest
markers and a second member fail closed without changing source. A name is
required; no offline authoring wizard is provided. See the
[workspace guide](suite-workspace.md) for the full source and prompt contract.

`--live` explicitly selects the separate resource/PortableAgent bundle path,
including `--no-apply`, `--dry-run` and `--out -` previews. Orka remains its
default runtime; kagent requires `kmx create --live --runtime kagent <name>`.
Runtime selection is independent of `--provider-type`, which selects model
access. Live-only flags without `--live` refuse before configuration or writes;
`--runtime kagent` alone does not dispatch to live creation. `--live` conflicts
with `--workspace` and bypasses discovery even below an invalid marker.
Quickstart and console creation remain live. Help and completion never discover,
prompt, mutate or load operational configuration.

### Default Orka contract

With `--live`, every default Orka bundle contains a new, same-name Provider and
referencing Agent in `core.orka.ai/v1alpha1`, a metadata-only Secret skeleton, and optionally
a fresh Task. It does not install Orka or adopt the installer's shared Provider.
Start with the [first-Task guide](orka.md#author-an-orka-agent-and-get-an-answer)
for a context-pinned local run, separately provisioned result account, and the
release/main authorization and connection limits.

For offline preview, without tools, kubeconfig reads or cluster calls:

```bash
kmx create --live preview --namespace orka-system \
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
kmx --context <ctx> create --live my-azure-agent --namespace <ns> \
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
`kmx create --help` for all flags and defaults.

- `--out -` prints rendered YAML only and implies offline; it writes no files
  unless `--bundle-path` explicitly names a bundle directory. `--no-apply`
  writes an exclusive local rendered artifact. Default artifact file:
  `agents/<name>.yaml`. An existing rendered artifact is never overwritten:
  an applying create without `--task` keeps one matching every byte except the
  generated schema-source comment block. The preview's original attribution
  remains unchanged; online creation still validates against installed CRDs and
  strict server admission, and reports that distinction on stderr. Any other
  byte difference, including another comment or whitespace, is refused by name.
  The comparison is repeated before deployment but does not lock out subsequent
  concurrent edits. Offline and `--dry-run` refuse any existing file. Input and
  final YAML reject known credential shapes.
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
  `eval/example.yaml`, for [`kmx bundle evaluate`](agent-lift.md#evaluating-a-deployed-revision);
  cases are not part of the portable digest, and a rerun accepts any `eval/`
  directory.
  The portable digest covers the **exact bytes** of `agent.yaml`: even changing
  a comment or whitespace creates a new revision by design. Target bindings
  do not change it; rendered YAML and its digest do reflect them. A rerun can
  reuse an existing byte-identical bundle; if either file differs, creation
  refuses and names the differing file. A failed deploy leaves the bundle and
  the rendered artifact for retry, and **rerunning the same command is safe**:
  bundle files must remain byte-identical; the rendered artifact may differ only
  in its generated schema-source comment block. The cluster side reconciles as
  below.
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
  Task. Provider and Agent are reconciled as `kmx bundle lift` does
  ([ownership](agent-lift.md)): created with the bundle's ownership markers,
  reused or updated (under a resourceVersion precondition) when they carry them,
  adopted when unmarked but identical, and refused otherwise — both are
  inspected before the first write. The optional Task is always newly created,
  never reused or resubmitted. For manual creation split out only those custom
  resources and preserve that order and the UID/generation readiness checks,
  using an explicit context and namespace. Failures leave partial state and
  nothing is rolled back; the error says so, and a rerun reuses what it wrote.
- `--task` authorizes a model call and requires an existing
  `--result-service-account` in the selected namespace; `create` creates no
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

No-name `kmx create --live` terminal use offers a wizard for missing required
inputs and explicit Apply/Cancel; Escape/Ctrl-C cancel without writing. `TERM=dumb` uses linear
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
or otherwise manages kagent infrastructure. This profile does not change the
default runtime or enable standalone chat, list, show, evaluation, console,
interactive `/lift`, quickstart, `local up`, installer or AKS payload support.
Bundle lift/status requires the separate explicit v0.10.3 profile; create's
exact v0.10.2 successful artifact/receipt contract is retained. Old AKS lift
records remain teardown-only.

An offline review needs no cluster, kubeconfig read, or tool on `PATH`:

```bash
kmx create --live reviewer --runtime kagent --namespace agents \
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
answer text or system instructions. This receipt does not authorize later
lifecycle reuse. Explicit v0.10.3 bundle placement requires its own qualified
target and UID-bound receipt; an existing v0.10.2 deployment is a conflict,
never adopted or upgraded. This v0.10.2 create profile does not enable
evaluation; explicit [v0.10.3 Go bundle evaluation](agent-lift.md#evaluating-exact-v0103-go-kagent)
is separate. Console bundle operations and interactive `/lift` remain Orka-only.

### Editing an agent

The hidden editor retirement stub is removed with the entire `agent` group.
`kmx create --live` writes reviewable resource/bundle YAML you own; default
create authors workspace source. A live Orka Agent
is edited as the Kubernetes resource it is:

```bash
kubectl --context <ctx> -n <namespace> edit agents.core.orka.ai <name>
```

`kmx orka show <name> --namespace <ns>` reads it back with the chain it
depends on.

## Interactive chat

```bash
kmx chat --namespace orka-system hello-world-agent
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
The separate create-only `--live --runtime kagent --task` path sends once and never
retries an ambiguous result. Interactive Orka `/retry` explicitly resends; that
does not promise exactly-once execution.

`kmx quickstart` is Orka and does not participate in that policy. It creates one
Task, polls that Task's result over a single context-pinned connection, and never
resubmits: connection or forward loss ends the wait rather than repeating the
model call. A missing or whitespace-only result remains unavailable and is
polled on that same Task; once a nonblank result arrives, output with no printable
content is refused. Neither case resubmits the Task.
