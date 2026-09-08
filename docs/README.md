# Kaimahi documentation

Kaimahi is an incubation project: an agent defined in one YAML file,
running on Kubernetes via [kagent](https://kagent.dev), driven from your
terminal, with a governance plane in front of it. Everything documented
here runs today and was run before it was written down. Where something
is demonstrated once rather than maintained, or schema-valid rather than
live-verified, the doc says so in those words.

## Start here

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
kmx up                                    # kind cluster + local model + kagent + two agents (~5-10 min)
kmx agent chat hello-world "Who are you?"

kmx plane                                 # the governance plane: proxy + spend ledger
kmx govern hello-world                    # the agent now spends through it
kmx ledger                                # what it cost
```

[Getting started](getting-started.md) is the walkthrough: prerequisites,
what `kmx up` actually does, the agent YAML, and how to talk to it.
[kmx.md](kmx.md) is the command reference. From a clone, `make up`,
`make chat`, `make plane` and `make govern` run the same binary.

## By what you want to do

| I want to… | Read | The commands |
|---|---|---|
| Get an agent running and talk to it | [getting-started.md](getting-started.md) | `kmx up`, `kmx agent chat`, `kmx status`, `kmx down` (or `make up`, `make chat`, …) |
| Know exactly what the one command does, and what it deliberately does not | [kmx.md](kmx.md) | `kmx ctx`, `kmx up`, `kmx agent create`, `kmx plane`, `kmx govern`, `kmx version` |
| Create an agent of my own, as reviewable YAML | [kmx.md](kmx.md#kmx-agent-create) | `kmx agent create <name> --tools <server>:<tool>` |
| Use a hosted model (Copilot, Anthropic, OpenAI, Azure AI Foundry, OpenRouter, anything OpenAI-compatible) | [models.md](models.md) | `make model-secret`, `make copilot-secret`, `make use PRESET=…` |
| Give the agent a tool | [tools.md](tools.md) | `make chat AGENT=hello-tools …` |
| Put a budget on the agent's LLM spend, see the ledger, keep real API keys away from the agent | [spend.md](spend.md) | `kmx plane`, `kmx govern`, `kmx ledger` (or `make plane`, `make govern`, `make ledger`), `make budget` |
| Control which tools the agent can call, and audit every call | [tool-governance.md](tool-governance.md) | `make govern-tools`, `make tool-allow`, `make tool-audit` |
| Point Kaimahi at **my own** MCP server and **my own** agent | [govern-your-agent.md](govern-your-agent.md) | `kmx tools add <name> --url … --tool <tool>:<fields>`, `kmx tools govern --server …` |
| Have a human approve a denied action with a bounded, expiring grant | [approvals.md](approvals.md) | `make approvals`, `make approve`, `make grants` |
| Let the agent post to Slack, one approved message at a time | [slack.md](slack.md) | `make slack-secret`, `make slack-mcp`, `make govern-slack`, `make slack-post` |
| Let the outside world trigger an agent (webhooks), governed | [inbound.md](inbound.md) | `make inbound-credential`, `make inbound-secret`, `make inbound-audit` |
| Close the Slack loop: a mention triggers the agent, the agent answers in the thread (AKS) | [inbound.md](inbound.md#slack-events-the-loop) | `make inbound-expose`, `make exposure-scan`, `make inbound-unexpose` |
| Be told in Slack that a request is waiting, and approve or deny it from Slack as yourself | [approvals.md](approvals.md#deciding-from-slack) | `make slack-approvers`, `make notify-slack`, `make slack-mention` |
| See what the plane's pods can and cannot reach, and prove it | [egress.md](egress.md) | `make netpol-verify`, `make egress-copilot`, `make egress-copilot-off` |
| Let the agent read issues and pull requests through GitHub's hosted MCP server, with the token in plane custody | [hosted-upstreams.md](hosted-upstreams.md) | `make github-secret`, `make govern-github`, `make github-ask`, `make github-revoke` |
| Install a specific version, check what I am running, and upgrade — including the plane, with data in it | [releases.md](releases.md) | `go install …/cmd/kmx@v0.1.0`, `kmx version`, `kmx backup`, `kmx plane` |
| Give the demo, start to finish, in about thirty minutes | [demo.md](demo.md) | the order of the steps above and what each should print |
| Cut a real release: an agent drafts the notes from what merged, proposes the branch and the builds, a human approves the branch cut and the publish by version and repository, and builds run under a standing constraint | [release-agent.md](release-agent.md) | `make release-secret`, `make ado-secret`, `make govern-release`, `make release-bind`, `make release` |
| Say what a workflow reaches, what may happen without you, and in what order — in one file, and run it | [workflows.md](workflows.md) | `kmx workflow list`, `kmx workflow show`, `kmx workflow govern`, `kmx workflow run` |
| Show the whole thing on money: an invoice that does not match, an answer a person has to approve by amount and payee, and a later invoice that tries to redirect the payment — or [tell it without running it](ap-demo.md#telling-it-without-running-it) | [ap-demo.md](ap-demo.md) | `make erp`, `make govern-ap`, `make ap-demo`, `make ap-injection` |
| Govern an agent that is not a kagent agent — what my runtime has to be told, and what it cannot have | [foreign-runtime.md](foreign-runtime.md) | the credential, the two seam URLs, the plane's certificate authority, and one NetworkPolicy selector |
| Know who an agent acted for, and stop a token living forever | [identity.md](identity.md) | `make credentials`, `make credential-renew`, `make ledger`, `make inbound-audit` |
| Run the plane for real: replicas, probes, metrics, back up and restore the ledger | [operations.md](operations.md) | `make backup`, `make restore`, `make plane-metrics` |
| Run all of this on a real cluster (AKS) instead of kind | [aks.md](aks.md) | `TARGET=aks`, `make aks-cluster`, `make aks-down` |
| Fix something that went wrong | [FAQ.md](FAQ.md) | |

The docs build on each other in roughly the table's order: tool
governance assumes the plane from [spend.md](spend.md) is deployed,
approvals assume tool governance, Slack assumes approvals. Each doc says
what it assumes at the top.

## What is governed today, and what is not

Governance is opt-in per agent. This is the one table that says what
the plane covers, kept in one place so it cannot drift between docs.

| Surface | Status |
|---|---|
| LLM calls through a `governed-*` preset | **Governed**: authentication, monthly budgets that fail closed, a ledger of every call, real keys held only by the proxy ([spend.md](spend.md)) |
| LLM calls through a plain hosted preset (`openai`, `anthropic`, …) | **Ungoverned, by choice.** Nothing meters it. Switch to a governed preset to govern it |
| Tool calls through `kaimahi-tools` (after `make govern-tools`) | **Governed**: authentication, a committed upstream table, tools-only protocol scope, a per-credential allowlist, every call audited ([tool-governance.md](tool-governance.md)) |
| Tool calls through `kagent-tool-server` directly | **Ungoverned, by choice.** `make govern-tools` to govern it |
| Approvals and bounded grants | **Governed**: a denial files a request, a human mints a grant bounded by expiry and/or use count, the grant lapses ([approvals.md](approvals.md)) |
| Slack posting | **Governed**: posting is not allowlisted, it is approved per use and audited with the grant id. There is no ungoverned Slack path in the repo ([slack.md](slack.md)) |
| The Slack MCP server's own endpoint authentication | **Not effective, and no longer load-bearing.** The server (v1.3.0) ignores the injected credential on its HTTP transport; NetworkPolicy now admits only the proxy to that pod, so the bypass it was meant to close is closed at the network instead ([egress.md](egress.md)) |
| Pod-level network egress (NetworkPolicy) | **Governed** in the `kaimahi` namespace: default-deny both ways, each pod allowed exactly its paths, the Slack pod out on 443 only, and a probe proves the negative on every PR ([egress.md](egress.md)). Residuals stated there: an IP/port rule is not a URL allowlist; the `kagent` and `ollama` namespaces are unpoliced; AKS does not enforce policy unless provisioned for it |
| Inbound events (webhooks → agent) | **Governed**: signature or bearer auth before any work, replay window, per-hook rate limit and bounded queue, the target's budget checked at the door, and a bounded grant consumed per event ([inbound.md](inbound.md)). The pre-auth rate limiter and the queue are per replica by design (ceiling = replicas × rate); the budget, the grant use and the replay guard are exact in Postgres |
| The plane itself: replicas and concurrency | **Two stateless replicas** that agree on every decision: budgets, grant uses, replay dedupe, request dedupe and approval immutability are each decided in one Postgres transaction under the credential's row lock, proven by concurrent tests in CI (N calls against a cap with room for one admit exactly one, across both replicas). Migrations run under a lock; a replica can be killed mid-cycle; a Postgres restart drops readiness, never restarts a proxy. **Postgres is one replica**, with `make backup` / `make restore` — durable, not highly available ([operations.md](operations.md)) |
| Metrics | **Prometheus on its own cluster-internal port** (9092), on no Service, opened only to a scraper by an explicit NetworkPolicy allowance. Decisions by seam and reason, ledger totals by credential *name*, live grants, queue depths, upstream latency, build info. No identifier is ever a label value — a test asserts the label set ([operations.md](operations.md#metrics)) |
| Internet-facing *gateway* upstreams (GitHub's hosted MCP server) | **Governed**: an upstream marked `internet: true` is reached only through the plane's one hardened dialer (https, 443, the host the table names, every resolved address checked and the checked address dialed, no redirects, bounded and capped), shared with the Copilot path; the GitHub token is plane custody, read-only, and the allowlist names read tools only; the network allowance is opt-in, 443 to public addresses, the same shape as Copilot's ([hosted-upstreams.md](hosted-upstreams.md)). The release agent added two more hosted seams on the same dialer: a write-scoped GitHub credential and Microsoft's hosted Azure DevOps server, which is authenticated by **Microsoft Entra** rather than a key — so an OAuth-authenticated hosted server and write tools both exist now, the release writes denied and approved call by call, with build dispatch bounded by a standing constraint instead ([release-agent.md](release-agent.md)). Residuals: an IP/port rule is still not a hostname rule; an Entra access token lives about an hour and is re-captured per session; and the ref an Azure DevOps build runs on is a nested argument the policy vocabulary cannot bind |
| Approval routing to Slack, per-approver identity | **Governed**: a filed request is announced in the pinned channel by the plane's own post, which rides the gateway under the plane's credential (allowlisted to the posting tool, channel-pinned, audited); `@kaimahi approve <id>` / `deny <id>` from a Slack user in the Secret-mounted approver list decides it, and the grant and audit rows carry `slack:<user id>`; the admin path records `admin`. Channel membership alone decides nothing; a non-approver is refused and audited ([approvals.md](approvals.md#deciding-from-slack)). Email and ticket routing are not built; a notification the plane cannot get out is logged (a known refusal is retried, three attempts in all; an ambiguous failure never), and `make approvals` remains the queue |
| Who an agent acted for | **Recorded, where the plane can substantiate it.** Every ledger, tool-audit and inbound row carries `acted_for`: `slack:<user id>` for a person the inbound signature vouched for, `none` where there is no person, `unknown` where the attribution was lost, and never an empty value that could mean either ([identity.md](identity.md)). The correlation is a run the plane opens and closes around an agent turn — one per credential the agent authenticates with — so the agent is never asked to name itself. **One known imprecision, documented rather than discovered**: for a client the plane did not deploy, `none` claims more than the plane knows, and it is accepted because no supported configuration reaches it ([identity.md](identity.md#where-none-is-stretched-and-why-that-is-accepted)). Per-person *budgets* are not built |
| Who *called* | **Recorded on the ledger and the tool audit, and honest about its worth.** `caller (claimed)` is the client's own `User-Agent`, self-reported and unverified; `from (observed)` is the peer address the plane saw at its own socket. Separate columns because they are worth different amounts. They decide nothing — this is what makes an audit row legible, not a new control — and they are what lets a reader tell an agent the plane deployed from a script holding its token ([identity.md](identity.md#who-called)). The MCP handshake's own client name is deliberately not used |
| Credential lifetime | **Bounded.** Every credential issued now carries an expiry (30 days by default; no way to ask for "never"), refused at the proxy, the gateway and the inbound door with a message naming the fault, the time and the fix, and audited like every other refusal. A credential with no expiry is the legacy class — still valid, and only ever shrinking ([identity.md](identity.md)). Renewal moves a date, not material; rotating the token is still re-issuing it |
| What an agent *sees* after a grant | **Lagging, not wrong.** Enforcement is immediate; the agent's discovered tool list updates on kagent's next RemoteMCPServer reconcile ([slack.md](slack.md#why-the-agent-is-never-the-one-denied)) |
| AKS | **Demonstrated, not maintained.** Six verified runs across 2026-09-01, 09-02, 09-03 and 09-06 (the plane, NetworkPolicy enforcement, the Slack loop through a public edge, the accounts-payable demo through the registry path, and `kmx lift` on a cluster it created and one it did not), each cluster deleted the same day. [aks.md](aks.md#what-was-verified-and-what-was-not) is the ledger that count comes from. CI stays on kind and keyless ([aks.md](aks.md)) |
| Public exposure | **One opt-in edge, one port.** Only the inbound edge (`make inbound-expose`, AKS) is internet-reachable: TLS on 443, one path, proven by `make exposure-scan`. Everything else stays cluster-internal ([inbound.md](inbound.md#putting-it-on-the-internet)) |

## How these docs are organised

They used to be eight runbooks named for the phase that built them. That
order was how the repo was built, not how anyone reads it: to find "how do
I govern tool calls" you had to know which phase had built the MCP
gateway. The files are now named for the capability, and the old
phase-named files are gone (their history is in git and on the board).

Each runbook mixed three things: how to use a capability, why it is
built the way it is, and what was verified when it shipped. The rule
for what moved where:

- **Kept, user-facing:** every command, everything you will see on
  screen, every caveat, gotcha and limitation, the "verified how" status
  of each claim (CI on every PR, live once, schema-valid only, not
  verified), and the design decisions you need in order to use the thing
  correctly (why Azure AI Foundry rides `provider: OpenAI`, why grants
  must be bounded, why the small model is pinned).
- **Moved out, historical:** alternatives-considered lists, porting
  provenance, surveys of what was not built, phase cross-references, and
  the narrative of the verifying run. All of that is in the delta sheets
  in [COORDINATION.md](COORDINATION.md), which is the project's record
  and stays the authoritative one.

If a caveat is missing from a capability doc that used to be in a
runbook, that is a bug in the restructure, not a decision. File it.

## Project docs, not user docs

- [development.md](development.md): for engineers changing the code — the
  mental model, the build, how the plane is put together, the invariants,
  and the traps.
- [entry-point-principles.md](entry-point-principles.md): why the
  developer entry point exists, what it must never become, and what is
  deliberately not built yet — the reasoning behind the entry point, as
  distinct from the decisions themselves.
- [COORDINATION.md](COORDINATION.md): the coordination board, decisions,
  and delta sheets. Owned by the coordinator session.
- [CLI-PROPOSAL.md](CLI-PROPOSAL.md): the `kaimahi agent create`
  scaffolder — surveyed, ruled on, prototyped in a pull request that was
  closed unmerged. Kept as the record of what was considered and why;
  nothing from it is on main.
- [SCENARIOS.md](SCENARIOS.md): the delegation journeys that argue for
  the governance plane.
- [NAMING.md](NAMING.md): the project name and what is still owed before
  it is final.
