# Runtime support

Orka is the default native workspace runtime, chat and quickstart runtime.
Explicit kagent workspaces use exact v0.10.3 Go/linux-amd64. Both packaged
profiles support root evaluation, evaluated publication and digest Lift with
runtime-specific evidence/bindings. Kagent Lift is create-only, refusing occupied
targets. Pinned AgentSessions chat separately supports native offline authoring,
prepare/source-free load/ordinary push and explicit `--development-host FILE`
evaluation/full-pass proof for exact-S evaluated push. It requires a private
host JSON with a literal bare loopback host:port and typed matching host/model
observations; no cluster settings or selectors, tools, auth, deploy/Lift,
chat/status or remote lifecycle. Live create and legacy bundle lifecycle/readers/
receipts/replay are removed. GET-only destination status is separate inspection;
legacy status readers and retirement remain unavailable.
Independent reference extraction/export and availability probes remain.
These facts set no preference in `kmx targets`.

The selected runtime owns execution, credentials, policy and retention. KMX owns
authoring, revision identity, target-aware lifecycle commands and evidence.
Supported operations differ by runtime; model selection is a separate choice.

## Read-only target view

```bash
kmx targets                         # offline table
kmx targets -o json                 # schemaVersion 1
kmx --context <context> targets --detect
kmx targets --sessions 127.0.0.1:8080
kmx targets --detect --sessions <host:port> --sessions-ca <ca.pem>
```

Table and JSON rows are alphabetical: **agentsessions, kagent, orka**. Tiers
describe compiled command support, not installed-target qualification or
feature parity. Each operation
has its own support flag and reason. Research candidates do not appear here.

Without probe flags, the command loads no operational configuration, runs no
tools, contacts no host and writes no state. It does not select, install or
deploy a runtime, and does not change other commands' defaults.

- `--detect` reads controller Deployments in `orka-system`, not workload
  namespaces. It uses existing `--context`, `KUBE_CTX`, a saved
  `kmx context use` selection, or kind context resolution and an available
  kubectl; it downloads nothing. A labelled controller is `present`; a successful list with no matching controller is
  `absent` within that namespace. Both kubectl's `List` and the API's
  `DeploymentList` envelopes are accepted; missing or null `items` is malformed,
  and generic `List` items must identify themselves as Deployments.
  Only the exact pinned controller image digest
  identifies a qualified version. Tags, sidecar images and foreign digests
  leave the version unknown. Presence proves neither readiness nor a result.
- `--sessions` independently reads one Session page and discards its metadata.
  It needs no Kubernetes configuration unless `--detect` is also supplied.
  Remote hosts require verified TLS; `--sessions-ca` supplies a private CA and
  also enables TLS on loopback. Plaintext is allowed only on literal loopback.
  A readable API proves no host version, harness, model or image identity.
  HarnessRegistry is not probed.
- kagent is not discovered. Explicit version-qualified operations check its
  installation; the target view does not grant mutation authority.

Unrequested reads are `not-probed`. Failed authentication, RBAC, network,
timeout or malformed-response reads are `unreadable`, never absent or permission
to fall through. Fixed diagnostics include typed codes and actions, not remote
bodies. The complete report preserves independent successful probes, then exits
nonzero if any requested probe is unreadable.

## Revision-pinned support and qualification matrix

**Evidence snapshot: 2026-10-14.** Rows are alphabetical, not ranked. Source and
artifact pins identify the tested scope; they do not imply support for every
version or optional operation. Native readiness alone is not semantic proof.

| Runtime | Tier and operations | Pin and evidence | Preparation owner |
|---|---|---|---|
| agentsessions | Pinned-chat native suite author/prepare/source-free load/ordinary push; explicit literal-loopback operator-observed development eval with full-passing private evidence and exact-S evaluated push. One fresh Session/config/Exec per ordered case run, shared explicit judging and repeat policy, no tools, cluster settings, registry/auth or remote lifecycle; Lift/chat/status refuse. Independent read-only reference extraction/export and availability probing remain; removed legacy bundle replay/evaluation is not this native path. | Consumer SDK `github.com/agent-primitives/sessions v0.2.0`; development runtime remains pinned to [source](https://github.com/agent-primitives/sessions/tree/ca85f47a2ec17ca6e16029cfa69419cfe29442b3). The SDK pin does not qualify a v0.2.0 daemon or change runtime/proof identities. The pinned development daemon/config/journal path was demonstrated once with an instruction-sensitive deterministic OpenAI-compatible fixture, not real-LLM quality or authenticated host/build/model attestation. Complete canonical five-record journals bind exact chat/config/input/model-request fingerprints; metadata-only receipts never include journal bodies. Receipt diff refuses AgentSessions proof. | Operator supplies pinned host, journal and matching model; KMX starts no infrastructure. |
| kagent | Exact v0.10.3 Go/linux-amd64 native workspace create/eval/evaluated push/create-only digest Lift. Eval sends fresh blocking A2A Tasks, unsecure only; trusted-proxy refused. Lift sends no Tasks and refuses occupied targets. GET-only destination status observes placement without qualifying images/auth. No Python/RemoteMCP execution, standalone run/chat, installer/discovery, retirement or rollback. | [v0.10.3 source](https://github.com/kagent-dev/kagent/tree/8878c39ac64421cb665c8e62ecc6411dd12f2fc2), controller index `b2572a59386b6bac1f5e652e135fdad54c0ee603d3f9ee60eb7c80bbaedf9b76`. [Qualifier](../scripts/ci/kagent-lift.sh): raw upstream Tool smoke/direct A2A MCP proof, separately tool-free two-case packaged eval/evaluated push/source-withheld digest Lift and occupied-repeat refusal. Final retirement Go build/live run are required before claiming qualification. | Operator supplies exact runtime, namespace and model Secret; CI bootstrap is external. |
| orka | Native workspace create/eval/evaluated push/digest Lift, runtime setup/status and independent live run/chat. Suite-owned objects refuse legacy invocation; GET-only destination status is available, not retirement. | [v0.2.0 source](https://github.com/orka-agents/orka/tree/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1); chart SHA-256 `b7596c4e35d7189a3b2cf25921cd50e6c31328dbb83bdee50e20c757e0f03b79`; controller `ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370`. Required Orka CI proves a native result, not every optional capability. | Operator or an explicit KMX Orka environment command; targets does neither. |

The [offline workspace path](suite-workspace.md) also authors, packages, loads
and publishes a one-member, tool-free kagent v0.10.3 Go/linux-amd64 declaration.
Runtime is KMX-private metadata bound to the OCI manifest, not a public format
field or deployment-time selector. Private suite-owned kagent runtime helpers
now prepare, place new objects and observe them, with complete caller-retained
proof required for read-only reuse. Root suite `eval --bindings FILE` now executes
all packaged cases with fresh blocking A2A Tasks and exports only full passing
runtime-specific suite evidence. It requires auth: unsecure only; trusted-proxy
is refused before admission/send. No Secret is created. The placement helpers
feed the shared release-lift seam: root digest lift consumes runtime-matching
suite evidence and author-owned bindings, creates new ModelConfig/Agent resources
only, and refuses any occupied target. Explicit same-package/model/profile promotion
retains AuthMode and the full controller/Go image digest/platform inventories;
source evidence is not destination evaluation or authenticated authorization.
Destination status reads unsigned per-object cluster metadata without evaluation
or local history. Retirement remains unavailable and Orka evidence never authorizes kagent. The declared build-profile images are inert, not Go ADK dependencies.
Live v0.10.2 create and all legacy bundle operations are removed outright.
Offline `eval diff` reads only strict full-passing native suite Orka/kagent
evidence; old Orka/kagent/AgentSessions bundle receipts are unreadable. No
migration or on-disk/resource deletion occurs. No logs, arbitrary native-resource
diff, rollback, legacy status reader or retirement is advertised.

### agentsessions remote lifecycle prerequisite

[#100](https://github.com/agent-primitives/sessions/pull/100), merged at
`8d97860b4b9c923b4140b6f5bfcadb718d689014`, adds a persisted **in-process**
HarnessRegistry. At that pin, `agentsessionsd` does not serve the registry.
An operator-only admin listener is required for remote registration lifecycle;
startup `--harness` flags and static built-in `chat` are not deployable
registrations. This prerequisite does not block the implemented pinned-chat
native development evaluation above.

The adapter must bind registration to an exact source/suite member and immutable
image. [Conformance](https://github.com/kaimahi-agents/kaimahi/issues/352) must prove
a bound Session result, restart/unknown outcomes, retained retirement and
reactivation. Descriptor IDs and replay versions are host-reported, not
authentication or image attestation. AgentKit remote-harness profiles await
[#34](https://github.com/orka-agents/agentkit/pull/34) and
[#35](https://github.com/orka-agents/agentkit/pull/35); arbitrary-harness verify
awaits [agentsessions #112](https://github.com/agent-primitives/sessions/issues/112).

### Unqualified candidates

These research pins have no compiled KMX lifecycle support. Operators prepare
them; KMX does not install them, publish images or own their infrastructure.
Before support, qualify immutable artifacts, Secret references, exact
revision/result binding and workload-only cleanup. Render/deploy/status/eval,
chat/logs/diff/rollback and cleanup remain unsupported for these candidates.

| Candidate / category | Pinned source | Blocker / stop rule |
|---|---|---|
| Agent Runtime Operator — Kubernetes runtime | [v0.28.1 `cc21d887c14121086a95c54c7122f6786001afed`](https://github.com/agentic-layer/agent-runtime-operator/tree/cc21d887c14121086a95c54c7122f6786001afed) | Stop at schema/readiness-only evidence; require revision-bound execution and scoped cleanup. |
| Agent Substrate — workspace/execution substrate | [v0.2.0 `10a1bfb2f58039608a0b8419379714945e4d65ac`](https://github.com/agent-substrate/substrate/tree/10a1bfb2f58039608a0b8419379714945e4d65ac) | Consume beneath a runtime. No peer adapter without an independent full lifecycle; retain required templates/snapshots. |
| AX — image execution target | [v0.3.1 `e70162a34037c221fe6fadefd98308c05a4ad8f3`](https://github.com/google/ax/tree/e70162a34037c221fe6fadefd98308c05a4ad8f3) | Preview projector is not an adapter/image. Require immutable instructions/model/image/result, logs/cancellation and scoped cleanup. |
| KARS — agent/security platform, BYO-image | [v0.1.26 `4288a3a00e88076a0c779c40fccb90790d4de6db`](https://github.com/Azure/kars/tree/4288a3a00e88076a0c779c40fccb90790d4de6db) | Requalify definition/image, policy and result APIs. Stop if input/result identity or workload-only cleanup cannot be proven. |
| kagent 1.0 alpha11 — unsupported runtime candidate | [Source `30e8a2c2ceb9a1fadc16e969aa7ee0107a42d7b2`](https://github.com/kagent-dev/kagent/tree/30e8a2c2ceb9a1fadc16e969aa7ee0107a42d7b2) | Revisit at beta; require immutable installation, readiness, revision-pinned definition, documented execution and a bound result. Native exact-v0.10.3 Go support does not extend to this pin. |
| Kubernetes Agent Sandbox — workspace substrate | [v1.0.4 `810726d89c71da77cdc82668bca1b00f5cd21ed8`](https://github.com/kubernetes-sigs/agent-sandbox/tree/810726d89c71da77cdc82668bca1b00f5cd21ed8) | Consume beneath a runtime. Sandbox readiness is not agent/evaluation evidence. |
| Mecatl — agent harness/runtime | [v0.0.41 `89316cd9ed449e2577344723b6df17dec4cde034`](https://github.com/stacklok/mecatl/tree/89316cd9ed449e2577344723b6df17dec4cde034) | Require immutable installation, revision-bound terminal results and workload-only cleanup. |
| OpenShell — sandbox/image target | [v0.1.2 `6648bd0c290efbc41ba131ee9831ee45cd431f94`](https://github.com/NVIDIA/OpenShell/tree/6648bd0c290efbc41ba131ee9831ee45cd431f94) | Sandbox access is not authored-agent evidence; require a complete image input/result contract. |

Frameworks/harness libraries such as AgentKit, ADK, MAF and LangGraph are not
automatically lifecycle targets. Foundry, Copilot, Ollama, KServe, vLLM and KAITO
are inference backends, not runtime adapters. Qualification and the Substrate
workspace boundary are tracked in
[#238](https://github.com/kaimahi-agents/kaimahi/issues/238).
The [AX activity protocol](../ax-harness/README.md) covers
its checkout-only projector, not a built or installed adapter.

## Terms and ownership

- **Runtime:** executes the agent and owns native enforcement and retention.
- **Target:** selected runtime, destination and deployment scope. A friendly
  context/name is an access locator, not durable destination identity.
- **Adapter:** compiled KMX implementation of supported runtime operations.
- **Inference backend/provider:** independent model route.
- **Workspace substrate:** execution machinery beneath a runtime.

KMX names mutation targets and obtains consent. Workload retirement never grants
cluster/shared-infrastructure teardown authority. Model-route configuration
is not proof of enforcement for every runtime operation.

## Adapter contract implemented today

`internal/kmx/runtime` carries neutral identity, capabilities, typed events and
lifecycle contracts, without Kubernetes, terminal or vendor SDK types. Adapters
validate and render only behavior they consume; unsupported verbs return typed
refusals. Discovery errors do not trigger fallback. The chat registry contains
only Orka; packaged kagent is composed explicitly for native workspace
preparation/evaluation/Lift, not discovery or chat. Packaged kagent evaluation and
digest Lift use separate private App seams feeding the common release engine.
Experimental public ports remain unwired.

A Session exposes its resolved agent, capabilities, commands, status, events,
`Send` and `Close`. Events are observations; only successful `Send` means its
terminal checks completed. Terminal presentation stays outside the contract.
The target catalog is diagnostic composition, not another registry or SDK.

## Native revision and target bindings

Native workspace metadata binds runtime/profile/model and cases to an exact OCI
manifest. Orka requires a Ready openai Provider; kagent takes author-owned
bindings with existing Secret name/key references, never values. Runtime/model
are not overridden during Lift. Secret metadata/key-presence observations and
current identity ResourceVersion brackets remain part of private proof.

Internal PortableAgent typed scaffolding may feed native rendering; this is not
a persistent legacy `agent.yaml` reader/writer. Public AgentSuite/ToolProvider
implementation-bundle graphs/build/pull remain separate portable formats; see
[format boundaries](bundle-format.md). Native receipts contain metadata and
digests, never prompts/answers, and do not authenticate callers or runtime hosts.

## Experimental wider interface model

`pkg/kmx` is alpha, unwired design evidence, not a replacement for the production
runtime contract. Setup-only `AgentEnvironment`, `AgentSuites` and
`AgentDeployments` separate logical environment establishment, validation plus
the initial image-backed build/push workflow, and workload placement. An
environment is not an exact deployment target or runtime selection. Image-free
native execution profiles remain outside that first build contract. Member
builders and OCI storage transports remain internal.
Focused suite ports live in `internal/kmx/agentsuite`; deployment runtime ports
and scoped receipt factories live in `internal/kmx/lifecycle`.
Retirement receipts grant no environment teardown authority. Read the
[lifecycle decision](kmx-lifecycle-interfaces.md) and
[application API](kmx-application-api.md) for signatures, persistence and recovery
boundaries. Concrete orchestration remains follow-up work.
