# Recorded spike proof

Post-review manual run on 2026-09-10 using [`run.sh`](run.sh). This is execution evidence,
not CI coverage. Source Agent, dependencies, schema snapshot and native Task are
in this directory; no kagent controller was installed.

- Source schema validation: Agent, ModelConfig and RemoteMCPServer accepted by
  the kagent v0.10.1 v1alpha2 OpenAPI schemas (offline Draft-7 check).
- Target: Orka v0.1.3, kind Kubernetes v1.32.2.
- Installed Agent/Provider/Tool/Task schemas exactly matched `pins.json`.
- Converted bundle passed `kubectl --context kind-kagent-shim-spike apply
  --dry-run=server --validate=strict`.
- Model: keyless in-cluster `qwen2.5:3b`, via Ollama 0.11.8's OpenAI-compatible
  endpoint. MCP server: independently installed kagent-tools 0.2.1, read-only
  Kubernetes tools, no access to Secrets.

## Real tool execution

A random value was placed in `ConfigMap/shim-proof-target` **after conversion**.
It was not in the system prompt or Task invocation. The native Task called:

```text
Orka tool: shim-4c93a72e18eb8dd4c3f84fc9b46250a9
Remote MCP name: k8s_get_resources
Arguments: {"resource_type":"configmap","resource_name":"shim-proof-target","namespace":"orka-system","output":"json"}
```

MCP server log:

```text
command execution successful command=kubectl args="[get configmap shim-proof-target -n orka-system -o json]"
```

Task result, exactly matching the value read separately from Kubernetes:

```json
{"result":"mcp-executed-001189a805289e3b59fef762"}
```

The script additionally asserted `ToolCallCompleted` for the generated Tool and
`ModelRequestStarted.content.toolCount == 5`. The five are the custom Tool and
the four normal memory tools documented in the spike contract. It then deleted
the cluster. Cloud/model API spend: **$0**; local compute and downloads only.
Machine-readable result, selected events and image identities are in
[`evidence.json`](evidence.json).

## Refusal at conversion

Using this repository's existing `k8s/tools-agent.yaml`, from the spike directory:

```sh
go run ./cmd/convert ../../k8s/tools-agent.yaml proof/tools.json
```

Actual exit: **1**. Stdout: **0 bytes**. User-facing diagnostic:

```text
convert: refused: Agent/hello-tools.spec.declarative.deployment: unsupported field
```

No output Agent was produced after ignoring its deployment/security declaration.
Fixture tests additionally cover unknown nested fields with false/null/empty
values, unresolved dependencies, nonpositive tool lists and cross-namespace
references.

## Earlier unsuccessful invocation retained in the evidence

Before the explicit argument invocation, a Task asked in prose to read
`shim-proof-target`. The model instead called the tool three times for a
ConfigMap named `proof`; each real MCP invocation returned a tool error. The
model's final answer described failure, although Task phase was `Succeeded`.
That run did **not** pass the result assertion. Only the subsequent explicit
invocation and fresh-value repetitions count as successful proof.
