# AgentKit overlap and AgentSuite build-profile proposal

[AgentKit](https://github.com/orka-agents/agentkit) is a BuildKit frontend that
compiles an `Agentkitfile` into a runnable OCI agent image. Kaimahi's
[AgentSuite draft](../agentsuite-spec.md) defines a portable agent definition
and says how a runnable Agent Sandbox Image is derived from it (§11–13), but
Kaimahi ships no builder. The two projects overlap on agent definition and
packaging, and they complement each other on everything else.

This note has three parts:

1. Code-level findings.
2. A field-by-field mapping between the formats.
3. A proposal to make AgentKit an AgentSuite build profile instead of a
   competing format.

The AgentSuite spec's informative reference still points to the pre-move
`sozercan/agentkit` repository. The `ai.sozercan.agentkit` label namespace
survives in AgentKit as a reserved legacy prefix
(`pkg/agentkit/config/labels.go`).

## Source baseline

Read on 2026-10-06. This is a code reading only; nothing was built or run.

| Source | Revision inspected |
| --- | --- |
| Kaimahi main | `b8a78af` |
| AgentKit main | `2f33945` |
| Orka Agent CRD | Kaimahi fixtures `internal/kmx/orkaschema/fixtures/v0.2.0` |

## What each side actually implements

### AgentKit

- **Build.** The build is one LLB step: `llb.Image(adapterRef)` plus one file,
  `/agent/agent.yaml` (`pkg/agentkit2llb/agent/convert.go`).
  - No tools, payloads or dependencies are copied in.
  - The adapter image defaults to a **floating tag**,
    `ghcr.io/orka-agents/agentkit/serve-<runtime>:latest`
    (`pkg/agentkit/runtimes/catalog.go`). It can be overridden with
    `--build-arg adapter=`.
  - Each adapter is a single-stage `FROM python:3.12-slim-bookworm` image
    running as `USER 1000` (`runtimes/*/Dockerfile`).
- **ABI.** `/agent/agent.yaml` is `abiVersion: v0`, with instructions resolved
  and **inlined** as a string (`pkg/agentkit/abi/render.go`). Image labels
  carry runtime, ABI, protocols and capabilities. The label
  `ai.orka.harness.version` is hard-coded to `orka.harness.v1`, even in images
  that are later run under harness v2 (`pkg/agentkit2llb/agent/image.go`).
- **Tools.** Every tool is an MCP *server*:
  - Stdio servers are an arbitrary `command`; the example in the `Tool` doc comment is
    `npx -y @modelcontextprotocol/server-fetch`. That resolves and downloads at
    container start.
  - Remote servers are `urlEnv` over streamable HTTP.
  - There is no operation allowlist. The model sees whatever operations the
    server advertises.
  - `image` tool sources are declared but rejected by the validator
    (`pkg/agentkit/config/validate.go`, `Validate`).
- **Capability gate.** `requiredCapabilities()` is checked against
  `RuntimeSpec.Capabilities`. No runtime in the catalog advertises
  `tool-approval` or `otel-export`. So in practice `tools[].approval:
  auto|always` and `observability.otel.endpointEnv` are rejected for every
  runtime today, even though the schema accepts them.
- **Harness v2 / ACP strict mode** (`runtimes/common/agentkit_serve_common/acp.py`,
  `validate_acp_runtime_binding`):
  - It rejects any baked `tools` or `brokeredTools`. Under v2 the image must be
    tool-less.
  - Tools arrive per session from the Orka supervisor as **at most one**
    loopback MCP server.
  - `AGENTKIT_ACP_AGENT_CONFIGURATION_DIGEST` must equal raw `sha256` of the
    exact `/agent/agent.yaml` bytes. Note: this is not JCS.
  - Model and provider must be loopback (`AGENTKIT_ACP_PROVIDER_BASE_URL`).
    `model.baseURL` from the file is ignored in this mode.
- **Orka deployment.** `agentkit render --target orka-agentruntime` emits only
  an `external-endpoint`, harness v1 `AgentRuntime`, and refuses `--image`
  (`pkg/agentkit/render/orka.go`). The v2 path is a manual operator procedure:
  1. Compose the supervisor image in an Orka checkout.
  2. Register `adapterName`/`adapterDigest`.
  3. Fence the controller epoch.

  See AgentKit `docs/orka.md`.
- **Not present:** multi-agent invocation (`expose` mcp/a2a is rejected), cost
  or budget control, and cluster bootstrap.

### Kaimahi

- **The `agentsuite` package is a validator only.** It checks the OCI layout,
  JCS identities, tool variant inventories, bundle closures, compositions and
  build profiles (`internal/kmx/agentsuite/validate.go`, `oci.go`).
  - There is no implementation of §12 image construction.
  - Nothing writes `/.agentsuite/binding.json`.
  - `ValidateSandboxBinding` only parses one.
- **The package is `internal/`**, so AgentKit, or any other module, cannot
  import it. Any shared builder needs it moved or published.
- **Orka targeting.** `PortableAgent` and `OrkaAgentExtension` expose tools,
  skills, rate limits and coordination (`internal/kmx/runtime/portable.go`).
  They have **no `runtimeRef`**, although the Orka v0.2.0 Agent CRD Kaimahi
  pins supports `spec.runtime.runtimeRef` → `AgentRuntime`
  (`orkaschema/fixtures/v0.2.0/agents.yaml`). So `kmx` cannot point an agent at
  an AgentKit runtime today.
- **Harness mode.** `kmx orka install` sets `controller.mode=harness-v2`. In
  the default Kaimahi cluster, any AgentKit image would therefore run in ACP
  strict mode, where baked tools are forbidden.

## Field mapping

Columns:

- **Agentkitfile**: AgentKit's authoring format.
- **AgentSuite**: Kaimahi's draft packaging format. *agent* means an agent
  manifest, *tool* a tool manifest, *profile* a build profile.
- **PortableAgent**: Kaimahi's current `agent.yaml`.

✅ means a direct mapping, ⚠️ a lossy or semantic mismatch, and ❌ no
counterpart.

| Agentkitfile | AgentSuite | PortableAgent | Notes |
| --- | --- | --- | --- |
| `apiVersion: v1alpha1`, `kind: Agent` | `schemaVersion: 1.0.0-draft`, `mediaType` | `apiVersion: kmx.kaimahi.dev/v1alpha1`, `kind` | ✅ envelope only |
| `metadata.name` | agent `id` | `metadata.name` | ⚠️ AgentSuite requires `^[a-z0-9][a-z0-9._-]{0,127}$`; AgentKit only requires non-empty |
| `metadata.labels` | — (could be an `extensions` record) | — | ❌ |
| `debug` | — | — | ❌ build-time concern |
| `runtime` (`pydantic-ai`, `maf`, `langgraph`) | profile `harness[]` (digest-pinned per platform) | `extensions.kagent.runtime` only | ⚠️ AgentKit picks a runtime by name, AgentSuite by image digest. This is the hook for the proposal below |
| `model.provider: openai-compatible` | agent `model.protocol` | — | ✅ same string |
| `model.name` | agent `model.model` | `spec.model.name` | ✅ |
| `model.baseURL` (a literal URL baked into the image) | agent `model.endpointEnv` (an env **name**) | — (Orka Provider) | ⚠️ AgentSuite never bakes the endpoint. AgentKit would need a `baseURLEnv` variant. ACP mode ignores `baseURL` anyway |
| `model.apiKeyEnv` (env name) | agent `model.secretRefs[]` (secret **ids**) | — (Provider `secretRef`) | ⚠️ different indirection. Needs a deploy-time binding, as Kaimahi's `bindings.yaml` does |
| `model.auth` (workload identity) | — | — | ❌ |
| `instructions` (inline or `{file}`) | agent `instructions {path, digest}` | `spec.instructions` (inline) | ⚠️ AgentSuite is digest-bound. AgentKit inlines after resolving. Mapping is lossless if the builder verifies the digest and then inlines |
| `tools[]` stdio `command` | tool manifest (`provider.operations`, `variants[]` with `entrypoint`, `arguments`, `files` inventory) + agent `tools[{id, version, executionMode: shared-sandbox}]` + composition | `extensions.orka.agent.tools[]` (Orka tool names) | ⚠️ the biggest gap. AgentKit runs whatever `command` resolves to at start, e.g. `npx -y`. AgentSuite requires an offline, inventoried payload and an explicit operation allowlist. Only bundled stdio servers map |
| `tools[].env` | variant `environment[{name, required, secret, delivery}]` | — | ✅ AgentSuite is richer: adds `secret` and `delivery` |
| `tools[]` remote `urlEnv` (streamable HTTP) | — (Appendix A excludes remote MCP) | Orka tool refs / Kagent `RemoteMCPServer` | ❌ not representable in AgentSuite |
| `tools[].headers`, `tools[].auth` | — | — | ❌ only meaningful for remote tools |
| `tools[].approval` | — (operation `effects[]` is free-form) | — | ❌ also gated off in AgentKit today |
| `brokeredTools[]` (`name`, `parameters` schema, `brokeredClass`, `schemaDigest`) | closest: `provider.operations[]` (`name`, `inputSchema` digest, `effects`) | — | ⚠️ similar shape: schema-only, digest-bound. `brokeredClass` read/write/coordination could become a standard `effects` vocabulary |
| `env[]` (agent-level names) | — (only model `endpointEnv` and per-tool variant env) | — | ❌ AgentSuite has no agent-level env list |
| `context.providers[]` (search, skills, memory) | — (Appendix A excludes mutable memory) | `extensions.orka.agent.skills[]` | ❌ skills map to Orka refs; memory is deliberately out of scope |
| `observability.otel.endpointEnv` | — | — | ❌ runtime concern |
| `expose.openai`, `expose.port` | — | — | ❌ runtime or protocol concern |
| — | agent `invokes[{agent, maxConcurrent, maxDepth}]` | `spec.coordination.allowedAgents` + `extensions.orka.agent.coordination.{maxConcurrentChildren, maxDepth}` | ❌ in AgentKit. ⚠️ Orka bounds apply per agent, AgentSuite bounds per edge, so mapping to Orka loses per-edge limits |
| — | `tools[].version`, compositions, `platform` | — | ❌ AgentKit has no tool versioning or platform resolution |

The overlap is real but narrow:

- **Clean mappings:** model protocol and name, instructions, agent identity,
  and tool environment names.
- **Lossy but workable:** the tool model and the secret/endpoint indirection.
- **Everything else** belongs to one side only.

## Proposal: AgentKit as an AgentSuite build profile

### Layering

```text
AgentSuite artifact   (Kaimahi spec: definition + distribution, digest-pinned)
        │ suite digest + agent id + platform + build profile
        ▼
AgentKit frontend     (BuildKit builder implementing AgentSuite §12)
        │ Agent Sandbox Image + /.agentsuite/binding.json
        ▼
Orka AgentRuntime     (harness v2 supervisor + ACP child)
        ▲
kmx                   (renders Agent.spec.runtime.runtimeRef, lifts, routes model
                       traffic through kaimahi-proxy)
```

Each layer has one owner:

- Kaimahi owns the spec and the validator.
- AgentKit owns the runtimes and the image builder.
- Orka owns execution.
- `kmx` owns the operator workflow.

`Agentkitfile` stays as AgentKit's quick single-agent authoring format. For a
suite, it becomes a front end that *emits* an AgentSuite agent instead of a
parallel package format.

### Build profile

AgentKit adapters are single-stage images on `python:3.12-slim-bookworm`, so a
build profile maps directly:

```json
{
  "id": "agentkit-pydantic-ai",
  "runtimeBase": [{ "platform": {"os": "linux", "architecture": "amd64"},
                    "image": { "digest": "sha256:<python:3.12-slim-bookworm>" } }],
  "harness":     [{ "platform": {"os": "linux", "architecture": "amd64"},
                    "image": { "digest": "sha256:<serve-pydantic-ai>" } }]
}
```

§12 step 3 ("compose the pinned harness filesystem") becomes a check plus an
append:

1. The builder verifies that the harness manifest's lower layers are exactly
   the `runtimeBase` layers.
2. It appends only the harness's own layers.

This replaces AgentKit's floating `:latest` adapter default with a digest, as
§11 requires.

### Builder changes (AgentKit)

1. **New frontend input mode.** Accept an AgentSuite OCI reference, `agent`,
   `platform` and `buildProfile` as frontend options. This sits alongside
   `agentkitfile.yaml`, which remains supported.
2. **Validate with Kaimahi's code.** This requires moving
   `internal/kmx/agentsuite` to an importable path, for example
   `pkg/agentsuite` or a small standalone module. Do not fork the validator.
3. **Render the ABI from the suite agent.**

   | Suite agent field | Rendered into `agent.yaml` |
   | --- | --- |
   | `model.protocol` | `model.provider` |
   | `model.model` | `model.name` |
   | `model.endpointEnv` | a new `model.baseURLEnv` (ABI v1) |
   | instruction file, after digest check | inlined |

4. **Copy tool payloads.** Copy each selected tool variant's files to its
   `installRoot` with `llb.Copy` from the suite content layer. Do not run
   package managers. Reject collisions as §12 requires.
5. **Embed the binding.** Write `/.agentsuite/binding.json` and set the
   `org.agentsuite.binding.digest` label. Set `ai.orka.harness.version` from
   the target protocol instead of hard-coding `v1`.
6. **Enforce the operation allowlist.** Add `tools[].operations` to the ABI, and
   have `RuntimeSession` filter MCP `tools/list` and `tools/call` to it. This
   is what satisfies runtime conformance check `ASR-OPS-001`.

### Kaimahi changes

1. Move or publish the AgentSuite validator (see builder change 2).
2. Add `runtimeRef` to `OrkaAgentExtension` so `kmx agent create/lift` can
   render `Agent.spec.runtime.runtimeRef`.
3. Make lifting automate AgentKit's manual v2 procedure: compose the
   supervisor image, register `adapterName`/`adapterDigest`, and set the
   configuration digest. This is the same kind of guarded, pinned workflow
   `kmx orka install` already does.
4. Map `invokes` to Orka coordination, and document that per-edge bounds
   collapse to per-agent ones until Orka supports edge-scoped limits.
5. Fix the informative reference in `agentsuite-spec.md` to
   `orka-agents/agentkit`.

### The conflict to resolve first: who runs bundled tools under harness v2?

The two specs disagree here:

- AgentSuite `shared-sandbox` says tool payloads are baked into the image and
  run inside the same sandbox as the harness.
- AgentKit's ACP strict mode says the image must contain **no** baked tools.
  Tools arrive from the Orka supervisor as at most one loopback MCP server.

Under the `harness-v2` mode that `kmx` installs, the two are incompatible as
written. The options:

| Option | Change | Trade-off |
| --- | --- | --- |
| A. Supervisor launches suite tools | Orka's supervisor reads `/.agentsuite/binding.json` and launches the bundled variants. It exposes them through its existing MCP broker, filtered to `provider.operations` | Keeps ACP strict mode intact and keeps policy in Orka. Needs Orka work. Best fit for "Orka is the policy authority" |
| B. ACP accepts inventoried tools | ACP strict mode allows baked stdio tools whose `command[0]` resolves to a path in the binding's inventory | Smallest change, in AgentKit only. But tools then run outside the supervisor's broker and governance |
| C. Add an AgentSuite execution mode | Define a `brokered` mode where the suite declares the contract (`provider.operations`) but the platform supplies the implementation | Closest to AgentKit's `brokeredTools`. A spec change, and remote providers are currently out of scope (Appendix A) |

**Recommendation: option A**, with option C as the follow-up spec revision:

- A keeps AgentSuite's offline, inventoried payloads.
- A keeps Orka as the single tool-policy enforcement point, which is the reason
  ACP strict mode exists.
- A needs no relaxation of either spec's security posture.

### Out of scope for AgentSuite; keep as AgentKit extensions

These can travel in a non-critical `extensions[]` record, for example
`{"name": "agentkit.orka-agents.io/v1", "critical": false, "digest": …}`, which
AgentKit reads and other builders ignore:

- `context.providers`, which includes memory (excluded by Appendix A).
- `observability`.
- `expose`.
- Remote MCP tools.
- `model.auth` workload identity.

## Suggested sequence

1. **Spec hygiene (Kaimahi).** Fix the AgentKit reference. Decide option A, B
   or C and record the decision in `agentsuite-spec.md` §9.3.
2. **Shared validator.** Move `agentsuite` to an importable package.
3. **AgentKit builder MVP.** Support a suite input with a tool-less agent:
   digest-pinned profile, inlined instructions, binding file and label. This
   is testable against Kaimahi's `testdata/minimal` and `coordinator-workers`
   fixtures once their placeholder `sha256:aaaa…` profile digests are replaced
   with real ones.
4. **`kmx` runtimeRef plus v2 registration.** Run the `coordinator-workers`
   fixture end-to-end on kind.
5. **Bundled tools.** Implement the chosen tool option. Add operation filtering
   and the `ASR-OPS-001` runtime conformance test.

## Open questions

- Should `Agentkitfile` remain a first-class authoring format, or become sugar
  that compiles to a one-agent suite?
- Should the ACP configuration digest move to the JCS digest of the binding,
  replacing raw `sha256` of YAML bytes? That would leave one identity scheme
  across both projects.
- Should AgentKit's brokered `read`/`write`/`coordination` classes become the
  standard values for AgentSuite operation `effects`?
- Who publishes the digest-pinned AgentKit build profiles: AgentKit releases,
  or Kaimahi's pinned toolchain?
