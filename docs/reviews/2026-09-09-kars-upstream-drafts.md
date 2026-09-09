# Drafts for the KARS maintainers

**What this file is.** Two send-ready reports for the maintainers of
[`Azure/kars`](https://github.com/Azure/kars), drafted from the findings
of the [comparison lane](2026-09-09-kars-comparison.md), plus the record
of why a third candidate is not being sent.

**Nothing here has been filed, posted, or sent.** The drafts are text for
the user to send, or not. This lane contacted nobody.

**Why send anything.** The mission is that running agents on AKS should
be easy, and that this project is a means to that rather than the end of
it. KARS is another means, from the AKS team. Two of the comparison
lane's findings are things its authors would want to know, and telling
them advances the mission whether or not anyone there ever hears of this
project. That is also why the drafts below mention no project, make no
comparison, and read as what they are — reports from someone who ran
their software.

**Everything in the drafts is either public in their repository or
reproducible from it.** No Azure identifiers, no account names, no
cluster names, no third-party application names.

---

## Verdicts

Each candidate was re-verified against `Azure/kars` today. `origin/main`
is still exactly `7eb039e` — `git ls-remote` returns that SHA for
`refs/heads/main` — so nothing here has decayed since the comparison
lane read it. Their issue tracker was read: no open or closed issue
covers either surviving finding.

| # | Candidate | Still holds? | Disclosed in `maturity.md`? | Route |
|---|---|---|---|---|
| 1 | `spec.commerce` / `spec.approval` / `spec.rateLimit` are inert end to end | **Yes**, and the code is unambiguous | **No.** That page has no row for any of the three. Their `docs/api/lifecycle.md:346` claims the opposite | **Private** (MSRC) |
| 2 | The runtime plugin fails open | **Half.** The grace period is deliberate and documented — dropped. The unparseable-response allow in the OpenClaw plugin is real and undisclosed | No, and it is not the part `CONTRACT.md` documents | **Private** (MSRC) |
| 3 | `status.reason: RouterEnforcing` attests byte delivery, not enforcement | **No — do not send.** Their own docs already define it exactly that way | n/a | **Do not send** |

**What this lane re-ran, and what it did not.** Every code-level claim in
both drafts was re-read against `Azure/kars` in this lane — every file
path, line number and quoted line below was checked today, and the
"static, no cluster needed" reproduction in draft 1 was executed. The
**cluster-side observations in draft 1 §B** (the completed order, the six
`HTTP 200`s, the empty audit, the control inference call) are transcribed
from the comparison lane's live kind run on 2026-09-09; that cluster was
torn down and this lane did not stand another one up. They are this
project's own record of its own run, but the user should know that is
what they are vouching for when they send it. Draft 2's dynamic
reproduction is a stub the reader runs; it was not executed here, and the
draft says so in its own words.

**Finding 1** — holds. `approval`, `commerce` and `rateLimit` compile to
ConfigMap key `profile.json`; the only loader of the directory that key
is mounted into accepts `.yaml`/`.yml`; no component anywhere in the tree
reads `profile.json`. `maturity.md` is silent on all three, and
`docs/api/lifecycle.md:346` positively asserts that
"runtime-side enforcement of commerce / rate-limit / approval is
co-located in the in-process AGT plugin", which the code does not
support. **Send privately.**

**Finding 2** — half of it holds. `docs/runtimes/CONTRACT.md:257`
documents the fail-open grace period explicitly, by design, with a
default of 3, a configuring env var and a "set to 0 to fail closed
immediately" escape. That is disclosed, deliberate, and not a finding.
What survives is narrower and is not disclosed anywhere: in the OpenClaw
plugin an `/agt/evaluate` response whose body does not parse as JSON is
an **explicit allow** that also **resets** the consecutive-failure
counter, so a router that is reachable but answering with a non-JSON body
never reaches the documented fail-closed state at all. **Send privately,
phrased as a question.**

**Finding 3** — does not survive, and should not be sent. The claim was
that `RouterEnforcing` attests byte delivery while reading as
enforcement. But `docs/api/conditions.md:89` already defines the reason
as "data-plane echoed back the compiled AGT-profile digest";
`conditions.md:94` says a ToolPolicy is `Ready=True / RouterEnforcing`
"only when at least one referencing sandbox's router has confirmed the
loaded profile digest matches what the controller compiled"; and the
condition message the controller actually writes
(`controller/src/tool_policy_reconciler.rs:543-551`) is "all {total}
referencing sandbox router(s) confirmed agt-profile digest". All three
are accurate descriptions of a digest echo. The word in `kubectl get` is
stronger than the mechanism, but a maintainer would reasonably answer
"that is what the reason means, and we wrote it down three times" — and
they would be right. Sending it would spend credibility on the one
candidate of the three where they are not wrong.

Its one live residue — `lifecycle.md:346`'s claim about the in-process
plugin enforcing commerce — is not a status-naming problem at all. It is
the clearest single piece of evidence for finding 1, and it has been
folded into draft 1 rather than sent on its own.

---

## Routing

Their `SECURITY.md` is explicit: **"Do NOT open a GitHub issue for
security vulnerabilities"**, and routes reports to MSRC
(https://msrc.microsoft.com/create-report, secure@microsoft.com), asking
for description, reproduction steps, impact assessment and suggested
mitigations. Both surviving findings are of the form "a control that is
documented as gating does not gate", which is that class. Both drafts are
written in the shape MSRC asks for and will paste into a GitHub issue
unchanged if the maintainers would rather have them in the open.

Recommendation: **send both to MSRC**, and say in the report that you are
happy for either to be moved to a public issue if they judge it isn't a
vulnerability. That respects their stated process without pretending
these are more severe than they are.

---

## Draft 1 — send privately

**Suggested subject / title:** `ToolPolicy spec.commerce, spec.approval
and spec.rateLimit are compiled to a ConfigMap key that nothing reads`

~~~markdown
### Summary

The controller compiles `ToolPolicy.spec.approval`, `spec.commerce` and
`spec.rateLimit` into ConfigMap key `profile.json` and mounts that
ConfigMap at `AGT_POLICY_DIR`. The only component that loads that
directory accepts `*.yaml` / `*.yml` and skips everything else, and no
component anywhere in the repository reads `profile.json`. A `ToolPolicy`
declaring `approval.mode: always`, a `commerce.perTransferCap` and a
`rateLimit` is accepted, reconciled and reported `Ready` — and gates
nothing.

I could not find this on `docs/maturity.md`, which is why I am reporting
it rather than assuming it is a known gap: that page distinguishes
"Enforced" / "Reconciler-only" / "Library-only" carefully enough that the
absence of a row reads as "not a distinct capability" rather than "not
yet wired".

### Version

Repository at `7eb039e`. `@kars-runtime/cli@0.1.26`, published
`ghcr.io/azure/*:latest` images, on kind.

### Reproduction — A. static, no cluster needed

```bash
git clone https://github.com/Azure/kars && cd kars
git checkout 7eb039e
git grep -n -e 'profile\.json' -- . ':(exclude)vendor/**'
```

Every tracked file, not just source. Outside `CHANGELOG.md` that is six
hits: one write, one mount comment, two Mermaid arrows in
`docs/api/lifecycle.md`, one unrelated
`controller/src/reconciler/tests.rs:826`, and the analogous
`InferencePolicy` doc comment. No reader:

- **Written** — `controller/src/tool_policy_compile.rs:58-95` compiles
  the three blocks to JSON; `controller/src/tool_policy_reconciler.rs:639`
  inserts it as `data["profile.json"]`.
- **Mounted** — `controller/src/reconciler/mod.rs:2157-2183` mirrors that
  ConfigMap into the sandbox namespace and mounts it at
  `/etc/agt/policies` with `AGT_POLICY_DIR` pointed at it. The comment at
  `:2158` names `profile.json` as the source key.
- **Skipped** — `inference-router/src/governance/mod.rs:330`:
  `if p.extension().is_some_and(|e| e == "yaml" || e == "yml")`. So
  `profile.json` is filtered out of the very directory it was mounted
  into. `runtimes/` does not read it either (only
  `runtimes/openclaw/skills/agt-governance/SKILL.md:27` mentions
  `$AGT_POLICY_DIR`, as prose).

The tool-call path never consults the policy engine at all:

- `inference-router/src/mcp/forwarder.rs:235` gates on
  `entry.tools.contains_key(suffix)` and forwards `arguments` opaquely.
  `grep -rn governance inference-router/src/mcp/` returns two comments
  and no call.

And `spec.rateLimit` has an independent reason not to apply — the
per-tool limiter is configured from env vars, not from the CR:

- `inference-router/src/governance/mod.rs:195-210` builds
  `McpSlidingRateLimiter` from `TOOL_RATE_LIMIT_MAX` (default 100) and
  `TOOL_RATE_LIMIT_WINDOW_SECS` (default 60). Those two names appear
  nowhere else in the tree, so nothing sets them from
  `ToolPolicy.spec.rateLimit`.

### Reproduction — B. on a cluster

1. `kars dev --release --target local-k8s`.
2. Register an MCP server per `docs/mcp.md` and confirm its tools list
   through the router's `/mcp` forwarder.
3. Bind a `ToolPolicy` to the sandbox:

   ```yaml
   spec:
     appliesTo:
       tool: "*"
       sandboxMatchLabels: {kars.azure.com/sandbox: <sandbox>}
     approval: {mode: always, channel: cli}
     commerce:
       dailyCap: "USD 1.00"
       perTransferCap: "USD 0.01"
       counterpartyAllowlist: ["did:example:nobody"]
     rateLimit: {rps: 1, burst: 1, window: "60s"}
   ```

4. Check what the cluster says:

   ```bash
   kubectl get toolpolicy <name> -o jsonpath='{.status}'
   kubectl get cm toolpolicy-<name>-profile -o jsonpath='{.data.profile\.json}'
   ```

   `status` is `Ready`. The ConfigMap contains the full commerce block.

5. Call a tool through `/mcp` that performs a priced action, then call it
   six more times back to back.

6. Read the router's public metrics on `:9090`:

   ```bash
   curl -s localhost:9090/metrics | grep -E 'kars_agt_(policy_evaluations_total|policy_rules|tool_rate_limits_total)'
   ```

**What I expected.** A refusal. `crd-toolpolicy.yaml:197-200` says of
`perTransferCap`: "Even within daily/monthly, a single transfer above
this is refused." `:178-182` says of `counterpartyAllowlist`: "Empty =
deny-all (fail-closed)". With `approval.mode: always` bound and the CR
`Ready`, I expected the first priced call to be blocked or held.

**What happened.** The call completed. The amount was 650× the
`perTransferCap` and over 6× the `dailyCap`, for a counterparty the
allowlist did not name. Six consecutive calls against `rps: 1, burst: 1`
all returned `HTTP 200`. `GET /agt/audit` returned `{"entries": []}`
after nine tool calls, and `policy_evaluations` was `0` against
`policy_rules: 10`.

**Control, on the same router and pod, immediately afterwards.** One
`POST /v1/chat/completions` produced two audit entries
(`inference:chat_completions:<model>` and `output:OK`) and incremented
`policy_evaluations`. So the audit chain and the policy engine are
working — they are simply not in the path of a tool call.

**What does work**, for completeness: `McpServer.spec.allowedTools` is
genuinely enforced. Removing one name from it made
`tools/call <that tool>` return `-32601 tool not found`, and `tools/list`
stopped advertising it. The tool-name gate is real; it is the finer
controls on `ToolPolicy` that are not consulted.

### Impact

- An operator reading the CRD field descriptions ("a single transfer
  above this is refused", "Empty = deny-all (fail-closed)") and then
  seeing `Ready` on the CR has every reason to believe the spend gate is
  on. It is not, and nothing in the CR status, the events, the metrics or
  the audit log says otherwise.
- `docs/api/lifecycle.md:346` explains that a `ToolPolicy` **without**
  `spec.agtProfile` is `Ready=True` because "runtime-side enforcement of
  commerce / rate-limit / approval is co-located in the in-process AGT
  plugin". The plugins call `/agt/evaluate`; the router evaluates the
  YAML `PolicyEngine` at that endpoint; neither path reads the compiled
  commerce, approval or rateLimit values. As written, that sentence tells
  an operator the gate is live in the case where it is most clearly not.
- `docs/mcp.md:169` describes a `ToolPolicy` as giving "per-tool rules
  (arguments, rate limits, approval)". The word *arguments* corresponds
  to no field on the CRD, and no argument value reaches the policy engine
  — the action grammar is `tool:<name>`, `inference:chat_completions:<model>`,
  `shell:<command>`, and the audit record's five keys do not include
  arguments either.
- I am aware the default generated AGT profile carries the comment "A
  separate approval gate can be re-enabled once the native approval UI is
  wired up", so approval-as-unfinished may well be known internally.
  `commerce` and `rateLimit` have no equivalent note anywhere I could
  find.

### Suggested fix

Roughly in increasing order of work — the first is worth doing whichever
of the others you pick.

1. **Say so.** Add a 🟡 *Reconciler-only* row to `docs/maturity.md` for
   `ToolPolicy` `commerce` / `approval` / `rateLimit`; correct
   `lifecycle.md:346`; drop "arguments" from `docs/mcp.md:169`. This is
   consistent with how the rest of that page already treats the
   `TrustGraph` and A2A gaps, and it is the change that stops an operator
   relying on a control that isn't there.
2. **Make the compiled profile loadable.** Emit it under a `.yaml` key
   in the same ConfigMap so the existing loader picks it up, and teach
   `PolicyEngine` the `commerce` / `rateLimit` / `approval` shapes. This
   is the smallest change that turns the existing plumbing into
   enforcement, and the digest-echo confirmation path would then cover it.
3. **Put the forwarder on the policy path.** `forwarder.rs:235` already
   holds the tool name and the argument object at the moment of dispatch,
   which is the only place in the request path where an amount is
   available at all. A `Governance::evaluate` call there is a necessary
   step but not a sufficient one: the current contract is a single
   `action` string (`tool:<name>`, `shell:<command>`, …) with no argument
   values in it, and the audit record's five keys do not carry them
   either — so `commerce.perTransferCap` cannot be compared against a
   real amount until the evaluate contract, the `PolicyEngine` matcher
   and the audit schema are argument-aware. That is a larger piece of
   work than the other three items here, and worth separating from them.
4. **Feed `spec.rateLimit` into `McpSlidingRateLimiter`** instead of
   `TOOL_RATE_LIMIT_MAX` / `TOOL_RATE_LIMIT_WINDOW_SECS`, or document
   those two env vars as the real control and mark `spec.rateLimit`
   accordingly.

### One question

Is the router's `/mcp` forwarder meant to be governed at all, or is the
tool gate deliberately only the in-sandbox plugin's, per the cooperative
model `docs/runtimes/CONTRACT.md` describes? If the latter, then `/mcp`
is a path that reaches the same upstream tools without the plugin in it,
and that seems worth documenting explicitly either way.
~~~

---

## Draft 2 — send privately, and it is a question as much as a report

**Suggested subject / title:** `OpenClaw AGT gate: an unparseable
/agt/evaluate response is an explicit allow and resets the fail-closed
counter`

~~~markdown
### Summary

In the OpenClaw runtime plugin, `evaluateAGTPolicy` resolves
`{ allowed: true }` when the `/agt/evaluate` response body does not parse
as JSON, regardless of HTTP status — and the line after the await then
resets the consecutive-failure counter to zero. Only transport-level
`error` and `timeout` events increment that counter. So a router that is
**reachable but answering with a non-JSON body** never advances toward
the fail-closed state that `docs/runtimes/CONTRACT.md` describes; every
tool call is allowed, indefinitely, with no counter ever climbing.

To be clear about what I am *not* reporting: the grace period itself is
documented and deliberate. `CONTRACT.md:257` says the runtime "SHOULD
allow the first N consecutive failures then fail-closed", `CONTRACT.md:99`
gives the env var and the "set to 0 to fail closed immediately" escape,
and allowing through a transient router restart is an obviously
reasonable trade. My question is only about the case that never counts as
a failure at all.

### Version

Repository at `7eb039e`.

### Reproduction — static

`runtimes/openclaw/src/index.ts`:

```ts
// :2789 — inside the response handler
res.on("end", () => {
  try { resolve(JSON.parse(data)); } catch { resolve({ allowed: true }); }
});
...
// :2814 — after the await
if (result.allowed !== false) govFailCount.value = 0; // reset on success
```

There is no `res.statusCode` check on this path. A `500` whose body is an
HTML error page, a truncated response, or anything else that fails
`JSON.parse` takes the `catch` branch, becomes `{ allowed: true }`, and
`allowed !== false` then clears `govFailCount`. `FAIL_CLOSED_THRESHOLD`
(`:2763`) is only ever incremented by the `req.on("error")` and
`req.on("timeout")` handlers at `:2792` and `:2801`.

(The adjacent case behaves correctly, for what it's worth: a `500` with a
*valid* JSON error body yields `result.allowed === undefined`, and the
call site's `if (!decision.allowed)` at `:2833` blocks. It still resets
the counter, but it blocks.)

### Reproduction — dynamic, no cluster needed

`KARS_ROUTER_URL` overrides the router base for tests, so this can be run
against a stub:

```js
// stub.js
require("node:http").createServer((_req, res) => {
  res.writeHead(200, { "Content-Type": "text/plain" });
  res.end("not json");
}).listen(9999);
```

```bash
node stub.js &
KARS_ROUTER_URL=http://127.0.0.1:9999 <run the OpenClaw runtime>
# then invoke any registered tool repeatedly
```

**Expected:** three consecutive unusable responses, then
`AGT governance unreachable (fail-closed)` and a blocked tool call.

**What the code does instead:** every call allowed, `govFailCount` reset
to `0` on each one, and no `fail-closed` log line ever emitted.

Being straight about provenance: I found this by reading the plugin, not
by hitting it in anger — I have not run the stub above against a live
sandbox. The three lines it turns on are unambiguous enough that I did not
want to sit on it, but if I have misread how `evaluateAGTPolicy`'s result
reaches the gate, that is the likeliest place for me to be wrong.

### Two smaller things in the same area

Both are plausibly deliberate; flagging in case not.

1. **`KARS_AGT_EVALUATE_FAIL_OPEN_GRACE` is not read by OpenClaw.**
   `FAIL_CLOSED_THRESHOLD` is the literal `3` at `index.ts:2763`. Only
   `runtimes/hermes/.../plugin/governance.py:38-41` reads the env var.
   `CONTRACT.md:99` marks the variable "runtime opt-in", so this may be
   exactly as intended — but an operator following "set to 0 to fail
   closed immediately" would get no effect on an OpenClaw sandbox, and
   nothing tells them so.

2. **The two plugins do not in fact match, while one says they do.**
   `governance.py`'s module docstring says it is a mirror of the OpenClaw
   implementation and that "the wire shape to `/agt/evaluate` and the
   fail-closed semantics are identical". Hermes is strictly stricter: it
   routes both `status_code >= 400` (`:210`) and a non-JSON body
   (`:214-217`) through `_grace_or_block`. Hermes has its own smaller
   version of the same default, at `governance.py:222` —
   `bool(data.get("allowed", True))`, so a 2xx JSON object with no
   `allowed` key is an allow that also resets the counter. The router
   always emits the key today, so I could not reach it; it is
   defence-in-depth rather than a live path.

### Impact

The AGT gate is the enforcement point for `shell:`, `egress:`, `tool:`,
`mcp:`, `a2a:`, `mesh:` and `handoff:` actions on the OpenClaw runtime.
A router that is up but returning non-JSON — a crash-looping handler, a
proxy error page, a partial write — presents as a fully open gate with no
escalation and no distinguishing log line, where the documented behaviour
is to close after three.

### Suggested fix

In `evaluateAGTPolicy`:

- Treat a non-`2xx` status, and a body that fails `JSON.parse`, the same
  way the transport `error` handler already does: increment
  `govFailCount` and apply the grace/fail-closed decision, rather than
  resolving `{ allowed: true }`.
- Reset `govFailCount` only on a response that actually parsed and
  carried an `allowed` boolean, rather than on `allowed !== false`.
- Optionally read `KARS_AGT_EVALUATE_FAIL_OPEN_GRACE` here too, so the
  documented knob works on both runtimes, and update the Hermes docstring
  either way once the two agree.
~~~

---

## What was not sent, and would need work first

The comparison lane also observed a reachability consequence of the
router's bind address. That is deliberately not written down anywhere in
this repository and is not part of these drafts: it is a live security
question rather than a documentation or wiring defect, and if it is sent
at all it should go to MSRC on its own, written by someone who has
re-verified it directly rather than transcribed from a report.
