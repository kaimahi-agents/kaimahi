# Legacy reference: kagent placement and tool isolation

> **Legacy Kaimahi/kagent implementation, not an Orka guarantee.** Orka is
> the platform; Kaimahi helps people get agents onto it. This page retains
> isolation limits, not the retired multi-runtime roadmap or removed create
> flags. Start at the [documentation index](README.md) for current work.
> Native-Orka-only versus kagent YAML authoring remains an open question.

## Do not confuse the boundaries

A process sandbox, network policy and model admission answer different
questions. VM/WASM isolation does not constrain allowed model spending;
the model proxy does not sandbox process execution or govern tool arguments.
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

The separate tool-sandbox command and installer are retired with the tool CLI.
Their former node-level effects are documented only in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/isolation.md).
Existing node modifications do not disappear on a plane apply; review their
ownership and any workloads selecting the installed RuntimeClass separately.
Do not infer an agent sandbox from a historical installation or remove a shared
runtime without checking its consumers.

## Evidence and remaining limits

Schema validation does not prove a cloud node's VM boundary, that an arbitrary
image obeys seam configuration, or that admitted outputs cannot exfiltrate data.
Network enforcement needs the [egress probes](egress.md), with an allowed
control. Model ledger rows prove observed model requests, not the absence of
alternate routes. Historical tool audit is stored data, not a live tool boundary.

Historical option rankings, speculative Hyperlight/WASM agent designs and
future API proposals remain retired. This integration introduces no
`orka.harness.v2` contract and makes no new isolation promise.
