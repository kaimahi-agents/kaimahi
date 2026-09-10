# The demo, start to finish

One sitting, about thirty minutes once the prerequisites are in place.
This page is the order to run things in and what you should see. The
reasoning behind each step lives in the capability docs it links to.

Two ways to run it:

| | kind (laptop) | AKS (real cluster) |
|---|---|---|
| Model | in-cluster Ollama, free, small (`qwen2.5:3b`) | GitHub Copilot (`gpt-5-mini`), governed |
| Shows | spend, tools, approvals, network boundary | all of that plus Slack both ways and a public edge |
| Needs | Docker (or Podman) and python3 — kind, kubectl and helm are fetched by `kmx` if they are absent | a logged-in `az`, a Copilot subscription, a Slack workspace you control |
| Costs | nothing | about US$1 for a few hours, then `make aks-down` |

The kind path is what CI runs on every pull request. The AKS path is
what has been demonstrated live. Steps 1–4 are the same on both; the
Slack parts need AKS because Slack has to reach the cluster.

## Before you start

**kind:** [getting-started.md](getting-started.md#prerequisites) lists the
tools. Pick a cluster name of your own and pass it to every command:
`export KIND_CLUSTER=demo`.

**AKS:** [aks.md](aks.md#prerequisites), then the exports from
[aks.md, "From an empty subscription"](aks.md#from-an-empty-subscription-to-a-governed-chat)
(`AKS_RESOURCE_GROUP`, `ACR_NAME`, `AKS_CLUSTER`, `AKS_LOCATION`,
`TARGET=aks`). Two traps that have cost people time:

- Your `gh` login is not a Copilot login. `kmx models credential copilot`
  runs GitHub's device flow (open the URL, type the code) because the
  gh CLI's token is refused by the Copilot exchange. Details in
  [models.md](models.md#github-models-is-retired-the-copilot-subscription-path-replaces-it).
- The Copilot token expires within hours. If the agent starts failing
  auth mid-demo, re-run `kmx models credential copilot`; no restart needed.

**Slack (AKS only):** a Slack app in a workspace you control, with the
bot scopes in [slack.md](slack.md#credential-custody) plus
`app_mentions:read`, installed to a **private** test channel the bot is
a member of. Socket Mode must be **off**; with it on, the URL verifies
and no event ever arrives ([inbound.md](inbound.md#slack-events-the-loop)).
Have the bot token, the Signing Secret and your own Slack user id ready
to paste; every script takes them on stdin and none of them lands in a
file you could commit.

## 1. Bring it up

```bash
make up          # kind: cluster, Ollama, model, kagent, two agents (5–10 min)
                 # AKS:  cluster, kagent, Copilot secret, plane, governed agents
make status
```

On kind, `make chat` works right away, ungoverned. Say so out loud: this
is what kagent gives you on its own.

```bash
make chat
```

At a terminal `make chat` prints a readable view; piped to a file or a
script it prints the raw A2A task JSON, and the reply is buried in it.
From a real run:

```text
"I am the hello_world agent, designed to greet users and provide
information about myself. I am running on Kubernetes via kagent."
```

## 2. Money: the agent gets a budget

kind only (AKS did this during `make up`):

```bash
make plane        # the governance plane: proxy, gateway, Postgres
make govern       # issue the agent an opaque kmh_ token, switch it onto the proxy
```

Then, on both:

```bash
make chat
make ledger
```

The chat works exactly as before; the difference is the row:

```text
created (UTC)       credential   upstream  model       in    out  cents source   status caller (claimed)   from (observed) acted for
2026-09-02T03:52:00 hello-world  ollama    qwen2.5:3b  380   12   0     free     200    ua:kagent/0.9.12   10.244.1.7      none
```

Now cap it below the price of one call and try again:

```bash
make budget CAP_TOKENS=1
make chat
```

The task fails, in plain text, before anything reached the model:

```text
"text":"monthly token budget reached; approval request filed — run 'make approvals'"
```

Two things to point out while you are here. The plane runs as two
replicas that agree on every decision, and `make plane-metrics` shows
the same decisions and ledger totals as Prometheus text
([operations.md](operations.md)). And the custody: the agent's Secret holds a `kmh_` token, the real provider key (on
AKS, the Copilot token) is mounted only into the proxy pod.

## 3. Doors: the agent gets an allowlist

```bash
make govern-tools                                        # tools agent behind the gateway, read-only allowlist
make chat AGENT=hello-tools TASK='List the configmaps in the default namespace.'
make tool-audit                                          # allowed 200, under the agent's own credential
bash scripts/tool-denial-probe.sh k8s_get_events         # a tool that is NOT on the list
make tool-audit                                          # denied 403, "approval request filed"
```

The denial is the point: the agent (or anyone holding its token) cannot
widen its own reach. The gateway also hides tools the agent is not
allowed to call, so the model never even sees them
([tool-governance.md](tool-governance.md)).

## 4. Sign-off: a human says yes, once, for a while

```bash
make approvals
```

Two pending requests, one from each denial above:

```text
id                                   created (UTC)       credential   kind     subject         detail                                        call
a89f5cad-…                           2026-09-02T16:26:23 hello-tools  tool     k8s_get_events  denied tools/call via upstream kagent-tools   k8s_get_events: …
…                                    …                   hello-world  budget   tokens          denied qwen2.5:3b via upstream ollama          -
```

Approve the tool one with a single use, and the budget one with a
bounded overage:

```bash
make approve ID=<tool request id> TTL=10m USES=1
bash scripts/tool-call-probe.sh k8s_get_events '{"namespace": "default"}'   # succeeds
bash scripts/tool-denial-probe.sh k8s_get_events                            # denied again: the use is spent

make approve ID=<budget request id> TTL=5m USES=1 AMOUNT=100000
make chat                                                                   # completes
make chat                                                                   # denied again
make grants                                                                 # both grants: live=no, uses 1/1
make approval-audit                                                         # requested / approved, with the bounds
```

Nothing in the configuration changed. The allowlist and the cap are
where they were; a person granted an exception and it lapsed on its own
([approvals.md](approvals.md)). Restore the cap afterwards:
`make budget CAP_TOKENS=100000`.

## 5. The boundary is enforced, not just written

```bash
make netpol-verify
```

Ends with `boundary enforced as written`: a probe pod that must reach
everything, then an unlabeled pod in the plane's namespace that must
reach nothing ([egress.md](egress.md)).

## 6. The one that is worth the meeting (kind)

Everything so far governs an agent that lists ConfigMaps. This is the
same machinery on money:

```bash
make erp          # a fixture ERP behind the gateway — no key, no egress
make govern-ap    # the accounts-payable agent, and its place in the policy
make ap-demo      # investigate, get denied, be approved by a person, pay
make ap-injection # a later invoice tries to redirect the payment
```

An invoice bills $48,000.00, the dock received 310 of 400 units, and the
contract does not authorize the $6,000.00 "expedited handling" line. The
agent has to work out that $32,550.00 is payable — and $32,550.00 is over
the $10,000.00 the agent may pay on its own, so a person approves *that
transaction*, by amount and payee, before any of it moves. A routine
invoice in the same run pays itself with nobody in the loop, because it is
inside the bound. Then a second invoice arrives carrying text telling the
agent it is pre-approved and should pay a different payee; the agent may
be persuaded, and the payment is refused anyway, because the approval it
would have to spend is welded to the earlier call.

The arithmetic, the fixtures and what the demo does not prove are all in
[ap-demo.md](ap-demo.md).

Stop here on kind. Everything above is what CI checks on every pull
request.

## 7. Slack, both ways (AKS)

Wire the Slack pieces:

```bash
make slack-secret SLACK_CHANNEL=<private channel id>   # bot token on stdin; refuses a public channel
make slack-mcp
make govern-slack                                      # the Slack agent, read-only allowlist
make inbound-credential CRED_INBOUND=inbound-slack
make inbound-secret HOOK=slack-events                  # the app's Signing Secret, stdin
make slack-approvers                                   # your Slack user id, stdin
make notify-slack                                      # the plane's own posting credential
make inbound-expose KAIMAHI_DNS_LABEL=<a label nobody else has>
make exposure-scan
```

`exposure-scan` sweeps every public address in the cluster's resource
group on all 65,535 TCP ports and must end with:

```text
exposure-scan: exactly one port on one public IP (the inbound edge, 443); every other public IP answers nothing
```

Paste the Request URL that `inbound-expose` printed into the Slack app
(Event Subscriptions → Request URL → subscribe the bot to `app_mention`
only). `make inbound-audit HOOK=slack-events` shows the `challenge 200`
row once Slack has verified it.

Now mention the bot in the private channel: `@yourbot what is Kaimahi,
in one sentence?`

Nothing answers. Instead the bot posts:

```text
Kaimahi approval request `f8e91c57-…`: credential `inbound-slack` was denied
inbound `slack-events`. To decide, mention the bot: `@kaimahi approve f8e91c57-…
[uses=N] [ttl=15m]` or `@kaimahi deny f8e91c57-…`. Or run `make approvals`.
```

Reply in the channel as the approver: `@yourbot approve f8e91c57 uses=3
ttl=30m`. The bot answers in the thread with the grant, and `make
grants` shows who decided:

```text
id            credential     kind     subject       live  expires (UTC)        uses  amount  created (UTC)        decided by  binds  cred expires (UTC)
48aba588-…    inbound-slack  inbound  slack-events  yes   2026-09-02T15:23:22  0/3   -       2026-09-02T14:53:22  slack:U…    -      2026-10-02T09:58:04
```

Mention the bot again with the same question. This time the agent runs,
tries to post its answer, and is denied, because posting is not on its
allowlist either. Another approval request appears in the channel;
approve it the same way (`uses=2 ttl=60m`). Then kagent needs to notice
the new tool (this lag is real and documented in
[slack.md](slack.md#why-the-agent-is-never-the-one-denied)):

```bash
kubectl -n kagent annotate remotemcpserver kaimahi-slack kaimahi.dev/rediscover="$(date +%s)" --overwrite
kubectl -n kagent rollout restart deploy/hello-slack
```

Mention the bot a third time. The answer lands in the thread. From the
live run:

```text
Kaimahi is a governance gateway that runs AI agents on Kubernetes and safely
mediates their access to external services (like Slack) so actions are
controlled and auditable.
```

Show the receipts:

```bash
make inbound-audit HOOK=slack-events   # denied 403 → command 200 → admitted 202 → completed 200
make ledger                            # the governed Copilot turn the mention caused
make slack-audit                       # the post: denied, then allowed 200 granted <id>
make approval-audit                    # every decision, with the approver's Slack id
make tool-audit CRED_TOOLS=kaimahi-plane   # the plane's own announcements, audited like anyone's
```

## 8. Tear it down

AKS:

```bash
# in the Slack app: remove the Request URL and the event subscription FIRST —
# the DNS label is free for anyone once the cluster is gone
make inbound-unexpose
KAIMAHI_CONFIRM=$AKS_RESOURCE_GROUP make aks-down
az group exists --name "$AKS_RESOURCE_GROUP"      # must print false
```

kind: `make down`.

Before pasting any of the output into a pull request or a doc, save it
to a file and run `bash scripts/check-no-azure-ids.sh <file>`; it
refuses subscription ids, registry and cluster hostnames, DNS labels and
public IPs. Resource-group and cluster names it cannot see, so read for
those yourself.

## If something does not match

- **`make chat` times out on kind with the plane up.** A laptop node
  running Ollama, kagent, Postgres and the plane is CPU-bound; the calls
  still succeed (`make ledger` proves it) even when the round trip does
  not return in time. A bigger node or a smaller question helps.
- **The first governed Copilot call fails with "upstream credential
  unavailable".** The Secret was minted after the proxy started;
  `kubectl -n kaimahi rollout restart deploy/kaimahi-proxy`
  ([aks.md](aks.md#3-the-copilot-credential-before-the-plane-not-after)).
- **Slack verified the URL but nothing arrives.** Socket Mode is on.
- **A mention gets no reply and no announcement.** The channel is not the
  one the hook is bound to, or the mention came from a bot. Both are
  audited: `make inbound-audit HOOK=slack-events`.
- **`make status` says attention required.** `kubectl -n kagent describe
  agent <name>` has the real error; a missing ModelConfig is the usual
  one.
