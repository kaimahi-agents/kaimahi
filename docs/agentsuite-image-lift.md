# AgentSuite image lift

Status: experimental implementation rebased onto main through #340, incorporating
the merged #339 buildx backend and #357/#360 archive/attestation updates.

Builds use the selected Docker buildx builder (`--builder`) and request SBOM and
max-mode provenance by default. `--require-attestations` requires both; an explicit
`--attestations=false` opts out. These options are shared by `suite build`,
`suite publish` and `console --workspace`. KMX does not start or manage a builder.
The mounted-configuration profile uses a no-RUN Dockerfile over its pinned harness,
without linking AgentKit/BuildKit Go clients or baking selected model settings.

Archive inspection checks content digests and attestation subjects before output
publication. Registry publication preserves the root index and attestation graph;
deployment pins the runnable platform manifest. Local publication records retain
both identities. Attestations remain unsigned and are not sandbox conformance or
signer-policy evidence. The image-set/registry policy contract remains open.

## Build once, select inference at deployment

The workspace path declares inference **capabilities**, not a model name, in the
AgentSuite. The selected model, endpoint and credential references belong to the
deployment environment. The pinned Pydantic AI harness reads its existing v0 ABI
from a read-only ConfigMap at `/run/agentsuite/agent.json` through `--config`.
The capability-based builder writes instructions, but no generated model config,
into its image layers. The same suite/image digest can serve different model
bindings. A binding change changes the plan and pod-template configuration digest
and rolls the workload without rebuilding the image.

This is the experimental `agentkit-v0-mounted-v1` mapping, qualified only for the
checked-in Pydantic AI harness digest, HTTP port 8080 and `/healthz`. The existing
exact-model builder route remains experimental and bakes its model settings.
Full inventory/sandbox-binding conformance remains open; the image-deployment
label is a publisher declaration. Contract follow-up:
[#356](https://github.com/kaimahi-agents/kaimahi/issues/356).

### CLI and terminal workspace

```sh
mkdir -p suites
kmx suite create suites/hello-world \
  --instructions 'Answer greetings briefly. Identify yourself as the AgentSuite sample.'
kmx suite publish hello-world --workspace suites \
  --registry "$REGISTRY/agent-demo" --builder "$BUILDX_BUILDER"
kmx suite deploy hello-world --workspace suites --environment local.json --plan
KAIMAHI_CONFIRM=kind-dev kmx suite deploy hello-world \
  --workspace suites --environment local.json
kmx suite run hello-world --workspace suites --deployment local-demo \
  --member hello-world --prompt Hello

kmx console --workspace suites --registry "$REGISTRY/agent-demo" --builder "$BUILDX_BUILDER"
```

In the workspace console: `n` creates local source; `b` builds/publishes all
members or reuses matching published images after checking their source linkage;
`l` reads an environment file and opens a typed plan; Enter applies it and Esc
cancels. `s` reads deployment status, `d` selects a recorded deployment, and `c`
sends an authenticated request to a single-member deployment. Multi-member runs
use `suite run --member`. Each chat request is independent; conversation history
and streaming chat are not implemented here. PgUp/PgDn scroll the result pane.

Source appears without a live Orka Agent. The existing console without
`--workspace` retains its native Orka experience. The workspace UI currently
collects name, instructions and token requirements; target/model configuration
uses the explicit environment file below rather than an inference-discovery form.

### Deployment environment

Both kind and AKS use this format, differing in context, cluster UID, namespace,
inference endpoint and credential references. The namespace, model endpoint and
Secrets must already exist. `suite deploy` fills omitted member image references
from the publication record; explicit conflicting images are refused.

```json
{
  "apiVersion": "kaimahi.dev/lift/v1alpha1",
  "name": "local-demo",
  "context": "kind-dev",
  "clusterUID": "<kube-system namespace UID>",
  "namespace": "agents",
  "adapter": "kubernetes-http-v1",
  "platform": {"os": "linux", "architecture": "amd64"},
  "members": {
    "hello-world": {
      "name": "hello-world",
      "inference": {
        "model": "model-a",
        "endpoint": "http://model.agents.svc.cluster.local:8000/v1",
        "credential": {"name": "model-key", "key": "token"},
        "capabilities": {
          "api": "openai-chat-completions-v1",
          "contextTokens": 8192,
          "outputTokens": 1024,
          "streaming": false,
          "toolCalling": false
        },
        "evidence": "operator-declared"
      },
      "inputs": {
        "agent-auth": {"secretRef": {"name": "agent-auth", "key": "token"}},
        "listen": {"value": "0.0.0.0"}
      }
    }
  }
}
```

Capability claims are explicitly operator-declared, not inferred from a model
name or `/models`. Context means total token window; output means supported
maximum output tokens. These fields are compatibility requirements, not request
token-budget enforcement. Streaming describes inference API support, not the
agent-facing chat transport. Unknown/insufficient required capabilities fail
planning. ToolProviders and invocation edges remain refused by this adapter.

Publication and deployment inputs/results are saved privately under
`<workspace>/.kmx/`, outside suite content. They allow a later CLI/TUI process to
locate deployments. They contain connection settings and Secret references, not
credential values or chat payloads. These are experimental local records, not a
portable image-set artifact, immutable release history or operation-ID recovery
store. Concurrent writers and recovery after an interrupted mutation remain
follow-ups under #346/#356. Keep `.kmx/` out of source publication/version control.

### Container registry integration

KMX consumes ordinary OCI container registry references. Suite/image publication,
digest resolution and pulls use the shared ORAS transport and Docker credential
store; neither the artifact nor the lift environment selects an Azure registry
type. `--registry` means a container registry/repository prefix. Set `REGISTRY`
to the login server of your selected registry before running the examples.

Registry-specific integration belongs above this transport as an explicitly
selected access/setup strategy. The proposed separation is:

- **Registry transport:** OCI push/pull, descriptors, authentication challenges,
  TLS and credential forwarding rules. Reused for all compatible registries.
- **Publisher access strategy:** default Docker credential store/helper; an ACR
  strategy may use Azure login to prepare credentials for that same transport.
- **Workload pull-access strategy:** existing node identity or namespace-scoped
  image-pull Secret references; an ACR/AKS strategy may inspect or explicitly
  configure Azure role assignments through the Azure-owned infrastructure layer.

Publishing and workload pulls use separate identities. Provider setup must not
be triggered by read-only lift planning or inferred merely from a hostname.
Return typed access requirements/results to the UI without adding Azure IDs,
tokens or ACR-specific fields to portable suite/image identities. Authentication
strategy failure must not silently downgrade to anonymous access.

Today core uses the default credential-store path and explicit pull Secret
references. Azure-specific setup is external (`az acr login`, prepared node
permissions) or in the opt-in smoke helper; a built-in ACR strategy is proposed,
not implemented. #223 owns Azure setup and #356 tracks the deployment handoff.

### ACR local-to-remote test case

Authenticate publication through the Docker credential store (`az acr login`).
AKS nodes need their own ACR pull access; a successful workstation login does not
grant it. Local kind can use namespace-scoped `imagePullSecrets`. Reusing an
image in another environment requires only another environment file and lift.

The smoke runner now exercises two model bindings and the CLI deployment
connection, and can retain its workspace for the TUI:

```sh
python3 scripts/ci/suite-lift-smoke.py --kmx ./bin/kmx \
  --context aks-demo --confirm-context aks-demo \
  --registry "$REGISTRY/agent-demo" \
  --namespace suite-remote-demo --workspace /tmp/suite-demo
```

For local kind pulling from that same ACR, use the local context and a fresh
namespace, the same workspace/registry, and `--acr-pull-credentials <acr-name>`.
That option creates a namespace pull Secret from the current Azure login; its
token expires and is for the smoke only. Omit `--kind-name` for an actual registry
pull test. The alternate localhost-registry mode below still preloads images.

Testing on kind and private ACR/AKS used the same suite and image digest with two
model names, authenticated model/agent requests and repeated lift preserving
resource UIDs. Actual pseudo-terminal runs exercised workspace build/reuse,
review/apply, status and chat on both targets. The controlled model checks the
selected name, credential and instruction text; this is not external-model
evaluation, capability discovery or native agent-platform registration.

## Implemented build-to-lift path

The branch now consumes #339's actual AgentKit builder. `suite build --suite-ref`
verifies that the local definition matches the published suite and emits the
experimental image-deployment record from the execution profile. `suite push-image`
publishes the built OCI archive as a runnable image, not as suite content.

`lift <suite-reference>` with an environment `members` map resolves the complete
suite and every member image before target writes. A shared suite deployment
interface in `internal/kmx/runtime/suite.go` prepares all members, inspects all
resources, rechecks after review and records unattempted/unknown member outcomes.
The concrete implementation is the standalone Kubernetes HTTP adapter; native
agent-platform adapters are not implemented by this path.

For a suite environment, replace top-level `inputs` with:

```json
{
  "members": {
    "hello-world": {
      "name": "hello-world",
      "image": "registry.example/team/agent@sha256:<image-digest>",
      "inputs": {
        "model-key": {"secretRef": {"name": "sample-model-key", "key": "token"}},
        "agent-auth": {"secretRef": {"name": "sample-agent-auth", "key": "token"}},
        "listen": {"value": "0.0.0.0"}
      }
    }
  }
}
```

Each agent requires exactly one member entry. Native source formats and other
runtime IDs remain unsupported rather than being silently converted to HTTP.
Full sandbox conformance and promotion-gate integration remain separate work;
the experimental label must not be presented as verified build provenance.

### Reproduce the real-container smoke

The generator [sample-suite.py](../scripts/sample-suite.py) creates a tool-free
sample with exact graph digests from the pinned #339 harness/base fixture.
The opt-in [smoke runner](../scripts/ci/suite-lift-smoke.py) uses `suite create`, builds and publishes
the sample, deploys it through the real lift command, checks an authenticated
answer and verifies repeated deployment preserves resource UIDs.
Its [controlled model](../scripts/ci/suite-model.py) checks model, credential and
instruction text (allowing trailing-newline normalization in the harness).

```sh
go build -o /tmp/kmx-suite-lift ./cmd/kmx
python3 scripts/ci/suite-lift-smoke.py \
  --kmx /tmp/kmx-suite-lift \
  --context kind-dev --confirm-context kind-dev \
  --kind-name dev --plain-http \
  --registry localhost:6000/suite-smoke \
  --namespace suite-smoke
```

For AKS, use a prepared context and authenticated ACR repository prefix, omit
`--kind-name`/`--plain-http`, and ensure nodes have image-pull permission. The
runner creates a fresh namespace and refuses an existing one. It leaves test
resources for inspection and never deletes a namespace or provisions a cluster.
The local kind mode preloads the exact image digest because the workstation's
localhost registry is not the node's localhost; this does not prove remote
registry image-pull identity.

Local and remote proof completed: suite generation/validation, registry push, real pinned
AgentKit/BuildKit build, image publication, suite plan, Deployment/Service
creation, authenticated response through the controlled model, and repeated lift
with preserved UIDs. The remote run pulled the private image from ACR on AKS
using registry-scoped kubelet `AcrPull` access, without local image preloading.
Both runs used the same smoke runner and adapter with different target bindings.
This establishes standalone HTTP execution on the tested targets, not native
agent-runtime registration, external-model evaluation, or general runtime parity.

## Target architecture: full-suite adapter deployment

The intended product is full-suite deployment through runtime adapters on both
local kind and remote AKS. Cluster location and agent runtime are independent
dimensions. Both destinations invoke the same adapter `Deploy` action; local
execution is not a separate renderer and remote lift does not copy live state.

```text
AgentSuite directory / OCI reference
  -> resolve immutable suite and member image selections
  -> resolve destination (cluster identity, namespace, runtime installation)
  -> validate every member and binding before writes
  -> selected runtime adapter renders the complete suite plan
  -> review / promotion gate
  -> adapter Deploy on kind OR AKS
  -> per-resource and per-member outcomes + aggregate deployment identity
  -> adapter status / execution / evaluation
```

The standalone HTTP-image implementation below is a retained prototype, not
the implementation of this full-suite target. It has its own environment,
renderer and reconciler and must be integrated into the shared lifecycle rather
than expanded into a parallel deployment engine. Its image label and HTTP-only
contract are provisional until reconciled with the image builder in #339.

### Audit of the existing adapter usage

Audited main at `f8c5278` and the local prototype:

| Surface | Existing behavior | Required suite change |
|---|---|---|
| `internal/kmx/runtime/lifecycle.go` | Shared Render/Deploy/Status/Evaluate interface, with a single prepared portable agent and rendered bundle | Add a validated suite/image input and suite result without weakening existing portable-agent identity |
| `internal/kmx/app/create_orka.go` | Constructs the Orka adapter and calls its Deploy action | Route suite members through an explicit lifecycle composition root |
| `internal/kmx/app/agent_bundle_lift.go` | Constructs the Orka adapter, gates and plans one local bundle, then calls Deploy with reconciliation | Resolve all suite members and target bindings once; gate/review the exact aggregate plan |
| Orka lifecycle adapter | Provider/Agent creation and reconciliation, status/evaluation, named coordination | Qualify built-image registration and exact suite invocation bounds |
| Second runtime lifecycle adapter | Configured Render/Deploy for exact-version create only; reconciliation, status/evaluation and coordination are refused | Complete lifecycle qualification through #294/#238 before claiming suite deployment parity |
| `internal/kmx/app/runtime_registry.go` | Chat registers only Orka; create/lift instantiate concrete lifecycle adapters directly | Use an explicit deployment-adapter registry, independent of chat support |
| `internal/kmx/runtime/registry.go` | Generic bookkeeping exists, but first-match probing is not a complete multi-runtime deployment selector | Require an explicit runtime or an unambiguous qualified installation; never infer runtime from kind versus AKS |
| Existing bundle lift/refusal tests | Non-Orka bundles are rejected before cluster reads | Expand supported behavior with tests and the corresponding repository boundary policy, not by deleting refusals |
| `.github/workflows/ci.yml` | Native runtime proofs use disposable kind; no equivalent live AKS suite proof | Run the shared suite scenarios on kind, then a separately configured AKS integration lane |

An interface implementation is not support parity. The existing second adapter
already has Deploy, but that action is create-only and cannot reconcile a suite
upgrade. Neither existing adapter consumes a complete AgentSuite or a built
member-image set. Prepared Kubernetes contexts are usable independently of
cluster provider, but that is not evidence that all four runtime/destination
combinations are qualified.

### Implementation sequence for the revised scope

- [ ] Agree #339 build output: member image descriptors, source-suite/composition
  binding, supported invocation modes and supported runtime inputs.
- [ ] Introduce one suite deployment input that retains all validated members,
  ToolProviders and invocation edges, plus exact image selections where used.
  Native declarative and baked-image execution must be explicit: do not render
  a native agent that silently ignores the selected image.
- [ ] Make deployment target configuration independent of suite behavior: cluster
  identity, namespace, selected runtime/version, inference and Secret references.
- [ ] Register deployment adapters independently of chat sessions. Reuse the
  existing lifecycle action and target guards; reconcile this boundary with
  #326's internal-first direction rather than copying its provisional public API.
- [ ] Plan every member before mutation, deduplicate only intentionally shared
  resources, and detect deployment-name/namespace collisions. Do not assume the
  invocation graph is a DAG: bounded cycles may need registration before routing
  is activated. Preserve declared edges and reject unsupported limits.
- [ ] Implement aggregate Deploy results with member/resource identity, partial
  outcomes and retry/recovery semantics. Avoid wrapping single-agent create in a
  loop that discovers a later member's incompatibility after earlier writes.
- [ ] Qualify initial install and update for both adapters. Create-only support
  must remain explicitly create-only until reconciliation is implemented.
- [ ] Carry promotion policy and evaluation identity through suite lift. Do not
  make registry input a bypass of the existing evidence gate.
- [ ] Prove the same full-suite scenario on local kind and remote AKS for each
  supported runtime/version pair, including image pull, inference, delegation,
  readiness, one real answer, partial failures and ownership checks.

## UI audit and suite-centered experience

The existing native Orka console is live-agent-centric. The broader target UX is
one suite workspace with member details and deployment comparisons underneath:

```text
AgentSuites                         Selected suite
  incident-response                Source / Artifact / Images / Deployments / Runs
    coordinator                    suite digest and publication reference
    analyst                        member -> platform -> harness -> image digest
    reviewer                       required inputs and runtime compatibility

Deployments
  local   kind-dev / agents        runtime + installed version + member outcomes
  remote  production / agents      same suite/image identities, target bindings

Actions
  Inspect -> Validate -> Build images -> Publish -> Plan Lift -> Lift
  Member run/chat and suite evaluation use the selected deployment's adapter
```

This is a proposed expanded layout; the implemented `--workspace` mode above is
the initial source/build/lift/status/chat slice. Local/remote
columns can remain useful comparison views, but a live local agent must not be
required to discover, build, publish or lift an OCI suite.

| UI entry point | Audit finding | Required change |
|---|---|---|
| `cmd/kmx/console_command.go` / `AgentTUIOptions` | Inputs are two contexts, one namespace and an `agents/` bundle root | Accept suite workspace/OCI sources and separate target runtime/namespace choices |
| `agent_tui.go` / `agent_tui_data.go` | Inventory rows are live agents; environment has context/local flag; no suite, composition or image identity | Add a suite read model independent of live inventory, with members, image selections and target deployments |
| `agent_tui_data.go` | Version displays a label or Kubernetes generation | Display suite digest, image digest, harness version, installed runtime version and resource generation as separate facts |
| `agent_tui_data.go` `canChat`/`canLift` | Actions depend on `!External`, relying on Orka-only inventory | Drive availability from deployment-adapter capabilities and compatibility results, with reasons |
| `agent_tui_bundle.go` | Resolves `agents/<name>/agent.yaml` and compares Git bytes | Compare selected suite/member and image identities; preserve the existing bundle pane as an explicitly different source view |
| `agent_tui_actions.go` / `/lift` completion | Lift starts from a local native agent; remote preparation is mixed into that journey | Start from a selected immutable suite/image set, choose kind or AKS destination and runtime, then show one typed plan |
| `chat_lift_bundle.go` | Captures printed plan text and compares it before deployment | Render a shared typed plan and confirm its fingerprint (#279); never parse CLI prose |
| `agent_tui_inference.go` | Edits live runtime inference configuration | Edit destination binding slots for suite deployments; distinguish bindable settings from baked image inputs requiring rebuild |
| Tool editing | Selects live runtime tools | Inspect provider contracts and callable Tools, author behavior changes in source, rebuild where needed; do not silently drift a pinned suite |
| `agent_tui_runs.go` / `agent_tui_runs_data.go` | Reads runtime Task lineage for a selected live agent | Filter by deployment/member identity and preserve missing linkage as unknown; do not infer observed delegation from suite edges |
| Suite CLI | Main has validate/push/pull, #339 adds experimental build | Console should call these same operations and consume build descriptors/progress, not shell out and parse output |

### UI delivery checklist

- [ ] Read-only suite explorer first: open extracted directory or resolved OCI
  artifact, list agents/providers/compositions/profiles and distinguish source,
  built image, deployment and run identities.
- [ ] Add Build and Publish actions over the same services as CLI. Show platform,
  exact harness/base selection, image result, and non-conformance warnings.
- [ ] Add suite deployment review with all members and resource decisions,
  destination runtime/version, image compatibility, consumed bindings and gate
  result. Show unsupported members before any write; never silently deploy a subset.
- [ ] Enable Lift on local and remote destinations through the same Deploy
  action. Keep environment preparation a separately scoped action.
- [ ] Expose partial deployment state and adapter-specific unavailable actions;
  keep readiness, a successful answer and passing evaluation separate.
- [ ] Add deployment-scoped run/chat/evaluate only where the adapter supports
  them. Closing observation must not cancel remote work implicitly.
- [ ] Test missing local agents, registry-only suites, duplicate member names
  across suites, changed tags, stale plans, two runtimes in one cluster, missing
  image bindings, partial writes and unsupported behavior.

### Coordination with open work

#339 owns the builder path and must inform image inspection and binding controls.
#326 owns lifecycle boundary design; its latest discussion favors internal-first
integration. #302/#301 own Lift vocabulary and command consistency. #340 changes
quickstart/chat interaction, not suite inventory or deployment. #273/#274 supply
the declared-suite versus observed-run distinction. This branch should implement
one shared suite operation layer before replacing the console's current handlers.

## Goal and boundary

```text
built agent image in OCI registry (including ACR)
  + source AgentSuite pinned by OCI digest
  + image execution contract selected by the suite build profile
  + destination environment
    -> validate and plan
    -> Kubernetes Deployment + ClusterIP Service
    -> current-generation readiness and deployment receipt
```

The first adapter is `kubernetes-http-v1`: one agent HTTP service on a prepared
Kubernetes cluster, including AKS. This is standalone image execution rather than
agent-platform registration. It requires no AgentSuite CRD and creates no cluster,
namespace, credentials, or inference deployment. ToolProviders and invocation
edges are refused until an adapter implements their complete contract.

## Execution and image contract

A build profile can declare:

```json
{
  "execution": {
    "kind": "kubernetes-http-v1",
    "protocol": "openai-chat-v1",
    "port": 8080,
    "healthPath": "/healthz",
    "inputs": [
      {"name": "inference-key", "environment": "MODEL_API_KEY", "secret": true},
      {"name": "agent-auth", "environment": "AGENTKIT_AUTH_TOKEN", "secret": true},
      {"name": "listen", "environment": "AGENTKIT_BIND", "secret": false}
    ]
  }
}
```

The image config label `org.agentsuite.image-deployment` contains a JSON record
with `schemaVersion: 1.0.0-draft`,
`mediaType: application/vnd.agentsuite.image.deployment.v1+json`, an exact
registry `suiteReference` ending in `@sha256:...`, the matching `suiteDigest`,
`agent`, `platform`, `compositionDigest`, `buildProfile`, and that same `execution`
object. `agentsuite.EncodeImageDeployment` is the builder integration helper.
The label is committed by the OCI config descriptor and image manifest digest;
it is not a signature, a substitute for the sandbox binding, or a conformance
claim about filesystem contents.

`kubernetes-http-v1` requires an explicit entrypoint and numeric non-root image
user. The process serves OpenAI-compatible `/v1/chat/completions` requests on the
declared port, has an unauthenticated readiness endpoint at `healthPath`, listens
on the Pod interface, and handles Kubernetes termination signals. It runs with
a read-only root filesystem and a fresh, size-limited `/tmp` volume. It receives
no Kubernetes service-account token. It must not require other writable paths.
The image fixes its invocation protocol; environment slots cannot change
`AGENTKIT_PROTOCOL` or `AGENTKIT_PORT`. Runtime protocol conversion and agent
registration are separate adapter contracts.

Every input slot is required and must be bound exactly once. Secret slots accept
only Secret name/key references. Literal slots are non-secret runtime settings;
only expose slots the harness actually supports. A fixed model URL remains fixed
in the image. To support per-environment inference URLs, the harness must read a
declared non-secret endpoint slot, which must match the Agent's `model.endpointEnv`.

## Deployment environment and use

Create the destination namespace and required Secrets separately, and configure
AKS image-pull access to ACR. Workstation registry login does not configure node
image-pull authentication. Inspect the target identity with:

```sh
kubectl --context production-aks get namespace kube-system -o jsonpath='{.metadata.uid}'
az acr login --name myregistry
```

Example `production.json` (replace `clusterUID` with that observed UID):

```json
{
  "apiVersion": "kaimahi.dev/lift/v1alpha1",
  "name": "hello-world",
  "context": "production-aks",
  "clusterUID": "replace-with-observed-cluster-uid",
  "namespace": "agents",
  "adapter": "kubernetes-http-v1",
  "platform": {"os": "linux", "architecture": "amd64"},
  "inputs": {
    "inference-key": {"secretRef": {"name": "model-access", "key": "api-key"}},
    "agent-auth": {"secretRef": {"name": "agent-access", "key": "token"}},
    "listen": {"value": "0.0.0.0"}
  }
}
```

Optional `imagePullSecrets` names existing pull Secrets when node identity is
not used. The environment name identifies this installation and its owned
Deployment/Service. It is independent of the agent identifier inside the suite.

```sh
kmx lift "$REGISTRY/hello-world:v1" --environment production.json --plan
kmx lift "$REGISTRY/hello-world:v1" --environment production.json
```

The existing remote-context confirmation applies to execution. `--plan` performs
read-only target inspection and server-side dry-run admission. Output contains
deployment identities, not literal environment values or Secret data. Execution
prints a JSON receipt to stdout, including partial results on failure; redirect
it to retain local evidence. It does not persist a release history automatically.

The Service is ClusterIP-only. Use an explicitly scoped port-forward for a smoke
test; readiness alone is not evidence that inference or agent authentication
works. No ingress, public load balancer, or automatic test request is created.

## Implementation map

1. Define a closed, versioned execution contract on build profiles and a
   digest-bound image-deployment record in the OCI image configuration.
2. Resolve a registry image tag once, select one exact platform manifest, verify
   manifest/config descriptors, and validate its deployment record. Reuse the
   existing Docker credential-store authentication, including `az acr login`.
3. Pull and validate the exact source suite, select the recorded agent,
   composition and build profile, and compare their execution contract with the
   image record. Image metadata is a publisher declaration, not build provenance.
4. Decode an explicit deployment environment: context, cluster UID, namespace,
   platform, adapter contract, and per-slot literal or Secret-key bindings.
5. Render a deterministic Deployment and Service using the selected image digest.
   Keep rendering independent of CLI and cluster access.
6. Inspect the exact target and existing resources, refuse foreign ownership,
   use resourceVersion/UID checks for updates, and never force conflicts.
7. Support `--plan`, preserve the remote-context guard, wait for rollout, and
   return a receipt with image/suite/plan/resource identities. Partial effects
   return an error, not automatic rollback or a claim of atomic deployment.
8. Test registry resolution, closed schemas, compatibility failures, rendering,
   target/ownership checks and CLI routing with local fakes.

## Integration dependencies

- This branch extends #339 with source-suite verification and deployment record
  emission. Upstream metadata placement and full execution qualification remain
  review items. A fixed model endpoint cannot be overridden by lift.
- Full AgentSuite sandbox inventory/binding/provenance validation remains #307.
  The image-deployment record identifies declared source and execution inputs;
  it does not assert that image filesystem composition is conformant.
- Suite-reference input uses explicit environment member-image references.
  Automatic suite-to-image association discovery remains separate work.
- Agent platforms and session services require their own qualified execution adapters.
- ToolProviders, delegation, runtime model substitution, evaluation gates,
  persistent deployment history, retirement and recovery need further contracts.
- A real private-ACR-to-AKS smoke must qualify workload image-pull identity,
  inference access, readiness, and an authenticated request. Unit tests do not
  substitute for that online proof.

## Verification on this branch

The contract, registry metadata resolver, environment renderer and reconciler
are covered by tests, including wrong platforms, ambiguous indexes, unmarked or
root images, stale composition/contract identity, duplicate/unknown environment
fields, literal secrets, foreign resources, target replacement and partial writes.

```sh
go test ./internal/kmx/agentsuite/... ./internal/kmx/imagelift ./cmd/kmx -count=1
go vet ./...
python3 scripts/check-doc-links.py
python3 scripts/check-repository-map.py
git diff --check
```

## Command compatibility

`kmx lift <image-reference> --environment <file> [--plan]` selects image lift.
The existing no-positional-argument managed-cluster route and `lift down` retain
their behavior and print their existing replacement guidance. Image requests
reject infrastructure flags, so a malformed deployment cannot provision AKS.
