# Inbound: letting the outside world trigger an agent

Everything before this let an agent act on the world (chat through a
governed model, call tools through a governed gateway, post to Slack
after a human approved it). This is the other direction: an external
event, a webhook, triggers a kagent agent. It is the plane's one ingress,
so the rules are stricter than anywhere else in the repo.

The short version: the plane accepts a webhook only on a hook the
committed config names; the caller has to prove it holds that hook's
credential (a signature, preferably); a human has to have approved the
hook, bounded, before any event on it runs; the agent it triggers has to
have budget left; every decision is audited; and an event the plane
cannot record is not honoured.

> This is the door where a **person** is visible: a Slack mention names
> the user who typed it, and the signature the bridge verified is what
> makes that name worth recording. The plane opens a run around the
> agent turn so the ledger and the tool audit can name them too —
> [identity.md](identity.md).

## What runs where

The bridge is a fourth listener in the `kaimahi-proxy` process, port
8082, with its own Service `kaimahi-inbound` in the `kaimahi` namespace.
No new pod. It dials exactly one place: the kagent controller's A2A
endpoint, `http://kagent-controller.kagent:8083/api/a2a/<namespace>/<agent>/`.
The agent then thinks through its governed model preset, so the spend
the event causes lands in the ledger under the agent's credential, the
same way a `make chat` does.

On kind the Service is a ClusterIP and the demo reaches it through a
port-forward; nothing on a kind cluster is reachable from the internet.
On AKS there is an opt-in public edge for exactly one hook, described in
[Putting it on the internet](#putting-it-on-the-internet). `make plane`
never creates it.

## Hooks

Hooks live in the committed upstream table, `k8s/plane/upstreams.yaml`,
under `inbound_hooks`. Four ship:

| hook | proof | triggers | notes |
|---|---|---|---|
| `demo` | Kaimahi signed webhook (`kaimahi-hmac`) | `hello-world` | the generic primitive; CI drives it end to end |
| `demo-bearer` | bearer token (`bearer`) | `hello-world` | for a source that can set a header but cannot sign |
| `slack-events` | Slack request signing (`slack`) | `hello-slack` | the one named source; `app_mention` only, from one channel; also carries the approval commands (`approve`/`deny`) from listed approvers (asserted keyless in CI; live verification pending); live-verified on AKS as the loop, see below |
| `slack-tools` | Slack request signing (`slack`) | `hello-tools` | the same Slack source pointed at the tool-using agent, so a mention can drive a governed tool call; its own signing secret and its own `tool_credential` |

Each hook names the plane credential it is bound to, how the caller
proves it, the agent it triggers, and `budget_credential`: the credential
the agent's governed preset carries (`hello-world`, issued by `make govern`,
for every agent in this repo). A hook the config does not name does not
exist. The plane answers 401 and does nothing else.

Per-hook bounds, with defaults: `max_body_bytes` (64 KiB),
`rate_per_minute` (60) and `burst` (10). Over the body limit is a 413.
Over the rate is a 429 before the plane reads anything. The rate limiter
runs before authentication on purpose: every later refusal writes an
audit row, and the bucket is what stops an unauthenticated flood from
turning into a database flood. The trade is that a flood starves its
hook rather than the plane. The limiter is per replica on purpose: it
is a flood guard, not a governance decision, and a bucket shared
through the database would be a store write per event — exactly the
amplification it exists to bound. With the plane's two replicas the
effective ceiling is therefore 2 × `rate_per_minute` (N × for N
replicas). The invocation queue is per replica and bounded too. Every
limit that IS a governance decision — the target's budget, the grant
use, the replay guard — is decided in Postgres and is exact across
replicas ([operations.md](operations.md)).

## How a caller proves itself

Three modes. Signing is the one to use whenever the source can sign.

**`kaimahi-hmac`**, the generic signed webhook. The caller sends three
headers:

```
X-Kaimahi-Delivery:  <a unique delivery id, [A-Za-z0-9._:-]{1,128}>
X-Kaimahi-Timestamp: <unix seconds when it signed>
X-Kaimahi-Signature: v1=<hex HMAC-SHA256 over "v1:<timestamp>:<delivery>:<body>">
```

The secret is shared between the caller and the plane, and only those
two. The delivery id is inside the signed string, so a captured request
cannot be replayed under a fresh id. Timestamps more than five minutes
from the plane's clock are refused. A bad, missing, or stale signature is
one answer, 401, so a forger learns nothing from which.

**`slack`**, Slack's Events API request signing: `X-Slack-Request-Timestamp`
and `X-Slack-Signature: v0=<hex HMAC-SHA256 over "v0:<timestamp>:<body>">`,
same five-minute window. The delivery id is the envelope's `event_id`.
Slack's `url_verification` handshake is answered with the challenge
after the signature checks, audited as `challenge`, and triggers nothing.
What happens after the signature is Slack-specific and is described in
[Slack Events: the loop](#slack-events-the-loop).

**`bearer`**: `Authorization: Bearer kmh_...`, the hook credential's own
token, plus `X-Kaimahi-Delivery`. It is the same `kmh_` credential the
proxy and gateway use, stored by sha256 only. Weaker than a signature
because the proof travels with every request; present so a source that
cannot sign still has a path. A real credential that is not the hook's
gets 403, not 401.

On the generic hooks the event text is the JSON body's `text` field
when the body is a JSON object that has one, otherwise the whole body
as text; it becomes the agent's prompt as-is. A body that is not UTF-8,
or whose text is empty after trimming (an empty `text`, an empty body),
is a 400. On the Slack hook, anything but the two envelope shapes above
is a 400 too, and the inner event is mapped rather than pasted (below).

## Approval: a hook is granted, bounded, or it does not run

Triggering an agent from outside is consequential, so it is an
approvable action in the [approvals](approvals.md) sense, not something
a credential alone opens. An event on a hook with no live grant is
denied with 403 and files a pending request (kind `inbound`, subject the
hook name, deduped while pending). A human approves it with bounds:

```
make approvals
make approve ID=<uuid> USES=100 TTL=24h
```

Each admitted event consumes one use. When the grant expires or is
spent, the next event is denied again and a fresh request is filed. That
is the event-count lever an ingress needs most, and it comes from the
existing permit machinery unchanged: a grant with no bound is still
refused, and liveness is evaluated in SQL at admission time.

Why not gate on credential and budget alone? Because a webhook exists to
run without a human in the loop, and the honest way to give it that is a
bounded allowance a human set, not an open door. Per-event approval was
the other option and it does not fit the deny-and-retry model: a source
does not retry on our schedule, so the event would be lost between the
denial and the approval. Bounded grants keep the human's decision and let
the source deliver.

The two things an event's agent might do that need their own approval
(a tool outside its allowlist, a Slack post) are still gated where they
already were. The inbound grant admits the trigger, nothing more.

## Budget: the door checks before the agent spends

Every inbound event causes spend, so before admitting one the plane
previews the target's budget: the credential named in `budget_credential`,
its month-to-date usage, its caps, and the headroom any live budget grant
adds. If the proxy would deny the agent's next call, the door denies the
event instead, with 429, and files the agent's budget request (the same
one the proxy would file; they dedupe). If the plane cannot read spend at
all, that is a 503, not a refusal: an ingress caller has to be able to
tell "no" from "try later". The preview consumes nothing. The proxy stays
the place that consumes a budget grant use, so one event never burns two.

A target whose credential does not exist is not governed, and an
ungoverned agent cannot be triggered from outside (403). The plane cannot
verify that `budget_credential` really is the credential the agent's
preset mounts; the proxy enforces regardless, so a wrong name only makes
the door check the wrong budget, never lets spend through ungoverned.

## Replay and the audit trail

Every decision about an attributable event is a row in `inbound_audit`,
append-only like the ledger and the tool audit:

```
make inbound-audit            # every hook
make inbound-audit HOOK=demo  # one hook
```

```
created (UTC)       hook   credential     delivery        decision  status   in  out detail                                        acted for
2026-09-01T21:25:17 demo   inbound-demo   live-7f640710   completed    200  368    3 task 348f6222-...
2026-09-01T21:25:09 demo   inbound-demo   live-7f640710   denied       409    0    0 replay: delivery already admitted
2026-09-01T21:25:07 demo   inbound-demo   live-7f640710   admitted     202    0    0 granted eac9d995-...
2026-09-01T21:25:02 demo   inbound-demo   probe-400d34b0  denied       429    0    0 target budget: monthly token budget reached; approval request filed
2026-09-01T21:24:12 demo   inbound-demo   probe-11ac4068  denied       403    0    0 inbound trigger not permitted: no live grant for hook demo; approval request filed
2026-09-01T21:24:10 demo   inbound-demo                   denied       401    0    0 unauthorized
```

The `admitted` row is the replay guard. Its (hook, delivery id) is unique
among admitted rows, and it is inserted in the same transaction that
consumes the grant use, row first. So a replay of an admitted delivery
hits the index before any use is burned, and nothing is admitted without
a row. A delivery that was denied stays retryable on purpose: a source
retrying after "no grant yet" is delivering, not replaying.

Admitted events run asynchronously. The caller gets a 202 with the event
id, a worker sends A2A `message/send`, and when the agent answers a
second row lands: `completed` (HTTP 200, the task id, and the token counts
the agent runtime reported for that invocation) or `failed` (why). The
token counts are attribution, not billing: the proxy priced and ledgered
the same call under the agent's credential, and the two agree (that row
above and the ledger row for it both say 368 in, 3 out). An agent turn
is bounded at five minutes and the queue holds sixteen events for two
workers, so an `admitted` row can legitimately wait a while; one that
never gets an outcome means the plane restarted with it queued or in
flight. The queue is not durable, and a restart says so this way.

If the audit trail cannot be written, every event is refused with 503
until a write succeeds. The refusal's own record attempt is the recovery
probe. This is the same rule the ledger and the tool audit run under.

## Running the demo

Assumes `make up` and `make plane` are done and `make govern` has issued
`hello-world`'s credential (the door needs it).

```
make inbound-credential                  # the demo hook's identity; its bearer token is discarded
make inbound-secret HOOK=demo GENERATE=1 # a signing secret, stored plane-side
make inbound-fire                        # 403: no grant yet, request filed
make approvals
make approve ID=<uuid> USES=2 TTL=10m
make inbound-fire                        # 202: admitted
make inbound-audit                       # wait for the completed row (tens of seconds on ollama)
make inbound-fire                        # 202: second use
make inbound-fire                        # 403: spent; a fresh request filed
```

`make inbound-fire` runs `scripts/inbound-probe.sh`, which signs the
event with the key from the `kaimahi-inbound-signing` Secret and passes
only when the plane answers exactly what it expected (`EXPECT=202` by
default). Useful knobs: `EVENT='...'` for the text, `DELIVERY=<id>` to
resend the same delivery (a replay, `EXPECT=409`), `AUTH=none` for an
unauthenticated event (`EXPECT=401`), `AUTH=forged` for a wrong key.

The first `make inbound-secret` on a fresh plane can take kubelet a
minute or two to project into the pod; until then the hook answers 503
"hook signing secret unavailable". That is the fail-closed answer, not a
bug: a signed hook with no secret refuses rather than accepting unsigned.

For a caller of your own: give it the hook's key
(`kubectl -n kaimahi get secret kaimahi-inbound-signing -o jsonpath='{.data.demo}' | base64 -d`)
and have it sign as above. For a source that has its own signing secret
(Slack does), paste that secret instead: `make inbound-secret HOOK=slack-events`
reads it from stdin.

For the bearer hook: `make inbound-credential CRED_INBOUND=inbound-bearer INBOUND_SECRET=kaimahi-inbound-token`
stores the token as a Secret in the `kaimahi` namespace, and
`make inbound-fire HOOK=demo-bearer AUTH=bearer` uses it.

## Slack Events: the loop

A message in Slack triggers the agent; the agent answers back into the
same Slack thread through the governed path. Both directions are
governed, and the loop closes only because two humans said yes: one
approval for the hook to fire at all, one for the bot to post.

```text
  Slack workspace                  internet          AKS, namespace kaimahi
  ┌──────────────────┐                             ┌────────────────────────┐
  │ @bot what is...  │ ── app_mention ──▶ :443 ──▶ │ edge (Caddy, TLS) ─────┼─▶ :8082 bridge
  │  (private test   │   signed, event_id          │   POST /hook/slack-    │      signature, window,
  │   channel)       │                             │   events only          │      channel allowlist,
  │                  │                             └────────────────────────┘      grant, budget, audit
  │                  │                                                                  │ A2A
  │ ┌──────────────┐ │                                                                  ▼
  │ │ bot: answer  │◀┼── chat.postMessage ◀── Slack MCP server ◀── gateway ◀── hello-slack (governed Copilot)
  │ └──────────────┘ │       (443 out)         (proxy-only ingress)  allowlist + tool grant + audit
  └──────────────────┘
```

**What triggers.** Only an `app_mention` written by a human. Everything
else a subscription can deliver — a plain `message` (which is what the
bot's own reply is once it lands in the channel), anything with a
`bot_id` or a `subtype`, a mention with no words — is acknowledged to
Slack with a 200, recorded in the audit trail as `ignored` with the
reason, and does nothing: no budget check, no grant use, no agent. That
is the loop guard, in one place. Subscribe the app to `app_mention`
only; the plane is written so that subscribing to more cannot make it
do more, but there is no reason to test that in production.

**From which channel.** The hook's `slack_channels_file` names a file
mounted from the Slack bot Secret: the same `SLACK_MCP_ADD_MESSAGE_TOOL`
value `make slack-secret` vets (private, bot is a member) and the MCP
server uses to restrict posting. A mention anywhere else is a 403,
audited. The file being unreadable or empty, or holding the server's
`true` ("post anywhere"), is a 503: the hook refuses rather than widen
to the workspace. The file is required for a `slack` hook: the config
is refused without it. No channel ID is in the repo; the committed table
names the file.

**What the agent is asked.** The mention becomes a task, not a pasted
message. The `<@bot>` tokens are stripped; the user's words are quoted
as data ("Their message was: …"), so a Slack message is a question to
answer, never an instruction to the plane; and the reply's destination
is named once, in the shape the posting tool takes: `channel_id` and
`thread_ts` (the mention's own `ts`, so a top-level mention gets its
answer in a thread under it, or the thread it was already in). Which
tools the agent may then call is not decided here: posting is admitted
by the gateway's allowlist plus a live tool grant, or the agent is told
it was denied and says so.

**What Slack is told.** Slack retries any event it did not get a 2xx for
within three seconds, up to three times, and counts failures towards
disabling the subscription. The bridge answers admitted events 202 in
milliseconds (the agent runs afterwards). A refusal a retry could not
change — no grant (403), wrong channel (403), replay of an admitted
event (409), malformed (400) — carries
`X-Slack-No-Retry: 1`. A 429 (the hook's rate limit, or the target
agent's budget) and a 503 (audit trail down, queue full, secret not
projected yet) carry nothing, so Slack retries them, which is what a
"not now" is for. After an event is admitted, its `event_id` is in the
audit index; a late retry is a 409 and burns nothing.

**Two approvals, both bounded.** The hook needs a live `inbound` grant
(`make approvals` shows the request the first mention files; `make
approve ID=… USES=… TTL=…`). The reply needs a live *tool* grant for
`conversations_add_message` on the `hello-slack` credential — the same
approval the governed Slack path uses, exactly as
[slack.md](slack.md#the-demo) walks through it,
including the rediscover-and-restart so the agent can see the tool. A
mention with the first grant and not the second runs the agent, which
has **no posting tool in its hands** (the gateway projects only the
allowlist onto `tools/list`, so kagent never discovered it), says it
cannot post, and stops: the inbound audit shows
`completed`, nothing lands in the channel, and the Slack audit shows no
call at all. A `denied 403` row appears there only when discovery is
stale (a grant that just expired, before kagent's next reconcile) or a
direct MCP client tries. Either way the second gate held.

**Both approvals can be given from Slack.** The same hook carries a
second verb: a mention whose words are `approve <id> [uses=N] [ttl=D] [amount=N]`
or `deny <id>` is an approval command, recognised after the signature
and the channel allowlist and *before* the grant gate, so deciding a
request needs no inbound grant and never runs the agent. Only a Slack
user in the hook's `slack_approvers_file` (a Secret-mounted list, `make
slack-approvers`; membership of the channel is not enough) may give one:
anyone else is refused 403 and audited, and a missing or empty list
fails commands closed (503) while questions keep working. The plane
also announces every filed request in the channel, through this same
posting path under its own credential (`make notify-slack`), so the
denial that filed the `inbound` request above shows up in the channel
with the command to type. The whole model, the defaults (one use,
fifteen minutes when the command names no bounds) and the identity the
grant records (`slack:<user id>`) are in
[approvals.md](approvals.md#deciding-from-slack). `make inbound-audit
HOOK=slack-events` shows commands as `command 200` rows with the outcome
in the detail, next to the mentions that ran the agent.

### Run it

On an AKS cluster with the plane, the Copilot Secret and
[slack.md](slack.md)'s `make slack-secret`, `make slack-mcp` and
`make govern-slack` in place:

```sh
TARGET=aks make inbound-credential CRED_INBOUND=inbound-slack   # the hook's identity
TARGET=aks make inbound-secret HOOK=slack-events                # paste the app's Signing Secret (stdin)
TARGET=aks make slack-approvers                                 # paste the approvers' Slack user ids (stdin)
TARGET=aks make notify-slack                                    # the plane's own posting credential
TARGET=aks make inbound-expose KAIMAHI_DNS_LABEL=<unique-label> # prints the Request URL
TARGET=aks make exposure-scan                                   # exactly one port, one IP
```

Then in the Slack app (api.slack.com/apps → your app): **Event
Subscriptions** → enable → Request URL `https://<label>.<region>.cloudapp.azure.com/hook/slack-events`
(Slack sends the challenge; the plane answers it once the Secret is
projected, and `make inbound-audit` shows the `challenge 200` row) →
**Subscribe to bot events** → `app_mention` only → save. The bot needs
the `app_mentions:read` scope (add it under OAuth & Permissions and
reinstall the app; the token does not change). Mention the bot in the
private test channel:

```sh
make inbound-audit HOOK=slack-events   # denied 403, request filed — and announced in the channel
# in the channel: @kaimahi approve <id> uses=3 ttl=30m   (or: make approve ID=<uuid> USES=3 TTL=30m)
make grants                            # decided by slack:U…
# mention again → admitted 202 → completed, with the agent's token counts
make ledger                            # the governed Copilot turn the mention caused
make slack-audit                       # the post: denied until the tool grant, then allowed 200 granted <id>
make tool-audit CRED_TOOLS=kaimahi-plane   # the plane's own announcements and replies, audited
```

### What the live run proved, and what CI proves

Verified live on 2026-09-02 on an AKS cluster created for it and
deleted the same day (Kubernetes 1.35.7, Cilium 1.18; the redacted
transcript is in the PR that shipped this):

- the edge came up with a **Let's Encrypt** certificate by TLS-ALPN-01,
  and `make exposure-scan` found exactly **one open port (443) on one
  public IP**, nothing on the cluster's other public IP, and one
  LoadBalancer Service in the cluster;
- Slack's **challenge** arrived signed and was answered (`challenge 200`)
  — after first being refused 401 because the wrong 35-character value
  had been pasted as the signing secret, which is the refusal working;
- a real `app_mention` in the private channel was **refused 403** with
  `X-Slack-No-Retry` and filed the `inbound` request; approved
  `USES=3 TTL=30m`; the next mention was **admitted 202** in 6 ms,
  ran the agent on governed Copilot (two ledger rows, 2,901 tokens, the
  outcome row `completed` with the agent's token counts), and the reply
  landed **in the thread under the mention**, through the gateway under
  a tool grant (`allowed 200 granted <id>`, then `1/2` uses);
- the bot's own reply produced no event (only `app_mention` is
  subscribed), so the loop guard was not even exercised in production;
- `make netpol-verify` with the edge policies present: boundary
  enforced as written;
- teardown: the app's Request URL and event subscription removed through
  the manifest API before the edge went, then `make aks-down`, re-checked
  gone.

One finding worth knowing before you try it: with **Socket Mode
enabled** on the app, Slack delivers events over its WebSocket and
never posts to the Request URL, while the URL still verifies. The
symptom is a verified URL, a correct subscription, and no events. Check
Socket Mode first.


CI cannot reach Slack and Slack cannot reach a kind cluster, so what CI
proves is by unit test: the challenge handshake, the v0 signature and
window, the `app_mention`-only rule and the bot/self guard, the channel
allowlist failing closed, the mention → task mapping (stripped mention,
quoted text, `channel_id` + `thread_ts`), the `X-Slack-No-Retry`
policy, and replay by `event_id`. The generic signed hook is still what
CI drives end to end on kind. The approval commands are driven end to
end on kind too, with synthetic signed mentions (`make slack-mention`,
synthetic user ids): a non-approver refused and audited, an approver's
command minting a grant in their name that the gateway then honours,
the same command again answered "already decided", a denial, and the
plane's own announcement admitted through the gateway (an `allowed 502`
row under `kaimahi-plane`, the upstream being absent there). Only the
live run proves Slack's real signatures, the challenge over a real
Request URL, the retry behaviour of a real Slack, the certificate, the
reply landing, the announcement landing, and a person typing the
command.

## Putting it on the internet

`make inbound-expose` (TARGET=aks only) applies `k8s/inbound-edge.yaml`:
one Caddy pod in the plane's namespace, terminating TLS with a Let's
Encrypt certificate it obtains by **TLS-ALPN-01**, behind an Azure
LoadBalancer whose public IP carries a DNS label the operator chooses
(`<label>.<region>.cloudapp.azure.com`). It forwards exactly
`POST /hook/slack-events` to the bridge's Service; everything else is a
404 that never reaches the plane. The manifest is rendered with the
label and FQDN at apply time and the render refuses to apply with a
token unfilled; neither identifier is committed, and
`scripts/check-no-azure-ids.sh` refuses a literal `cloudapp.azure.com`
name or a public IP anywhere in the tree.

Why this and not an ingress controller with cert-manager, or an Azure
front door: TLS-ALPN-01 is answered on 443 itself, so there is **one
public port** — no port 80 for an HTTP-01 solver or a redirect. There
are no CRDs, no cluster-wide controller, no second namespace; the edge is
one pod under the same default-deny as the rest of the plane, and its
whole reach is written in its policy: the internet on 8443 in, DNS, the
bridge on 8082 and 443-out for the ACME directory. The private key is
generated in the pod and stored on a PVC only that pod mounts; no
certificate or key ever exists on an operator's disk or in git. A front
door would still leave a public origin to lock down; an ingress
controller answers 80 and 443 for the whole cluster.

**What is exposed.** One IP, one port (443), one path, one method. The
proof is `make exposure-scan`: every public IP in the cluster's node
resource group, enumerated through the Azure API, connect-scanned on all
65535 TCP ports from outside the cluster. The edge must answer on
exactly 443 (the positive control — a scan that sees nothing is broken,
not safe) and every other public IP (AKS's outbound SNAT address) on
nothing; and, cluster-side, the only `LoadBalancer` Service in any
namespace must be the edge. IPs are masked in the output.

**What is not.** The proxy's data ports (8080, 8081), the admin port
(9091), Postgres, the MCP servers and the kagent API stay
cluster-internal; the edge cannot reach them (its egress names the
bridge only) and nothing outside can reach the edge on anything but the
TLS port. Being reachable admits nothing: the bridge's gates are
unchanged behind the edge.

**Tear it down.** `make inbound-unexpose` deletes the edge, its Service
and public IP, the certificate's volume and the policy allowance;
`make aks-down` deletes everything. Either way the DNS label is released
and **anyone can claim it next**, so remove the Request URL (or disable
Event Subscriptions) in the Slack app *before or as* the edge goes: an
app that still points at a freed name would send signed events —
harmless to anyone without the signing secret, but still your
workspace's traffic — to whoever owns the name later.

## What this does not do

- No durable queue: the invocation queue is per replica and bounded,
  so admitted-but-not-yet-run events die with that replica (a rolling
  restart drains them first; a crash does not), and their `admitted`
  rows without an outcome row say so.
- The rate limiter is per replica, in memory: the ceiling is N × the
  configured rate for N replicas ([operations.md](operations.md)).
- Sessions on the kagent side are attributed to the hook
  (`x-user-id: kaimahi-inbound/<hook>`), not to whoever sent the event.
- The plane's egress to the kagent controller on 8083 is allowed
  explicitly in `k8s/plane/network-policy.yaml` (the [egress](egress.md)
  boundary). No agent *pod* is reachable from the plane; the one other
  thing in the kagent namespace that is, is kagent's tool server on 8084,
  which the gateway reaches as a tool upstream.
- No public exposure on kind, and none by default on AKS: the edge is an
  opt-in step (`make inbound-expose`) that exists for the one Slack hook,
  and `make plane` never creates it. On kind the port-forward path is
  not subject to the policy.
- Socket Mode, the alternative that needs no public URL, would mean a
  long-lived WebSocket client in the plane and a second token to keep.
  Left out deliberately.
