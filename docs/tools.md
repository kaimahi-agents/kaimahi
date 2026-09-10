# Tools: giving the agent hands over MCP

Assumes a running cluster from [getting-started.md](getting-started.md).

The hello-world agent talks. `hello-tools`, the second agent `kmx up`
creates, can also act: it calls a tool over MCP, kagent's native tool
mechanism. The topology stays agent-as-code: one extra Agent YAML
([`k8s/tools-agent.yaml`](../k8s/tools-agent.yaml)) and a few lines of
helm values. Kaimahi ships no MCP runtime, proxy, or server of its own
here.

> **Tools wired this way are ungoverned.** The agent acts on the world
> through whatever it is wired to, with no allowlist enforcement outside
> the YAML, no approval workflow, and no audit trail in front of the
> calls. The only limits are the demo's own lockdown (read-only tool
> server, single-tool allowlist). To govern the calls, put the agent
> behind the enforcing gateway: [tool-governance.md](tool-governance.md).

## Run it

```bash
kmx up                     # enables kagent-tools and applies hello-tools
kmx agent chat hello-tools 'What pods are running in the ollama namespace?'
```

`kmx up` waits for the `kagent-tool-server` RemoteMCPServer to be
Accepted (the controller must connect and discover tools) and then
applies the agent. The first reconcile can race the tool-server pod and
retry for up to a minute. Everything else, `kmx agent chat`, `kmx use`,
`kmx down`, is unchanged. The model presets in
[models.md](models.md) apply to `hello-tools` too if you point its
`modelConfig` at one; `kmx use --agent hello-tools <preset>` switches it.

## What kagent ships for MCP

kagent 0.9.12 ships the whole MCP stack, all verified on a live cluster:

- **`MCPServer`** (v1alpha1) deploys an MCP server in-cluster from a
  container image. A stdio transport rides a sidecar that spawns the
  process per session (uvx/npx, 2 to 8 s startup, so mind client
  timeouts); plain HTTP is also supported.
- **`RemoteMCPServer`** (v1alpha2) points at an existing MCP endpoint
  (SSE or streamable HTTP).
- **`Agent.spec.declarative.tools[]`** wires an agent to a server, with
  `toolNames` allowlisting and `headersFrom` for authenticated servers.
- **`ToolServer`** (v1alpha1) is the legacy pre-0.9 API: bare transport
  config, no deployment machinery. Still served for compatibility, but
  the supported path at 0.9.12 is MCPServer/RemoteMCPServer. kagent's own
  chart publishes its bundled tool server as a RemoteMCPServer. Do not
  use ToolServer in new configs.
- **A bundled tool server.** The chart's `kagent-tools` subchart deploys
  kagent's own MCP server (k8s/helm/istio and other introspection tools,
  streamable HTTP on `:8084/mcp`) and creates the `kagent-tool-server`
  RemoteMCPServer pointing at it.

So the demo's tool server is neither ours nor third-party. It is
kagent's own, switched on in
[`k8s/kagent-values.yaml`](../k8s/kagent-values.yaml). Reusing it needs
no new images, no runtime egress, and exercises the same CRD path any
real connector uses.

## The tool server lockdown

Three layers, every knob the subchart offers:

- `tools.enabledTools: [k8s]`: the k8s provider only. No helm, istio,
  argo, cilium or prometheus tool surface.
- `tools.args: [--read-only]`: write tools disabled at the application
  layer. The server logs `Running in read-only mode` and discovers only
  the eight `k8s_*` read tools.
- `rbac.readOnly: true`: a get/list/watch ClusterRole instead of the
  subchart's default cluster-admin, with `allowSecrets` left `false`.
  The tool server **cannot read Secrets** even if asked.

## The agent

`k8s/tools-agent.yaml` adds a second declarative agent, `hello-tools`.
The original `k8s/hello-world.yaml` is never mutated: that agent stays
tool-free, and this file is a separate, reviewable diff. `hello-tools`
reuses the `hello-world-model` ModelConfig (keyless in-cluster Ollama,
`qwen2.5:3b`) and wires exactly one tool:

```yaml
tools:
  - type: McpServer
    mcpServer:
      apiGroup: kagent.dev
      kind: RemoteMCPServer
      name: kagent-tool-server
      toolNames:
        - k8s_get_resources
```

The single-tool allowlist keeps the small local model reliable (one
obvious tool to pick) and keeps the ungoverned surface minimal. Nothing
stops you widening `toolNames` to all eight read tools. Do it knowingly.

## Proving a real tool call

A Ready agent and a fluent reply prove nothing. A model can guess
`kube-root-ca.crt` without ever touching a tool. CI runs this check
keyless on every push, and it forces a live data round-trip:

1. Create a ConfigMap with an unguessable random name
   (`probe-<8 hex chars>`).
2. Ask `hello-tools` to list configmaps in that namespace.
3. `python3 scripts/verify-chat.py tool-chat.out k8s_get_resources $probe`
   then requires, fail-closed: A2A `state=completed`, a `function_call`
   for `k8s_get_resources` **and** a successful (`isError: false`)
   `function_response` in the task history, and the probe name inside
   that response's payload. The reply text is printed but not asserted:
   a 3B model garbles unguessable strings, and requiring a verbatim copy
   tested the model, not the tool path (CI went red on exactly that
   before the check moved).

The A2A task history is the invocation evidence: kagent records the tool
call and its response as structured message parts. The tool server's own
log is a second witness:

```text
msg="executing command" command=kubectl args="[get configmap -n default -o wide]"
```

Verified live on 2026-08-31: `qwen2.5:3b` called `k8s_get_resources`
with correct arguments on the first attempt and echoed the probe name
back. No model change was needed for the tool path.

## The model can garble what the tool returned

The sneakier failure mode. In repeat trials the model occasionally
invoked the tool correctly, received the probe in the response, and
still summarised it away ("There are no configmaps…"). The system
message therefore orders it to copy the tool table's NAME column
verbatim and never claim emptiness when rows exist. With that wording the
probe check passed 10 of 10 consecutive fresh-cluster trials. A 3B model
relaying data is still a 3B model: treat the A2A task history as the
truth and the prose as a paraphrase. If you swap in a different small
model, re-run the trials before trusting it. See the
[FAQ](FAQ.md#the-tool-call-worked-but-the-answer-is-wrong) for what this
looks like in practice.

## Where governance mounts

Every tool call here flows agent → RemoteMCPServer URL → tool server.
That URL is the seam. [tool-governance.md](tool-governance.md) puts
Kaimahi's enforcing MCP gateway between the two, so allowlists,
approvals and audit apply to every connector, including authenticated
ones (`headersFrom` plus a Secret whose value is typed, never written into
a manifest — `kmx credential capture` reads it from a terminal, and the
connector scripts read it from stdin), which this demo
deliberately avoids by choosing a keyless tool.

## Limitations

- Ungoverned by design until you run `kmx tools govern`. The full
  governed-versus-ungoverned picture is in
  [README.md](README.md#what-is-governed-today-and-what-is-not).
- The lockdown is the tool server's own posture, not a network boundary.
  Nothing here constrains what a pod can connect to.
- The small model's relaying is the weak link, as above.
