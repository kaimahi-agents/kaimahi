# Legacy reference: kagent placement and tool isolation

> **Legacy Kaimahi/kagent implementation, not an Orka guarantee.** Orka is
> the platform; Kaimahi helps people get agents onto it. This page retains
> isolation limits, not the retired multi-runtime roadmap or removed create
> flags. Start at the [documentation index](README.md) for current work.
> Native-Orka-only versus kagent YAML authoring remains an open question.

## Do not confuse the boundaries

A process sandbox, network policy and model/tool admission answer different
questions. VM/WASM isolation does not constrain allowed model spending or
approved tool arguments; the legacy proxy does not sandbox process execution.
A generated environment variable is configuration, not proof of either.

Migration preserves an application's **owner-managed Deployment**. Nothing
in this legacy reference requires converting that application into a
kagent Agent or creating a new Kaimahi runtime contract.

## Removed BYO create flags

`kmx agent create` now authors native Orka Provider + Agent resources and an
optional Task. `--image`, `--isolation` and `--run-as-user` are removed; there
is no replacement application-image scaffold or automatic ModelConfig/MCP
conversion. Keep image, identity, hardening and placement in the application's
own Deployment/chart. See the [create contract](kmx.md#kmx-agent-create) and
[model-traffic migration](migrate.md).

The former kagent BYO generator's virtual-node selectors, UID choices and
injected model/tool environment are historical behavior, not current flags
or an Orka isolation guarantee. No selector proves where a pod ran; no
`OPENAI_BASE_URL`, `SSL_CERT_FILE` or `KAIMAHI_MCP_URL` setting proves an arbitrary
image uses that route. The pinned kagent Agent schema does not expose
`runtimeClassName`; its Python/Go runtime choice selects an ADK, not a sandbox.
Do not infer these properties from an accepted Agent or an installed platform.

## The separate tool sandbox

`kmx tools sandbox` installs the retained WASM tool-runtime support; it is
not an agent isolation profile. Its **privileged node installer** writes
an upstream containerd shim and changes containerd configuration on Linux
nodes. Treat that as a cluster-level change, not ordinary agent onboarding.
An MCP server must actually select `runtimeClassName: wasmtime-spin-v2` and
run compatible code; installing the runtime alone sandboxes no workload.

`kmx tools sandbox status` reports the RuntimeClass, installer readiness
and pods selecting it. An unreachable API server is not “not installed”.
Read [sandbox.go](../internal/kmx/app/sandbox.go) and
[wasm/runtime.yaml](../k8s/wasm/runtime.yaml) before operating it.
Its existence does not make kagent's Agent schema expose RuntimeClass,
and gateway policy remains separate from tool-process containment.

## Evidence and remaining limits

Schema validation does not prove a cloud node's VM boundary, that an arbitrary
image obeys seam configuration, or that admitted outputs cannot exfiltrate data.
Network enforcement needs the [egress probes](egress.md), with an allowed
control. Model ledger and tool audit rows prove observed requests, not the
absence of alternate routes.

Historical option rankings, speculative Hyperlight/WASM agent designs and
future API proposals remain retired. This integration introduces no
`orka.harness.v2` contract and makes no new isolation promise.
