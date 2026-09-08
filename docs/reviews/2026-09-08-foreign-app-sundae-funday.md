# Governing an application this project has never seen

**Subject:** `pauldotyu/sundae-funday` — a third-party demo: an MCP
service plus two agents (a browser concierge and an A2A operations
agent), Microsoft Agent Framework, Python, a Helm chart, a kind path and
an AKS path. Nothing to do with kagent.

**The claim under test.** Somebody outside this project can point an
agent they already have at Kaimahi's two seams and get a spend ledger, a
tool-call audit trail and a dashboard, without adding a library, an
exporter, or a line of instrumentation — and running that agent on AKS is
easier with Kaimahi than without.

**The result is a split verdict, and the split is sharp.**

- **The enforcement half works, on somebody else's application, on kind
  and on AKS.** Their concierge's tool calls run through the gateway
  under an allowlist; a consequential call is refused; a human sees the
  actual transaction (`submit_order: draft_id draft-c2f2dedba1,
  customer_name Ada`); the approval is welded to that call; the same
  click then succeeds. Their source was not touched. No kagent, no CRD,
  no controller was installed on either cluster.
- **The addressing half does not.** Their MCP client cannot reach the
  gateway by configuration — it cannot set an `Authorization` header,
  and it always appends a trailing slash the gateway 404s. Their
  framework's model client speaks the **OpenAI Responses API**, which
  the model seam has no entry for and, once you add one, meters as
  **zero tokens**. Their operations agent cannot connect to the gateway
  at all, because the gateway relays the upstream's *capability
  advertisement* unchanged and then refuses the methods it promises.
- **Two of those were closed here; two were not.** The header and the
  path were closed by an in-pod nginx shim, and the model path by an
  edit to the *committed* upstream table, which the next `kmx plane`
  overwrites. The zero-token meter was **not** closed — the ledger still
  records `0 in / 0 out` for every governed model call in this report.
  Neither was the operations agent: it was left calling its own MCP
  server around the gateway, so its tool calls appear in **no audit row
  at all**. None of the four is a thing an adopter should have to
  discover. That list is this lane's most useful output.

Nothing in this lane changed product code.

---

## 1. Their thing, unchanged (the baseline)

`make kind-create`, from their README, on a clean machine:

| | |
|---|---|
| what it did | built the image, started their five-container observability stack, created kind cluster `sundae`, side-loaded the image, `helm upgrade --install` |
| wall clock | **77 s**, first run, image build included |
| result | three pods Running; `POST /api/chat` answered the menu correctly in **175 s** (qwen3:8b, CPU) |
| deviations | Ollama ran as a container publishing the host's `:11434` rather than a host install — the same address their compose and kind paths use. Their `Makefile` reads `OPENAI_BASE_URL` from the environment; this shell had one set for an unrelated tool and it silently became the model endpoint. Their chart has no config checksum, so a `helm upgrade` that only changes the ConfigMap does not restart the pods. Both are their side; neither is a Kaimahi finding. |

Their AKS path, later in the run: `helm upgrade --install` against
their chart, **55 s to three healthy pods**, one turn in **158 s**. Their
README's Azure path additionally assumes an Azure AI Foundry endpoint and
workload identity provisioned by a separate Terraform repository, and the
chart's default image tag (`0.1.0`) is not a tag they publish — the
published tags are `0.1.0-<sha>`. This lane substituted an in-cluster
Ollama for the Foundry endpoint, because the lane spends nothing on
models; so what was measured of their path is the chart-and-cluster half,
not the model half.

## 2. The plane beside them

```console
$ kmx plane --context kind-sundae --source .          # FAILS
kind load docker-image kaimahi-proxy:p15 --name kaimahi-p1
ERROR: no nodes found for cluster "kaimahi-p1"

$ KIND_CLUSTER=sundae kmx plane --source .            # 26 s, plane serving
```

`--context` steers every `kubectl`, but the kind image side-load takes
its cluster name from `KIND_CLUSTER` alone. `docs/kmx.md` documents both
variables and says `KUBE_CTX` defaults to `kind-$KIND_CLUSTER`; what it
does not say is that `--context` on its own is not enough for this
command, and the failure names a cluster the operator never mentioned.

**On AKS the equivalent is better than expected.** The default
`kmx lift --byo` run still includes the `kagent` phase ("the agent
runtime") and the `agents` phase ("the same agents you ran locally,
governed from the start") — a runtime and two demo agents an adopter did
not ask for. But the phases are independently runnable and the plane does
not need the ones before it:

```console
$ kmx lift --byo --step boundary  …   #  61 s — policies, ledger, and netpol-probe PROVES enforcement
$ kmx lift --byo --step plane     …   #  93 s — az acr build + deploy
```

Two commands, **2 m 34 s**, and a governance plane on a cluster this
lane did not create, with **no kagent, no agents, and no model
credential**. The documented Copilot hand-off is only demanded by the
`credential` phase; skipping it costs nothing if you are not deploying
their agents. That answers the first of the three questions: **`kmx lift
--byo` fits beside somebody else's deployment.** It does not assume it
owns the cluster — but its default path still installs a runtime the
adopter did not ask for, and there is no flag to say "plane only". The
phase names are the flag.

## 3. Onboarding their MCP server

```console
$ kmx tools add sundae --url http://sundae-mcp.demo:8101/mcp/ --pod-port 8101 \
    --tool list_menu: --tool check_availability:flavors,sauce,toppings \
    --tool quote_order:session_id,size,flavors --tool submit_order:draft_id,customer_name \
    --server-egress keep
The plane validated the table: ado, erp, github, github-release, kagent-tools, slack, sundae.
  submit_order: an approval binds draft_id, customer_name.
…
configmap/kaimahi-upstreams-extra created
networkpolicy.networking.k8s.io/kaimahi-upstream-sundae-egress created
networkpolicy.networking.k8s.io/kaimahi-upstream-sundae-ingress created
error: no matches for kind "RemoteMCPServer" in version "kagent.dev/v1alpha2"
ensure CRDs are installed first
```

Three of the four documents applied — the three a foreign runtime
actually needs. The fourth is the kagent CRD, and on a cluster with no
kagent the command **exits 1 after succeeding**, prints "ensure CRDs are
installed first", and — because it stopped — never restarts the proxy,
which reads the table at boot. So a successful onboarding looks like a
failure and silently leaves the new upstream unloaded. Both clusters
needed a manual `kubectl rollout restart deploy/kaimahi-proxy`.

`kmx tools govern` has the same shape and one extra trap:

```console
$ kmx tools govern --server kaimahi-sundae --secret kaimahi-sundae-token \
    --credential sundae-concierge --tools list_menu,check_availability,quote_order
Error from server (NotFound): namespaces "kagent" not found
```

The Secret defaults to the `kagent` namespace. `--secret-namespace demo`
fixes it — but the first attempt had **already minted the credential in
the plane**, and the token is shown once. The recovery message is
excellent and its instruction is a raw `psql -c "DELETE FROM credential
…"`. With the flag, the credential and the allowlist are issued, and the
command still exits 1 on the `RemoteMCPServer` tail.

Net: **everything a non-kagent runtime needs from these two commands
works; both of them report failure while doing it.**

## 4. What their configuration could not express

### 4.1 The tool seam's URL, from both ends

Their client normalises `SUNDAE_MCP_URL` by appending a slash
(`normalize_url`), so the request path is always `…/mcp/`:

```console
--- POST /upstream/sundae/mcp        HTTP 200
--- POST /upstream/sundae/mcp/       HTTP 404   ← what their client sends
```

And the mirror image, one layer down: their MCP server (FastMCP)
**redirects** `/mcp` to `/mcp/`, and the gateway refuses redirects:

```console
tools/call list_menu → HTTP 502  "tool upstream redirected (refused)"
```

So the upstream URL must carry the trailing slash and the gateway's own
path must not. Neither is written down; both cost a measurement.

### 4.2 The credential, on the tool seam

Their MCP client's only header hook is `header_provider=inject_trace_headers`
— W3C trace context, nothing else, no configuration. There is no value
in their chart, their environment or their Deployment that puts a bearer
token on an MCP request. **By configuration alone, their application
cannot authenticate to the gateway.** This is the refutation the lane
was told to expect, and it is narrow: it is about *addressing*, not
enforcement.

What closed it — an adopter-owned sidecar patched into their Deployment,
which is configuration in the sense the lane allows (their Deployment,
not their source), and which fixes the path problem at the same time:

```nginx
server {
  listen 8099;
  location /mcp/ {
    proxy_http_version 1.1;
    proxy_set_header Authorization "Bearer ${KMH}";   # from the Secret kmx wrote
    proxy_buffering off;                              # the gateway relays SSE
    proxy_pass http://kaimahi-mcp-gateway.kaimahi:8081/upstream/sundae/mcp;
  }
}
```

`SUNDAE_MCP_URL: http://127.0.0.1:8099/mcp/`, and their client is
governed. It is ~15 lines and it belongs in this project, not in an
adopter's head.

### 4.3 The model seam speaks a protocol their framework does not

```console
POST …/upstream/ollama/v1/responses  →  HTTP 403 "path not allowed"
```

In `agent-framework-python` 1.14, **`OpenAIChatClient` is the Responses
client** — its own docstring says so. It posts to `/v1/responses`. The
plane's table declares exactly one path per upstream and ships
`v1/chat/completions`. Every model call from their app was refused, and
their framework offers no switch.

Three separate things then have to be true to fix it, and each is a
finding:

1. **There is no `kmx models add`.** Tool upstreams have a whole
   onboarding command; model upstreams have none.
2. **The overlay refuses to carry one.** The natural place is the
   ConfigMap `kmx tools add` writes to; the proxy fails closed at boot:
   `config: overlay ollama-responses.json carries "upstreams", which an overlay
   may not set (allowed: tool_upstreams, standing_constraints)`. So the
   entry has to go into the **committed** table — which the next `kmx
   plane` re-applies and discards.
3. **Once it is there, the meter reads zero.** Same call, both shapes:

   | seam | ledger |
   |---|---|
   | `v1/chat/completions` | `11 in / 16 out` |
   | `v1/responses` (upstream reported `input_tokens 13, output_tokens 16`) | **`0 in / 0 out`** |

   The plane reads `prompt_tokens`/`completion_tokens`; the Responses API
   returns `input_tokens`/`output_tokens`. A Responses-API upstream is
   ledgered as a call that cost nothing — and a **token budget over it
   can never be exhausted**. This is the most consequential single
   finding in the lane: it is not a refusal, it is a silent
   under-count on the enforcement path.

Kaimahi's bundled model tier is a second, independent block: the pinned
`ollama/ollama:0.11.8` has no `/v1/responses` at all (404 direct, no
plane involved). This lane ran `0.33.3` instead, as an adopter's own
choice about their own Deployment.

### 4.4 Their operations agent cannot connect at all

```text
mcp.shared.exceptions.McpError: method not relayed by the Kaimahi gateway (tools only)
  … agent_framework/_mcp.py:1394  await self.load_prompts()
ERROR:    Application startup failed. Exiting.
```

The client is **not** at fault, and this is worth being exact about. It
guards the call: `if self._supports_prompts: await self.load_prompts()`.
It asked because the `initialize` result said the server supports
prompts — and the gateway relays that result **verbatim** from FastMCP
while enforcing a tools-only method set. The gateway advertises a
capability and then refuses it, and a spec-compliant client dies on
startup.

The shape of the fix is the projection the gateway already performs on
`tools/list`, applied one message earlier: strip everything but `tools`
from the relayed `initialize` capabilities. It is the same idea in the
same place, but **not a one-line change** and this report should not
size it as one: `initialize` today takes the streaming `forward` path,
and only `tools/list` gets the buffered path a projection needs, so the
work is deciding how much of an `initialize` response the gateway is
willing to buffer.

Because of it, the operations agent in this lane was left pointing
straight at their MCP server — which cost a second adopter-owned
NetworkPolicy punching a hole in the boundary `kmx tools add` had just
drawn, and its tool calls appear in **no audit row at all**. That is the
honest state of the wiring: one of their two agents governed, one not,
and the reason is one line of capability projection.

## 5. The trails, read by someone who did not build the plane

A real session of their application on **AKS**: a customer builds a
sundae, presses confirm, is refused, an operator approves, the same
click succeeds.

```console
$ kmx flow sundae-concierge
created (UTC)       credential       kind     what          outcome    cents detail
2026-09-08T23:16:10 sundae-concierge model    qwen3:8b      200            0 0 in / 0 out via ollama-responses called by claimed "ua:agent-framework-python/1.14.0" from 10.244.0.59
2026-09-08T23:16:11 sundae-concierge tool     quote_order   allowed        - upstream 200 quote_order: session_id aks-governed-1, size Classic Sundae, flavors (list) [8a0b08140eb7] called by claimed "ua:python-httpx/0.28.1" from 10.244.0.59
2026-09-08T23:19:09 sundae-concierge approval tool:submit_order requested  - -
2026-09-08T23:19:09 sundae-concierge tool     submit_order  denied         - upstream 403 submit_order: draft_id draft-c2f2dedba1, customer_name Ada [3bd57b343b9b] called by claimed "ua:python-httpx/0.28.1" from 10.244.0.59
2026-09-08T23:19:32 sundae-concierge approval tool:submit_order approved   - by admin expires=2026-09-08T23:39:32Z uses=1
2026-09-08T23:19:41 sundae-concierge tool     submit_order  allowed        - upstream 200 submit_order: draft_id draft-c2f2dedba1, customer_name Ada [3bd57b343b9b] …
-- 13 events, 0 cents, 2 refused
```

**Is it legible?** Yes, with four caveats an outsider would hit.

- The denial and the admitted call carry the same digest
  `[3bd57b343b9b]`, so "what a human approved is what ran" is readable
  off the page. The `draft_id` is their real draft, not a placeholder.
- **`0 in / 0 out`, zero cents, on every model row** — §4.3.
- One of their two agents is missing from the trail entirely — §4.4.
  Nothing in the reading says so; the absence is invisible.
- `kmx flow` has no `acted for` column; the audit views behind it do, and
  there **every row of this session says `none`** — the plane's own word
  for *there is no person*, on rows a person created by clicking Confirm
  in a browser:

  ```console
  $ kmx audit tool sundae-concierge
  … submit_order  allowed 200  granted <grant>  submit_order: draft_id draft-c2f2dedba1,
      customer_name Ada [3bd57b343b9b]  ua:python-httpx/0.28.1  10.244.0.59  none
  ```

  This is the stretched `none` `identity.md` documents and accepts. Its
  bound is stated there as "No supported configuration reaches it", and
  the condition that voids the ruling is a foreign runtime **becoming a
  supported configuration** — which a lane experiment does not make true.
  What this run adds is not a trigger but evidence: the imprecision is
  no longer hypothetical, and the two caller columns are what make it
  visible on the row.

The kind session reads the same, with `prompts/list … denied 403`
repeating every 90 seconds — a crash-looping pod, legible as a fault.
One difference worth stating rather than glossing: **on kind the
`submit_order` denial and approval were driven by hand at the seam**,
with `curl` under the same credential, because their concierge never
produced a confirmable draft there. On AKS the identical loop ran
through their own `/api/confirm`, which is why that is the transcript
above. The rows differ only in the `caller (claimed)` column —
`ua:curl/8.11.1` against `ua:python-httpx/0.28.1` — which is exactly the
column added to answer that question.

**What their app did when a call was refused.** It did not swallow it
and did not hallucinate an order. The MCP client raised `McpError`
carrying the plane's message verbatim, their handler let it escape, and
the browser got `HTTP 500 Internal Server Error` with no explanation.
The reason is in the pod log and in `kmx approvals`. A framework that
surfaces the refusal is the good case; nobody has written the code that
turns it into "waiting for approval" in a UI.

## 6. The network position, written as an adopter needs it

**On kind, and on AKS with Cilium, the block is real and silent.** Their
concierge pod, with a valid credential:

```console
$ kubectl -n demo exec deploy/concierge -- python3 -c "urlopen('http://kaimahi-mcp-gateway.kaimahi:8081/healthz')"
urllib.error.URLError: <urlopen error timed out>
```

The plane's committed rule admits one namespace, named `kagent`. This is
the change, and it is the whole change:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-demo-to-kaimahi-seams
  namespace: kaimahi
spec:
  podSelector:
    matchLabels:
      app: kaimahi-proxy
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: demo   # ← your application's namespace
      ports:
        - {protocol: TCP, port: 8080}
        - {protocol: TCP, port: 8081}
```

Measured on AKS/Cilium, removing and restoring it:

```console
gateway from demo, policy REMOVED:  HTTP 000 (curl 28 — timed out)
gateway from demo, policy RESTORED: HTTP 200
```

**Would I ask an operator to apply that?** Yes, and without much
hesitation. It is *additive* — Kaimahi's own file is not edited, so the
next `kmx plane` does not fight it — it names one namespace and two
ports, it grants ingress to the plane rather than egress from anything,
and it is a rule about who may talk to *our* pod. It is a smaller ask
than the two changes beside it, which I would flag rather than wave
through: the sidecar (§4.2) mutates their Deployment, and the
ops-agent hole (§4.4) weakens a boundary this project had just drawn.

One caveat that cost half an hour and belongs in the docs: **their
kind values set `hostNetwork: true`**, and a `namespaceSelector` cannot
match a pod that is not on the pod network. The policy above applied
cleanly and the app stayed blocked. `components.*.hostNetwork: false` in
their values — their own knob, their AKS default — fixed it. Any
host-networked runtime needs an `ipBlock` for the node instead, which is
a materially bigger ask.

## 7. Observability: what an operator can and cannot see

`kmx lift --byo --step observability` **cannot run at all**:

```console
FAILED [1/1] Azure Monitor workspace, Container Insights, the scrape job and a workbook (3.6s)
cannot tell whether -n kaimahi networkpolicy kaimahi-proxy-metrics-azure already exists …
  exit status 1: Error: flags cannot be placed before plugin name: --context
```

`objectExists` in `internal/kmx/app/lift_observability.go:372` builds
`kubectl … -n kaimahi networkpolicy <name> -o name` — **with no `get`
verb**, so kubectl treats `networkpolicy` as a plugin name. It is called
from `recordPreExistingState`, which is not gated on `--byo`, so this
blocks the observability phase on *every* lift. It fails closed, which
is the right instinct and no comfort.

So the phase was reproduced by hand — `az aks enable-addons monitoring`,
`az aks update --enable-azure-monitor-metrics`, then this repository's
own `k8s/observability/network-policy.yaml` and `scrape-config.yaml` —
and then queried, which is the only way to answer the question honestly:

```console
--- up{job="kaimahi-plane"}                     series: 2   (both proxy replicas)
--- up{namespace="demo"}                        series: 0
--- count by (pod) ({pod=~"concierge.*|ops-agent.*|sundae-mcp.*"})  series: 0
```

**The plain answer.** Managed Prometheus, wired the way this project
wires it, carries **the plane and nothing of theirs**. The scrape job is
scoped to `namespace: kaimahi`, pods labelled `app=kaimahi-proxy` — by
design, and the file says why. Their chart annotates every pod
`prometheus.io/scrape: "true"` and exposes `/metrics` on each service;
none of it is collected, because Managed Prometheus does not honour those
annotations without a job that says so. An operator who wants their
agent's own metrics writes a second scrape job — and the ConfigMap is
cluster-wide and singular, so they must *merge* it, which is exactly the
situation `kmx lift` refuses to resolve for them.

**Container Insights is the opposite, and this half was measured.** It
is namespace-agnostic, so their pods' logs land in the same workspace as
the plane's — including the sidecar's access log, which is a record of
every governed tool call leaving their pod:

```console
ContainerName     PodNamespace   Lines   (last 30 min)
concierge         demo           389
sundae-mcp        demo           307
ops-agent         demo           235
kaimahi-mcp-auth  demo            41
postgres          kaimahi         72
proxy             kaimahi         44
```

(The add-on needed a second `az aks enable-addons` before its agents were
scheduled — an Azure-side quirk in this subscription, not a Kaimahi one.)

What an operator gets, then: **the plane's metrics and everyone's logs**,
plus the workbook — and, separately from Azure entirely, the four trails
`kmx flow` reads, which are the part that actually answers "what did my
agent do". Their own OTel traces still go wherever their `OTEL_EXPORTER_OTLP_ENDPOINT`
points; Kaimahi neither collects nor conflicts with them.

## 8. The two numbers

**How long the repointing took.** Wall clock from the first Kaimahi
command to their application answering a question with both seams
governed: **32 minutes on kind** (22:09 → 22:41), of which roughly 25
were diagnosis of the four blockers in §4. Knowing what this report
knows, the whole Kaimahi side on AKS — boundary, plane, onboarding,
credential, policy, Secret, sidecar, `helm upgrade` — took **6 minutes**
(23:04 → 23:10). The steady-state cost is
one values file, one Secret copy, one Deployment patch and one
NetworkPolicy.

**How many times Kaimahi's source was read instead of its docs: five,
and only one of them had to be.**

- `k8s/plane/upstreams.yaml`, for the **shape of a model upstream** —
  **unavoidable**. No document describes adding one and no command
  writes one.
- `k8s/plane/network-policy.yaml` — avoidable; `foreign-runtime.md`
  quotes the rule verbatim.
- A `grep` for whether kind enforces NetworkPolicy — avoidable;
  `egress.md` answers it.
- `k8s/ollama.yaml`, to install only the model tier without bringing
  kagent — **avoidable, and I was wrong about why I read it**:
  `kmx up --step ollama` runs that one step. The read produced the right
  manifest by the wrong route.
- `internal/kmx/app/lift_observability.go` — this one was diagnosing the
  defect in §7, not looking for a value, and a report should not count
  debugging a bug as a documentation gap.

So the honest figure an adopter would care about is **one**: the model
upstream, which is item 1 of §9.

## 9. Does Kaimahi make wiring this easier?

**For the thing their author actually named as painful — "getting
endpoints right, and getting a full observability view" — the answer
today is: partly, and not yet by itself.**

*Easier, and provably so.* Two seam URLs and one credential replaced
every place their app names a model or a tool server, and what came back
is a spend ledger, an argument-bound audit trail and an approval that a
human can read before granting. Their AKS deployment did not move, their
Terraform was not involved, their source was not touched, and no kagent
exists on either cluster. `kmx lift --byo` put a governance plane on a
cluster it did not own in 2 m 34 s and proved the network boundary before
using it. That is a real answer to "wiring the pieces together", and it
is the half of the observability view their OTel stack does not give
them: not traces, but *what was allowed, what was refused, and what a
human approved*.

*Not yet, and here is the list.* For the answer to be an unqualified
yes, these have to exist:

1. **A model upstream an adopter can add** — a `kmx models add`, or at
   minimum an overlay that accepts `upstreams`. Today the only route
   edits a ConfigMap the next `kmx plane` overwrites.
2. **Responses-API support on the model seam** — the path, and the
   `input_tokens`/`output_tokens` shape. One current framework speaks it
   by default and that is all this lane measured; the meter reads zero
   for it, which is the part that matters however many others there are.
3. **Capability projection on `initialize`** — advertise `tools` only,
   since tools is all the gateway relays. It is the difference between
   "their agent runs" and "their agent will not start"; §4.4 says why it
   is not the one-liner it looks like.
4. **A credential path for a client that cannot set a header** — the
   nginx shim of §4.2, shipped and documented, or a documented
   alternative. It is the only reason a config-only integration failed.
5. **`kmx tools add` / `tools govern` completing on a cluster with no
   kagent** — skip the CRD, restart the proxy, exit 0.
6. **The `get` verb in `lift_observability.go`**, and a scrape job an
   adopter can extend to their own pods without hand-merging a
   cluster-wide ConfigMap.
7. **A trail that can say who a foreign runtime acted for**, or a word
   other than `none`. §5 is the occurrence `identity.md` said would void
   the ruling.

Items 2, 3 and 5 are small. Item 1 is a design decision. Item 7 is a
ruling to revisit. None of them is the enforcement engine, which worked
on the first application from outside this project that has ever been
pointed at it.

## 10. Spend and teardown

One AKS cluster (`Standard_D8s_v5`, one node, Cilium, Free control-plane
tier), an ACR Basic, the Standard load balancer their chart's
`LoadBalancer` Service created and its public IP. Alive **22:51 → 23:48
UTC, 57 minutes**. At the published westus3 retail rates for those SKUs
— the node $0.384/hr, ACR Basic ~$0.007/hr, the load balancer
~$0.025/hr, the IP ~$0.004/hr — that is **≈ US$0.40**, a computed figure
rather than an invoiced one, plus a few cents of Log Analytics and
Managed Prometheus ingestion. **No model spend at all**: every token in
this report came from a local Ollama, on kind and on AKS alike.

**Teardown, in the order the rules require.**

`kmx lift down --byo` was run first, and did exactly what it promises on
a cluster it did not create — nothing, loudly:

```console
Managed Prometheus was already on before this run; leaving it on.
Container Insights was already on before this run; leaving it on.
the NetworkPolicy kaimahi-proxy-metrics-azure was there before this run,
  or its origin was never established; leaving it.
ama-metrics-prometheus-config in kube-system was there before this run …
  If you merged this run's job into it, remove the kaimahi-plane job by hand.
kmx lift down: nothing this run created is left. 0 removed by recorded id, 0 already gone.
```

Then the rules for a cluster **this lane did create**: both monitoring
add-ons disabled first so ingestion stops (verified: no data-collection
rule associations left on the cluster), then the resource group deleted
and the deletion proved.

```console
$ az group exists --name <the lane's rg>                    false
$ az group exists --name MC_<the lane's rg>_<cluster>_westus3   false
```

The two workspaces the add-ons wrote into are pre-existing subscription
defaults in resource groups this lane did not create; they were **not
deleted and not adopted**. Locally: the kind cluster, their compose
stack, the Ollama container and its volume are gone, and the stale
kube-context was removed.
