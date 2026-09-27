# Orka quickstart Kubernetes tool

The quickstart wizard installs `k8s-get-resources`, an Orka HTTP Tool that ports
the read-only listing capability of the older `hello-tools` example.
The name uses hyphens because it is a Kubernetes Tool resource name.

New quickstart agents reference this tool by default. `--tools` replaces that
default with an explicit list. Default system instructions direct cluster
questions to the tool; custom instruction files remain user-owned.

Selecting an existing quickstart Agent adds the tool reference if absent,
preserving other tools and its system prompt. An explicitly disabled reference
is preserved. The update checks resourceVersion and waits for the updated Agent
generation to become Ready. Subsequent Tasks use the new tool; active workers
keep their startup configuration. This cluster update does not rewrite older
local Agent YAML files.

Try:

```text
List deployments in namespace orka-system using k8s-get-resources.
```

In Orka interactive chat, `/tools` opens a searchable list of registered tools
and existing references. The screen clears before loading. Use arrows or j/k to
select, press `/` to search, Space to toggle,
and Enter to save. Escape returns without changes; Ctrl-C exits chat. The four
automatic v0.1.3 memory tools are shown as locked on because the worker injects
them independently of the Agent references.

`/agent` opens a searchable list of AI agents in the current namespace. Enter
connects to the selection, clears the visible conversation and resets `/retry`.
Tool updates take effect on the next Task and preserve other Agent configuration.

## Implementation

- `k8s/orka-k8s-tool.yaml`: Tool, Service, Deployment and read-only RBAC.
- `scripts/orka-k8s-tool.py`: standard-library HTTP server, embedded into KMX and
  installed in the `kmx-k8s-tool` ConfigMap.
- `internal/kmx/app/orka_k8s_tool.go`: installation and existing-agent attachment.

The server permits only a fixed resource allowlist and optional namespace. It
issues Kubernetes GET requests using its own service account, verifies the API
server certificate, and returns names/namespaces plus selected status fields.
It never invokes a shell, returns Secret data, or exposes ConfigMap contents and
pod environment values. Lists are limited to 100 items and report truncation.

The Service is cluster-internal and has no application-level authentication;
its endpoint exposes only these read-only projections. It does not provide
Kaimahi tool authorization or tool auditing. The worker's own permissions are
separate from the dedicated reader service account's read-only permissions.

Supported resources: pods, services, namespaces, nodes, configmaps,
persistentvolumeclaims, deployments, statefulsets, daemonsets, replicasets,
jobs and cronjobs. Omitting namespace lists across namespaces.
For pods, the optional `phase` parameter filters at the Kubernetes API; use
`Running` to omit completed worker Jobs when listing running pods.

## Verification

```sh
python3 -B scripts/test_orka_k8s_tool.py
go test ./internal/kmx/app -run 'TestQuickstart(K8sTool|ToolDefault)'
```

Live verification on 2026-09-16 attached the Tool to `hello-world-agent`, ran a
fresh Orka Task, observed `POST /resources` returning 200, and received the actual
three deployment names. The reader account's `delete pods` authorization was
denied. The former MCP fixture was removed with the unsupported legacy
runtime; this native Tool is the maintained example.
