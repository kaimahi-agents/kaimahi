# Slack: posting as an approved action

Assumes the governance plane is deployed ([spend.md](spend.md)), the MCP
gateway is understood ([tool-governance.md](tool-governance.md)), and you
know how a denial becomes a bounded grant ([approvals.md](approvals.md)).

Everything the plane governs up to approvals protects an agent that lists
ConfigMaps. Nothing in that demo *needs* governance. This path fixes
that: the agent posts into a Slack channel humans read, which is the
first genuinely consequential action in this repo.

The deliverable is not "Slack works". It is this cycle:

```text
  agent / MCP client            plane                          human
    │ "post this to Slack"        │                              │
    │ ─── no posting tool ──────▶ │ the agent's credential        │
    │ ◀── "I can't do that" ───── │ projects 1 of the 14 tools    │
    │                             │ the server offers             │
    │ conversations_add_message   │                              │
    │ ──────────────────────────▶ │ DENIED (not allowlisted)     │
    │ ◀──── JSON-RPC -32001 ───── │ + approval request FILED     │
    │       "request filed"       │                              │ make approvals
    │                             │ ◀─── make approve ID=… ───── │ TTL=15m USES=1
    │                             │ the grant enters the          │
    │ ◀── tool now discoverable ─ │ projection: the capability    │
    │ conversations_add_message   │ APPEARS for the agent         │
    │ ──────────────────────────▶ │ ADMITTED via grant ──────────┼──▶ Slack
    │ ◀──── "Posted." ─────────── │ (use consumed, audited)      │   (a human
    │ "post another one"          │                              │    reads it)
    │ ──────────────────────────▶ │ grant spent → not live →     │
    │ ◀── "I can't do that" ───── │ the capability DISAPPEARS    │
```

The connector is the payload. The approval gate is the point.

The shape is stronger than "the call is refused": under the gateway's
*visible = callable* rule, an approval does not merely permit an action
the agent was already reaching for. It **materialises the capability**,
and its expiry takes the capability away again. See
[Why the agent is never the one denied](#why-the-agent-is-never-the-one-denied).

> **This is the first component in the repo with deliberate INTERNET
> egress.** See [What the internet-egress pod
> means](#what-the-internet-egress-pod-means). NetworkPolicy now bounds
> it: out on 443 only, in from the proxy only ([egress.md](egress.md)).

## Which Slack MCP server, and why

Kaimahi writes **no connector code**. Slack MCP servers already exist and
kagent already deploys MCP servers. The candidates, surveyed 2026-09-01
against the npm registry, GHCR and the running image rather than from
documentation alone:

| Candidate | Verdict |
|---|---|
| Slack's own hosted MCP server (`https://mcp.slack.com/mcp`, [docs](https://docs.slack.dev/ai/slack-mcp-server/)) | **Rejected.** Not self-hostable, and it authenticates with confidential OAuth 2.0 **user** tokens from a registered Slack app. A headless agent posting as a bot is not the shape it serves. The gateway can now reach a hosted server through its hardened dialer ([hosted-upstreams.md](hosted-upstreams.md)), but an OAuth user-token flow is still out of scope. Revisit if approval routing ever needs to act as a person. |
| [`@modelcontextprotocol/server-slack`](https://www.npmjs.com/package/@modelcontextprotocol/server-slack) (the reference server) | **Rejected.** Repo archived 2025-05-29; npm marks it *"Package no longer supported"*, last publish 2025-04-25. A deprecated package is not something to hand a workspace token. |
| `@zencoderai/slack-mcp-server`, `ubie-oss/slack-mcp-server`, assorted `@mseep/*` forks | **Rejected.** Forks of the archived lineage: a single `0.0.1` publish, or GitHub-registry-only publishing that needs a PAT. None is maintained enough to hold a workspace token. |
| [`korotovsky/slack-mcp-server`](https://github.com/korotovsky/slack-mcp-server) | **Chosen.** MIT, ~1.8k stars, actively maintained (npm `1.3.0`, 2026-05-14). Runs as a long-lived container serving **streamable HTTP**, verified in-cluster, so the gateway relays to it with no `npx` fetch at pod start. Multi-arch image on GHCR. |

Where documentation and measurement disagreed, the measurement won. The
`SLACK_MCP_API_KEY` finding below is the example: the docs say the server
enforces it, and it does not.

Provenance and pinning, because this is third-party code that holds a
Slack workspace token:

- Pinned **by digest**, not by tag:
  `ghcr.io/korotovsky/slack-mcp-server@sha256:35cbc988d9282409e27b755957e48a6096fcf037dee72118e97177fe38b1a1b3`
  (the multi-arch index for `v1.3.0`). A tag can be moved; the bytes that
  run must be the bytes that were reviewed. CI asserts the manifest is
  digest-pinned.
- Why it is trusted enough: MIT, source public, an active maintainer,
  and, decisively, it never needs to be trusted with more than we give
  it. It runs in its own pod in the plane's namespace with a bot token
  scoped to one private channel, its posting tool is restricted
  server-side to that channel ID, and every call the agent makes to it
  is allowlisted and audited by the gateway.
- **Honest caveat.** This project's headline feature is a "stealth mode"
  that authenticates with a user's browser session cookies (`xoxc`/`xoxd`)
  to avoid needing workspace-admin approval. Kaimahi deliberately does
  **not** use that path: it uses a proper `xoxb` bot token, and
  `scripts/slack-secret.sh` refuses anything else. A bot acts as itself;
  a session token acts as a person.

## Architecture

```text
namespace kagent                          namespace kaimahi
┌──────────────────────┐                 ┌──────────────────────┐
│ hello-slack agent    │  Authorization: │ kaimahi-proxy pod    │
│  modelConfig:        │  kmh_… (Secret- │  :8080 LLM proxy     │
│   governed-copilot ──┼──▶ resolved via │  :8081 MCP GATEWAY   │
│  tools[] ──▶ kaimahi-│  headersFrom)   │  authn → scope →     │
│   slack RemoteMCP ───┼────────────────▶│  allowlist/grant →   │
└──────────────────────┘                 │  relay → audit       │
                                          └──┬────────────┬──────┘
  the agent holds ONLY a kmh_ token          │            │
  no Slack token exists in this namespace    │            │ injects
                                              ▼            ▼ SLACK_MCP_API_KEY
                              ┌──────────────────┐  ┌──────────────────────┐
                              │ kagent-tools     │  │ kaimahi-slack-mcp    │
                              │ (tool upstream)  │  │ MCPServer, digest-   │
                              └──────────────────┘  │ pinned, xoxb token   │
                                                     │ via envFrom Secret   │
                                                     └──────────┬───────────┘
  Both Slack-path upstreams are in-cluster; the gateway           │ INTERNET
  reaches the internet only for `internet: true` entries.         ▼
  This POD, however, talks to the internet.              api.slack.com
```

- **Placement**: the Slack MCP server runs in the **plane's** namespace,
  not the agent's. kagent reconciles an `MCPServer` in any namespace
  (verified on the live cluster), so the workspace token sits next to the
  Copilot key in `kaimahi`, and `kagent` holds nothing but opaque `kmh_`
  tokens.
- **Upstream table**: `k8s/plane/upstreams.yaml` carries `slack` as one
  of its six `tool_upstreams` entries, in-cluster. CI asserts every entry
  not marked `internet: true` resolves to an in-cluster hostname; the
  three marked ones (GitHub's hosted MCP server, the write-scoped GitHub
  seam the release agent uses, and Microsoft's hosted Azure DevOps server)
  are reached only through the hardened dialer
  ([hosted-upstreams.md](hosted-upstreams.md)), so an internet-facing
  upstream cannot slip in silently.
- **No ungoverned Slack path is *shipped*.** The tools docs keep an
  ungoverned wiring for contrast; this path ships none, so the only route
  this repo wires is through the gateway. That is a statement about the
  committed configuration; the containment is the NetworkPolicy that
  admits only the proxy to the Slack MCP server's pod
  ([egress.md](egress.md)). See
  [What the internet-egress pod means](#what-the-internet-egress-pod-means).

## Credential custody

Three secrets, split so no pod holds more than its job needs:

| Secret (ns) | Holds | Reaches |
|---|---|---|
| `kaimahi-slack-bot` (kaimahi) | `SLACK_MCP_XOXB_TOKEN`, `SLACK_MCP_ADD_MESSAGE_TOOL` (the channel ID) | the **MCP server pod only** |
| `kaimahi-slack-mcp-key` (kaimahi) | `SLACK_MCP_API_KEY` | the MCP server pod **and** the proxy, which injects it upstream |
| `kaimahi-slack-token` (kagent) | the agent's `kmh_…` opaque token | the **agent only** |

- The bot token never appears in YAML, argv, env listings or logs.
  `spec.deployment.env` in `k8s/slack-mcp.yaml` is plaintext YAML and
  carries only host/port/log-level; everything secret arrives through
  `secretRefs`, which kagent renders as `envFrom.secretRef` (verified
  against the live 0.9.12 CRD). CI fails if a secret-capable key appears
  in that plaintext map.
- The **channel ID is never committed**. It is workspace-identifying, so
  it rides the Secret and is passed per task at demo time.
- `scripts/slack-secret.sh` captures the token stdin-only into a 0600
  file, checks `auth.test`, and **refuses to store anything** unless
  `conversations.info` says the channel `is_private` and the bot is a
  member. It also refuses a non-`xoxb` token.

Least-privilege bot scopes for this demo: `chat:write` (post),
`groups:read` (prove the channel is private), `groups:history` (the
read-only tool), `users:read` (name resolution). `chat:write.public`,
which Slack offers by default, lets a bot post to **any** public channel
without being invited. Drop it.

## Run it

```sh
make plane                                   # the governance plane
make plane-copilot-secret                    # the demo model runs governed Copilot
make slack-secret SLACK_CHANNEL=C0XXXXXXXXX  # stdin-only; refuses a non-private channel
make slack-mcp                               # the digest-pinned server, in-cluster
make govern-slack                            # kmh_ credential + READ-ONLY allowlist + agent
```

After `make govern-slack` the credential may call `conversations_history`
and nothing else. `make tool-allowlist CRED_TOOLS=hello-slack` shows it;
the gateway projects it onto `tools/list`, so the agent cannot even see
a posting tool.

The demo agent runs `governed-copilot`. `qwen2.5:3b` fails at both halves
of this task, composing a message and calling a tool, so the ollama path
is CI's, not the demo's.

### The demo

```sh
# 0. What the agent can even see: the server offers 14 tools; the
#    credential projects one. Posting is not among them.
kubectl -n kagent get remotemcpserver kaimahi-slack \
  -o jsonpath='{.status.discoveredTools[*].name}'      # conversations_history

# 1. Ask the agent to post. It cannot: there is no posting tool in its
#    hands to call.
make slack-post SLACK_CHANNEL=C0XXXXXXXXX MESSAGE='Kaimahi governance demo.'

# 2. The gateway's enforcement point, exercised directly — this is the
#    DENIAL that files the request (and what any MCP client that tries
#    the call gets).
UPSTREAM=slack GOVERNED_SECRET=kaimahi-slack-token \
  bash scripts/tool-denial-probe.sh conversations_add_message
make slack-audit          # denied 403, "approval request filed"

# 3. A human looks at the queue and grants a BOUNDED permit.
make approvals            # copy the id (kind=tool, subject=conversations_add_message)
make approve ID=<uuid> TTL=15m USES=1

# 4. The grant is now in the projection, so the capability appears.
#    kagent re-discovers on reconcile; nudge it and roll the agent.
kubectl -n kagent annotate remotemcpserver kaimahi-slack \
  kaimahi.dev/rediscover="$(date +%s)" --overwrite
kubectl -n kagent get remotemcpserver kaimahi-slack \
  -o jsonpath='{.status.discoveredTools[*].name}'      # now includes the post tool
kubectl -n kagent rollout restart deploy/hello-slack

# 5. The agent posts. For real, into a channel a human reads.
make slack-post SLACK_CHANNEL=C0XXXXXXXXX MESSAGE='Kaimahi governance demo.'
make slack-audit          # allowed 200, detail: granted <grant-id>

# 6. The use is spent. Ask again — nothing is posted.
make slack-post SLACK_CHANNEL=C0XXXXXXXXX MESSAGE='And again?'
make grants               # the grant, live=no, uses 1/1
make approval-audit       # requested / approved / denied, with the bounds
```

Nothing in step 5 widens the *configuration*: the static allowlist still
holds one read-only tool throughout. `make slack-allow SLACK_TOOLS=…` is
the config lever, and it is deliberately not what the demo uses. The
point is that a human said yes to one post, for fifteen minutes, once.

`make slack-down` removes the agent, the seam and the server. The
Secrets survive; delete them to revoke.

## The other direction: Slack triggers the agent

Everything above is the agent posting *out*. The inbound bridge
([inbound.md](inbound.md#slack-events-the-loop)) closes the loop: an
`app_mention` in the same private channel, delivered by Slack's Events
API to a public HTTPS edge on AKS, triggers `hello-slack`, which answers
in the thread through this same governed path. Two approvals gate it:
an `inbound` grant for the hook and the tool grant for the post. Nothing
in this document changes for that; the bot's reply is just an approved
post whose author happens to be a Slack user rather than `make slack-post`.

The same mention hook carries one more verb: an approver can decide a
pending request from the channel, and the plane announces filed
requests there under a credential of its own, through this same
gateway and posting tool ([approvals.md](approvals.md#deciding-from-slack)).
Step 3 of the demo above can therefore be typed in Slack instead of the
terminal.

## Why the agent is never the one denied

Measured, not assumed: kagent wires an agent only to tools it
**discovered**. Naming an undiscovered tool in
`spec.declarative.tools[].toolNames` does nothing. This was verified by
patching `conversations_add_message` into the agent's `toolNames` while
the allowlist excluded it, and watching the agent report it had no such
tool rather than attempt a call.

Because the gateway projects `tools/list` down to what the credential
may call right now, a non-allowlisted tool is invisible, so a kagent
agent can never *produce* the JSON-RPC denial for it. Three consequences
worth stating plainly:

- **The denial and auto-filing are exercised at the gateway**, by a
  client that attempts the call directly (`tool-denial-probe.sh`, and any
  non-kagent MCP client). This is the real enforcement point, and the
  one CI asserts.
- **For an agent, the guarantee is stronger than refusal**: the
  capability is not in its hands at all. There is no call to smuggle, no
  retry loop, no prompt injection that reaches a tool the credential
  cannot call. A denied tool is not a locked door, it is a door that is
  not in the room.
- **An approval is therefore constructive**: minting a bounded grant puts
  the tool into the projection, kagent re-discovers it, and the agent
  gains the capability for exactly the grant's life. Exhaustion removes
  it again on the next reconcile.

The cost is a discovery lag: enforcement is immediate, but what an agent
*sees* changes only on kagent's next RemoteMCPServer reconcile, which is
why step 4 nudges it. That lag is a known gateway limitation; this is
where it becomes visible in the demo path.

## What the internet-egress pod means

`kaimahi-slack-mcp` opens connections to `api.slack.com`. That is a
first for this repo, and it is worth stating plainly rather than burying:

- **The gateway's own reach to Slack is in-cluster.** Both Slack-path
  `tool_upstreams` entries are in-cluster Services; the gateway dials the
  internet only for entries marked `internet: true`, through its hardened
  dialer ([hosted-upstreams.md](hosted-upstreams.md)), and CI asserts
  every unmarked entry stays in-cluster.
- **The pod's egress is bounded by NetworkPolicy** ([egress.md](egress.md)):
  DNS plus TCP 443 to non-private addresses, nothing else, and only the
  proxy may connect to it. What that does *not* constrain: an IP/port
  rule is not a URL allowlist, so the pod can still TLS to any public
  host on 443 — bounded by the server's own code and the three
  application layers above.
- **The mitigation we hoped for does not work.** The plane supports
  injecting a tool upstream's own bearer credential from proxy-side
  custody (`credential_file` in the upstream table), which would let the
  Slack server reject any caller that did not come through the gateway.
  Measured on the live cluster with slack-mcp-server v1.3.0: it **does
  not enforce** `SLACK_MCP_API_KEY` on its `http` transport. An
  unauthenticated `initialize` and `tools/list` both answered 200, as did
  a wrong bearer; its SSE transport also served an unauthenticated
  stream. The injection is wired, tested and fails closed on our side,
  and it is documented here as not load-bearing today.
- **What closes the direct-access bypass** is that NetworkPolicy:
  default-deny ingress to the Slack pod except from the proxy. It is
  built and probed on every PR. One correction to an earlier version of
  this doc: kind's `kindnet` *does* enforce NetworkPolicy (it runs
  `kube-network-policies`; the probe proves it on the CI cluster). AKS
  does not unless provisioned with `--network-policy` — see
  [egress.md](egress.md) for that residual.

Blast radius today is bounded by the credential rather than the network:
the bot token carries `chat:write` for one workspace, and the MCP server
itself restricts `conversations_add_message` to the single channel ID in
`SLACK_MCP_ADD_MESSAGE_TOOL`. That is three independent layers before a
message reaches a channel (gateway allowlist/approval, server-side
channel restriction, and Slack's own scopes), but none of them is a
network boundary.

## What CI covers, and what it cannot

CI is **keyless** and stays that way (public, fork-exposed repo): no
Slack token, no Copilot token, ever. The Slack MCP server is therefore
**not deployed in CI**. Rather than stand up a fake Slack to paper over
that, the boundary is made structural: the gateway decides *before* it
forwards, so the whole approval cycle runs, the gateway then **dials**
the committed `slack` upstream, and the call answers **502** because no
such Service exists. `scripts/tool-admit-probe.sh` **fails on a 200**,
so CI cannot silently start reaching a tool server, and it fails on any
503 that is a pre-forward denial wearing a 503 (a Postgres blip must
never read as "admitted").

Be precise about what that proves. It proves the allowlist and grant
decisions, and that an admitted call is actually forwarded. It does
**not** validate the upstream URL itself: any unreachable URL answers
502 alike. CI needs a throwaway upstream credential in the plane for the
dial to happen at all. It is not a Slack credential, and no Slack token
exists in CI.

CI asserts:

- the Slack manifests are valid against the live kagent CRDs;
- the committed wiring carries no credential: no secret-capable key in
  plaintext `env`, exactly the two expected `secretRefs`, a
  digest-pinned image, a single Secret-resolved `headersFrom`, no
  `xox[bpca]-` token shape anywhere in the tree, and **posting absent
  from the committed allowlist**;
- the gateway's upstream table has both entries and both are in-cluster;
- the agent-side Secret holds only a `kmh_…` opaque token;
- the full cycle over the `slack` upstream: post **denied 403** and a
  request auto-filed → bounded approval → **admitted**, audited
  `allowed … granted <id>` → use exhausted → denied again.

CI does **not** cover: the Slack MCP server pod itself, its runtime
token custody, the gateway relaying a real Slack response, or Slack
accepting the message. Those were live-verified once (2026-09-01) on a
cluster with a real bot token, and nowhere else.

## Operational notes

- The proxy image tag is the one in `k8s/plane/proxy.yaml`; re-run
  `make plane` to roll it. `imagePullPolicy: Never` means a same-tag
  rebuild needs the restart `make plane` already does.
- The Slack MCP server runs with `--no-cache` deliberately: its optional
  user/channel caches would pull a directory of the whole workspace into
  the pod. Tools are addressed by channel ID. `channels_list` is
  consequently not useful and is not allowlisted.
- **A burned grant does not guarantee a delivered message.** A tool-grant
  use is consumed *before* the forward (the conservative direction), so
  if the plane cannot read the Slack server's upstream credential (the
  Secret deleted, or the proxy rolled before it existed) the call is
  refused 503 and the audit row reads `allowed 503 granted <id>`: the
  human's approval is spent and nothing was sent. The audit trail says
  so plainly; re-approve after fixing the Secret. `make slack-mcp`
  checks for the key Secret up front for exactly this reason.
- Rotating the bot token: re-run `make slack-secret`, then
  `kubectl -n kaimahi rollout restart deploy/kaimahi-slack-mcp` (the
  server reads its env at start). The gateway key is generated once and
  kept, since rotating it under a running server would break injected
  calls until both sides roll.
- On AKS this path is deployed only for the inbound loop
  ([inbound.md](inbound.md#slack-events-the-loop)), on a cluster that is
  created for it and deleted the same day. Putting a real workspace
  token into a temporary cloud cluster is still credential exposure;
  the loop is the added proof that justifies it, and the token, like
  the cluster, is meant to be gone within hours (`make aks-down` deletes
  the Secret with everything else; rotate the token in Slack afterwards
  if you want to be sure).

## Limitations

The full governed-vs-ungoverned table is in
[README.md](README.md#what-is-governed-today-and-what-is-not). Specific
to this path:

- **The Slack MCP server's own endpoint auth is not effective**
  (v1.3.0, http transport). The plane injects a credential the server
  does not check.
- **Slack is the only chat route for approvals** (the CLI path, `make
  approve`, remains and records `admin`). A filed request
  is announced in the pinned channel by the plane's own governed post
  (credential `kaimahi-plane`, allowlisted to the posting tool), and a
  listed approver decides it with `@kaimahi approve <id>`; the grant
  records `slack:<user id>` ([approvals.md](approvals.md#deciding-from-slack)).
  Channel membership alone decides nothing.
- **What the agent sees lags a grant** until kagent's next reconcile.
  Enforcement does not lag.
- **A spent grant is not a delivered message** when the upstream
  credential is unreadable. Read the audit row.
