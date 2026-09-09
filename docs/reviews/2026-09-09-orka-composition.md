# Orka and Kaimahi: how these two become one better thing

**Subject:** [`orka-agents/orka`](https://github.com/orka-agents/orka) —
"Cloud and AI-native multi-agent orchestration platform for Kubernetes".
MIT, Microsoft-governed today and intended for donation to a
community-governed foundation. Seventeen CRDs in the `v0.1.3` release bundle this lane
installed — twenty-six in the staged chart on `main`, twelve in `main`'s
own `deploy/orka.yaml`, and "twelve cluster-scoped CRDs" in their
`v0.1.3` getting-started page; the number depends entirely on which
artifact you mean. A controller with an embedded React dashboard, a REST API plus OpenAI- and
Anthropic-compatible endpoints, an authenticated provider proxy, durable
execution events, RuntimePools that keep real coding-agent CLIs warm
over ACP, and an approval gate. **It is owned by a teammate.**

**This lane is not a comparison and not a competitive assessment.** The
mission says running agents on AKS should be easy and that this project
is a means to that, not the end. Five projects now solve overlapping
parts of that and at least three are inside the same company. The
question this report answers is how these two things become one better
thing — and the answer that most of this repository should stop was
explicitly permitted.

**Read and run at commit `597a8ab` (`main`, 2026-09-08) and at tag
`v0.1.3`, on kind, on 2026-09-09.** Nothing in their repository was
modified. Nobody there was contacted: no issue, no comment, no pull
request. Nothing in this repository's product code changed either; this
lane is a report. Everything below was observed on a cluster this lane
created and then destroyed, or read in their source with a line cited.

---

## The answer, first

**Kaimahi's last remaining exclusive claim is gone, and Orka is where it
went.** The [KARS comparison](2026-09-09-kars-comparison.md) merged onto
`main` this morning — commit `4b61c9b`, 12:18, about five hours before
this lane's first Orka command — opened with "**Kaimahi's central
claim survives, and it is now the only one.** An approval welded to the
digest of specific argument values is a thing KARS does not have", and
closed by recommending we take it upstream because nobody else had it. Orka
already has it. Not in shape only, and not on a roadmap: this lane
watched a US$999 payment park before execution, showed a human the exact
arguments (`payee "Globex Ltd", amount_usd 999, immediate true`) and a
sha256 over them, held for eleven minutes with the payee's endpoint
untouched, and released exactly once on a decision carrying the
approver's identity. Their binding is *stronger* than ours in two ways
we do not implement at all: the digest also covers the tool's own spec
version and the version of the credential it will use, so an approval
granted before a tool or its secret changed cannot be redeemed after.

**The composition seam is the model, and it is narrower than expected in
both directions.** Orka's OpenAI-compatible endpoint is not a proxy by
default — it injects Orka's own coordinator prompt and 23 tool
definitions — measured at ~2,000 prompt tokens for a seven-word
message — into every request, and the only way off that path is a
per-request HTTP header. It authenticates every caller, refuses model
names it has no Provider for, and never lets a client name its own
upstream. That is real credential custody at the edge. What it does not
do is meter: no money concept anywhere in their tree — their roadmap
names "Cost tracking" but defines it as token aggregation — no cost
metric on their Prometheus surface, and a foreign caller's tokens
recorded nowhere this lane could find.

**Governing a workload Orka did not launch: partly, on the model seam
only, and the same third-party application both prior lanes used could
not complete a turn.** The app's framework speaks the OpenAI Responses
API; Orka has no `/openai/v1/responses` route, so the request fell
through to the dashboard's single-page-app catch-all and came back
`HTTP 200` with HTML. Their own metrics counted it as a **2xx success**.
This is the third project in a row to be broken by the Responses API,
and it is the single most portable finding across all three lanes.

**And the recommendation is the third one.** Most of this repository
should stop. Two capabilities are worth taking to Orka and one of those
is small. The rest is either already theirs, better theirs, or was only
ever a means to demonstrate the part that is now theirs too.

---

## 1. A non-technical person creates a working agent

Measured the way W31 measured
`create-kaimahi-agent`: prerequisites installed, commands typed, wall
clock. On kind. The AKS half was not run — see §6.

### 1a. What Orka asks for before you start

Their [getting-started page at
`v0.1.3`](https://github.com/orka-agents/orka/blob/v0.1.3/website/docs/getting-started.md)
lists four: Docker 17.03+, `kubectl`, access to a Kubernetes cluster,
and an LLM API key (Anthropic, OpenAI, or Azure OpenAI). (`main`'s page
lists three and swaps Docker for OpenSSL; this lane installed the
`v0.1.3` bundle, so the four above are the ones that applied.) **Orka does not create the cluster.** There is
no `curl | sh`, no installer, and no published binary of their CLI —
they publish tags but no GitHub Release entries — their own docs say so
twice (`website/docs/getting-started.md:113`, and
`reference/release-status.md:107`, "There are currently no GitHub
Release entries and no written release notes") — so `orka` the CLI
exists only behind `make build-cli` in a checkout with a Go toolchain. The comparison worth making is therefore not
"whose install is faster"; it is that our front door provisions the
cluster and theirs assumes one.

For this run the cluster took **11.9 s** (`kind create cluster`, node
image already local — on a cold machine it is a several-minute pull).

### 1b. The documented release path, timed

Clock starts at the first Orka command.

| step | at |
|---|---|
| `kubectl create namespace` + the `harness-wrapper-auth` Secret | 0 s |
| `kubectl apply -f …/v0.1.3/deploy/orka.yaml` | 1 s |
| `rollout status deploy/orka-controller-manager` returns | **43 s** |
| an API client that can actually create anything | **91 s** |
| `Provider` + `Agent` applied, both `Ready` | 114 s |
| first `Task` reaches `Succeeded` | **146 s** |

Two pods, 17 CRDs (the release bundle's count; `main` has 26), a
controller with an embedded dashboard, and a governed first task in
**two and a half minutes**. That is fast, and the
manifest is honest about what it needs: the README's own comment says
the manifest mounts a Secret it does not create, "so make the namespace
and that Secret first or the Pods never start" — a defect written down
next to its workaround, which is the right place for it.

**Against us:** `create-kaimahi-agent` measured **1 prerequisite and
178 s** on a clean machine, and that figure includes creating the
cluster and provisioning a toolchain. Orka's 146 s excludes both. On
like-for-like work the two are within half a minute of each other: 146 s
against 178 s, or 158 s once this run's 11.9 s of cluster creation is
added to Orka's side to make the comparison fairer to us. Saying so is
the point. There is no
speed argument here for either project.

### 1c. The one step that is broken, and how it fails

The raw-manifest path is the first one the README shows, and it does not
create an API client. The documented fix is:

```console
$ kubectl -n orka-system create serviceaccount orka-client
serviceaccount/orka-client created
$ kubectl -n orka-system create rolebinding orka-client \
    --clusterrole=orka-api-editor-role --serviceaccount=orka-system:orka-client
rolebinding.rbac.authorization.k8s.io/orka-client created
```

Both commands succeed. The ClusterRole they name does not exist in the
v0.1.3 bundle:

```console
$ kubectl get clusterrole orka-api-editor-role
Error from server (NotFound): clusterroles… "orka-api-editor-role" not found

$ kubectl auth can-i create tasks.core.orka.ai -n orka-system \
    --as=system:serviceaccount:orka-system:orka-client
no - RBAC: clusterrole… "orka-api-editor-role" not found
```

Kubernetes accepts a RoleBinding to a ClusterRole that is not there, so
the operator gets two success messages and an account that can do
nothing, and finds out at the first `403`. This is the failure shape
this project has a name for — a passing condition looser than
the claim — and we have shipped it ourselves
more than once.

**The version mismatch is the interesting part, and it is not confined
to the old release.** `orka-api-editor-role` appears nowhere at
`v0.1.3` — not the role, not the instruction. It also appears nowhere in
`main`'s own `deploy/orka.yaml`. Yet `main`'s README (`:123`) and
getting-started page (`:93`) both tell you to install *the v0.1.3
manifest*, and `main`'s troubleshooting page (`:199`) then tells you to
bind that role. **The documentation as it stands today pairs an install
of one artifact with a client-setup step for a different one**, and the
only place the role actually exists is a checkout's
`config/rbac/api_editor_role.yaml`, under the different, unprefixed name
`api-editor-role`.

**Their documentation does anticipate exactly this**, in the same
section: if a bundle lacks the helper roles, apply that file "from a
checkout containing the authorization update", and use the unprefixed
name. That instruction is right, it names the right different name, and
it worked first time — but it requires a clone, which is the one thing
the released path advertises you will not need.

**The Helm path does not have this problem at all**: the v0.1.3 chart creates `orka-client` and
its own `…-client` ClusterRole and binding
(`charts/orka/templates/rbac.yaml:192`, `serviceaccount.yaml:14-19`). The
defect is confined to the raw-manifest path, which is the one printed
first in both the README and the getting-started page.

**And it is already fixed for the next release**:
`manifest_staging/deploy/orka.yaml:602` defines `orka-api-editor-role`,
added by the same commit that brought route authorization. This is
recorded because the lane measured it, not because anyone should raise
it.

Everything after that point worked as documented on the first attempt.

### 1d. The model substitution, stated plainly

The lane guardrail was local models or an existing seat, and no spend.
Orka's `Provider` CRD takes an optional `baseURL`
(`api/v1alpha1/provider_types.go:36-38`), so `type: openai` was pointed
at an Ollama container on the kind network — `qwen2.5:3b` first, then
`qwen2.5:7b` when the smaller model would not emit tool calls. **US$0.00
was spent.** Where a result below depends on model competence rather
than on Orka, this report says so.

---

## 2. Can Orka govern a workload it did not launch?

**This is the most important question in the lane**, because it is the
one that decides whether these two projects compose or overlap. The
answer is: **on the model seam, yes, by configuration alone — and that
seam has three conditions an adopter has to meet, one of which is not
expressible in most applications' configuration.** On the tool seam,
no.

### 2a. There is no adoption path for a Pod

Orka builds its workloads from CRs. A `type: ai` Task becomes a Job the
controller writes; a `type: agent` Task runs in a controller-owned
RuntimePool. There is no injection path: the release bundle installs
four `ValidatingAdmissionPolicy` objects and no
`MutatingWebhookConfiguration` — and `MutatingWebhookConfiguration`
appears **nowhere in their repository**, at `main` or at the tag — so
nothing can attach a sidecar or rewrite an env var on a Deployment
somebody else applied. An existing Deployment stays exactly
as it is, and Orka has no opinion about it.

That is the same structural answer the [KARS
comparison](2026-09-09-kars-comparison.md) reached about KARS, and it
was the claim this project has been leaning on. It survives here — but
much less of it than expected, because of what comes next.

### 2b. The seam that does exist

Orka serves `/openai/v1/chat/completions` and
`/anthropic/v1/messages` on the same port as its API. Any workload that
can set `OPENAI_BASE_URL` and `OPENAI_API_KEY` can point at it. Three
things happen there, and all three are worth this project's attention:

**It authenticates — and on the version this lane ran, that is all it
does.** Unauthenticated is `401`; the bearer token is a Kubernetes
ServiceAccount token checked through TokenReview. An OpenAI client sends
its "API key" as `Authorization: Bearer`, so a SA token drops straight
into the field the app already has. This is the thing our own tool seam
could *not* do for the same third-party app — [the foreign-app
report](2026-09-08-foreign-app-sundae-funday.md) had to put an nginx
shim in the pod because their MCP client could not set a header at all.

**The authorization half is newer than the release, and this report
nearly got it wrong.** At `v0.1.3` the compat groups take only the
authentication middleware — `oai.Use(externalAuth)`,
`internal/api/server.go:395-396` — so **any authenticated ServiceAccount
token in the cluster could drive the model seam**, with no per-route
permission check. On `main` the same groups go through
`s.externalAPIGroup(…)` (`server.go:458`), which wraps every route in
`authorizeExternalRoute` and runs a real SubjectAccessReview against
`coreAPIPolicy("create", "chats", "")`
(`internal/api/external_authorization.go:214`). That file does not exist
at `v0.1.3`; it arrived in commit `25da6e4`, *"fix(api): enforce caller
authorization across external routes"*, dated 2026-09-06 — three days
before this lane ran, and three weeks after the tag it ran against.
**So the credential-custody praise below is earned on both versions; the
access-control praise is earned only on `main`.** Anyone repeating this
report's conclusion should say which.

**It refuses to be told its own upstream.** The caller names a model,
not a URL. `local/qwen2.5:3b` resolved; a bare model with no matching
Provider was refused:

```console
$ POST /openai/v1/chat/completions  {"model":"assistant", …}
{"error":{"message":"failed to resolve provider: no provider \"\" found
 and no 'default' Provider CRD exists","type":"invalid_request_error"}}
```

`/openai/v1/models` lists exactly what the cluster's `Provider` objects
allow. The key stays in the cluster. **This is credential custody for a
workload Orka did not launch, and it is genuinely good.**

On `main`, that authorization costs the adopter something worth naming:
a workload's default ServiceAccount has no `create` on `chats`, so a
cluster admin adds one Role and one RoleBinding. **The honest adoption
cost for their model seam is two configuration values, one `Provider`
object, and — from `main` onward — one RoleBinding**, against the two
configuration values the foreign-app report needed for ours. Close, and
in their favour on custody.

**It is not a proxy by default.** This is the surprise. The handler's
own comment is exact — "Inject Orka tools and run the server-side
agentic loop by default. Set `X-Orka-Tools: disabled` to use as a
transparent proxy instead" (`internal/api/openai_compat.go:289-290` on `main`, `:278-279` at
`v0.1.3`; gated at `compat_coordinator.go:30` on `main`, `:35` at the
tag). Asking for one word
back:

```console
$ POST /openai/v1/chat/completions
   {"model":"local/qwen2.5:3b",
    "messages":[{"role":"user","content":"Reply with the single word: ping"}]}
  … "usage":{"prompt_tokens":2050,"completion_tokens":1785}
  content: "CRITICAL RULES:\n- Delegate deliberately — do enough research
            to scope the task, then let agents do the deep dive…"
```

Seven words in, 2,050 prompt tokens out: the model was handed Orka's
coordinator system prompt and 23 tools, and the small model recited them
back. With the header, the same request is a clean proxy — 24 prompt
tokens, my own system message honoured, 0.29 s:

```console
$ POST /openai/v1/chat/completions   -H 'X-Orka-Tools: disabled'
  content: "Echo"   usage: {"prompt_tokens":24,"completion_tokens":2}
```

**The header is the only switch.** There is no Helm value, no CRD field,
no per-Provider default — `compat_coordinator.go:30` reads the request
header and nothing else. So an application whose model client cannot
send a custom header (which is most of them, configured by environment
variable) cannot reach the transparent path, and instead silently gets
an orchestrator wearing its agent's name. The behaviour is documented
on at least four of their pages and in the source comments themselves;
the absence of a server-side default is the gap.

### 2c. Running it: the same third-party app, live

`pauldotyu/sundae-funday` — the identical application, image and chart
that the foreign-app report governed with two configuration values and
that the KARS comparison rebuilt for.

```console
$ helm upgrade --install sundae … \
    --set config.OPENAI_BASE_URL=http://orka-api.orka-system:8080/openai/v1 \
    --set config.OPENAI_CHAT_MODEL=local/qwen2.5:3b \
    --set-string secret.data.OPENAI_API_KEY="$(kubectl -n orka-system create token orka-client)"
```

Two configuration values and a token in the field their chart already
has for one. **Three pods healthy in 21 s**, their image and their chart
untouched, no Orka CRD applied to their namespace, nothing injected into
their Deployment. That is as far as it gets.

```console
$ POST /api/chat  {"session_id":"s1","message":"What flavors do you have?"}
Internal Server Error

# their pod:
File ".../agent_framework_openai/_chat_client.py", line 2539, in _parse_response_from_openai
    metadata: dict[str, Any] = response.metadata or {}
AttributeError: 'str' object has no attribute 'metadata'
```

Their model client is `agent_framework.openai.OpenAIChatClient`, which
in `agent-framework` 1.14 is the **Responses** client. Orka has no
`/openai/v1/responses` route. The request did not 404 — it fell through
to the dashboard's single-page-app catch-all:

```console
$ POST /openai/v1/responses  -H "Authorization: Bearer $TOKEN"
<!DOCTYPE html> … <title>Orka</title> …

# Orka's own log, three times:
INFO api-server request completed  {"method":"POST",
  "path":"/openai/v1/responses","status":200,"duration":"14.041µs"}
```

Fourteen microseconds is the static file handler. And their Prometheus
surface files it as a success:

```
orka_api_requests_total{endpoint="/openai/v1",method="POST",status="2xx"} 3
```

So an operator watching Orka's dashboards sees three successful POSTs to
the OpenAI-compatible API while the client is crashing on HTML it cannot
parse, three stack frames from the cause. A `404` would have been
strictly better; `501 unsupported` better still.

**Both prior lanes hit this same wall from their own side.** Our model
seam had no Responses entry, and once one was added it metered as zero
tokens. KARS routes `/v1/responses` and forwards it honestly to an
upstream that refuses it. Orka returns a web page. Three projects, three
different wrong answers, one cause: the Responses API is now the default
wire shape of a major framework. **This is the finding with the widest
reach of anything in three lanes of work.**

### 2d. The tool seam: no

Orka's `Tool` CRD describes a tool *Orka* will call on an agent's
behalf — an HTTP endpoint or an MCP backend it dials from its own worker
(`api/v1alpha1/tool_types.go:16-40`). There is no gateway a foreign
application can route its own MCP traffic through, and therefore no way
for Orka to see, record, or refuse a tool call made by a workload it did
not launch. The sundae app's concierge talks to its own MCP service
directly; nothing in Orka observes it.

**This is the one place the two architectures are genuinely
complementary rather than duplicated**, and it is narrow: an enforcing
MCP gateway that a foreign workload points at, which is what this
project built and proved on somebody else's application on two clusters.

---

## 3. The approval bound to the call — theirs, live

The [KARS comparison](2026-09-09-kars-comparison.md), merged this
morning, closed with "One thing, and it is the thing this project has been
building toward since P12: **an approval welded to the exact call.**",
and recommended taking it to them.
**That sentence is now false, and Orka is the counterexample.** This
section is the evidence, because a claim that reverses a recent
conclusion deserves more of it than one that confirms.

### 3a. What their type says

`internal/approvals/approvals.go:24-44` — an `Approval` carries
`TargetTool`, `TargetArgsDigest`, `TargetSpecDigest`,
`TargetArgsPreview`, `ToolCallID`, `DecisionActor`. The identity of an
approval *is* the call:

```go
// internal/approvals/key.go:74-87 (verbatim, one elision marked)
func ApprovalID(namespace, taskName, taskUID, targetTool, targetArgsDigest string, targetSpecDigests ...string) string {
	parts := []string{
		strings.TrimSpace(namespace),
		strings.TrimSpace(taskName),
		strings.TrimSpace(taskUID),
		strings.TrimSpace(targetTool),
		strings.TrimSpace(targetArgsDigest),
	}
	// … targetSpecDigest appended when non-empty …
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
```

and redemption requires all three to match, not just the name:

```go
// workers/ai/approval_gate.go:711-718 (verbatim)
func resolvedDecisionMatchesTarget(decision approvals.ResolvedApproval, target approvals.ApprovalTarget) bool {
	if decision.TaskUID != "" && decision.TaskUID != target.TaskUID {
		return false
	}
	return decision.TargetTool == target.TargetTool &&
		decision.TargetArgsDigest == target.TargetArgsDigest &&
		decision.TargetSpecDigest == target.TargetSpecDigest
}
```

`preScan` (`approval_gate.go:179-262`) runs over the model's tool calls
*before* any of them execute, and an approved decision is consumed once
— `injectIdempotencyKey` at `:862`, `markFired` at `:855`.

### 3b. What it does on a cluster

A `Tool` named `pay-vendor` pointed at a payment endpoint that logs what
it is asked to move; an `Agent` with
`coordination.approvalRequiredTools: [pay-vendor]`; a Task asking to pay
Globex Ltd US$999.

```console
$ GET /api/v1/tasks/pay-globex/approvals
  id                e0d5aac3d1ca388d…
  status            pending
  targetTool        pay-vendor
  targetArgsDigest  727e3a2a31b3…
  targetArgsPreview {"amount_usd": 999, "immediate": true, "payee": "Globex Ltd"}
  targetSpecDigest  c55735d79d6f…
```

The payment endpoint's log was empty then, and was still empty eleven
minutes later. A human is shown the actual transaction, not the tool
name — including the `immediate: true` argument the model invented,
which is precisely the case argument-level policy exists for. On the
earlier Acme run the same shape held and, once approved, the transfer
executed **exactly once** with exactly those values:

```console
$ POST …/approvals/4bdb1a32…/decision  {"decision":"approve","reason":"invoice INV-1001 verified"}
  status "approved"   decisionActor "system:serviceaccount:orka-system:orka-client"

# the payee endpoint, once:
TRANSFER EXECUTED: {"amount_usd":42,"payee":"Acme Supplies"}
```

`decisionActor` is our `decided_by`, and it is the authenticated caller,
not a self-reported string.

### 3c. Where theirs is stronger than ours

`TargetSpecDigest` has no counterpart in this project.
`approvalTargetSpecDigest` (`approval_gate.go:443-505`) folds into the
digest the tool's own definition, the *version of the Secret* the tool
will authenticate with — `:606-616` refuses to bind an approval at all
when that version cannot be read, and `:473`/`:495` fold the auth ref's
UID and resourceVersion into the digested identity, both only when the
tool actually declares `spec.http.authSecretRef` — and the identity of
any `OutboundAccessPolicy` in force (`approvalOutboundPolicyVersion`,
`:420-450`, folded in at `:468-476`). So an approval granted before a credential was rotated, or
before the tool's URL changed, cannot be redeemed afterwards — it goes
stale and asks again (`staleDecisionForTarget`, `:720-733`). Their test
file carries the adversarial cases by name:
`TestApprovalGateMismatchedResolvedApprovalRequestsNewApproval`,
`TestApprovalGateBlockingOverflowFailsClosedForUngatedCustomTool`,
`TestApprovalTargetArgumentsHashesURLInterpolationAsExecuted` — that
last one hashes arguments *as they will be interpolated into the URL*,
a substitution trap this project has not considered.

**It also fails closed in more places than ours does.** The worker never
executes a gated tool without a matching decision baked into its
environment at Job creation; when there are too many decisions to fit
that environment variable, the overflow is replaced by a synthetic
`Declined` entry whose reason says so in words —
`internal/approvals/approvals.go:21`,
`ResolvedApprovalBlockingOverflowID`, "custom tool execution must fail
closed". If the event store is absent the controller injects no
decisions at all and the API returns `501` rather than an empty list.
Every one of those is a place a system could have quietly permitted
instead.

One deliberate limitation, stated in their own comment rather than
found: approvals do not expire. `ApprovalExpired` is a defined event
type that nothing in the tree emits, and `task_controller.go:4303-4305`
explains why — "There is no expiry producer yet, so passive expiresAt
evaluation would silently resume consequential work." A pending approval
waits indefinitely for a human. That is the right way round, and the
reasoning is better than the omission.

### 3d. Where ours is still different, and it is small

Three things, and none of them is a moat:

1. **Scope.** `agent_types.go:283-287`: `approvalRequiredTools` "is only
   honored when coordination is enabled and autonomous mode is true".
   The gate covers custom `Tool` CRDs called by an autonomous coordinator
   `type: ai` worker. It does not cover an ordinary Task, and it does not
   cover a workload Orka did not launch. Ours sits in a gateway in front
   of any caller.
2. **Money.** Their approval has no monetary threshold, no per-transfer
   cap and no standing constraint — the gate is "this tool needs a
   human", not "this tool needs a human above US$50". Our standing
   constraints and price gate have no counterpart.
3. **The audit row.** Their durable events record `toolName`,
   `toolCallID` and `argumentBytes` — the byte *count* of the arguments,
   never their values (`workers/ai/main.go:1373`,
   `approval_gate.go:1150`, both `len(…Arguments)`). **This holds for the
   `type: ai` path only.** On the harness-v2/ACP path tool content is
   projected into the event's `ContentText`, redacted rather than
   omitted (`internal/harness/v2/eventjournal/mapper.go:686-705`), and
   harness v1 passes frame metadata through into event content
   (`internal/harness/mapper.go:122-128`). Neither was run. The values exist only inside a pending approval's
   preview, and disappear from the readable surface once decided. Ours
   records an `arg_summary` on every call, approved or not.

### 3e. Three things a maintainer might want to know

Offered here, not filed anywhere. All three were observed on v0.1.3.

- **The `WaitingForApproval` condition can never be `True`.** Observed
  first, then found in the source. During an eleven-minute park the
  Task's conditions read exactly `JobCreated=True`, and `kubectl get
  task` said `Running`, while the controller logged `autonomous task
  waiting for approval` every 33 s. The condition is defined at
  `internal/controller/task_controller.go:88-89` as "indicates a running
  task is parked on a human approval" — and every one of its four write
  sites in the tree hardcodes `metav1.ConditionFalse`
  (`acp_task_queue.go:888`, `:1046`, `task_controller.go:2458`,
  `:2550`), all of them at terminal transitions with the message "task
  is terminal" or "task execution is settled". Parking itself only
  rewrites `Status.Message` (`parkOnPendingApproval`, `:4309`).
  Meanwhile `website/docs/operations/agent-runtime-security.md:32` tells
  operators: "Treat `WaitingForApproval=True` as an intentional parked
  state, not a failure." **An operator following that instruction is
  watching for a value the code cannot produce.** This is the highest
  of the three: it is the difference between a human noticing a parked
  payment and not.
- **An autonomous task that runs out of iterations with the work undone
  reports `Succeeded`.** Observed: `TaskSucceeded | reached max
  iterations (3)`, while the Task's own stored result said
  "Phase 2: Pay vendor Globex Ltd — **In progress**" and the payment
  never happened. The reason string is accurate; the phase is not.
- **An Agent whose `approvalRequiredTools` names a built-in reports
  `Ready` and "Agent configuration is valid", and every Task against it
  fails.** The check is real and correct — it lives in
  `validateAITaskAgentCompatibility` (`task_controller.go:3228`) and
  fires at Task admission with a good message
  (`approvalRequiredTools cannot include built-in tool "delegate_task"`)
  — but it runs at Task time, not Agent time. Confirmed for
  `delegate_task`, `request_approval`, `web_search`, `merge_pull_request`
  and `create_agent`: all five Agents validate, all are unusable.

---

## 4. Capability by capability

**Overlap** = the same thing built twice. **Complement** = different
things that compose. **Gap** = neither has it. Every cell traces to
something run on the cluster above or read at `597a8ab` / `v0.1.3`.

| capability | Orka | Kaimahi | verdict |
|---|---|---|---|
| **Approval bound to a specific call** | Yes. Digest over canonical arguments **plus tool spec version plus credential version** (`key.go:73`, `approval_gate.go:711`); parks before execution; single-use idempotency key; approver identity recorded. Scoped to autonomous coordinator Tasks calling `Tool` CRDs (`agent_types.go:284`). | Yes. Digest over argument values; in a gateway, so it covers any caller including workloads we did not launch; standing constraints and a monetary threshold. | **Overlap**, and theirs is the deeper binding. Ours reaches further. Built twice. |
| **Money-denominated spend ledger + price gate** | **None built, and none planned in money.** No price, currency, USD or cents anywhere in the Go tree (the only grep hits are incidental, e.g. `internal/security/secretscan.go:13`). Token counts per model request are recorded in durable events; there is no aggregate, no budget enforced against them, and no cost metric among the `orka_*` metrics — despite the README's "at what cost". Their roadmap does name it — `multi-agent-coordination.md:1011`, "**Cost tracking** — Per-task/child token usage aggregation" — but that is *token* aggregation, so the money gap stands even against the plan. | Postgres ledger in money, `cost_source`, and a gate that refuses a call whose cost cannot be computed rather than ledgering a zero. | **Gap on their side.** The single clearest thing we have that they do not. |
| **Model seam for a foreign workload** | Yes — authenticated, Provider-scoped, key never leaves the cluster. Per-route authorization (SubjectAccessReview) only from `main`; at `v0.1.3` any authenticated cluster token could use it. Not a proxy unless the client sends `X-Orka-Tools: disabled`. No metering. No `/openai/v1/responses`. | Yes — metering proxy with a ledger; also no Responses support until recently, and it metered zero. | **Overlap.** Theirs has the better custody story, and from `main` the better access-control story; ours has the meter. |
| **Tool seam for a foreign workload** | No. `Tool` describes what Orka dials, not a gateway a foreign app routes through. | Yes — enforcing MCP gateway, allowlist, argument policy, proven on a third-party app on kind and AKS. | **Complement.** The one architectural thing that composes rather than duplicates. |
| **Governing a Deployment already running** | No adoption path: no mutating webhook in the bundle, workloads built from CRs. Model traffic only, by repointing a URL. | Two config values, their Deployment untouched. | **Complement**, narrowed to the model seam. |
| **Durable audit / execution record** | Durable events on a PVC-backed store (`ReadWriteOnce`, `deploy/orka.yaml:11225` at `v0.1.3`; `:8118` on `main`) — survives restart. Per-request token counts, tool names, `toolCallID`, `argumentBytes`. **Argument values are never recorded** on this path — `argumentBytes` only (`workers/ai/main.go:1373`). The harness-v2/ACP path is different: tool content is projected into the event's `ContentText`, redacted rather than omitted (`internal/harness/v2/eventjournal/mapper.go:686-705`). That path was read, not run. | Postgres ledger with `arg_summary`, caller claim and observed address. Tool *responses* recorded nowhere. | **Overlap.** Neither records what the other does — theirs the model-request shape, ours the argument values. |
| **Credential custody** | Split, and the split is in the code. ACP runtimes never see a key — they reach the provider proxy. A native `type: ai` worker receives it: `job_builder.go:1301` mounts the Provider Secret as `OPENAI_API_KEY`/`ANTHROPIC_API_KEY` via `secretKeyRef`, gated by `directProviderSecretsAllowed`, which is unconditional for `type: ai` because `isUntrustedComputeTask` (`:207-209`) is true only for `type: container`. Observed on a live pod, alongside `ORKA_ALLOW_BASH=true`. The README's "developers get a ServiceAccount token, not an API key" is true of developers, not of the agent process; their narrower `security.md` claims only the RuntimePool path and is accurate. | The key never reaches the agent on either seam. | **Overlap** on the proxy path, **gap on theirs** for native workers. |
| **Orchestration, sessions, memory, chat** | Coordinator/specialist delegation, autonomous loops, transcript search, memory proposals, SSE chat with an agentic orchestrator, RuntimePools keeping Codex/Claude/Copilot/OpenCode warm over ACP. | Partly built, much smaller. | **Overlap, theirs far ahead.** Nothing here for us to add. |
| **Runtime and CRDs** | 17 CRDs at `v0.1.3` (26 in `main`'s staged chart), its own controller, its own runtimes. | kagent as the runner, plus our plane. | **Overlap at the layer, different choices.** Not ours to reconcile. |
| **Observability** | 25 `orka_*` metric names emitted by the live v0.1.3 controller this lane scraped — labelled counters only appear after first use, so the tree defines more. Structured logs. OpenTelemetry traces and GenAI-semconv metrics behind `-enable-tracing` (off by default) and the standard `OTEL_EXPORTER_OTLP_ENDPOINT` (`internal/tracing/tracing.go:151`) — but the published chart has no value that sets either, so an operator hand-wires it. No token metric and no cost metric in any of them. | Prometheus via a PodMonitor an adopter can extend; OTel is a board candidate, unbuilt. | **Overlap, theirs ahead.** Take theirs; stop the OTel candidate. |
| **Pod hardening** | Non-root, read-only rootfs, all capabilities dropped, observed on a live worker; four admission policies. | No pod-level isolation attempted. | **Gap on ours, theirs ahead.** |
| **Egress control** | Split the other way. The base chart ships **zero** NetworkPolicy templates; the six that exist are in the staged harness-v2 chart, and the controller writes deny-all policies only for ACP RuntimePools and repository-monitor validation Tasks. A plain `type: ai` Job has unrestricted egress. | NetworkPolicy egress on the governed path, on by default. | **Overlap, ours ahead** on the default install. |
| **Cluster provisioning / front door** | Assumes a cluster. No installer, no published CLI binary — tags but no releases. | `curl \| sh` then one command, cluster included, 1 prerequisite. | **Gap on their side**, and it is the mission stated directly. |
| **AKS path** | Not exercised in this lane (§6). | `kmx lift --byo` onto somebody else's AKS cluster in 2 m 34 s. | **Not established.** |

One line of that table needs its evidence stated rather than summarised.
On the compat path there is no Task, so there is no event stream, and
`internal/api/openai_compat.go` contains no call that appends an
execution event or records usage — the string `ExecutionEvent` does not
appear in the file. After this lane's calls through it,
`/api/v1/sessions` was empty and the only trace of them was a request
counter. **A foreign workload's spend through Orka's model seam is not
recorded.** That is the shape of the gap, and it is the same one our own
model seam exists to close.

---

## 5. What we would be asking of them, and what it would cost

Nothing in this section was filed. It is a list for a conversation.

**1. A `/openai/v1/responses` route, or at minimum an honest refusal.**
The smallest change with the widest reach in this report. Today an
unrouted path under `/openai/v1` is swallowed by the dashboard's
catch-all and returned as `200` with HTML, and their own metrics count
it as a success. A route that returns `501` with a JSON error body would
turn an undiagnosable `AttributeError` three frames deep into one line
an adopter can read; a translating handler onto the existing
chat-completions path would make Microsoft Agent Framework work
unmodified. **Size: a route registration and an error body for the first;
a request/response shape translation for the second.** We would be
bringing them a bug and a diagnosis, not a design — the easiest kind of
contribution to accept, and the one that helps people who never hear of
either project.

**2. A money-denominated ledger and a price gate.** (Their roadmap
already wants the neighbouring thing — `multi-agent-coordination.md:1011`
lists "Cost tracking" as per-task token aggregation — so this is an
extension of a direction they have chosen, not a new one imposed on
them.) Their durable events
already carry `inputTokens`, `outputTokens`, `model` and `provider` per
request, on a PVC-backed store. What is missing is a price table, a cost
folded onto those events, an `orka_model_cost_total` metric beside the
ones they already publish, and — the part that matters more than the
arithmetic — **a refusal when a 2xx comes back with usage that cannot be
read**, rather than recording a plausible zero. This project shipped that
bug first and only then the fix, which is the honest way to introduce it.
**Size: a price source (CRD field or ConfigMap), a cost computation at
the six or so points where usage is already parsed, one metric, and one
policy decision about the unreadable case.** Medium, and it lands
entirely inside code they own.

**One caveat that makes this bigger than it looks**, and it is worth
saying before anyone starts: their provider proxy cannot host this.
`cmd/orka-provider-auth-proxy/proxy.go:131-215` attaches a bearer token,
enforces size and concurrency limits, refuses redirects and compressed
bodies, and then streams the response through byte for byte
(`providerproxy.StreamBoundedResponse`, `:212`). It never parses a body
in either direction — `usage` and `model` do not appear in the package
at all. Token capture happens only in Orka's own LLM client
(`internal/llm/{anthropic,openai}/provider.go`), which is a different
path. So the meter has to live in the client and the compat handler,
not in the proxy, and a ledger for traffic that goes through the proxy
alone is a new capability rather than an addition. Worth knowing before
proposing it, and worth contrasting with KARS: KARS at least tries to
read the usage block and silently records nothing when it cannot; here
there is no code path that could try.

**3. Argument values on the audit row.** Their events record
`argumentBytes` and never the values; the values exist only inside a
pending approval's preview and vanish once decided. They have already
written the hard part — `events.SanitizeExecutionEventJSON` and
`boundApprovalTargetArgsPreview` bound and sanitise exactly this data for
the approval preview. **Size: small but not trivial.** `SanitizeExecutionEventJSON` is already
exported (`internal/events/redaction.go:155`), but
`boundApprovalTargetArgsPreview` is unexported in `internal/approvals`
(`key.go:168`) while the emission sites live in `workers/ai`
(`main.go:1366`, `approval_gate.go:1143`), so it needs exporting or
moving — and the two harness emitters (`internal/harness/mapper.go:105`,
`internal/harness/v2/eventjournal/mapper.go:1328`) would be left
inconsistent unless they are done at the same time. This is the one
place our ledger sees something theirs cannot.

**4. A server-side default for the transparent-proxy mode.** A Provider
field or Helm value that sets the `X-Orka-Tools: disabled` behaviour for
callers that cannot send headers. **Size: one config field read where
`compat_coordinator.go:30` reads the header.** Small, and it is what
makes their model seam usable by applications configured through
environment variables — which is most of them.

**5. A tool seam for workloads Orka did not launch.** The genuinely
complementary piece, and the only one that is a design rather than a
patch: an enforcing MCP gateway a foreign application points at, with
the allowlist and argument policy in front of it. We have run this on a
third-party application on two clusters. It is a new component in their
tree and it should be offered as a proposal with our implementation as
the reference, not as a pull request.

**6. Three small correctness items — not four.** The three in §3e are
still open on `main` and were re-checked there: the condition that
cannot be `True`, the `Succeeded` phase on iteration exhaustion
(`task_controller.go:4480-4482`, still `TaskPhaseSucceeded` at
`597a8ab`), and the Agent that validates but cannot run a Task.

**The RBAC role from §1c is already fixed and must not be offered.**
`manifest_staging/deploy/orka.yaml:602` defines `orka-api-editor-role`,
added by the same commit `25da6e4` that brought route authorization, so
it ships with the next release. This report nearly listed it as a
contribution. That is precisely the failure the W48 upstream lane
recorded — **a candidate recorded against a pinned version decays** —
and the only reason it was caught here is that somebody was asked to
assume every claim in this document was false. Any of the remaining
items should be re-checked against `main` on the day it is raised, not
on the day it was found.

**What we should not offer.** The argument-bound approval itself. Theirs
is further along than ours, and arriving with a second implementation of
something a project already has — and has tested more adversarially
than we have — is the behaviour the prime directive on this board exists
to prevent. The right move is the opposite: read `approval_gate.go`
before we touch ours again.

---

## 6. What we should stop maintaining

Specific, and each one because Orka does it and does it better.

- **OpenTelemetry.** It is a candidate on this board and unbuilt. Orka
  has real OTLP trace and metric exporters with GenAI semantic
  conventions, behind `-enable-tracing` and the standard
  `OTEL_EXPORTER_OTLP_ENDPOINT`. KARS has an exporter too. Close the
  candidate — and note for §5 that Orka's published chart sets neither
  the flag nor the endpoint, so wiring it in their `values.yaml` is a
  small, welcome contribution rather than something for us to build.
- **Sessions, durable memory, transcript search.** Partly built here,
  substantially built there, on a PVC-backed store with a review flow for
  memory proposals. Stop.
- **Interactive chat.** Theirs is an agentic orchestrator with SSE
  streaming that creates and manages agents and tasks for you
  (`internal/api/chat.go` and its tool loop, ~2,600 lines), embedded in
  a dashboard of some 38,000 lines of TypeScript that ships inside the
  controller binary. Ours merged into `main` while this lane was
  running, as PR #158. **That timing is the point rather than an
  awkwardness:** this recommendation is not about old work nobody
  wanted, and saying "stop" about something merged today is the only
  honest version of it. Stop.
- **Multi-agent orchestration.** Never start. Coordinator/specialist
  delegation with depth and concurrency limits, autonomous loops with
  persisted plan state, and warm pools of real coding-agent CLIs are the
  centre of their project.
- **Pod-level hardening.** Non-root, read-only rootfs, dropped
  capabilities and admission policies are already in their bundle and in
  KARS. Nothing to add.
- **The argument-bound approval, as a differentiator.** Keep the code —
  it is what makes the gateway in §5.5 worth offering — but stop
  describing it as the thing this project uniquely has. As of today that
  sentence is wrong.

---

## 7. What was not measured, and why

- **The AKS half of both scenarios.** Not run. The guardrail was no
  spend, and this lane spent nothing. So every number here is kind, and
  the AKS comparison is structural: `kmx lift --byo` put a governance
  plane on somebody else's AKS cluster in 2 m 34 s; Orka's equivalent
  was not attempted and its chart's AKS behaviour is unknown to this
  report.
- **`main` rather than `v0.1.3`.** The live cluster ran the published
  v0.1.3 release, because that is the only path a non-technical person
  can take. `main` needs a registry you can push to, nine images built
  locally, digest pins, a webhook certificate and a provider proxy. So
  RuntimePools, harness v2, ACP coding agents, execution workspaces,
  gateways, repository monitors and security scanning were **read, not
  run**. Any of them could change the picture and none was measured. The
  approval gate was read on `main` and run on v0.1.3, and the two are the
  same code: `git diff v0.1.3 HEAD` over `internal/approvals/` is empty,
  and over `workers/ai/approval_gate.go` is a single refactor of one
  `errors.As` call. Every line cited in §3 holds at both.
- **The redirection test was proved by their tests and their code, not
  by a live redirect.** This lane could not steer a local 7B model into
  calling an approved tool with different arguments after approval. The
  binding is established by `resolvedDecisionMatchesTarget`
  (`approval_gate.go:711`) and by
  `TestApprovalGateMismatchedResolvedApprovalRequestsNewApproval`, which
  passes; the live half is the park and the single-fire release.
- **One live experiment was contaminated and is reported as such.** In
  the Acme run this lane approved two pending approvals — the gate's own,
  correctly showing `Acme Supplies / 42`, and a second, empty-argument
  one the model produced by calling `request_approval` itself. The
  transfer that followed matched the first. Which one it redeemed cannot
  be established from the outside, because argument values are not
  recorded; the clean Globex run was done afterwards for that reason.
  **The stray empty-argument approval is worth naming on its own:** a
  human looking at that queue saw a pending `pay-vendor` approval whose
  displayed arguments were `{}`, sitting beside a real one. It
  authorises nothing — its digest is the digest of the empty object —
  but it is an invitation to a wrong click.
- **This report was adversarially checked before it was opened**, by a
  reader told to assume every claim false, and that pass changed four
  things. It caught the report attributing `main`'s route authorization
  to the `v0.1.3` cluster the lane actually ran (§2b now separates
  them); three citations in §3c that pointed at line numbers past the
  end of the file, taken from the test file rather than the source; a
  CRD count repeated three times that matched no artifact either project
  ships; and the mechanism of §1c, which is a documentation version
  mismatch rather than a missing role in one bundle. **Every one of
  those was in a passage the author had already re-read.** A second pass
  found five more: a contribution item that upstream has already fixed
  and staged (§5.6), an absolute claim about argument recording that
  holds only for the `type: ai` worker (§3d, §4), a prerequisites list
  taken from the wrong version's page (§1a), and two code blocks
  presented under `file:line` headers that had been reflowed rather than
  quoted (§3a). Two further corrections were caught by the author before
  either pass — a quotation attributed to the KARS report that appears
  only in this lane's prompt, and an interval of "eight days" for
  something that happened five hours earlier.

  A third pass added six more — a roadmap entry that names the
  neighbouring capability (§4, §5.2), a contribution sized "small" that
  needs an unexported function moved first (§5.3), four citations that
  were `main` line numbers attached to `v0.1.3` observations, and an
  arithmetic claim ("within seconds") that the numbers did not support.
  **One of its findings was itself wrong** and was checked rather than
  accepted: it reported that Orka's docs never say they publish no
  GitHub Releases, having grepped only the tag and one page; the
  statement is at `website/docs/getting-started.md:113` and
  `reference/release-status.md:107`. A reviewer told to assume
  everything is false will produce some false positives, and taking
  those on trust would have been the same error in the other direction.

  **The pattern is worth more than the individual fixes.** Every defect
  was a claim about a *version*: main's code credited to a v0.1.3 run,
  main's docs credited to a v0.1.3 bundle, a fix that landed three days
  before the run, a page that changed between the tag and today. A
  report that compares two moving projects has to say which commit each
  sentence is about, and this one did not until it was made to.
- **Nobody was contacted and nothing was filed.** No issue, no comment,
  no pull request, no message. This document is the output.

---

## 8. Spend and teardown

**US$0.00.** No Azure resource was created and no hosted model was
called. Every token came from an Ollama container on the local machine
(`qwen2.5:3b`, then `qwen2.5:7b`), on CPU.

Teardown, on a cluster this lane created:

```console
$ kind delete cluster --name orka-eval   Deleted nodes: ["orka-eval-control-plane"]
$ kind get clusters                      No kind clusters found.
$ kubectl config get-contexts | grep orka (none)
$ docker rm -f orka-ollama; docker volume rm orka-ollama; docker rmi ollama/ollama
$ docker ps -a | grep orka                (none)
$ docker volume ls | grep orka            (none)
$ docker images | grep -E 'orka|ollama'   (none)
```

Every Orka image lived inside the kind node and went with it. The two
pre-existing local `sundae-funday` tags from the earlier foreign-app and
KARS lanes were left alone rather than matched by name. No Azure or Slack
identifier appears in this report or in anything this lane produced.

---

## 9. Three options, and the recommendation

### Option A — ours into theirs, and we keep building

Contribute §5's list; keep this repository as a governance plane beside
kagent. **Cost:** everything currently on the board, indefinitely, plus
the contribution work. **What it buys:** a second Kubernetes agent
platform in the same company, whose one advertised differentiator now
exists in the other one, built better. The mission says success is not
measured in adoption of this repository. This option is measured in
nothing else.

### Option B — theirs under ours

Depend on Orka for orchestration, sessions, credential custody and the
provider proxy; shrink to the enforcing gateway, the money ledger and
the argument policy on top. **Cost:** a real integration — our plane
would have to sit in front of their compat endpoint (which is an
orchestrator by default, switchable only by a header we would have to
persuade every caller to send), and our approval layer would sit beside
theirs rather than replace it, since theirs runs inside their worker
where ours cannot reach. **What it buys:** two approval gates on one
cluster with different scopes and different digests, which is worse than
either alone. This lane went looking for this option and did not find a
seam that supports it.

### Option C — most of this repository stops

Stop the plane as a product. Move the two things that survive contact
into Orka: the money-denominated ledger with a price gate, and the
enforcing tool seam for workloads nobody launched. Take the Responses
finding and the four small correctness items to them as bugs. Keep, for
as long as it is useful, only the part that has no counterpart anywhere:
**the front door that creates the cluster.** Orka assumes a cluster,
publishes no CLI binary, and has no installer; KARS creates a whole Azure
estate or nothing. One command from nothing to a working agent on AKS is
the mission stated directly, and it is the last thing here that nobody
else has built.

**Cost:** the board empties. Several lanes' work becomes a reference
implementation attached to a proposal rather than a product. That is a
real loss and it should be said plainly rather than reframed.

### The recommendation

**Option C, and the argument for it is one sentence: the capability this
project was continuing to exist in order to prove is now in a project
that is further along with it, inside the same company, heading for a
foundation.**

This morning the honest answer to "given KARS exists, what does this
add?" was one thing, and the recommendation was to take that one
thing upstream because nobody else had it. The reason it had not been
taken upstream yet was that nobody outside this project had ever used
it. That reason has expired: somebody outside this project built it,
tested it more adversarially than we did, and shipped it.

What remains is not nothing, and it is not a product. It is a money
ledger that refuses what it cannot price, a tool seam for workloads
nobody launched, one bug that breaks Microsoft Agent Framework against
every model gateway we have now measured, and an installer that starts
from no cluster. Four things, three of which belong in somebody else's
repository and one of which might.

**And this is a recommendation about work, not about people.** The
teammate who owns Orka built the thing this project spent P12 and P13
arriving at, and built it with a spec digest and a credential-version
digest we did not think of. The correct response to that is not to look
for a niche it does not fill. It is to go and help.
