# Runtime support

Orka is the current default for create, chat, lift and quickstart. Kagent is
create-only for an already-installed exact v0.10.2. agentsessions is eval-only
until its lifecycle adapter lands; local reference-chat verification accompanies
that evaluation path. None of these facts sets a preference in `kmx targets`.

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

Table and JSON rows are alphabetical: **agentsessions, kagent, orka**. The tiers
are `eval-only`, `create-only` and `supported`; they describe compiled command
support, not installed-target qualification or feature parity. Each operation
has its own support flag and reason. Research candidates do not appear here.

Without probe flags, the command loads no operational configuration, runs no
tools, contacts no host and writes no state. It does not select, install or
deploy a runtime, and does not change other commands' defaults.

- `--detect` reads controller Deployments in `orka-system`, not workload
  namespaces. It uses existing `--context`, `KUBE_CTX`, saved `kmx ctx`, or kind
  context resolution and an available kubectl; it downloads nothing. A labelled
  controller is `present`; a successful list with no matching controller is
  `absent` within that namespace. Only the exact pinned controller image digest
  identifies a qualified version. Tags, sidecar images and foreign digests
  leave the version unknown. Presence proves neither readiness nor a result.
- `--sessions` independently reads one Session page and discards its metadata.
  It needs no Kubernetes configuration unless `--detect` is also supplied.
  Remote hosts require verified TLS; `--sessions-ca` supplies a private CA and
  also enables TLS on loopback. Plaintext is allowed only on literal loopback.
  A readable API proves no host version, harness, model or image identity.
  HarnessRegistry is not probed.
- Kagent is not discovered. Only explicit create checks its installation.

Unrequested reads are `not-probed`. Failed authentication, RBAC, network,
timeout or malformed-response reads are `unreadable`, never absent or permission
to fall through. Fixed diagnostics include typed codes and actions, not remote
bodies. The complete report preserves independent successful probes, then exits
nonzero if any requested probe is unreadable.

## Revision-pinned support and qualification matrix

**Evidence snapshot: 2026-10-10.** Rows are alphabetical, not ranked. Source and
artifact pins identify the tested scope; they do not imply support for every
version or optional operation. Native readiness alone is not semantic proof.

| Runtime | Tier and operations | Pin and evidence | Preparation owner |
|---|---|---|---|
| agentsessions | `eval-only`: text-only evaluation, one Session per case; local reference-chat verify with zero live model calls. No deploy/status/retire/re-lift or Orka lift gate. | Module `v0.1.3-0.20261008190619-e680ea10d3b4`, [source](https://github.com/aramase/agentsessions/tree/e680ea10d3b4). [Live eval CI](../scripts/ci/live-eval-loop.sh) builds that daemon and pins its AIKit image. Neither receipt nor replay attests the remote host/version/image. | Operator supplies host, journal and matching model; KMX runs no infrastructure. |
| kagent | `create-only`: render and new-object create against exact v0.10.2; optionally one A2A turn within create. No standalone run, chat, discovery, status, evaluation, retirement, reconciliation or rollback. | [Source `68df64f671800c37c4204d81ebe0dd66ec35d223`](https://github.com/kagent-dev/kagent/tree/68df64f671800c37c4204d81ebe0dd66ec35d223); controller OCI index SHA-256 `6adeef9ac70056e5871773a78233a77f12d1357f9f3484aaee59fe8abc6450b9`. [CI](../.github/workflows/ci.yml) pins official charts and proves exact controller/commit/images, admission and an answer. | Operator; CI installs externally before KMX create. |
| orka | `supported`: render, deploy/reconcile, status, evaluation, chat, one-shot run and native workload retire. Retire deletes/releases workload resources, not retained registrations or target infrastructure. | [v0.2.0 source](https://github.com/orka-agents/orka/tree/5f4eb543b2b35a3afb8e7ea01f5f53985e25d4c1); chart SHA-256 `b7596c4e35d7189a3b2cf25921cd50e6c31328dbb83bdee50e20c757e0f03b79`; controller `ghcr.io/orka-agents/orka@sha256:7c1727f92d5c0cf05d464c6eb9e70b8342cb5a7a87c2e35338f051d7b85aa370`. Required Orka CI proves a native result, not every optional capability. | Operator or an explicit KMX Orka environment command; targets does neither. |

No standalone `logs`, arbitrary `diff` or `rollback` capability is advertised.
Orka status reports drift; that is not a universal diff API. The view explains
unsupported operations without invoking them.

### agentsessions adapter prerequisite

[#100](https://github.com/aramase/agentsessions/pull/100), merged at
`8d97860b4b9c923b4140b6f5bfcadb718d689014`, adds a persisted **in-process**
HarnessRegistry. At that pin, `agentsessionsd` does not serve the registry.
An operator-only admin listener is required; startup `--harness` flags and
static built-in `chat` are not deployable registrations.

The adapter must bind registration to an exact source/suite member and immutable
image. [Conformance](https://github.com/kaimahi-agents/kaimahi/issues/352) must prove
a bound Session result, restart/unknown outcomes, retained retirement and
reactivation. Descriptor IDs and replay versions are host-reported, not
authentication or image attestation. AgentKit remote-harness profiles await
[#34](https://github.com/orka-agents/agentkit/pull/34) and
[#35](https://github.com/orka-agents/agentkit/pull/35); arbitrary-harness verify
awaits [agentsessions #112](https://github.com/aramase/agentsessions/issues/112).

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
| Kagent 1.0 alpha11 — unsupported runtime candidate | [Source `30e8a2c2ceb9a1fadc16e969aa7ee0107a42d7b2`](https://github.com/kagent-dev/kagent/tree/30e8a2c2ceb9a1fadc16e969aa7ee0107a42d7b2) | Revisit at beta; require immutable installation, readiness, revision-pinned definition, documented execution and a bound result. Existing create support is unchanged. |
| Kubernetes Agent Sandbox — workspace substrate | [v1.0.4 `810726d89c71da77cdc82668bca1b00f5cd21ed8`](https://github.com/kubernetes-sigs/agent-sandbox/tree/810726d89c71da77cdc82668bca1b00f5cd21ed8) | Consume beneath a runtime. Sandbox readiness is not agent/evaluation evidence. |
| Mecatl — agent harness/runtime | [v0.0.41 `89316cd9ed449e2577344723b6df17dec4cde034`](https://github.com/stacklok/mecatl/tree/89316cd9ed449e2577344723b6df17dec4cde034) | Require immutable installation, revision-bound terminal results and workload-only cleanup. |
| OpenShell — sandbox/image target | [v0.1.2 `6648bd0c290efbc41ba131ee9831ee45cd431f94`](https://github.com/NVIDIA/OpenShell/tree/6648bd0c290efbc41ba131ee9831ee45cd431f94) | Sandbox access is not authored-agent evidence; require a complete image input/result contract. |

Frameworks/harness libraries such as AgentKit, ADK, MAF and LangGraph are not
automatically lifecycle targets. Foundry, Copilot, Ollama, KServe, vLLM and KAITO
are inference backends, not runtime adapters. Qualification ownership is in
[#238](https://github.com/kaimahi-agents/kaimahi/issues/238); the
[Substrate evaluation](reviews/2026-09-10-substrate-evaluation.md) explains the
workspace boundary. The [AX activity protocol](../ax-harness/README.md) covers
its checkout-only projector, not a built or installed adapter.

## Terms and ownership

- **Runtime:** executes the agent and owns native enforcement and retention.
- **Target:** selected runtime, destination and deployment scope. A friendly
  context/name is an access locator, not durable destination identity.
- **Adapter:** compiled KMX implementation of supported runtime operations.
- **Inference backend/provider:** independent model route.
- **Workspace substrate:** execution machinery beneath a runtime.

KMX names mutation targets and obtains consent. Workload retirement never grants
cluster/shared-infrastructure teardown authority. A model-traffic bridge's
controls are not proof of governance for every runtime operation.

## Adapter contract implemented today

`internal/kmx/runtime` carries neutral identity, capabilities, typed events and
lifecycle contracts, without Kubernetes, terminal or vendor SDK types. Adapters
validate and render only behavior they consume; unsupported verbs return typed
refusals. Discovery errors do not trigger fallback. The chat registry contains
only Orka; Kagent is composed by explicit create, not discovery or chat.

A Session exposes its resolved agent, capabilities, commands, status, events,
`Send` and `Close`. Events are observations; only successful `Send` means its
terminal checks completed. Terminal presentation stays outside the contract.
The target catalog is diagnostic composition, not another registry or SDK.

## Portable revision and target bindings

`agent.yaml` is a closed, versioned definition: core fields plus zero or one
runtime extension. Unknown fields and multiple extensions fail. Its exact
bytes, including whitespace/comments, determine the portable digest.
`PreparePortableRender` rejects extension or coordination behavior the adapter
cannot honor before rendering. Orka accepts named-helper coordination; Kagent
refuses it. Model/provider selection does not select a runtime.

Creation `bindings.yaml` holds namespace, model endpoint and Secret name/key
references, never credentials. Bindings do not change the portable digest;
rendered resources have their own digest. Review-only Secret skeletons must not
be bulk-applied. Later Orka lift uses explicit target flags and saved state;
Kagent bundles have no later lifecycle consumer. See
[bundle format](bundle-format.md) for fields and compatibility rules.

Receipts record identities and digests, not prompts or answers. agentsessions
sends instructions as execution config and checks the host-selected model;
Session metadata does not choose it. Config is journaled, so it must contain no
credentials. Runtime cancellation/history and target cleanup remain separate.

## Experimental wider interface model

`pkg/kmx` is alpha, unwired design evidence, not a replacement for the production
runtime contract. `AgentEnvironment`, `AgentSuites` and `AgentDeployments`
separate target preparation, artifact/image shipping and workload placement.
Internal ports and scoped receipt factories live in `internal/kmx/lifecycle`.
Retirement receipts grant no target teardown authority. Read the
[lifecycle decision](kmx-lifecycle-interfaces.md) and
[application API](kmx-application-api.md) for signatures, persistence and recovery
boundaries. Concrete orchestration remains follow-up work.
