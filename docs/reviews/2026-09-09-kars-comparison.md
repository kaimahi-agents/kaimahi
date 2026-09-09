# Given KARS exists, what does this project still add?

**Subject:** [`Azure/kars`](https://github.com/Azure/kars) — "Agent
Reference Stack for Kubernetes", an open-source reference implementation
from the Azure Cloud Native team (the team behind AKS and Azure Linux).
Twelve CRDs, eight first-class agent runtimes plus BYO, a Rust inference
router in every sandbox pod, a hash-chained audit log, token budgets, an
encrypted agent mesh, and one CLI from laptop to AKS. Not an officially
supported Microsoft product; CRDs at `v1alpha1`; no SLA.

**Why this lane exists.** The published feature list overlaps this
project's almost entirely and in places exceeds it. The mission says
running agents on AKS should be easy and that Kaimahi is a means to that,
not the end. So the question was not how we compare. It was whether we
should still exist — and "not enough" was a permitted answer.

**Read at commit `7eb039e` (2026-08-31), CLI `@kars-runtime/cli@0.1.26`,
published images `ghcr.io/azure/*:latest`, run on kind on 2026-09-09.**
Nothing in their repository was modified, and nobody there was contacted.
Nothing in this repository's product code changed either; this lane is a
report.

---

## The answer, first

**Kaimahi's central claim survives, and it is now the only one.** An
approval welded to the digest of specific argument values is a thing KARS
does not have — not per-call, and on the path an adopter would actually
use, not per-tool either. That is question 1, and it is settled by a
US$6.50 order that completed while the policy binding that sandbox
declared `approval.mode: always` and a per-transfer cap of one cent.

**The architectural difference is real but narrower than assumed.** KARS
cannot govern a Deployment it did not build. But its BYO path took a
third-party image with its source untouched, and the distance between
"KARS built this pod" and "this pod existed already" turned out to be one
image rebuild and four CRs, not a rewrite. "No adoption required" is a
genuine niche and this report says how narrow.

**Where they are ahead of us, they are clearly ahead**, and the list is
not short: pod-level isolation we do not attempt, a per-tool egress guard
in the kernel, seven admission policies, cosign-signed images with SBOMs,
a signed-OCI policy distribution path, and a `maturity.md` that is the
most honest document of its kind I have read — it distinguishes
"enforced", "reconciled but not gated", "library-only" and "roadmap", and
it is right more often than it is wrong.

**And the recommendation at the end is not "stop".** It is that two of
the seven things this project has built are worth taking to them, and the
rest is worth keeping only if the argument-bound approval is what we
lead with.

---

## 1. Does their approval bind a CALL or a TOOL?

**Neither, on the MCP tool path. It does not bind at all.** The tool-name
allowlist is real and enforced; every finer control on `ToolPolicy` is
accepted, reconciled, reported `Ready`, and never consulted.

### What the CRD offers

`ToolPolicy.spec` (`deploy/helm/kars/templates/crd-toolpolicy.yaml`)
carries four things that look argument-aware:

| field | what it promises |
|---|---|
| `approval.mode` | `never` \| `always` \| `aboveThreshold`, with a currency `threshold` |
| `commerce.perTransferCap` | "Even within daily/monthly, a single transfer above this is refused" |
| `commerce.counterpartyAllowlist` | "Empty = deny-all (fail-closed)", AP2 DID or domain |
| `rateLimit` | `rps` / `burst` / `window` |

and `appliesTo` selects by tool **name**, MCP server name, and sandbox
labels. So the docs' "one `ToolPolicy` = one tool gate" reads per-tool,
while `docs/mcp.md:169` says a `ToolPolicy` gives you "per-tool rules
(arguments, rate limits, approval)". The word *arguments* appears in no
field of the CRD.

### What happens when you use it

A third-party MCP server was registered as an `McpServer` CR and its
tools called through the router's own `/mcp` forwarder, with this bound
to the sandbox:

```yaml
spec:
  appliesTo: {tool: "*", sandboxMatchLabels: {kars.azure.com/sandbox: sundae-concierge}}
  approval: {mode: always, channel: cli}
  commerce:
    dailyCap: "USD 1.00"
    perTransferCap: "USD 0.01"
    counterpartyAllowlist: ["did:example:nobody"]
  rateLimit: {rps: 1, burst: 1, window: "60s"}
```

The CR's own status read `phase: Ready`, `reason: RouterEnforcing`.

```console
$ … tools/call sundae.quote_order  {size: CLASSIC, flavors: [VANILLA, CHOCOLATE]}
  draft-650e12d923   total_display "$6.50"

$ … tools/call sundae.submit_order {draft_id: draft-650e12d923, customer_name: Ada,
                                    idempotency_key: kars-probe-1}
  {"order_id": "sundae-03157899", "status": "submitted", "total_display": "$6.50"}
```

A US$6.50 transaction, under a US$0.01 per-transfer cap, a US$1.00 daily
cap, a counterparty allowlist naming nobody, and `approval: always`. Then
six calls back to back under `rps: 1, burst: 1` — six `HTTP 200`.

And nothing was written down:

```console
$ GET /agt/audit          →  {"entries": []}                 # after nine tool calls
$ GET /agt/status         →  policy_evaluations: 0, policy_rules: 10
```

**The control test matters more than the result.** One inference call
through the *same router on the same pod*, immediately afterwards:

```console
$ POST /v1/chat/completions  →  200
$ GET /agt/audit             →  2 entries
  {"action":"inference:chat_completions:claude-haiku-4.5", "decision":"allow", …}
  {"action":"output:OK",                                   "decision":"allow", …}
```

So the audit chain and the policy engine work. They are simply not in the
path of a tool call.

### Why, in their code

- The `/mcp` forwarder gates on tool-name membership and nothing else —
  `inference-router/src/mcp/forwarder.rs:228-239` checks
  `entry.tools.contains_key(suffix)` and then passes `arguments` opaquely
  upstream. The whole `inference-router/src/mcp/` module (12 files)
  contains no reference to the governance engine.
- `approval`, `commerce` and `rateLimit` are compiled to JSON by
  `controller/src/tool_policy_compile.rs:58-95` and written as ConfigMap
  key `profile.json` by `controller/src/tool_policy_reconciler.rs:639`.
  Nothing reads that key — and the router's profile loader takes YAML
  only (`inference-router/src/governance/mod.rs:330`: `e == "yaml" || e
  == "yml"`), so `profile.json` is filtered out of the very directory it
  is mounted into. `spec.commerce` is inert end to end.
- `type: approval` in an AGT profile *is* enforced, but as a terminal
  deny (`governance/mod.rs:503-506` maps `RequiresApproval` to
  `allowed=false`). There is no pending-approval store, ticket, or
  redemption anywhere in the tree. Their own generated default profile
  says so, in a comment above the blanket `tool:*` allow: *"A separate
  approval gate can be re-enabled once the native approval UI is wired
  up."*
- The action grammar the policy engine matches on is a string the
  runtime builds — `tool:<tool_name>`,
  `inference:chat_completions:<model>`, `shell:<command>`. No argument
  value is ever in it. The audit record has exactly five keys —
  `action`, `agent_id`, `decision`, `result`, `timestamp` — so arguments
  are not merely unenforced, they are unrecorded.
- **Where tool gating does live is the more interesting finding:** in the
  agent's own runtime plugin, which calls the router's `/agt/evaluate` and
  asks. The router answers a question; it does not sit in the path. The
  Hermes plugin fails **open** after three consecutive failures
  (`runtimes/hermes/…/governance.py:38-41`) and treats a malformed
  response as allow (`:220`). `docs/runtimes/CONTRACT.md:211` calls the
  contract cooperative, which is exact.

### What does work

`McpServer.spec.allowedTools` is genuinely enforced, catalog-side and at
dispatch (`forwarder.rs:315-318` fails closed on an empty list,
`:332` filters). Removing one tool from it:

```console
$ tools/list                        →  [list_menu, check_availability, quote_order]
$ tools/call sundae.submit_order    →  -32601 "tool not found: sundae.submit_order"
```

**So: KARS binds a tool name. That is the whole of it.** Kaimahi's
approval — bound to `submit_order: draft_id draft-c2f2dedba1,
customer_name Ada`, spendable on that call and no other — has no
counterpart here.

### One thing they have that we lack

`commerce.counterpartyAllowlist` is inert today, but it is the *shape* of
the gap this project's own P13 lane found open and left open: "the
constraint bounds the amount but not the payee, and the agent walked
through it." They wrote the field down. We have the enforcement engine
and not the field.

### And one overclaim, stated precisely

`status.reason: RouterEnforcing` comes from
`controller/src/status/router_confirmation.rs:212-215`, which defines
`Confirmed` as *"every referencing sandbox's router echoed the exact
digest the controller published"* — a sha256 over the `agt-profile.yaml`
bytes. It is a byte-delivery receipt for one ConfigMap key. It attests
that a file arrived and parsed. It does not attest that `approval`,
`commerce` or `rateLimit` were shipped — those live in the JSON key that
is never mounted — nor that any rule was evaluated. "The router confirms
it loaded these policy bytes" would be accurate. We have shipped our own
status overclaims and had them found; this is that, and it is worth
naming without relish.

---

## 2. Can KARS govern an agent it did not deploy?

**No — the pod is built wholesale from a CR. But the BYO path is closer
to "point your existing thing at us" than the twelve-CRD headline
suggests, and the premise this lane started from was wrong.**

### The premise that was wrong

The router does **not** bind loopback. `inference-router/src/main.rs:506`
is `format!("0.0.0.0:{}", config.port)`, a string literal with no
override, and the comment above it (`:484-490`) explains why: the
controller reaches the listener over the per-sandbox Service DNS, which
only works off loopback. `docs/runtimes.md` and several source comments
describe the endpoint as loopback-only; the bind says otherwise.

Two consequences follow, both from their own code:

- `/v1/*` carries no auth middleware — `main.rs:329` merges the inference
  routes into the public router while `:339-351` guards only
  `admin`/`egress`/`spawn`/`internal`.
- Caller identity is a self-reported header:
  `routes/chat_completions.rs:286-297`,
  `headers.get("x-kars-sandbox") … .unwrap_or("unknown")`. That line is
  the `agent_id: "unknown"` this lane observed on its own audit rows.

**So the enforcement boundary for the model seam is the NetworkPolicy,
not the router.** `controller/src/reconciler/mod.rs:1063-1080` admits
port 8443 from the operator namespace and from namespaces carrying the
label `kars.azure.com/role: sandbox`. That is a defensible design — but
it is a different design from the one the docs describe, and it is worth
an adopter knowing which of the two they are relying on. This lane
verified the reachability consequence on its own cluster and is
deliberately not printing the recipe: it is their finding to handle, not
ours to publish, and their `SECURITY.md` routes such things to MSRC.

### What adoption actually costs

There is no mutating webhook (`MutatingWebhookConfiguration`: zero
occurrences repo-wide), no injection annotation, and no adoption path.
The controller *does* watch Deployments
(`reconciler/mod.rs:3389-3393`) but the mapper (`:3443-3454`) returns
`None` unless the Deployment already carries KARS's own labels — it is a
reverse index to the owning CR, not a hook. The pod spec is a literal
(`mod.rs:2038` init `egress-guard`, `:2076-2087` agent + router), and
`crd.yaml:21` makes `runtime`, `sandbox` and `inferenceRef` required.

So an existing Deployment must be re-expressed as a `KarsSandbox`. What
that took, for `pauldotyu/sundae-funday` — the same third-party app the
[foreign-app report](2026-09-08-foreign-app-sundae-funday.md) measured
against us:

1. **Rebuild the image to run as UID 1000.** Theirs ships UID 10001. This
   is load-bearing, not cosmetic: the egress guard is
   `--uid-owner 1000 … -j DROP` (`reconciler/pod_spec.rs:91-134`), so at
   10001 the agent's traffic escapes the guard entirely. The controller
   pins `runAsUser: 1000` (`mod.rs:1950`) and never checks the image.
2. **Four CRs**: `McpServer`, `InferencePolicy`, `ToolPolicy`,
   `KarsSandbox`.
3. **One `networkPolicy.allowedEndpoints` entry** so the router may reach
   their MCP service.
4. Their two seam values, exactly as with us:
   `OPENAI_BASE_URL=http://127.0.0.1:8443/v1` and
   `SUNDAE_MCP_URL=http://127.0.0.1:8443/mcp`.

Their **source was not touched** and their chart was not used. The
`org.kars.runtime.contract` label the docs ask for is never read
(`examples/byo-quickstart/k8s/clawsandbox.yaml` calls registry-side label
introspection "a Phase 4 add-on"); BYO validation is `contractVersion` in
`{"v1"}` plus a self-described "deliberately permissive" image-reference
check (`reconciler/byo_contract.rs:71`).

**Verdict.** The difference is one image rebuild and a CR, against our
two environment variables. That is a real difference — an adopter who
cannot rebuild the image, or whose agent is a Deployment they do not own,
cannot use KARS at all — but it is a narrow one, and the honest framing
is "no redeploy required" rather than "no adoption required". It is not
the moat it looked like from the README.

---

## 3. What does it cost to get an agent running?

Measured the way the foreign-app report measured us. **On kind. The AKS
half was not run** — see §6.

### 3a. Their quickstart, unchanged

**Prerequisites (5):** `kind`, `kubectl`, a container runtime, Node 22+,
and an inference-provider credential.

**Commands (2), plus five interactive answers and one browser login:**

```console
$ npm i -g @kars-runtime/cli            #  7 s
$ kars dev --release --target local-k8s
```

| | |
|---|---|
| wall clock, first command to a chatting agent | **12 m 27 s** |
| of which human-in-the-loop | ~3 m 25 s (device-code login round trip + five prompts) |
| machine time (sum of the 14 reported steps) | **4 m 48 s** |
| slowest steps | image pull 1 m 4 s, `kind load` 1 m 46 s, Prometheus+Grafana 44 s |
| result | 12 CRDs, controller, mesh relay + registry, Headlamp, kube-prometheus-stack, Grafana, one governed sandbox |
| first model call through the router | **0.7 s**, metered, audited |

It worked. The stepper is legible, every step is timed, and the failure
modes I hit were mine.

**Against us:** `create-kaimahi-agent` measured 1 prerequisite and
**178 s** on a clean machine (W31). The comparison is not
apples-to-apples and saying so is the point — KARS spends its extra ten
minutes bringing up a monitoring stack, a mesh, a dashboard and a
controller. But that is also the criticism the foreign-app report levelled
at `kmx lift`, which "still installs a runtime the adopter did not ask
for", and it lands here harder: there is no way to ask `kars dev` for
less.

### 3b. The same third-party app, governed

`t0` at the first `docker build`; the BYO sandbox `Running` at
**1 m 52 s**; the tool seam live and forwarding at **7 m 17 s**. About
five minutes of that was one mistake of mine (a wrong `SERVICE` value in
*their* app, not KARS) plus the restart it cost — so **roughly 3 minutes
of actual work**, once the shape was known.

Learning the shape was the expensive part, and it is measurable:
**four times I read a CRD rather than a doc**, every one because the
documentation's example did not apply. Their BYO example
(`docs/runtimes.md:113-128`), applied verbatim:

```console
$ kubectl apply -f - --dry-run=server
The KarsSandbox "my-byo-agent" is invalid:
* spec.runtime.byo.contractVersion: Required value
* spec.sandbox: Required value
* spec.inferenceRef: Required value
```

Three required fields missing from the page that teaches the feature. The
example that ships under `examples/byo-quickstart/` is correct — the
defect is confined to the doc.

**Against us:** the foreign-app report governed the same application by
changing **two config values**, with their Deployment, their chart and
their image all untouched, and put a governance plane on a cluster it did
not own in 2 m 34 s. That difference stands.

### 3c. One thing neither of us can do

Their app's model client is `agent_framework.openai.OpenAIChatClient`,
which in `agent-framework` 1.14 is the **Responses** client
(`_chat_client.py:589`, `responses_mode=True`). KARS routes `/v1/responses`
— and relays it:

```console
$ POST /v1/responses  {"model": "claude-haiku-4.5", …}
  {"error":{"message":"model claude-haiku-4.5 does not support Responses API.",
            "code":"unsupported_api_for_model"}}
$ POST /v1/responses  {"model": "gpt-4.1", …}
  {"error":{"message":"model gpt-4.1 is not supported via Responses API.", …}}
```

Neither string exists in the KARS tree: the refusal is GitHub Copilot's.
So **Microsoft Agent Framework — one of their eight first-class runtimes
— cannot complete a turn against the provider their own quickstart
recommends**, using the framework's default chat client. The router does
not translate.

We hit the same wall from the other side: our model seam had no
Responses entry at all, and once one was added it metered as zero tokens.
Theirs has the path and forwards honestly to an upstream that refuses.
Both are the same underlying fact — the Responses API is now the default
wire shape of a major framework — and it is the single most portable
finding across both lanes.

---

## 4. How real is it?

Real, and better than its `v1alpha1` label suggests. The controls that
are advertised as kernel- and pod-level are the ones that work; the ones
advertised as policy are where the gaps are. Everything below was
observed, not read.

### Works as documented

- **`kubectl exec` into the agent container is refused** by a
  `ValidatingAdmissionPolicy`, with a break-glass namespace label that is
  audited. Sibling containers stay reachable for ops. This is a good
  control and the error message is a model of its kind.
- **The egress guard, the seccomp installer, the read-only rootfs, the
  distroless router.** All present in the running pod.
- **Token budgets are enforced.** With `{dailyTokens: 10,
  perRequestTokens: 5}`: `max_tokens: 100` was refused before the call
  ("Requested max_tokens=100 exceeds InferencePolicy
  tokenBudget.perRequestTokens=5"), and after one call spent 19 tokens
  the next four were refused with "Daily token budget exceeded (19/10
  tokens). Resets at the next UTC midnight."
- **The hash-chained audit verifies**, and the tool-name allowlist holds
  (§1).
- **`maturity.md` is largely accurate.** It says the policy engine gates
  "exec / fetch / spawn / mesh send" — and it does not claim tool calls.
  It carries no row for tool approval at all. Read carefully, it does not
  make the claim §1 refutes; the CRD, `docs/mcp.md` and the CR status do.

### Does not

- **The token budget resets when the pod restarts.** Proven: a sandbox at
  19/10 and refusing, `kubectl delete pod`, same UTC day, spending again
  from zero. The cause is in the router's own startup log, on the stock
  quickstart agent as well as mine:

  ```
  WARN "Could not create token-budget persistence dir — falling back to in-memory"
       path=/var/lib/kars error="Read-only file system (os error 30)"
  WARN "Failed to open audit JSONL writer — local mirror disabled"
       dir=/var/log/kars/audit error="Read-only file system (os error 30)"
  ```

  `maturity.md` calls these counters "✅ Enforced — on-disk persistence in
  the router". **And an adopter cannot fix it:**
  `KarsSandbox.spec.sandbox.writablePaths` is parsed
  (`controller/src/crd.rs:876`) and never read by anything in
  `controller/src/reconciler/`; the router container gets exactly one
  mount, `admin-token`, read-only (`reconciler/mod.rs:2086-2107`). The
  reconciler even creates the right volume and never mounts it —
  `mod.rs:2230-2239`, `// writable emptyDir volume for trust store +
  audit log persistence`. This needs a chart change.
- **So the audit log is not durable either.** With the JSONL mirror
  disabled, `/agt/audit` is an in-process buffer that dies with the pod.
  `audit_jsonl.rs:4-5` states the opposite as a definition-of-done.
- **The budget is post-hoc, not reserved.** `check_budget` takes a read
  lock and releases it (`budget.rs:225`); `record_usage` takes the write
  lock after the response (`:285`). One call overshot a cap of 10 by 9,
  and N concurrent calls at 99% of budget all pass. Our plane does a
  reservation plus a row lock for exactly this reason (P9).
- **`kars audit tail` cannot run on the published images.** It
  `kubectl exec`s `sh` into the distroless router
  (`cli/src/commands/audit.ts:124-142`); the router has no shell, and the
  exec-ban policy would refuse it anyway.
- **The audit chains model output into the `action` field.**
  `chat_completions.rs:1209`:
  `format!("output:{}", &sanitized[..sanitized.len().min(200)])` — 200
  **bytes**, no ellipsis, so truncated and complete are
  indistinguishable, and a multi-byte character straddling byte 200 is a
  panic on a `String` byte-slice. I did not trigger it.
- **The `/v1/traces` endpoint the runtimes export to does not exist.**
  `runtimes/maf-python/…/otel.py:6-7` asserts "the router sidecar exposes
  an OTLP/HTTP collector at `/v1/traces` and `/v1/metrics`" and defaults
  there; no such route is in the router's table. I watched the exporter
  fail: `Failed to export span batch due to timeout, max retries or
  shutdown`.
- **`docs/quickstart.md` still offers GitHub Models as the free option.**
  Their configured endpoints answer **410 Gone**
  (`models.github.ai/catalog/models`, `…/inference/chat/completions`) —
  the service was retired 2026-07-30. We shipped the same stale claim once
  (D7→D8) and it cost a lane a day.
- **`docs/blueprints/00-index.md:39` says each audit record is signed.**
  `audit/merkle.rs:24-25` says, in their own words, "do not describe the
  router's audit output as 'signed'." `maturity.md` has this right; the
  blueprint does not.

**None of this is disqualifying and it should not be read that way.** It
is a `v1alpha1` reference stack with an unusually honest maturity page,
and a maintainer reading the list above would recognise most of it. It is
here because the comparison is worthless without it.

---

## 5. The secondary questions

**Budgets vs. the ledger.** They count **tokens**; we count **money**.
There is no price table anywhere in their tree — every counter is `u64`
tokens. The unit is the sandbox, taken from a self-reported header and
bucketed as `unknown` when absent. And the sharpest detail: the GitHub
Copilot upstream returns a `copilot_usage` block carrying real prices
(`cost_per_batch`, `total_nano_aiu`) which the router relays to the
client **verbatim and unread** — zero occurrences of `copilot_usage` in
the tree. Price data reaches the one component that could ledger it and
is written straight through. Our `cost_source` and the price gate that
refuses a call it cannot price have no counterpart.

**Their audit vs. ours.** Theirs is tamper-*evident* — SHA-256 chained,
verifiable by replay, and honestly labelled as detection-not-signing on
the maturity page. Ours is a Postgres ledger with argument summaries, a
caller claim and an observed address (W42). Theirs chains three fields;
`/agt/audit` does not return `prev_hash` or `hash`, so the chain cannot
be checked from what the read surface gives you. Theirs records model
output; ours records tool arguments. **Neither records what the other
does**, and that is the most useful sentence in this section.

**OTLP.** Traces and metrics, no logs, and only from the runtime
adapters, not the router — the router declares no OTel dependency and
`telemetry/gen_ai.rs` is `#![allow(dead_code)]` despite claiming to emit
GenAI spans for every model call. The endpoint *is* adopter-settable
(`OTEL_EXPORTER_OTLP_ENDPOINT`), which is more than we offer, but it
defaults to a route that does not exist. Their Prometheus surface is
real: 35 `kars_*` metrics on `:9090`, including
`kars_tokens_total{sandbox,model,direction}` — and no cost metric.
**The observability gap an adopter asked us to close is not closed there
either.**

---

## 6. What was not measured, and why

**The AKS half of question 3 was not run.** `kars up` provisions its own
resource group, ACR, AKS cluster, Azure AI Foundry project, Content
Safety binding and model deployment — it does not fit beside a cluster
you already have, which is itself part of the answer. The user ruled to
skip it rather than spend on it. So the AKS comparison in this report is
structural, not timed: `kmx lift --byo --step boundary,plane` put a
governance plane on somebody else's AKS cluster in 2 m 34 s; the KARS
equivalent creates the cluster.

**Their MCP OAuth path, the mesh, the A2A gateway, Headlamp, `kars
handoff` and confidential isolation were not exercised.** Any of them
could change the picture in their favour and none was measured.

**One live check is reported at the architectural level only** (§2), for
the reason given there.

---

## 7. Spend and teardown

**US$0.00.** No Azure resource was created. Every token came from a
GitHub Copilot seat the user already holds, on `claude-haiku-4.5`, chosen
to keep quota use minimal. The calls were a few dozen prompts of well
under a hundred tokens each; the largest single reading this lane took
from `kars_tokens_total` was 247 tokens across both sandboxes.

Teardown, on a cluster this lane created:

```console
$ kind delete cluster --name kars-dev     Deleted nodes: ["kars-dev-control-plane"]
$ kind get clusters                       No kind clusters found.
$ kubectl config get-contexts | grep kars (none)
$ docker images | grep -c kars            0
$ command -v kars                         (removed; npm rm -g @kars-runtime/cli)
$ ls ~/.kars                              No such file or directory
```

Every image tag this lane pulled or built is gone. Two pre-existing
local `sundae-funday` tags from the earlier foreign-app lane were left
alone rather than matched by name. The GitHub OAuth grant made for the
device login is the user's to revoke.

---

## 8. So: given KARS exists, what does Kaimahi still add?

One thing, and it is the thing this project has been building toward
since P12: **an approval welded to the exact call.** KARS can tell you
that `submit_order` is allowed; it cannot tell you that *this* order, for
*this* amount, to *this* payee, was the one a human said yes to, and it
cannot stop the yes being spent on a different one. That is not a gap in
their roadmap — the fields exist and are inert, the approval type is a
terminal deny, and the tool gate lives inside the sandbox with the agent
and fails open. It is a different design, and ours is the one that can
answer the question. Beside it sit two smaller things that survive
contact: a ledger denominated in money rather than tokens, with a gate
that refuses what it cannot price; and the ability to govern a workload
without rebuilding its image. Everything else this project has — budgets,
audit, egress, isolation, a CLI, an AKS path — they have, and in several
cases have better.

**Is that enough to justify continuing? Yes, but not as a platform.** The
mission says success is measured in whether running an agent on AKS got
easier, including by routes that go around us entirely, and on that
measure a twelve-CRD reference stack from the AKS team is a route that
goes around us and gets there. Continuing to build a second one is the
product-centric reading the mission already corrected once. The honest
shape of the work is narrower and more useful: **take the call-bound
approval to them.** It is a `ToolPolicy` field, a pending-grant store
keyed by an argument digest, and a redemption check in the forwarder that
already sees the arguments and passes them through untouched — landing in
the layer that is theirs to own, in a project whose `commerce` block
shows they already want it. A fix there helps everyone running agents on
Kubernetes whether or not they ever hear of this project, which is the
mission stated directly. The rest of this repository is then worth
keeping for as long as it is the place that proof is demonstrable, and no
longer.

If the answer is that we should contribute there instead of continuing
here: that is close to what this report concludes, and the difference is
only that one capability is not yet ready to hand over, because nobody
outside this project has ever used it.
