# Isolation options for agents and their tools

*Status: option A is built (`--image`, `--isolation`); B–E are not. This note
separates three questions that get asked as one, records why A was the only
one shippable today, and states what the others would cost.*

**One finding changed the design after it was written: there is no `kata`
profile.** Kata is a RuntimeClass and kagent exposes no `runtimeClassName`,
so scheduling onto a Kata-capable node runs an ordinary container there —
isolated in appearance only. `--isolation kata` is refused, with that
reason. `virtual-node` ships because ACI virtual nodes are selected by
nodeSelector and toleration alone, which is the mechanism kagent does
expose.

The question: **if not a plain Kubernetes pod, can an agent run in a
micro-VM, a WASM sandbox, or an open sandbox — and how would
`kmx agent create` express that?**

---

## What the survey established

Verified against the live CRDs and the plane's source, not assumed.

| Claim | Evidence |
|---|---|
| kagent's `runtime` field cannot select a sandbox | `declarative.runtime` is an enum of exactly `python \| go` — ADK *implementations*, choosing an image and a readiness probe |
| The Kubernetes-native lever is not exposed | `runtimeClassName` is absent from **both** `declarative.deployment` and `byo.deployment`; `nodeSelector`, `tolerations`, `affinity` are present |
| kagent's own `sandbox` field is narrower than it sounds | `spec.sandbox` is `network.allowedDomains` — egress policy for "sandboxed execution paths", not runtime selection |
| Hyperlight is not a Kubernetes runtime | an embeddable micro-VM library; there is no `runtimeClassName: hyperlight` to select |
| `spec.type` is `Declarative \| BYO` | BYO "expects it to serve the agent over the A2A protocol on port 8080" |
| **BYO has no `modelConfig` and no `tools`** | `spec.byo` has exactly one property, `deployment`. The only lever is `deployment.env` |
| **Governance survives a runtime swap** | `plane/go.mod` has no kagent module. The model seam is an OpenAI-compatible `baseUrl`; the tool seam is MCP JSON-RPC |

The last two rows are the ones that decide the design, and they pull in
opposite directions.

## Three questions, not one

"Run the agent in Hyperlight" and "run the agent in WASM" sound like one
request and act on different layers.

| Layer | The question | Today | Changing it costs |
|---|---|---|---|
| **L1 — the artifact** | What *is* the agent? | a kagent `Agent`: model, instructions, tool allowlist | the reviewable YAML, and with it the governance wiring |
| **L2 — the workload** | What runs the agent process? | a pod, python or go ADK, served by kagent's controller | A2A serving, sessions, model plumbing, tool wiring |
| **L3 — tool execution** | What runs the work the agent asks for? | an MCP server in its own container | the boundary where the blast radius actually is |
| **L4 — governance** | Who may do what, and what did it cost? | the Kaimahi plane, at protocol seams | nothing — it is runtime-agnostic |

**BYO acts on L1 and L2 together. SpinKube acts on L2. Hyperlight acts on
L3.** A single `--sandbox` flag covering them would be a design mistake.

---

## The options, with their chances

| Option | Layer | Available today | Chance it works | What it costs |
|---|---|---|---|---|
| **A. BYO + VM-isolated node pool** | L1+L2 | **yes** | **high** — every piece exists and is exposed | the declarative governance wiring (below) |
| B. Deployment + `runtimeClassName` | L2 | yes, outside kagent | high, mechanically | you own A2A serving, sessions, tool wiring |
| C. SpinKube / runwasi | L2 | CRD yes, agent no | **low** | WASI-compiled LLM/MCP clients are immature; a long-lived loop fits WASM's request model badly |
| D. Hyperlight around tools | L3 | needs building | **medium** | a host process per MCP server; unmeasured per-call cost |
| E. Contribute `runtimeClassName` upstream | L2 | no | medium, slow | unblocks Kata *and* runwasi properly, for everyone |

## Chosen: A — BYO on a VM-isolated node pool

The closest honest answer to "hyperscale VM", and the only one that needs no
new runtime.

`spec.byo` takes your own image and expects A2A on port 8080. Combine it
with the `nodeSelector`/`tolerations` that *are* exposed and target a
Kata-enabled pool — on AKS, `kata-mshv-vm-isolation`. Each agent gets a
micro-VM. kagent's controller and CRD stay. We write no runtime.

### The trade it makes, stated first

**A BYO agent has no `modelConfig` and no `tools`.** Those fields exist only
under `declarative`. So the moment an agent becomes BYO:

- `--model governed-ollama` no longer applies. The governed preset is a
  `ModelConfig` reference, and BYO has nowhere to put it.
- the tool allowlist in the Agent YAML disappears. Wiring to the gateway
  becomes the image's business.
- **governance stops being visible in the artifact.** It survives only if
  the image is configured to use the proxy and the gateway — through
  `deployment.env`, the one lever BYO has.

That is a real cost, and it cuts against the project's own default:
governed-by-default is a property of the *declarative* path. Isolation buys
a stronger boundary around the process and loses the boundary that was
legible in YAML.

Both matter. Neither substitutes for the other:

| | Protects against | Blind to |
|---|---|---|
| Micro-VM isolation | a compromised agent process escaping onto the node | what the agent spends, calls, or exfiltrates *through its allowed seams* |
| The governance plane | unbudgeted spend, un-allowlisted tools, unaudited actions | anything happening inside the process |

An isolated, ungoverned agent is not an upgrade. So the design has to carry
the governance across the boundary, not drop it.

### How it surfaces in `agent create`

```
kmx agent create <name>
  --image <ref>              make it a BYO agent (serves A2A on :8080)
  --isolation kata | none    schedule onto a VM-isolated pool (default: none)
```

Rules that keep it honest:

1. **Defaults change nothing.** No `--image`, no `--isolation`, and the
   manifest is exactly today's declarative Agent.
2. **`--isolation` requires `--image`.** A declarative agent cannot be
   VM-isolated: `runtimeClassName` is not exposed, so node placement is the
   only lever, and it is only meaningful for a workload we are placing
   deliberately. Refusing is better than a flag that silently does nothing.
3. **`--image` carries the governance seams across.** kmx injects the
   proxy's `baseUrl` and the gateway's endpoint into `deployment.env`, plus
   the credential reference — the same values the governed preset would have
   used. It prints exactly what it injected.
4. **And says plainly what it cannot check.** kmx cannot verify the image
   honours those variables. The command must say so rather than imply the
   agent is governed because the env is present. *"Configured, not proven"*
   is the honest status, and the ledger is where it becomes proven.
5. **`--isolation kata` refuses on a cluster with no such nodes.** Fail
   closed with the reason, like `agent create` already does when it cannot
   read a `ModelConfig`. A toleration that matches nothing schedules onto an
   ordinary node and looks like it worked.

### What `Spec` gains

`scaffold.Spec` grows `Image` and `Isolation`, both zero-valued to today's
behaviour. `Generate` branches on `Image` for `type: Declarative` versus
`type: BYO`; `Isolation` only adds `nodeSelector`/`tolerations`. The
generator stays a pure function of the spec — testable with no cluster.

### Gates before it ships

- a BYO image on a Kata pool answers `kmx agent chat`, and `kubectl` shows
  it on an isolated node;
- **a ledger row appears for its model calls** — this is the one that
  matters, because it is the difference between governed and merely
  configured;
- a tool call through the gateway lands in the audit trail;
- `--isolation kata` on a cluster without those nodes refuses rather than
  quietly scheduling normally.

---

## The others, and why not yet

**B — Deployment + `runtimeClassName`.** Mechanically the cleanest isolation
and the largest loss: A2A serving, sessions, model plumbing and tool wiring
all become ours. It replaces the runtime, which is the thing option A exists
to avoid.

**C — SpinKube.** Kubernetes is not the obstacle; the agent is. LLM SDKs and
MCP clients compiled to WASI are immature, and an agent loop fits WASM's
request-scoped model badly. Worth a spike precisely because it is cheap to
disprove — and a failed spike is a finding, not a waste.

**D — Hyperlight around tools.** The most interesting of the unbuilt
options, because it sits where the blast radius is. The agent loop is LLM
calls and network waits; **tool and code execution** is the dangerous part,
and that is exactly where the plane already sits. kagent points the same
way: `spec.sandbox` exists today only as egress policy for "sandboxed
execution paths", beside `executeCodeBlocks`. If option A lands and one
thing follows it, this is it.

**E — upstream `runtimeClassName`.** The delegate-don't-build path, and the
one that helps everyone. Slow, and not ours to schedule.

## Risks

- **Isolation invites overclaiming.** A micro-VM is not a boundary for
  everything an agent can reach; the model seam and the gateway still are.
  Whatever ships must say what it does *not* protect.
- **The BYO cliff is easy to miss.** Losing `modelConfig` and `tools` is
  invisible until you read the CRD. Anything we generate must make the
  governance wiring explicit in the YAML, not implicit in an image.
- **"Configured" is not "governed".** Env variables are an intention; the
  ledger is the evidence. Documentation must not blur them.
- **A flag with one working value is not a provider model.** If only option A
  ships, `--isolation kata` is a concrete feature. Naming it honestly beats
  pretending to a generality we do not have.

## Not proposed

Writing an agent runtime. kagent's controller, A2A serving, sessions, model
providers and MCP wiring would all have to be replaced, and rebuilding an
upstream's control surface is the mistake that caused this project's only
restart. Every option above keeps that runtime or borrows another project's
— never builds ours.
