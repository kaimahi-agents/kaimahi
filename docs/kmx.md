# `kmx` — agent tooling with Orka as the default

[Orka](orka.md) remains the default runtime. Root `create` authors one tool-free
native workspace member offline; `--runtime kagent` selects exact v0.10.3
Go/linux-amd64. Both profiles support full-workspace evaluation, evaluated
publication and digest Deploy with runtime-specific evidence. Kagent Deploy creates
absent objects only and refuses occupied targets. Runtime/model are packaged,
never overridden; Orka inference uses Ready `openai` Providers.

Legacy `bundle` commands, user `agent.yaml` readers/writers/receipts,
`create --live` including previews/wizard, root run bundle mode and `--bundles`
flags are removed outright, with no retirement stubs. Unknown routes/flags refuse
before configuration. Old Orka/kagent/AgentSessions bundle receipts are unreadable;
no migration or deletion of saved files/resources occurs. Suite status and
retirement remain unavailable, without replacement. Public AgentSuite/ToolProvider
implementation bundles, build/pull and extracted-directory transfer are unrelated
formats and remain supported; so do alpha `pkg/kmx` interfaces.

Chat `/lift` and console Deploy only print
`Deploy moved: kmx deploy <ref>@<digest> --to-context … --evidence …`.
No release GUI or source-less live-copy fallback remains. Host Foundry/Copilot
chat controls remain independent; live tools/inference editing and automatic
wizard tool attachment are retired. Independent AKS setup/teardown and internal
App.Lift remain. Plane and `kmx agent` commands remain removed. Native
suite-owned resources remain reserved against legacy adoption, copying and
retirement; direct live invocation is separate. Readiness is not an answer or
evaluated execution.

## Install

**On Windows, use WSL for now.** Install and run kmx inside your Linux
WSL distribution, not PowerShell or Command Prompt. Native Windows builds
are not supported yet; track [Windows support in #578](https://github.com/kaimahi-agents/kaimahi/issues/578).

The stable release is available through the official Homebrew tap:

```bash
brew install kaimahi-agents/tap/kmx &&
  kmx_prefix="$(brew --prefix kaimahi-agents/tap/kmx)" &&
  "$kmx_prefix/bin/kmx" version
```

The fully qualified formula trusts only `kmx`. Homebrew installs the CLI, not
Docker, Podman or Go; local kind still needs a container engine. To build the
v0.8.1 release with Go 1.26+ instead:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.8.1
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
`--version=v0.8.1` pins it, `--bin-dir=DIR` changes the destination, and
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

### Deploy compatibility (unreleased)

**Deploy is unreleased.** v0.8.1 uses `kmx lift` with the same arguments.
Current main uses canonical `kmx deploy`; `kmx lift` remains a hidden deprecated
compatibility command for at least one released version. It keeps its accepted
arguments, flags, stdout, exit codes and stderr stage labels, adding exactly one
stderr line per legacy invocation:

```text
kmx lift is deprecated; use kmx deploy (same arguments).
```

Only Deploy appears in normal help and completion. Help and completion remain
operationally inert. Deploy uses the same evaluation, publication, consent and
runtime guards. JSON keys and values stay unchanged, including stage/result
`lift` fields and the `targets` operation value `"lift"`; public Go/API and
internal `Lift` identifiers, evidence, storage paths and
`kaimahi.dev/lift-record` annotations are not renamed. Generated recovery commands
use `deploy`; legacy result stdout is not rewritten. Removal requires a later
announced breaking release.

Use `kmx --help` and `kmx <command> --help` for flags and defaults. The Cobra tree
also generates completion; this guide describes contracts rather than duplicating
every flag. Command definitions are in [`cmd/kmx`](../cmd/kmx).

**Released in v0.5.0:** `context show/use` and `local up/down` are the canonical
grouped commands below. Historically, v0.4.1 used `ctx [context]` and root
`up/down`. The old context-switching `ctx` alias, root `up/down`, the AKS `lift`
alias and `lift down` are removed outright, without retirement stubs. Use `context show/use`,
`local up/down` and `aks up/down` for those operations.
Everyday operations are now root `create/chat/run`, live Orka inspection is
`orka show/list`. Legacy `bundle` commands are removed outright.
These routes and root workspace `push` are available in v0.7.0;
historical v0.5.1 uses the `agent` routes.
The provisional [multi-member source](suite-workspace.md#provisional-multi-member-source-offline-only)
format supports internal offline preparation/inventory loading only. Root workspace
push (including `--unevaluated`), eval and workspace Deploy refuse it; existing
single-member authoring/create prompts and public-case receipt rules are unchanged.

Single-member [source v3](suite-workspace.md#explicit-runtime-profiles-offline-only)
packages explicit runtime profiles under one S. Offline root push requires
`--runtime-profile ID [--platform OS/ARCH] --unevaluated` and retains all profiles.
Legacy workspace push rejects these selectors. Suite build resolves exact profile
tuples with the same selector names, but AgentKit refuses profiled plans. No
profiled execution, proof creation/export, evaluated publication, image association
or Deploy is enabled, including for a declared Orka profile. External portable
`eval verify` remains a separate consistency check.

Root `eval` evaluates a full packaged native Orka/kagent workspace, or pinned
AgentSessions chat through explicit `--development-host FILE`, and retains offline `diff` and
`from-sessions` helpers. Public Orka eval uses fresh candidates and verifies cleanup
before passing export; private Quickstart/console remain canonical. `eval cleanup`
lists possible leftovers; a full run ID selects confirmed, identity-safe deletion.
See [candidate cleanup](suite-workspace.md#inspect-and-clean-candidate-leftovers).
Standalone packaged evaluation remains unimplemented.
Root `deploy <ref>@<digest>` deploys an evaluated native suite from a registry or
`--from-layout`, with a destination context (explicit `--to-context` or named
`--to`) and private `--evidence FILE`.
Orka requires `--inference provider:NAME`; packaged kagent instead requires
author-owned `--bindings FILE`. Kagent is create-only and refuses occupied targets,
even when evaluation UIDs match; evidence never crosses runtimes. `--plan` is GET-only; `--promote` explicitly selects
source-only evidence for an eligible destination; candidate evidence requires it
even in the same environment, with absent canonical resources. No model/runtime
override, build, repack, publish or adoption. Plain Deploy stays create-only;
explicit `--update` supports only eligible retained Orka placements and rollback
with original passing evidence. Kagent refuses updates. See
[update and rollback](suite-workspace.md#update-and-rollback). Reports support `-o json`.
`eval/deploy --to NAME` may supply selectors from workspace `kmx.yaml`; Deploy has no
default destination. Concrete values are resolved once and retained, never reread
during confirmation. Explicit-reference Deploy without `--to` is workspace-independent.
Without a positional reference, `kmx deploy --to NAME` composes missing Orka
workspace evaluation, exact-version publication and destination Deploy. It requires
saved `publishRepository` and the destination's `evaluationTarget`, selects and
discloses source-only promotion automatically, and confirms the frozen plan once.
Four default stderr lines report Plan/Eval/Push/Deploy; `-v` and `-o json` expose
details. Verified private exact-version records permit GET-only skips, never
adoption, repair or update. See [composed Deploy](suite-workspace.md#composed-workspace-lift).
Root eval/push remain individually available; explicit-reference Deploy still requires
its digest/private evidence. Kagent cannot join composition until its own
candidate-scoped placement/cleanup is qualified.
See [digest deploy](suite-workspace.md#digest-lift) for gates and partial outcomes. See the
[alignment record](command-semantics.md) for current versus proposed names.

### Current Orka path and explicit version-qualified operations

| Command | Contract / reference |
|---|---|
| `kmx ctx [--workspace <directory>] [--check] [-o table\|json]` | list named workspace destinations (name, cluster context, namespace, declared runtime, inference/bindings reference, registry/repository), offline by default. No current/default pointer or switching. Optional checks cover local context names and anonymous registry reachability only, not runtime/inference/Secret readiness. [Named destinations](suite-workspace.md#named-destinations) |
| `kmx targets [-o table\|json] [--detect] [--sessions <host:port>]` | offline compiled support by default; explicit read-only probes report present/absent/unreadable separately from qualified versions. No runtime selection, installation or deployment. [Target view and support matrix](runtime-adapters.md#read-only-target-view) |
| `kmx orka install` | verify the pinned v0.2.0 release chart; apply its CRDs; install harness-v2 with fullname `orka-api` on the selected context; keep the chart-generated snapshot key private; optionally create a keyless Provider. Refuses old v0.1.3 installs rather than upgrading. [Orka](orka.md) |
| `kmx orka status` | Table-only Orka runtime report: running controller version, Deployments, CRDs and Providers; distinguish unreadable from absent and running version from pin. `-o table` only; root status is separate destination inventory. |
| `kmx create <name> [--model <model>] [--instructions <file>] [--workspace <directory>]` | default offline authoring of one tool-free native suite member; terminal prompts complete missing model/file inputs, scripts require both flags. Orka by default, or exact kagent v0.10.3 Go/linux-amd64 with `--runtime kagent`. `--runtime agentsessions` separately authors pinned chat offline only; ordinary push is supported; eval requires an explicit development host; evaluated push consumes matching passing evidence offline, while Deploy/chat/status refuse. Private metadata only; authoring itself does not deploy/evaluate. Discover the nearest ancestor workspace, or select/initialize an exact root with `--workspace`. Optional `--description`; live-only flags and a second member refuse. No cluster configuration or image construction. [Workspace guide](suite-workspace.md) |
| `kmx push [--workspace <directory>] [reference]` | discover the nearest ancestor `workspace.yaml`, or select an exact root without fallback with `--workspace`; prepare its validated one-member native suite and authored cases (packaged as public AgentSuite evaluations under `evals/<member>/`), then copy its exact OCI graph to a registry or `--to-layout` target. Require latest completed managed exact-S proof unless --evaluation explicitly overrides or --unevaluated explicitly opts out. Preserve explicit tags; bare registry push derives `s-<full-S-hex>` from saved publishRepository. Local layouts require a reference. Print selected proof, absolute workspace and OCI manifest digest on stderr. Legacy required member images are empty. V3 uses explicit --runtime-profile and optional --platform with --unevaluated only, retaining all public declarations and compositions. Does not evaluate or deploy. Existing registry policy applies. [Workspace guide](suite-workspace.md) |
| `kmx eval [--workspace <directory>] --to-context <ctx> (--inference provider:NAME\|--bindings FILE) [--out <private.json>] [--show-content] [-o text\|json]` | evaluate every ordered native Orka case through one fresh Task per declared run (default one); optional KMX `runs`/`minPasses` requires the threshold with no unknown run, each run receiving its own deadline and repeat preflight showing the total execution budget; packaged kagent requires `--bindings FILE`, unsecure auth and fresh blocking A2A Tasks (no `--inference` or `--result-port`), without retry; non-pass summaries show assertion IDs/types and judge scores/threshold; --show-content opts into sanitized bounded excerpts, expected values and rationale on stderr; `llmJudge` requires explicit `--judge-base-url` and optional credential-variable `--judge-key-env`, with two independent samples inside each run's deadline, using `max_completion_tokens` (1024) without a temperature override; the receipt binds this request style (old judge receipts require fresh evaluation), with no default content output or endpoint recovery echo; the diagnostics-only policy clarification preserves existing evidence identities; defaults: namespace `orka-system`, case timeout 5m (max 9m), automatically selected free IPv4 loopback result port (explicit `--result-port` is honored without fallback). Both require a preinstalled runtime and model-binding prerequisites; Orka also requires result access. Output defaults to text for all runtimes; Orka-only `-o json` emits one payload-free outcome document on success or execution abort, with diagnostics on stderr. Abort counts distinguish executed Tasks from planned runs and assertion passes; fully executed threshold misses remain ordinary run-pass summaries. Offline unsupported/budget/output checks precede configuration; one guard covers owned pair placement and execution. Only full passing metadata is exported once, privately outside workspace/cache. Hermetic tests and pinned runtime CLI qualification cover the bounded scenarios. [Workspace evaluation](suite-workspace.md#evaluate-the-whole-workspace) |
| `kmx eval --to <name> [--out <private.json>]` / `kmx deploy <ref>@<digest> --to <name> --evidence FILE [--promote] [--plan]` | resolve workspace `kmx.yaml` selectors once; invocation flags override saved fields. Runtime expectation is checked against packaged runtime. Eval may omit `--to` only when `evalTarget` is saved; Deploy has no default destination. Explicit-reference evidence/gates/remote guards remain unchanged. [Named destinations](suite-workspace.md#named-destinations) |
| `kmx deploy --to <name> [--plan] [-v] [--show-content] [-o json]` | compose missing Orka workspace evaluation → publication → Deploy; require saved repository and explicit evaluationTarget, freeze selectors/source once, automatically disclose source-only promotion and obtain one scoped confirmation. Verify exact-version skips read-only; failure detail matches standalone eval. --show-content opts into sanitized bounded failure content; --verbose alone does not. Explicit-reference and plan-only Deploy refuse --show-content. [Composed Deploy](suite-workspace.md#composed-workspace-lift) |
| `kmx eval export [--workspace <directory>] --member <id> --out <file>` | export portable evidence for the latest complete exact-suite/member evaluation, offline and without configuration/model/cluster/registry calls. Requires captured tagged Go module version/checksum or full clean VCS provenance; modified/unstamped builds and old records without metadata refuse. New detached file only, never replacement or private-gate authority. [Public evidence](suite-workspace.md#export-portable-evaluation-evidence) |
| `kmx eval verify --suite <layout-or-digest> [--from-layout <layout>] --evidence <file> --expect <file> [-o text\|json]` | offline public-evidence CI gate with a complete, independently reviewed ExpectedEvaluation committed beside the suite. Digest input requires explicit local layout. No state/cache/config/model/cluster/registry access. Success-only summary exits0; safe typed refusal code exits1. Does not replace private push/Deploy gates. [CI verification](suite-workspace.md#verify-portable-evaluation-evidence-in-ci) |
| `kmx eval cleanup [<run-id>] --to <name>` | without a run, read-only candidate listing; with one full run ID, preview exact UIDs, confirm even on local kind, then identity-safe cleanup. Explicit context/namespace works without source or inference. Liveness is unknown; unanchored or unverifiable orphans refuse. [Cleanup contract](suite-workspace.md#inspect-and-clean-candidate-leftovers) |
| `kmx eval [--workspace <directory>] --development-host <host.json> [--out <private.json>]` | evaluate one packaged AgentSessions chat member on explicit literal-loopback, unauthenticated operator-observed development host; no cluster configuration or selectors, registry/auth/deploy/chat. One fresh Session/config/Exec per declared run; optional `runs`/`minPasses` and explicit `llmJudge` share Orka/kagent semantics, including per-run deadlines and unknown veto. Full-policy-pass-only private metadata. [Development host contract](suite-workspace.md#agentsessions-development-evaluation) |
| `kmx push --evaluation <private.json> [--workspace <directory>] <reference>` | consume a strict private full-passing Orka/kagent or development AgentSessions suite receipt; evidence must match the packaged runtime and require exact suite manifest identity plus complete offline coherence before transfer/provenance. Explicit evidence overrides managed lookup without fallback. Root push otherwise requires a completed exact-S pass, unless --unevaluated explicitly opts out; unsigned observations are not authentication or a policy gate. [Workspace guide](suite-workspace.md) |
| `kmx suite build <directory> ...` | resolve one agent/platform composition and build an OCI image-layout tar through AgentKit using Docker buildx. The current implementation is explicitly experimental and non-conformant because it treats the selected harness image as a monolithic AgentKit adapter and does not compose the runtime-base image. |
| `kmx suite push <directory> <registry/repository:tag>` | deterministically archive and validate an extracted AgentSuite, then push it to an OCI registry. An existing tag with the same digest is a no-op; a different digest requires `--force`. It also attaches unsigned [suite provenance](#suite-provenance) before moving the tag; `--provenance=false` skips it and `--require-provenance` fails without moving the tag if it cannot be attached. Prints the pushed and provenance digests. See [registry safety](#registry-authentication). |
| `kmx suite push <directory> --to-layout <layout> <repository:tag>` | deterministically archive and validate an extracted AgentSuite, then add its referenced artifact to a local OCI image layout. New layouts are published atomically; existing layouts retain unrelated references. Output states whether the layout was created or updated. No provenance is attached, since a conformant AgentSuite layout holds exactly one manifest. |
| `kmx suite pull <registry/repository:tag-or-digest> --output <directory>` | pull and validate one AgentSuite from an OCI registry, then atomically extract it into a new directory. Prints the resolved digest and any [suite provenance](#suite-provenance) that names it, for information only; tag pulls also print a digest-pinned reference for repeatable pulls. See [registry safety](#registry-authentication). |
| `kmx suite pull <repository:tag> --from-layout <layout> --output <directory>` | resolve and validate one AgentSuite reference from a local OCI image layout, then atomically extract its content into a new directory. Existing output is never replaced. |
| `kmx suite policy FILE [--administrative-policy FILE]` | resolve a portable v1 deny-write or v2 IPv4/TCP egress policy offline; print canonical effective JSON then its digest. Administrative deny paths union; administrative network allowances intersect (empty denies all). Inputs and output are bounded to 4 MiB; explicit empty administrative flags refuse. No operational configuration, enforcement or provider selection. [Policy contract](portable-policy.md) |
| `kmx suite validate <path>` | validate an extracted AgentSuite content directory or OCI image layout offline, including strict JSON, canonical identities, closed references, tool locks, build profiles and archive safety. `-o json` emits the validated summary. [AgentSuite specification](https://github.com/agent-primitives/suite/blob/main/spec.md) |
| `kmx suite diff <before> <after>` | validate and semantically compare two extracted AgentSuites or local OCI image layouts, including supplemental packaged content and artifact identities. The default unified view uses `-` and `+` field lines; `--view split` renders before and after values side by side, and `-o json` emits the versioned change and affected-agent report. [Suite artifact diff](#suite-artifact-diff). |
| `kmx eval from-sessions UID... --sessions SOURCE --assertions CASE.yaml --out MANIFEST.json` | Independent private reference extraction/export only. [Reference extraction](#reference-extraction) |
| `kmx eval explain [failure-id] [--workspace DIR] [--show-content\|--clear]` | inspect private local failures offline without operational config or model calls; metadata by default, bounded content only with --show-content. --clear removes one ID, or all only with explicit --workspace, never passing proof or runtime history. Owner-only retention keeps the last 10 non-pass runs across suite revisions, 64 KiB answers and 128 KiB records. [Local failure inspection](suite-workspace.md#inspect-or-clear-local-evaluation-failures) |
| `kmx eval diff BEFORE AFTER` | Offline strict full-passing native suite evidence only; old bundle receipts unreadable. [Native receipt diff](#native-receipt-diff) |
| `kmx run --agent NAME --prompt TEXT` | One Task on a live Orka Agent, including suite-owned Agents, with answer-only stdout; not workspace-local execution or suite evaluation. [Task execution and results](#task-execution-and-results) |
| `kmx task result TASK [--wait 5m]` | Read existing Task phase/answer without resending; retain context/namespace. [Task execution and results](#task-execution-and-results) |
| `kmx context show` / `kmx context use <context>` | read effective context/source/posture or remember KMX's guarded access selection; never change kubectl current-context |
| `kmx console` | Live Orka inventory and source-reviewed native workspace creation/eval/push/Deploy. Ready is placement only. Interactive Deploy and bundle comparison are retired; `--demo` uses sample data. |

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
| `kmx quickstart` | kind + keyless Ollama + pinned Orka v0.2.0 Helm chart + the fixed tool-free native `hello-world-agent` source/full eval/publication/reviewed Deploy/placement Verify + a separate fresh answer Task; no Kagent installation or plane/governance enabled. [Getting started](getting-started.md#one-command-and-an-agent-that-answers) |
| `kmx quickstart --interactive` (`-i`) | Experimental TUI: author an Orka agent while kind, Ollama/model, and Orka start in the background; then full eval, exact publication, reviewed Deploy/placement Verify and a separate fresh answer Task. Flags for the other quickstart mode are refused before configuration loads. The retired `kmx quickstart-wizard` spelling refuses and names this command. |
| `kmx local up` | the runtime and no agent: cluster, ollama, model, orka. `--step` selects exactly one of those four; the three legacy steps are removed and are refused as unknown |
| `kmx aks up` / `kmx aks down` | Temporary compatibility route, hidden from root help and shell completion; not a first-class KMX domain. Setup provisions AKS and installs Orka only, with optional Azure monitoring add-ons; the owner configures the model Provider. It does not deploy a model plane or capture its credentials. Teardown preserves cleanup of recorded owned resources, including historical runs. `--payload` defaults to `orka` and is the only payload (no Provider is created); the legacy payload is refused as retired, and an existing legacy lift can still be inspected and torn down. The root AKS aliases are removed; use `kmx aks up` / `kmx aks down`. [AKS](aks.md) |
| `kmx orka list` | live Orka Agent inventory in one namespace; table/JSON/YAML. [Inventory contract](#kmx-orka-list) |
| `kmx orka show <name>` | live Orka Agent detail and dependencies; requires `--namespace`, supports `--output table\|json` and `--tasks`. [Detail contract](#kmx-orka-show) |
| `kmx chat <name> [message]` | interactive Orka session (`--runtime auto\|orka`, `--namespace`, default `orka-system`). Chat is always a session: an optional message is its first turn, and the one-shot Task is `kmx run`. The former `--interactive` flag is removed; Kagent chat is not restored by its create capability |
| `kmx status --to NAME` | GET-only namespace inventory of deployed Orka/kagent Agents, per-object suite/consumed-evidence provenance and current placement readiness. Separate candidate roots, readable partial results, no local-history enrichment or writes. `--workspace DIR`, `-o table\|json`; saved namespace required. [Contract](destination-status.md). |
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

Generated retries and follow-up commands preserve the executable used to launch
the operation. For example, `./bin/kmx aks up ...` produces a
`./bin/kmx ... --step orka` recovery rather than a bare `kmx` that could resolve
to an older installation on `PATH`.

The implicit `kind-kaimahi-p1` fallback refuses when kubeconfig contains other
possible targets and nobody chose this one. An empty machine, or corroboration
that the fallback is already current, permits first setup. Current-context is
never substituted as kmx's target. Kind creation/image loading also require
context `kind-$KIND_CLUSTER`; confirmation cannot override that mismatch.

`kmx local down` checks the container engine because kind deletes by container name,
not kubeconfig. A listed kind cluster missing from kubeconfig requires explicit
named confirmation; no matching container cluster is a no-op. An absent context
is not proof there is nothing to delete.

AKS setup has cloud-specific consent: creation/BYO `kmx aks up` confirms cluster; created
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
- Root `status --to NAME` emits a table or one versioned JSON document, including
  full digests, per-resource reasons and `complete`. `provenance not recorded`,
  `unknown` and `not kmx-owned` remain distinct. Exit1 unknown/unhealthy/drift
  overrides2 known pending;0 requires complete ready/empty. Missing fields are
  never filled from companions or local history. Candidate roots remain separate.
  Bare status names both usage routes; `orka status` remains table-only and
  refuses JSON/YAML. See [destination status](destination-status.md).

Completion (`source <(kmx completion bash)`, similarly zsh; fish uses
`kmx completion fish | source`) performs bounded read-only lookups for contexts
and agents, no guard/download/forward/mutation. Static completion works offline.

## `kmx create`

`kmx create NAME [--model MODEL] [--instructions FILE] [--workspace DIR]`
authors one tool-free native member offline. Orka is default; explicit
`--runtime kagent` selects exact v0.10.3 Go/linux-amd64. Description is optional.
Create and push share nearest-ancestor `workspace.yaml` discovery; explicit
`--workspace DIR` selects/initializes exactly that root without fallback.
Invalid markers and second members refuse without source changes. Missing runtime
in older authored workspace source stays Orka, not a persistent `agent.yaml` reader.

A visible stdin/stderr terminal prompts for any omitted exact model or instructions
file. Supplied flags are preserved; explicitly blank flags refuse. Enter a literal
file path, not inline instructions or shell syntax. No model is inferred or looked up.
Nonterminal use requires both flags. Without a workspace, terminal use then asks
`Start one here? [Y/n]`; nonterminal use requires an explicit workspace.
Help/completion are inert. Create never loads cluster settings,
bootstraps runtime/model, builds images, deploys or evaluates. `--live` and its
live-only/preview flags are unknown before config loads. See [workspace source
and cases](suite-workspace.md).

Experimental `kmx create NAME --workspace DIR --members FILE --flow FILE` imports
strict JSON members and a public `kmx.flow.v1alpha1` document as one offline suite.
All three flags are required; single-member/runtime selectors refuse, including
explicit defaults. It does not evaluate, run, publish or deploy the flow. See
[experimental flow authoring](suite-workspace.md#experimental-flow-authoring-offline-only)
for file shapes, path confinement, limits and the explicit read-only APIs.

## Task execution and results

`kmx run --agent NAME --prompt TEXT` or `--prompt-file PATH` (`-` for stdin)
submits one Task to a Ready live Orka Agent, including a suite-owned Agent placed
by Deploy. Select its context with `--context` and its namespace with `--namespace`
(default `orka-system`); `--agent` names the live Agent. `--wait` defaults to 5m,
maximum 9m. stdout contains only the answer; stderr retains Task identity and
retrieval instructions. Bundle-directory selection is removed.

`kmx chat NAME --namespace NAMESPACE --context CONTEXT` opens the supported Orka
chat path against a live Agent, including suite-owned Agents. Each native chat
message creates a fresh Task; neither run nor chat executes workspace source or
produces fresh suite evaluation evidence. Invocation retains the caller's
effective authority and existing access checks; it does not authorize adoption,
update, copying or retirement of the Agent or its companion resources.

`kmx task result TASK` reads an existing Task without resending. It reads phase
once by default and opens a result session only for a readable terminal answer.
`--wait` waits 10s–9m and reports phase changes. Pending/unreadable results exit 2;
failed/cancelled Tasks exit 1. Preserve the Task's context and namespace.

## Native receipt diff

`kmx eval diff BEFORE AFTER` compares strict full-passing native Orka/kagent
suite receipts offline. Both require the same runtime, exact observed subject
and ownership, and complete ordered case inventory. Execution IDs and answer
hashes are not comparison identities. Incompatible/incomplete evidence refuses
nonzero; zero is a comparison result, not a new evaluation pass. Old bundle
receipts are unreadable. The wide legacy normalized receipt API is removed,
not converted or retained as a promotion gate.

## Reference extraction

`kmx eval from-sessions UID... --sessions SOURCE --assertions CASE.yaml
--out MANIFEST.json` remains a bounded read-only private extraction/export utility.
It stores single-turn UID/prefix references and authored assertions, not recorded
inputs/answers. Existing output appends atomically; duplicates refuse the whole
addition. Remote hosts require verified TLS; `--sessions-ca` supplies a private CA.
Keep manifests private. No inference or runtime/host attestation is performed.

Legacy RecordedExecution and bundle replay/evaluation/judge wiring are removed;
runtime-core assertion and llmJudge implementations and tests remain retained.
Native Orka/kagent and development AgentSessions eval wire authored llmJudge with an explicit endpoint;
reference manifests still have no native consumer. Root eval requires authored
workspace cases and private full-passing evidence, with no subset/recorded-input/
model/runtime override. Native AgentSessions
`--development-host FILE` evaluation is separate and remains supported; its
passing proof supports evaluated push, not receipt diff or Deploy.

### Editing an agent

The hidden editor retirement stub is removed with the entire `agent` group.
`kmx create` authors workspace source; live source editing is not a KMX shortcut. A live Orka Agent
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
Interactive Orka `/retry` explicitly resends; that
does not promise exactly-once execution.

`kmx quickstart` is Orka and does not participate in that policy. It creates one
Task, polls that Task's result over a single context-pinned connection, and never
resubmits: connection or forward loss ends the wait rather than repeating the
model call. A missing or whitespace-only result remains unavailable and is
polled on that same Task; once a nonblank result arrives, output with no printable
content is refused. Neither case resubmits the Task.
