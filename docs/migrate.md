# Migrating an application onto Orka, with its model traffic governed

`kmx migrate` takes an application that already exists, that this project
did not write, and puts its model traffic onto
[Orka](https://github.com/orka-agents/orka) — authenticated,
Provider-scoped and recorded — without changing the application.

**"Without changing the application" means:** no source edit, no rebuilt
image, no fork of anybody's chart. What changes is four environment
variables and one mounted file on a Deployment the adopter owns, and
`kmx` writes that patch to a file rather than applying it.

Sections 1-8 were measured on a kind cluster on 2026-09-09, against
Orka `v0.1.3` — the published release bundle — and the application
[`pauldotyu/sundae-funday`](https://github.com/pauldotyu/sundae-funday)
at `bd2a035`, image `ghcr.io/pauldotyu/sundae-funday:0.1.0-36.1.gbd2a035`,
its own Helm chart, unmodified. [Section 9](#9-the-same-migration-on-aks)
is the same migration run again on **AKS** on 2026-09-10, same Orka tag and
the same application, and says which of the differences are Orka's, which
are ours and which are just AKS. Nothing in Orka's repository was touched on
either run: no issue, no comment, no pull request.

---

## The answer, first

**It works, and the two things that stood in the way are handled in the
seam rather than in the application.**

- The application's framework speaks the **Responses API**. Orka's
  OpenAI-compatible endpoint has no `/openai/v1/responses` route: the
  request falls through the dashboard's single-page-app catch-all and
  comes back `HTTP 200` with HTML, and the client dies inside its own
  parser on `'str' object has no attribute 'metadata'`. **The seam now accepts
  the Responses API and translates it onto chat completions in both
  directions.** Reproduced here before it was fixed, and fixed here.
- Orka's compatible endpoint is **not a proxy by default**: it injects its
  own coordinator prompt and tool definitions and runs a server-side
  agentic loop, and the only switch is the per-request header
  `X-Orka-Tools: disabled`. An application configured by environment
  variables cannot send a header. **The seam sends it.**

What that second one costs, measured on one cluster with one model, the
same application and the same question:

| through | model calls | prompt tokens | wall clock | answer |
|---|---|---|---|---|
| `orka` (header sent) | 2 | 539 + 736 | **22.2 s** | the shop's five flavours, from the application's own menu tool |
| `orka-coordinator` (Orka's default) | 0 metered, 3 attempted | none readable | **16 min 24 s, then HTTP 500** | none — Orka's coordinator was still looping on its own tools long after its caller had gone |

And what the application gets that it did not have: every model call is a
row with a credential, a model, token counts and an outcome — including
the ones that were refused.

---

## 1. What the adopter starts with

A running application, three pods, its own chart, pointed straight at
Orka's compatible endpoint the way Orka's own documentation suggests:

```console
$ helm upgrade --install sundae ./deploy/helm/sundae-funday -n demo \
    --set image.tag=0.1.0-36.1.gbd2a035 \
    --set secret.create=false --set secret.existingSecret=app-secrets \
    --set config.OPENAI_BASE_URL=http://orka-api.orka-system:8080/openai/v1 \
    --set config.OPENAI_CHAT_MODEL=local/qwen2.5:3b
```

(The `app-secrets` Secret holds a ServiceAccount token in
`OPENAI_API_KEY` — an OpenAI client sends its "API key" as
`Authorization: Bearer`, which is the field Orka authenticates. Create
that Secret separately, as here; a token passed with `--set-string` lands
in the shell history and the process arguments.)

The concierge is Ready in 14 s and all three pods settle shortly after.
Then ask it a question:

```console
$ POST /api/chat  {"session_id":"s1","message":"What flavors do you have?"}
Internal Server Error

# the application's own log:
File ".../agent_framework_openai/_chat_client.py", line 2539, in _parse_response_from_openai
    metadata: dict[str, Any] = response.metadata or {}
AttributeError: 'str' object has no attribute 'metadata'

# Orka's log, at the same second:
INFO  api-server  request completed  {"method":"POST",
  "path":"/openai/v1/responses","status":200,"duration":"1.353703ms"}
```

**That is the starting position: the application cannot complete a single
turn, and the endpoint's own log calls it a success.** The framework's
`OpenAIChatClient` is the Responses client in `agent-framework` 1.14 and
posts `/v1/responses`; Orka routes `/openai/v1/chat/completions` and
`/openai/v1/models` (and the Anthropic pair) and nothing else under that
prefix, so the path reaches the dashboard's static handler. 1.3 ms is the static file
handler, not a model.

This is the third project measured against this application that the
Responses API breaks, each in a different way, and it is the reason the
translation below lives in the seam rather than in a wrapper around one
application.

---

## 2. Every command, and what each one creates

The plane must already be on the cluster (`kmx plane`; 39 s here). Then:

```console
$ kmx migrate concierge --namespace demo --model local/qwen2.5:3b
```

That is the whole command. It took **9.1 s** and did seven things, in
this order, refusing at each step rather than continuing past it:

**1. Read the workload.** Which container talks to a model, and what it
is configured with today — from the live object, including variables that
arrive through `envFrom` a ConfigMap, which is where most charts put them:

```
demo/concierge container "concierge" reads OPENAI_BASE_URL today:
  OPENAI_BASE_URL = http://orka-api.orka-system:8080/openai/v1 (from ConfigMap app-config)
  OPENAI_CHAT_MODEL = local/qwen2.5:3b (from ConfigMap app-config)
```

A container that reads no base URL anywhere is refused here, with nothing
written: a patch that sets a variable the application never reads is a
migration that reports success and changes nothing.

**2. Check the endpoint can serve what is being asked of it.**

```
Orka Provider "local" is ready, so model "local/qwen2.5:3b" resolves there.
```

Orka resolves `<provider>/<model>` against its own `Provider` objects. A
missing or unready Provider is refused before anything is written — that
refusal is the Provider scoping this migration is buying, and it is
cheaper as one message than as an application that rolls and then fails
every turn.

**3. Two files.**

| file | what it is | who applies it |
|---|---|---|
| `migrations/concierge.yaml` | a ServiceAccount, Role and RoleBinding in Orka's namespace; a NetworkPolicy in the plane's namespace | **kmx** |
| `migrations/concierge-patch.yaml` | four environment variables and one mounted file for the adopter's Deployment | **you** |

kmx does not apply the second one. It creates objects it owns in a
namespace it was named, and does not silently mutate somebody else's
workload — the same rule `kmx tools sidecar` follows.

**4. The identity the seam presents to Orka.**

```console
serviceaccount/kaimahi-migrate created
role.rbac.authorization.k8s.io/kaimahi-migrate created
rolebinding.rbac.authorization.k8s.io/kaimahi-migrate created
```

The Role carries exactly one rule — `create` on `chats` in
`core.orka.ai` — which is the permission Orka's own route table
authorizes `POST /openai/v1/chat/completions` against. Asked of the API
server directly, as a `SubjectAccessReview`, which is the same question
Orka's middleware asks:

```console
$ kubectl create -f sar.yaml     # user: system:serviceaccount:orka-system:kaimahi-migrate
{"allowed":true,"reason":"RBAC: allowed by RoleBinding \"kaimahi-migrate/orka-system\"
 of Role \"kaimahi-migrate\" to ServiceAccount \"kaimahi-migrate/orka-system\""}

$ kubectl create -f sar.yaml     # user: system:serviceaccount:orka-system:probe
{"allowed":false}
```

On `v0.1.3` — the version measured here — the compatible endpoint takes
authentication without a per-route authorization check, so this Role is
inert; route authorization arrived after that tag. It is created anyway,
and §7 says exactly what that means for the evidence.

**Do not check this with `kubectl auth can-i`.** It answers `no` for the
account that is in fact allowed — `chats` is not a registered resource
type, so the shorthand does not resolve the group and the review it sends
is not the one Orka sends. That cost twenty minutes here; the explicit
`SubjectAccessReview` above is the reliable form.

**5. The seam's allowance for the application's namespace.**

```console
networkpolicy.networking.k8s.io/kaimahi-proxy-ingress-demo created
```

The plane's namespace is default-deny in both directions and its
committed ingress rule admits the agent namespace alone — deliberately,
because the namespace an adopter's application runs in is not this
repository's to name in a committed manifest. This object is the plane's,
one per admitted namespace, and it opens the model port only. The tool
seam's port is a separate decision with its own commands.

It is load-bearing rather than decorative, measured by taking it away:

```console
# with kaimahi-proxy-ingress-demo deleted, from inside the application's pod:
$ python3 seamprobe.py          # → hangs; killed at 60 s
command terminated with exit code 124

# re-applied:
$ kubectl apply -f migrations/concierge.yaml
networkpolicy.networking.k8s.io/kaimahi-proxy-ingress-demo created
$ python3 seamprobe.py          # → 200
```

**6. The token the plane presents to Orka.**

```console
kubectl -n orka-system create token kaimahi-migrate --duration=720h # (into Secret kaimahi-orka-token, from the pipe)
kubectl -n kaimahi apply -f - # (Secret kaimahi-orka-token)
The seam's token for orka-system/kaimahi-migrate expires 2026-10-09T22:26:25Z.
```

720 h was asked for and **30 days** was granted: the API server caps a
TokenRequest at its own maximum, so what it granted is what is printed
rather than what was requested. The token goes from the API server's
reply into a Secret through a pipe — never an argument, a file or a log.

**7. The credential the application presents to the plane**, and the
authority it verifies the seam with:

```console
Governed credential "concierge" issued; Secret demo/kaimahi-concierge-token created.
The plane stores only its hash — the real upstream keys stay with the proxy.
It expires 2026-10-09T22:26:31Z — `kmx credentials` shows every deadline.
secret/kaimahi-plane-ca created
```

Then the one command that changes the adopter's own object:

```console
$ kubectl -n demo patch deployment concierge --patch-file migrations/concierge-patch.yaml
deployment.apps/concierge patched
```

which sets, on that one container:

```yaml
OPENAI_BASE_URL:   https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/orka/v1
OPENAI_API_KEY:    from Secret kaimahi-concierge-token, key api-key
OPENAI_CHAT_MODEL: local/qwen2.5:3b
SSL_CERT_FILE:     /etc/kaimahi/plane-ca/ca.crt      # + the Secret that holds it, mounted
```

Explicit `env` wins over `envFrom`, so this overrides the application's
own ConfigMap without editing it — which matters, because that ConfigMap
belongs to somebody's Helm release. **It also means a `helm upgrade`
re-renders the Deployment and takes the patch straight back out.** `kmx
migrate` prints these four values at the end for exactly that reason:
they belong in the application's own release afterwards.

---

## 3. What is true afterwards that was not true before

The same question, to the same application, on the same cluster:

```console
$ POST /api/chat  {"session_id":"s3","message":"What flavors do you have?"}
200  {"reply":"Hello! We currently have the following flavors available: Vanilla Bean,
      Chocolate, Strawberry, Mint Chip, and Coffee. Would you like to know more about
      our sundae options?","source":"menu"}
```

22.2 s, two model calls, and `"source":"menu"` — the reply came from the
application's own `list_menu` tool, so a **tool-calling turn** completed
through the translation.

A later turn on the same setup went further: the concierge called several
of its own tools and came back with a made-up-to-order recommendation
("Classic Sundae with Vanilla Bean ice cream, Hot Fudge sauce, and a mix
of Rainbow Sprinkles and Oreo Crumble"), 259.9 s on a busy CPU node, and
the model calls behind it are rows like the others. The tool calls
themselves are not governed — see §8.

### Authenticated

```console
1. seam, no credential            -> 401 unauthorized
2. seam, an unknown credential    -> 401 unauthorized
4. Orka directly, no credential   -> 401 {"error":{"code":401,"message":"missing authorization header"}}
```

Two credentials exist and the application holds exactly one of them: a
`kmh_` token for the plane, which buys it a metered, budgeted seam and
nothing else. What it no longer holds is any credential for a model —
the ServiceAccount token that opens Orka stays in the plane's namespace
with one reader, and a copy of the application's own Secret buys an
attacker a governed, recorded seam rather than an endpoint.

### Provider-scoped

The caller names a model, never a URL. A name no Provider allows is
refused by Orka and the refusal arrives verbatim, because an error body
is relayed rather than translated:

```console
3. seam, a model no Provider allows -> 400 {"error":{"message":"failed to resolve provider:
   no provider \"nosuchprovider\" found and no 'default' Provider CRD exists", ...}}
```

### Recorded

```console
$ kmx flow concierge
created (UTC)       credential  kind   what                   outcome  cents detail
2026-09-09T22:27:16 concierge   model  local/qwen2.5:3b       400          0 0 in / 0 out via orka called by claimed "ua:agent-framework-python/1.14.0"
2026-09-09T22:29:29 concierge   model  local/qwen2.5:3b       400          0 0 in / 0 out via orka called by claimed "ua:agent-framework-python/1.14.0"
2026-09-09T22:30:12 concierge   model  local/qwen2.5:3b       200          0 36 in / 2 out via orka called by claimed "ua:Python-urllib/3.12"
2026-09-09T22:30:30 concierge   model  local/qwen2.5:3b       200          0 539 in / 21 out via orka called by claimed "ua:agent-framework-python/1.14.0"
2026-09-09T22:30:42 concierge   model  local/qwen2.5:3b       200          0 736 in / 36 out via orka called by claimed "ua:agent-framework-python/1.14.0"
2026-09-09T22:34:21 concierge   model  nosuchprovider/gpt-4o  400          0 0 in / 0 out via orka called by claimed "ua:Python-urllib/3.12"
-- 6 events, 0 cents, 2 refused
```

Refusals are rows too, and the model Orka rejected is on the row that
records the rejection. Three honest limits on that trail:

- **`0 cents` is not "free".** The `orka` upstream is classified
  `metered` with no price configured, so tokens are always counted and a
  cost appears only when a real price for that model is configured. Under
  a *cents* budget an unpriced model is denied outright rather than
  admitted at zero.
- **An unauthenticated call leaves no row.** There is no credential to
  attribute it to. The 401s above are in the proxy's metrics, not the
  ledger.
- **The rows are ordered by time, not causally linked.** The plane
  records no correlation id, so concurrent turns interleave.

---

## 4. Obstacle 1: the Responses API, handled in the seam

The plane's model table now lets one upstream declare **two** paths: the
one a client may POST (`client_path`) and the one forwarded upstream
(`path`).

```json
"orka": {
  "base_url": "http://orka-api.orka-system.svc.cluster.local:8080/openai",
  "path": "v1/chat/completions",
  "client_path": "v1/responses",
  "classification": "metered",
  "credential_file": "/etc/kaimahi/upstream-creds/orka/token",
  "extra_headers": {"X-Orka-Tools": "disabled"}
}
```

Only one pairing is implemented — Responses onto chat completions — and
every other combination is refused when the config loads, rather than
accepted and forwarded in a shape the endpoint cannot read.

Three rules hold the translation together:

1. **Refuse what cannot be honoured.** A field the translator does not
   understand is an error naming the field, never a field dropped in
   transit. `previous_response_id` (this seam holds no conversation
   state), `store: true` (it stores nothing), an input item or content
   part that is not text, a tool that is not a function: each is a `400`
   that says which.
2. **The meter is not in the path of the translation.** Token counts are
   read out of the endpoint's own body under the endpoint's own protocol.
   A translation bug can produce a wrong answer; it cannot produce a
   wrong row.
3. **Nothing is invented.** Every value in the envelope going back is one
   the endpoint reported, one the client sent, or a constant stating this
   seam's own behaviour (`"store": false`, `"truncation": "disabled"`).

A **streamed** request on a translating upstream is refused rather than
half-translated: turning a chat-completions SSE stream into the Responses
API's semantic events is a second translator, and half of one hands the
client a stream it cannot parse after the first byte has already left,
where no refusal is possible any more.

### What rule 1 cost, and what it bought

The first migrated turn failed:

```
ChatClientException: ... this seam translates the Responses API onto chat completions
and has no translation for "include" — refused rather than forwarded without it
```

Microsoft Agent Framework appends `include: ["reasoning.encrypted_content"]`
to **every** request it makes, whatever model it is talking to
(`_chat_client.py:1415-1418`, unconditional unless the request uses
service-side storage). Refusing it would have meant no application built
on that framework could ever use this seam.

So `include` is accepted, for exactly one value, with the reason written
down: `include` asks for *optional* extra output, a Responses endpoint
omits what the model did not produce, and a chat completion carries no
reasoning item at all — so answering with none of it is the same answer
that endpoint would give for a model that does no reasoning, not a field
dropped in transit. Every other value is still refused by name.

**That is the rule working, not the rule failing.** A permissive
translator would have dropped `include` silently on the first turn and
nobody would ever have looked at it. The strict one produced a legible
error in the application's own log naming the field — which is what
turned a one-line fix into a decision made on purpose.

---

## 5. Obstacle 2: the injected coordinator, and what it costs

`X-Orka-Tools: disabled` is the only way off Orka's server-side agentic
loop — no Helm value, no CRD field, one per-request header
(`internal/api/openai_compat.go`, gated in `compat_coordinator.go`). An
application configured by environment variables cannot send one, so the
seam sends it from the committed table.

To make the cost measurable rather than argued about, the table carries
the same endpoint twice: `orka` sends the header, `orka-coordinator` does
not. Both are reached by the same credential, and both are metered, so
the difference lands in the same ledger.

The raw shape of it, seven words in, straight at Orka's endpoint with
`curl`, same cluster, same model:

| request | prompt tokens | completion | wall clock |
|---|---|---|---|
| `X-Orka-Tools: disabled` | **36** | 2 (`" pong"`) | **0.56 s** |
| Orka's default | *no usage, no choice* | — | **7 m 14 s** |

The second row is not a slow answer. After 7 m 14 s the response carried
no `usage` and no `choices` at all — nothing a client could read as a
completion.

And through the application, §6.

---

## 6. The same application, through Orka's default behaviour

`kmx migrate concierge --namespace demo --model local/qwen2.5:3b
--upstream orka-coordinator --no-apply` writes the same patch pointed one
upstream over; applying it and asking the same question produces this, in
Orka's own log:

```
INFO anthropic-compat  premature end of turn — injecting continue message and re-looping
  {"iteration": 0, "retries": 1, "content_prefix": "Could you please provide more details
   about what kind of flavors you are interested in? Are they specific to a cuisine t…"}
INFO anthropic-compat  tool loop iteration  {"iteration": 1, "tool_calls": 1}
INFO anthropic-compat  premature end of turn — injecting continue message and re-looping
  {"iteration": 2, "retries": 2, "content_prefix": "I have successfully checked for any
   pending tasks. If there are no running or completed tasks, I'll be ready to assist f…"}
```

The application asked what flavours the shop has. What answered was
Orka's coordinator, checking whether there were any pending Orka tasks —
its prompt, its tools, its loop, wearing the application's name.

**It never came back.** The plane's own upstream client is bounded at
five minutes (`plane/internal/proxy/proxy.go`), and the call reached that
bound:

```console
$ kmx flow concierge
2026-09-09T22:36:51 concierge model local/qwen2.5:3b 502  0 in / 0 out via orka-coordinator …
2026-09-09T22:42:18 concierge model local/qwen2.5:3b 502  0 in / 0 out via orka-coordinator …
2026-09-09T22:47:45 concierge model local/qwen2.5:3b 502  0 in / 0 out via orka-coordinator …

# the plane's log, at the first of those:
ERROR proxy: upstream call failed  upstream=orka-coordinator
  err="Post \"http://orka-api.orka-system.svc.cluster.local:8080/openai/v1/chat/completions\":
       context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
```

The framework retried twice more, each attempt spent five minutes the
same way, and after **16 minutes 24 seconds** the browser got:

```console
$ POST /api/chat  {"session_id":"c1","message":"What flavors do you have?"}
500  ChatClientException: ... service failed to complete the prompt: upstream unreachable
```

Meanwhile **Orka's loop carried on server-side after its caller was
gone** — `tool loop iteration {"iteration": 11}` arrived five minutes
after the first 502, for a request nobody was waiting for any more.

So the honest form of the comparison is not "slower". It is: through the
header, the application's own agent answered in 22.2 s on two metered
calls. Without it, one question spent 16 minutes, produced no answer, no
readable token count, and three rows saying the seam gave up — while the
endpoint kept working on an agent the application never asked for.

**This is the difference the header makes, and it is not a performance
tuning knob.** With it, the endpoint is a transparent proxy and the
application's own agent runs. Without it, the application's agent is
replaced by somebody else's, silently, by default, and the only way to
say otherwise is a header most applications cannot send.

Worth being fair about what this measurement is and is not: a small
model on CPU is the worst case for a prompt that is mostly tool
definitions, and a larger model would finish the loop. The token counts
are the portable part; the wall clock is this cluster's.

---

## 7. What did not work, and what it cost

- **`kubectl auth can-i` answered `no` for an account that is allowed.**
  `chats` is not a registered resource type, so the shorthand does not
  resolve `core.orka.ai` and the review it sends is not the one Orka
  sends. An explicit `SubjectAccessReview` gives the right answer. ~20
  minutes, and a claim that would have been wrong in this document.
- **The first migrated turn failed on `include`** (§4). One redeploy of
  the plane, ~5 minutes, and it improved the design.
- **A stale log line read as a live failure.** After redeploying the
  plane, the application's log still ended with the previous failure and
  the next request appeared to have failed identically. It had not — the
  next request was fine. Read the timestamps, or probe the seam directly
  before believing the application's tail.
- **`kubectl cp` to `deploy/<name>` does not work**; it needs a pod name.
  Minor, twice.
- **Orka's `/metrics` on port 8080 is swallowed by the same
  single-page-app catch-all** that swallows `/openai/v1/responses`, and
  returns the dashboard's HTML. The real metrics are on the
  controller-runtime metrics service (`:8443`, authenticated), which the
  migration's ServiceAccount is deliberately not allowed to read. So the
  metric that counts an unrouted POST as a 2xx success is not quoted here
  from this run; the log line above is, and it says `status: 200` for
  `/openai/v1/responses`.
- **AKS: measured separately, in [§9](#9-the-same-migration-on-aks).** Every
  number in §§1-8 is kind, on one machine, with `qwen2.5:3b` on CPU, and
  that lane spent nothing.
- **Not measured: Orka's `main`.** The release bundle `v0.1.3`
  authenticates its compatible endpoint without authorizing it per route;
  route authorization arrived after that tag. The Role this command
  creates is therefore inert on the version measured here, and was
  verified by `SubjectAccessReview` rather than by watching Orka enforce
  it. It is created anyway, because the alternative is a migration that
  works today and returns a `403` nobody can place on the adopter's next
  upgrade.

---

## 8. Limits, stated

- **The tool seam is not part of this.** `kmx migrate` governs model
  traffic. The application's MCP calls still go straight to its own tool
  server; `kmx tools add` and `kmx tools govern` are the commands for
  that, and doing both silently would be deciding it for the adopter.
- **Streaming is refused** on a translating upstream (§4).
- **A tool-calling turn through this seam fails part of the time, and the
  refusal is correct.** Measured on AKS over 11 identical turns: **6
  answered, 5 did not**, each failure a `400` naming
  `previous_response_id`. The chain is short and none of it is Orka's or
  AKS's — see §9 — but the honest summary is that **the seam refuses to
  continue a conversation it does not hold, and this framework asks it to,
  because it decides whether the server stores state by reading its own
  request option rather than the `"store": false` in our answer.** Ignoring
  the field is not the fix: when the framework sends it, it deliberately
  strips the server-issued item identities from the history it inlines —
  assuming the server already holds them — so answering anyway would answer
  from a conversation missing pieces we never stored. §9 has the mechanism,
  line by line.
- **A `helm upgrade` undoes the patch.** The four values belong in the
  application's own release; `kmx migrate` prints them.
- **The seam's token expires, and how soon is the CLUSTER's decision, not
  ours.** `kmx migrate` asks for 720 h either way. kind granted all of it —
  30 days. **AKS granted 24 hours** (§9), so on a managed cluster the
  migration stops working after a day unless it is re-run. Read the expiry
  the command prints; do not carry the number from one cluster to another.
  Re-running `kmx migrate` mints another, and the application's own
  credential is untouched by that. Measured on kind: a second run 28 minutes
  later reported both files unchanged, every object `unchanged`,
  `Credential "concierge" already issued and kaimahi-concierge-token is
  bound to it; keeping both`, and a fresh token — 20.6 s, and the
  application answered afterwards without being touched. The same second run
  on AKS took 18.4 s and said the same things.
- **The endpoint address is committed, not configurable.** `orka` and
  `orka-coordinator` name
  `orka-api.orka-system.svc.cluster.local:8080` in
  `k8s/plane/upstreams.yaml`, because an upstream that carries a
  credential and a header is a reviewed entry in this repository rather
  than something a command invents. An Orka installed elsewhere means
  editing that line and redeploying the plane.
- **`--upstream orka-coordinator` is a measuring instrument**, not a
  recommendation. It points an application at an orchestrator that will
  answer as itself.
- **The credential is named after the Deployment**, so two Deployments
  called `concierge` in different namespaces would ask for the same
  credential name. The second run does not overwrite the first — the
  plane refuses, because the token is shown once and cannot be recovered
  — and the refusal names the collision and offers `--credential`,
  because the generic recovery for a missing Secret is "delete the row
  and re-run", which here would delete another workload's live
  credential.
- **The hop from the seam to Orka is plain HTTP.** Orka's compatible
  endpoint serves `http` on 8080 in the release bundle, so the
  ServiceAccount token crosses one in-cluster hop unencrypted, bounded by
  the NetworkPolicy on both ends. The seam the APPLICATION talks to is
  TLS under the plane's own authority; this is the far side, and it is
  Orka's to change rather than ours.
- **The model seam has no per-credential allowlist**, so both Orka
  upstreams — like `ollama` and `copilot` before them — are reachable by
  **every** credential this plane has issued, not only the one `kmx
  migrate` made. What bounds a credential on this seam is its budget, not
  a list of upstreams. That is a property of the seam rather than of this
  command, and it is worth knowing before adding
  `orka-coordinator`: any credential can spend five minutes of somebody
  else's coordinator loop through it.

---

## 9. The same migration on AKS

Run on 2026-09-10 on a cluster in `westus3`: one `Standard_D8s_v5`, Free
control-plane tier, Azure CNI Overlay with Cilium, Kubernetes 1.35.7. Same
Orka `v0.1.3`, same application at `bd2a035`, same model — `qwen2.5:3b` on
an in-cluster Ollama, so that a wall-clock difference is the cluster's and
not a different model's.

**The application was deployed BEFORE Orka**, which is the realistic
adoption order and the one §1 could not show: on kind, Orka was already
there.

### 9a. What it costs to arrive

Every kubectl and helm call carried an explicit `--context`, and so does
every command below. On a shared kubeconfig that is not hygiene, it is the
difference between two lanes and one incident.

**These rows do not add up to the elapsed time, and are not meant to.**
Several overlap — the model was still downloading while the application's
chart was being installed — so the total below is the observed clock, not
the column's sum. Where a row's kind figure is not in this document it is
marked `[c]` and comes from
[the Orka composition report](reviews/2026-09-09-orka-composition.md),
`[8]` means §8 of this document, and the rest are §§1-2.

| step | AKS | kind |
|---|---|---|
| `kmx lift --step cluster` | **4m40s** | 11.9 s for `kind create cluster` `[c]` |
| `kubectl apply` Ollama -> `ollama pull qwen2.5:3b` done | 40 s | — |
| `helm upgrade --install sundae` issued -> concierge Ready | 35 s | 14 s |
| Orka namespace created -> controller rolled out | **61 s** | **43 s** `[c]` |
| Orka `Provider` "local" Ready | not measured (under 6 s) | — |
| `kmx lift --step boundary` | 1m1s | — |
| `kmx lift --step plane` | 1m38s (of which `az acr build` 1m14s) | — |
| **`kmx migrate`** | **40.6 s** | **9.1 s** |
| `kubectl patch` -> rolled out | 6 s | — |
| the migrated question answered | 50.3 s | 22.2 s |
| `kmx lift --step observability` | 6m5s | — |
| second `kmx migrate` | 18.4 s | 20.6 s `[8]` |

**Orka's own install is measured the way its own lane measured it** — from
the first Orka command to `rollout status` returning — and on that footing
it is **61 s against kind's 43 s**. Slower, by about 40%, and unremarkably
so: it is two image pulls from ghcr onto a cold node.

**The trap here is worth naming**, because it nearly went into this table as
"44 s against 43 s — the same". 44 s is what `rollout status` takes on AKS,
and that command was only issued once the pods already existed. Orka's own
lane started its clock at the first Orka command. Two different intervals
wearing the same units, and the shorter one happened to flatter the cluster
being written up.

`kmx migrate` is 4.5x slower on AKS — 40.6 s against 9.1 s — doing exactly
the same work: the same seven steps, the same two files, the same objects.
It was not broken down further, so this document does not say which step
carries the difference, and the repeat-run figures cannot be used to work it
out either: 18.4 s on AKS against 20.6 s on kind, where kind's repeat run
was **slower than kind's own first run** (20.6 s against 9.1 s). Two numbers
that disagree in that direction are measuring conditions rather than work,
and decomposing a 31.5 s gap with them would be arithmetic dressed up as a
finding. The gap is stated and left unattributed.

### 9b. What the application had to be told

Exactly what §2 says, and nothing extra. Four environment variables and one
mounted file, written to `migrations/concierge-patch.yaml` and applied by
one `kubectl patch`. No source edit, no rebuilt image, no chart fork. The
one addition on AKS is not the application's at all: **Orka's `Provider` has
to be authored by hand**, because nothing in this repository emits one —
`kmx migrate` only reads its `.status.ready` and refuses when it is missing.

Before Orka existed, the deployed application was Ready and failing in
1.9 s with `APIConnectionError('Connection error.')`. That is the state an
adopter is actually in when governance arrives beside a workload that
already exists, and it is not the state §1 describes.

Once Orka was there and the application pointed straight at it, §1
reproduced exactly, including the endpoint calling it a success:

```console
$ POST /api/chat  {"session_id":"s1","message":"What flavors do you have?"}
500  AttributeError: 'str' object has no attribute 'metadata'

# Orka's log, at the same second:
INFO api-server request completed {"method":"POST",
  "path":"/openai/v1/responses","status":200,"duration":"5.249173ms"}
```

5.2 ms where kind measured 1.35 ms. Same behaviour, slower static handler.

### 9c. What the ledger recorded

After the patch, the same question, answered the same way §3's was — from
the application's own menu tool, through the translation — though **not with
the same answer**: §3's names five flavours and this one names four, Coffee
absent. Same tool, same shape, a different sample from a 3B model.

```console
$ POST /api/chat  {"session_id":"s3","message":"What flavors do you have?"}
200  {"reply":"... Vanilla Bean, Chocolate, Strawberry, and Mint Chip ...",
      "source":"menu"}
```

50.3 s, `"source":"menu"`, so a tool-calling turn completed through the
translation. And the rows:

```console
$ kmx flow concierge
2026-09-10T01:28:02 concierge model local/qwen2.5:3b 200 0 539 in / 44 out via orka called by claimed "ua:agent-framework-python/1.14.0" from 10.244.0.30
2026-09-10T01:28:32 concierge model local/qwen2.5:3b 200 0 749 in / 107 out via orka called by claimed "ua:agent-framework-python/1.14.0" from 10.244.0.30
```

**539 prompt tokens on the first call — the same number kind measured**, on
every one of the eleven turns §9d ran. That is the portable part, and it is
portable because it is fixed: the same system prompt and the same tool
definitions, counted by the same endpoint under its own protocol. The
follow-up prompt is **not** fixed — it carries the previous completion, so
across this run's turns it was 749 once and 736 four times, against kind's
736. So the honest form of "the meter is portable" is narrower than it
looks: *identical inputs meter identically, and the wall clock does not
follow them* — which is what §6 predicted, and this is the first run in a
position to check it.

Refusals 1-3 of §3 came back byte-for-byte, including Orka's own error
body relayed rather than translated. **§3's fourth — Orka refusing an
unauthenticated caller directly — was not re-run here**, so this section
claims nothing about it. The seam's NetworkPolicy is
load-bearing under Cilium as it is under kind's CNI: with
`kaimahi-proxy-ingress-demo` deleted the probe hung until it was killed at
60 s, and answered `401`/`400` again the moment it was re-applied.

### 9d. What did not work: a turn that fails 5 times in 11

Running the identical question 11 times, **6 answered and 5 returned
`500`**. Every failure is the same, and the ledger names it without needing
the application's log at all:

```
... 200  0 539 in /  41 out via orka     <- the model replied
... 400  0   0 in /   0 out via orka     <- the seam refused the follow-up
```

against a successful turn, which is always a second `200` carrying the
whole conversation — 736 or 749 prompt tokens, every time:

```
... 200  0 539 in /  21 out via orka
... 200  0 736 in / 106 out via orka
```

Every one of the eleven turns opened identically at 539 prompt tokens, and
the first reply's size does not predict which way it goes: 52 completion
tokens went on to answer, 41 did not. Whatever selects the continuation path
is inside the framework, and this lane did not pin it down further than the
four steps below.

The application's log names the field:

```
ChatClientException: ... previous_response_id asks this seam to continue a
conversation it holds no state for — send the whole conversation in `input` instead
```

The chain, read rather than guessed:

1. The seam answers with `"id": "resp_<id>"`
   (`plane/internal/proxy/translate.go:666`) and `"store": false`, which is
   this seam saying it holds nothing to continue from.
2. **The client reads the answer, but not that field of it.**
   `_get_conversation_id` reads `response.conversation` and
   `response.id` — but it decides whether to bother by testing
   `store is False`, and the `store` it tests is `options.get("store")`,
   the option on its **own outgoing request**, never the `store` in our
   reply (`agent_framework_openai/_chat_client.py:925-937`). The
   application sets no `store`, so the value is `None`, the guard does not
   fire, and it falls through to `return response.id`. Our `"store": false`
   is not disbelieved; it is never consulted.
3. That id becomes the conversation id, and because it begins `resp_` the
   next request carries
   `run_options["previous_response_id"] = conversation_id` (`:1443-1446`).
4. The seam refuses it, which is right. When a request carries a
   continuation marker the client deliberately strips server-issued item
   identities from the inline history (`:1650-1658`), so the `input` it
   sends is **not** the whole conversation. Honouring the request anyway
   would answer from a truncated conversation — a wrong answer instead of a
   refusal, which is the trade §4 exists to refuse.

**Whose is it?** Ours and the framework's, in that order of who can fix it
cheaply — and the answer is that neither can, cleanly. It is not AKS's:
nothing in the chain touches the cluster. It is not Orka's: the request
never reaches Orka. §4 accepted `include` because a truthful answer existed;
here it does not, so the refusal stands and this is a stated limit rather
than a bug with a patch behind it.

**Was it happening on kind?** Not established either way, and worth being
careful about rather than tidy. §3's kind ledger does carry two `400` rows
from the same client, which §§4 and 7 attribute to `include` — nothing in
that trail distinguishes an `include` refusal from this one, and the kind
lane ran a handful of turns rather than eleven. Nothing in the chain above
touches the cluster, so there is no *mechanism* by which kind would escape
it; that is an argument, not a measurement, and it is left as one.

What the run does establish is the shape of the mistake to avoid: **an
intermittent failure looks like no failure until somebody runs it eleven
times**, and a demo reported from its successes will not show you one.

### 9e. What `kmx lift` did, and what it did not

**The lift is never run against a real cluster in CI.**
`grep lift .github/workflows/*.yml` returns exactly one hit, and it is a
repository-map claim entry rather than anything that runs the lift. Its Go
unit tests do run — `lift_test.go`, `lift_audit_test.go`,
`lift_audit_linux_test.go` and `lift_observability_test.go`, under the
ordinary `go test` job — so "untested" would be wrong; what no job can do is
create a subscription and watch. This run used four of its eight phases and
found four defects. None blocked the migration; none was fixed mid-run.

**Which phases, and why not the others.** `cluster`, `boundary`, `plane` and
`observability` ran. `kagent`, `agents` and `verify` are kagent-shaped and
this path has no kagent — that is the point of `kmx migrate`. `credential`
checks for `kaimahi-copilot-token` and refuses without it, and an Orka
migration has no use for a Copilot token, so **a full `kmx lift` cannot
complete for this scenario at all** and the phases have to be named one at a
time. Not a bug in the phase; the phase LIST is kagent-and-Copilot shaped,
and nothing says so before you find out.

**The observability phase had never been watched to complete on a cluster.**
A `kubectl` call with no verb made it fail closed on every lift; that was
fixed in #155, which landed *after* #119's live run, and no live run has
happened between then and this one. It works: 6m5s, and it created the Azure
Monitor workspace, the Log
Analytics workspace, Managed Prometheus, Container Insights, the
`NetworkPolicy` that lets the add-on reach the ops port, the `PodMonitor`,
and the workbook. Proven collecting rather than merely applied — the metrics
add-on's target allocator, asked for its own jobs:

```console
$ curl .../jobs/podMonitor%2Fkaimahi%2Fkaimahi-plane%2F0/targets
{"ama-metrics-...":{"targets":[{"targets":["10.244.0.115:9092"],
  "labels":{"__address__":"10.244.0.115:9092",
  "__meta_kubernetes_namespace":"kaimahi", ...,
  "__meta_kubernetes_pod_container_port_name":"ops", ...
```

(`...` marks keys dropped for width; the reply carries the full label set.)

Four defects, all confirmed on the cluster:

- **The lift's plane reports its version as `unknown`, defeating the
  handshake built to identify a remote plane.** `buildFromSource` reads the
  checkout's revision and passes `--build-arg VERSION=<sha>`
  (`internal/kmx/app/plane.go:279-285`). `liftPlane`'s `az acr build` passes
  no build-arg at all (`internal/kmx/app/lift_steps.go:221-234`), so
  `plane/Dockerfile:12`'s `ARG VERSION=unknown` stands. Off the running
  proxy's own ops port:
  `kaimahi_build_info{go_version="go1.26.8",version="unknown"} 1`, and every
  admin call prints `plane unknown (admin contract 2)`, which reads like a
  routine line rather than a handshake failing. It is exactly backwards: on
  kind you can always `git rev-parse` the checkout in front of you.
  **What the unit tests were not catching:** nothing asserts the
  `az acr build` argv — `grep -rn "acr" internal/kmx/app/*_test.go` returns
  two unrelated comment hits. The plane phase's command line is untested.
- **The observability phase prints the subscription id to the terminal.**
  `Options.Banner` states the rule in its own doc comment — "It deliberately
  does not print the subscription id. The id is an identifier this project
  keeps out of terminals and transcripts" — and the phase then echoes
  `--azure-monitor-workspace-resource-id /subscriptions/<GUID>/...` because
  the runner echoes its argv. `scripts/check-no-azure-ids.sh` refuses a
  committed GUID for this exact reason, and the lift prints one on every
  run, into the transcript the operator is most likely to paste.
- **Any lift invocation moves the shared kubeconfig's `current-context`, and
  says so in a line nobody reads.** A resumed `--step` goes through
  `liftCredentials` -> `az aks get-credentials --overwrite-existing`
  (`internal/kmx/app/lift_steps.go:35-39`); the `cluster` phase does the
  same thing from inside `scripts/aks-up.sh`. Either way Azure prints
  `WARNING: Merged "<cluster>" as current context in ~/.kube/config`. Two
  corrections to how this is easy to state wrongly: it is **not** silent,
  and it is **not** per phase — `internal/kmx/app/lift.go:113` calls
  `liftCredentials` once per invocation and skips it when the run starts at
  `cluster`, so a full `cluster`-to-`verify` lift never calls it at all.
  kmx itself is unaffected either way, because it pins `--context`
  everywhere. What is affected is every *other* tool the operator runs
  afterwards, and this is the mechanism that aimed a previous lane's checks
  at the wrong cluster.
- **`--step <phase>` re-prints the full banner, including "creating a
  cluster and everything around it", for phases that create no cluster.**
  Cosmetic, but the banner is the thing an operator is asked to read before
  acting on a subscription.

### 9f. What a second run costs someone who is not us

18.4 s, and it changes nothing it does not have to. Both generated files
reported `Unchanged`, every object `unchanged`, `Credential "concierge"
already issued and kaimahi-concierge-token is bound to it; keeping both`,
and a fresh seam token. The application's own Deployment was not touched and
went on answering at the same rate as before — which, per §9d, is not every
time. The very next turn after this second run was one of the five that
failed on `previous_response_id`, and that is worth stating rather than
implying a clean "and it answered afterwards": the second run neither caused
that nor cured it.

The honest total is the clock, not the column. From the first `kmx lift`
command to the application answering through the governed seam:
**15 minutes 19 seconds** (01:13:12Z to 01:28:31Z). That is less than
§9a's rows add up to, because several of them overlap — the model was still
downloading while the application's chart was going on.

Of those 15 minutes, **4m40s is AKS creating the cluster** before any of
this project's code runs at all, and a further **1m14s is Azure compiling
our image** — `az acr build` uploads a context and builds it in the
registry, so the Dockerfile and the trigger are ours and only the builder is
Azure's; it sits inside `kmx lift --step plane`, not before it. Wiring
Azure-managed observability afterwards cost **6m5s** more, and that one is
Azure's throughout — it ran after the governed answer, so it is time an
adopter spends on monitoring rather than on getting an answer.

### 9g. Which differences are whose

| difference | whose |
|---|---|
| 4m40s to have a cluster at all | **AKS** |
| 1m14s of the plane phase, building the image | **shared** — our Dockerfile and our trigger, Azure's builder (`az acr build`), inside our step |
| 6m5s for the monitoring add-ons | **AKS** |
| seam token 24 h instead of 30 days | **AKS** — the API server caps the `TokenRequest`; kmx asked for 720 h on both |
| `kmx migrate` 40.6 s instead of 9.1 s | **ours** — same work; the whole gap is in what a repeat run skips (§9a), not broken down further |
| plane reports `version="unknown"` | **ours** (§9e) |
| Orka install 61 s against kind's 43 s | **AKS**, and unremarkably — two image pulls onto a cold node, measured the same way on both (§9a) |
| `POST /openai/v1/responses` -> `200` with HTML | **Orka's**, unchanged from §1 |
| the answer takes 50.3 s instead of 22.2 s | **the node's** — same model, same first prompt, CPU inference on a different machine. §6 already warned the wall clock is the cluster's |
| 5 turns in 11 refused on `previous_response_id` | **ours and the framework's** (§9d) — not AKS's, not Orka's |
| the first prompt meters 539 tokens on both | **nobody's** — a fixed input meters identically. The follow-up prompt is not fixed and did not (§9c) |

### 9h. Not measured on AKS

- **Orka's default coordinator** (§§5-6). Re-running it would have cost
  another 16 minutes of a billing cluster to re-demonstrate a conclusion
  §6 already reached, and nothing about it is cluster-shaped.
- **Orka's `main`.** Same as §7: the Role `kmx migrate` creates is inert on
  `v0.1.3`, which is the tag both runs used.
- **A node smaller than `Standard_D8s_v5`.** The lift's own default is
  `Standard_B4ms`, and this run deliberately did not use it: the cluster had
  to carry the plane and its ledger, Orka's two pods, Ollama with a 3B
  model, three application pods and two monitoring add-ons. Whether the
  default would have held is untested, so this document does not say it
  would not.

---

## Teardown

**The kind cluster of §§1-8** is gone:

```console
$ kmx down                     # kind delete cluster --name migrate
Deleting cluster "migrate" ...
Deleted nodes: ["migrate-control-plane"]

$ kind get clusters
No kind clusters found.
$ kubectl config get-contexts -o name | grep migrate
(nothing)
```

kind, one machine, no cloud resources and no spend, so there is nothing
else to prove gone. `kind delete cluster` deletes by CONTAINER name and
never reads the kubeconfig, which is why the line above names the node it
actually removed.

**The AKS cluster of §9** is gone too, and that one has to be proved rather
than asserted, because it was billing. Note the confirmation names the
RESOURCE GROUP here, not the cluster — the opposite of every other guarded
command in this document:

```console
$ KAIMAHI_CONFIRM=<your-rg> kmx lift down \
    --resource-group <your-rg> --cluster <name> --registry <name>
aks-down: resource group '<your-rg>' deleted; kubeconfig entries removed.
kmx lift down: resource group <your-rg> is gone (az group exists says false).
  Cleanup covers that group and the outside resources in this run's record;
  other resources and billing were not checked.
```

and then, independently of what the command claims:

```console
$ az group exists --name <your-rg>                         false
$ az group exists --name MC_<your-rg>_<cluster>_westus3    false
$ az aks list --query "[?name=='<cluster>'].name" -o tsv    (nothing)
$ az acr list --query "[?name=='<registry>'].name" -o tsv   (nothing)
$ kubectl config get-contexts -o name | grep <cluster>      (nothing)
```

The node resource group is checked separately on purpose: it is created by
AKS rather than by us, it is where the disks and the load balancer actually
live, and a run that deleted only the group it made would leave it behind.

**Spend: about US$0.35, computed from published westus3 retail rates rather
than read off an invoice** — billing lags by hours, and this lane ended
before it caught up, so the figure is arithmetic and is labelled as such.
The lane held resources from 01:13:20Z to 01:56:45Z — **43 m 25 s**, and
that is an upper bound, because the clock starts before the cluster existed.
At 0.724 h, one `Standard_D8s_v5` ($0.384/hr) on the Free control-plane tier
($0), with an ACR Basic (~$0.007/hr), the cluster's outbound load balancer
(~$0.025/hr) and its public IP (~$0.004/hr) is **≈ US$0.30**; a 1 GiB
managed disk and 19 minutes of Log Analytics and Managed Prometheus
ingestion take it to roughly **US$0.35**. **No model spend at all**: the
model ran on the cluster's own CPU. The lane's cap was US$5.

The rates are published retail figures rather than anything this run can
evidence, which is the second reason to read the total as an estimate.

---

## See also

- [docs/models.md](models.md) — the model seam, budgets and the price gate
- [docs/tools.md](tools.md) — the tool seam, for the other half
- [docs/foreign-runtime.md](foreign-runtime.md) — what a runtime this
  project did not write has to be told
- [the Orka composition report](reviews/2026-09-09-orka-composition.md) —
  where both obstacles were first measured
