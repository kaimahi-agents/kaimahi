# SPIKE — kagent YAML to native Orka Tasks

**Throwaway experiment, not a supported authoring surface or `kmx` feature.**
No controller, CRD installer, agent loop, reconciliation or new public CLI is
provided. Do not use with sensitive workloads. This directory is a separate Go
module so its SDK dependency does not enter `kmx`.

## Offline conversion

From this directory:

```sh
go run ./cmd/convert proof/source.yaml proof/tools.json > /tmp/shim-converted.yaml
```

Input is one YAML stream containing exactly one declarative Agent, its
ModelConfig, selected RemoteMCPServers, and optionally its prompt ConfigMap.
The second argument is a **reviewed invocation-schema snapshot**, keyed by
server name; its entries contain `name`, `description`, and `inputSchema`.
Conversion performs no network access and never reads Secret values.

Output always includes the Provider **and** Agent, individual HTTP Tools, and
the adapter's routing ConfigMap (plus a referenced prompt ConfigMap). There is
no agent-only output mode. Nothing is emitted when any conversion fails.

### Exact spike subset

- Source: `kagent.dev/v1alpha2`, kagent **v0.10.1**. Target:
  `core.orka.ai/v1alpha1`, Orka **v0.1.3**. Immutable commits and installed-schema
  fingerprints are in [`proof/pins.json`](proof/pins.json).
- Input metadata: explicit `name` and `namespace` only. All dependencies share
  that namespace. Agent/server descriptions are retained as provenance
  annotations, not prepended to the prompt.
- Agent: `type: Declarative`; explicit `modelConfig`; exactly one literal
  `systemMessage` or `systemMessageFrom: {type: ConfigMap, name, key}`; explicit
  nonempty `tools` selections. No runtime, deployment, skills, BYO, templates,
  A2A configuration or memory declarations.
- ModelConfig: `provider: OpenAI` or `Anthropic`; explicit `model`,
  `apiKeySecret`, `apiKeySecretKey`; optional matching `openAI.baseUrl` or
  `anthropic.baseUrl`. No generation settings, passthrough auth, inline
  credentials, custom TLS or other provider options.
- Tools: `type: McpServer`; explicit `apiGroup: kagent.dev`,
  `kind: RemoteMCPServer`, `name`, nonempty `toolNames`; optional same namespace.
  Remote server: description, URL, explicit `protocol: STREAMABLE_HTTP`, and
  optional named Secret-backed `headersFrom`. No literal headers or overrides.
  Transport-owned headers and built-in tool-name collisions are refused.
- ConfigMap: only the referenced prompt key. No Secret documents, extra input
  resources, source labels/status, duplicate keys, YAML aliases/anchors/merges,
  or unknown fields—even if their value is false, null or empty.

Use **one converted Agent per isolated namespace**. The fixed adapter Service
and routing ConfigMap are deliberately not a multi-agent deployment manager.
The converter does not resolve conflicts with objects already on a cluster.
Generated resources are an operator-owned snapshot, not a second reconciled
source of truth.

## Adapter

`cmd/adapter` listens on `:8080`, reading `/config/routes.json` once and Secret
files under `/secrets/<name>/<key>` per invocation. Secret mounts are the
operator's responsibility; the keyless proof needs none for MCP.

`POST /tools/<generated-id>` accepts one JSON argument object. The fixed route
selects the endpoint and original remote name; callers cannot choose either.
The official MCP Go SDK handles initialization, protocol negotiation,
Streamable HTTP JSON/SSE responses and session cleanup. One session is opened
per invocation. The adapter never retries `tools/call`.

Successful results contain text and optional structured content. Image/audio,
resource content, result metadata, annotated text and interactive results are
refused at execution. Protocol-owned `serverInfo` metadata is omitted rather
than exposed to the model. Structured numbers use the SDK's binary64 decoding;
magnitudes at or above 2^53 are refused, not returned as rounded identifiers.
Use text/string values for exact numeric representations.
Transport/protocol/tool failures return non-2xx, generic
stage diagnostics—not arbitrary upstream errors that could contain secrets.
`HEAD` on a known route checks local configuration readiness only; it does not
prove the remote server is reachable. `GET /healthz` checks the process.

This is trusted cluster-local glue: no inbound identity, multi-tenant isolation,
egress policy, OAuth, arbitrary remote TLS customization, live schema discovery
or resource provisioning. Do not expose its Service to untrusted callers.
Schema refresh and remote server operation are separate from conversion.

## Manual cluster proof

Prerequisites: Docker, kind, kubectl, helm, Go 1.26, Python with PyYAML, internet
for pinned images/charts and the local model download. No model API key needed.

```sh
bash proof/run.sh /tmp/kagent-shim-proof
```

This refuses an existing `kagent-shim-spike` cluster, creates its own kind
cluster, installs the pinned Orka bundle and the standalone **kagent-tools
0.2.1** server with read-only Kubernetes permissions, and runs `qwen2.5:3b`
through its OpenAI-compatible local endpoint. No kagent controller is installed.
All kubectl/helm calls select the context explicitly. The exit trap removes the
created cluster even if verification fails.

Before applying, [`proof/check.py`](proof/check.py) checks the installed schemas,
the Agent/Provider pair and the exact supported native Task shape. A strict
server-side dry-run additionally checks admission and target field validation.
The checker is **preflight, not admission**: it cannot constrain bypassing
callers, subsequent object edits or changes between check and apply. Use only
fresh, operator-controlled resources for this experiment. Supported Tasks cannot
add tools or override model/system instructions. This experiment explicitly
accepts the four normal target memory tools (`recall_memory`, `remember`,
`propose_memory`, `search_transcript`) and associated prompt augmentation. It
does not claim that the source MCP list is the entire effective tool set.

The Task must call the selected MCP Kubernetes tool and return a random value
created in a ConfigMap after conversion. Verification requires exact result
value equality, a successful ToolCall event, the expected advertised tool count,
and a successful Kubernetes command in the MCP server log—not merely Task
`Succeeded`. API authentication is runtime-generated and piped, never saved to
an evidence file. See [`proof/PROOF.md`](proof/PROOF.md) for the recorded run.

`proof/tools.json` contains the selected invocation schema captured from this
server's `tools/list`; discovery annotations were explicitly excluded from the
snapshot format. No discovery annotation is used to grant authority.

## Keyless tests

```sh
go test -race ./...
(cd proof && python3 -m unittest test_check.py)
```

CI runs only fixtures and in-process HTTP/MCP test servers. It does not run the
manual cluster proof or call a live model/server. Tests cover refusal before
output, deterministic conversion, provider presence, protocol/session handling,
original-name mapping, request limits, Secret rotation, error handling,
cancellation and no tool-call replay.
