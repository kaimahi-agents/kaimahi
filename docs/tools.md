# Legacy reference: the kagent MCP example

> **Legacy kagent example, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This page remains to explain
> the retained `hello-tools` manifests and their tests, not as a competing
> onboarding tutorial. Start at the [documentation index](README.md).
> Keeping this reference does not settle whether future authoring is
> native-Orka-only or includes kagent YAML.

## What is wired

[k8s/tools-agent.yaml](../k8s/tools-agent.yaml) defines `hello-tools`, a
separate declarative Agent using the keyless `hello-world-model` and one
tool, `k8s_get_resources`, from the chart-managed `kagent-tool-server`
RemoteMCPServer. The hello-world Agent remains tool-free in its own file.

The direct path is agent → RemoteMCPServer URL → kagent tool server.
**It bypasses the Kaimahi MCP gateway**, so the plane's tool allowlist,
argument policy, grants and audit do not apply. Agent `toolNames` is a
selection for the runtime, not an independent external enforcement point.
Model routing is separate: a governed model does not govern tool traffic.

[k8s/kagent-values.yaml](../k8s/kagent-values.yaml) enables the bundled
`kagent-tools` subchart. The server speaks streamable HTTP at `:8084/mcp`;
this example deploys no Kaimahi-written MCP runtime or connector.

## The tool server lockdown

The committed chart values configure:

- `tools.enabledTools: [k8s]`: no other provider toolsets;
- `tools.args: [--read-only]`: application-layer writes disabled;
- `rbac.readOnly: true`: get/list/watch rather than cluster-admin;
  Secrets access remains disabled by the chart's configuration.

Those layers constrain the server's authority, not arbitrary network
connections from the agent. A read-only tool can still reveal sensitive
cluster metadata; absence of writes is not confidentiality.

The Agent also has non-root UID 1001 for the pinned image, capability drops,
no privilege escalation, default seccomp and a read-only root filesystem
with writable `/tmp`. Do not copy that image-specific UID blindly into BYO
images; see [isolation](isolation.md).

## Discovery and permission are different

The pinned kagent integration uses `MCPServer` for in-cluster server
workloads and `RemoteMCPServer` for endpoints. The Agent selects tools from
what the controller discovered. A declared `toolNames` entry does not make
an undiscovered tool available or grant remote authorization.

In the legacy governed alternative,
[kaimahi-tools.yaml](../k8s/kaimahi-tools.yaml) points a separate seam at the
TLS gateway and resolves the opaque credential from a Secret. The gateway
projects discovery and checks every actual call; see
[tool governance](tool-governance.md). A permission change may require
rediscovery and an agent restart before the runtime sees the new list.

An agent unable to see a posting/payment tool may never call it, so it
may file no approval request. Direct MCP clients still encounter the
per-call gate. Discovery is not a substitute for testing admission.

## Inspecting an existing deployment

```sh
kubectl -n kagent get agent hello-tools -o yaml
kubectl -n kagent get remotemcpserver kagent-tool-server -o yaml
kmx agent chat hello-tools "What ConfigMaps are in the default namespace?"
```

Chat is not a pure status read: it can spend model tokens and invoke the
tools currently wired to that agent. Check the model and tool URLs first;
a running deployment may have been repointed since the committed example.
The native governance commands and their side effects are documented in
[tool governance](tool-governance.md#existing-operator-commands).

## Evidence of an actual call

A Ready Agent or fluent answer is insufficient. The retained verification
pattern creates a ConfigMap with an unpredictable name, asks the agent to
list it, and requires the structured A2A task history to contain:

1. a completed task;
2. a `function_call` for the requested tool;
3. a successful `function_response` with that unpredictable value in its
   payload, not merely in the model's reply.

[scripts/verify-chat.py](../scripts/verify-chat.py) implements that check.
It does not certify every future turn. A small model can call the tool
correctly and still summarize the response incorrectly; inspect structured
history rather than accepting prose as the system of record.

The example's prompt asks the model to copy resource names faithfully.
That is a reliability hint, not a security boundary or a correctness proof.
Its historical trial transcript is removed rather than presented as a
current guarantee for another model or deployment.

## Limits and related references

Direct tool traffic has no plane audit. [Network policy](egress.md) covers
specific plane workloads, not the entire agent namespace. Tool results are
not filtered or redacted by adding the legacy gateway. Retained
[approvals](approvals.md) bind only declared policy fields, not every effect.
The [bring-your-own stub](govern-your-agent.md) preserves custom-server
scaffolding limitations without duplicating its old end-to-end tutorial.
