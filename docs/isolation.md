# Legacy reference: kagent placement and tool isolation

> **Legacy Kaimahi/kagent implementation, not an Orka guarantee.** Orka is
> the platform; Kaimahi helps people get agents onto it. This page retains
> shipped flag behavior and isolation limits, not the retired multi-runtime
> roadmap. Start at the [documentation index](README.md) for current work.
> Native-Orka-only versus kagent YAML authoring remains an open question.

## Do not confuse the boundaries

A process sandbox, network policy and model/tool admission answer different
questions. VM/WASM isolation does not constrain allowed model spending or
approved tool arguments; the legacy proxy does not sandbox process execution.
A generated environment variable is configuration, not proof of either.

Migration preserves an application's **owner-managed Deployment**. Nothing
in this legacy BYO reference requires converting that application into a
kagent Agent or creating a new Kaimahi runtime contract.

## Existing agent flags

The retained kagent scaffold accepts:

```text
kmx agent create <name> --image <image-reference>
  --isolation virtual-node|none
  --run-as-user <numeric-uid>|root
```

`--image` chooses a BYO Agent expected to serve A2A on port 8080.
`--isolation` and `--run-as-user` require that image; they are not generic
flags for declarative agents. Without an image, the existing declarative
scaffold is unchanged. See [agent generator](../internal/kmx/scaffold/agent.go)
and [CLI flags](../cmd/kmx/agent_commands.go).

[placement.go](../internal/kmx/scaffold/placement.go) supports only
`virtual-node` and `none`. The former emits node selectors and tolerations
for an ACI virtual node; it does not provision one or verify the actual
execution boundary. A selector matching no node leaves the workload Pending.
Check where the pod actually ran before making an isolation claim.

**`kata` is refused.** The pinned kagent schema does not expose
`runtimeClassName` in declarative or BYO deployment settings. Scheduling
onto a Kata-capable node without selecting its RuntimeClass can run an
ordinary container there. kagent's Python/Go runtime selector chooses an
ADK implementation, not a sandbox. The sandbox domain field is network
configuration, not runtime selection.

## BYO loses declarative wiring

BYO has deployment settings, not declarative `modelConfig` and `tools`.
The image must implement its own model and MCP clients. When the legacy
scaffolder considers it governed, it injects model/gateway environment,
a Secret reference and the plane CA. The actual values are in
[GovernanceEnv](../internal/kmx/scaffold/placement.go); inspect the emitted
manifest rather than assuming the model-selection flag rewrites those URLs.

kmx cannot prove an arbitrary image honors `OPENAI_BASE_URL`, its credential
or `SSL_CERT_FILE`, nor that it uses `KAIMAHI_MCP_URL` for every tool call.
No plane selected means no such environment and no added trust mount.
A configured image must still demonstrate real model ledger rows and,
where intended, tool audit rows. Those rows prove observed requests, not
that the image has no alternate network route. See [foreign runtime](foreign-runtime.md).

## Image-dependent hardening

Every BYO scaffold drops capabilities, disables privilege escalation and
sets the default seccomp profile. The image-specific half is explicit:

| `--run-as-user` | Generated posture |
|---|---|
| positive numeric UID | that UID, non-root, read-only root filesystem, writable `/tmp` volume |
| `root` | explicit root, writable root filesystem, warning |
| omitted | no guessed UID/non-root guarantee; writable filesystem and warning |

Numeric `0`, negative IDs and usernames are refused; use the deliberate
`root` spelling only when required. Inspect image metadata and requirements
rather than copying the declarative image's UID 1001 into an unrelated image.
An explicit UID is not a proof that the application can run with its file
permissions; validate startup and useful work after applying it.
Source: [identity.go](../internal/kmx/scaffold/identity.go).

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

[Placement tests](../internal/kmx/scaffold/placement_test.go) and
[identity tests](../internal/kmx/scaffold/identity_test.go) check generated
fields and refusals. They do not prove a particular cloud node's VM boundary,
that an arbitrary image obeys seam configuration, or that admitted outputs
cannot exfiltrate data. Network enforcement needs the [egress probes](egress.md).

Historical option rankings, speculative Hyperlight/WASM agent designs and
future API proposals have been removed. This reference changes no runtime,
introduces no `orka.harness.v2` contract and makes no new isolation promise.
