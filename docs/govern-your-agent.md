# Governing your own agent and your own tools

Every other doc here governs something this repository ships. This one
starts from what you have: **an agent, and an MCP server we have never
seen.** By the end the agent calls that server through the enforcing
gateway, under a credential, an allowlist, and an audit trail with its
name on it — and the only NetworkPolicy admitting traffic to that server
is the one that admits the proxy.

That last clause is deliberately narrower than "no pod can reach it".
NetworkPolicy rules are **additive**: a second policy selecting the same
pods can allow what this one does not, and nothing in this path can stop
one being written. So what you have is measured rather than asserted —
`scripts/upstream-boundary-probe.sh` below reads the cluster's real
behaviour, which is the only claim worth making.

Assumes the governance plane is deployed (`kmx plane`, see
[spend.md](spend.md)) and that you have read
[tool-governance.md](tool-governance.md), which is the reference for what
the gateway enforces. This is the procedure.

The agent itself is not this page's job: it must already exist, and
`kmx agent create <name>` ([kmx.md](kmx.md#kmx-agent-create)) is how you
make one. Everything below repoints an existing agent at a governed
seam.

> **Ungoverned by default.** Nothing here happens on its own. An agent
> whose `tools` point straight at an MCP server calls it with no
> credential, no allowlist, no argument policy and no audit row — and
> that is the state every agent is in until somebody runs these
> commands. Governance is opt-in per agent, per seam.

## What onboarding actually is

Five things have to exist, and until now every one of them was written
by hand:

| # | The thing | Who owns it now |
|---|---|---|
| 1 | an entry in the gateway's upstream table, with each tool's `policy_fields` | kmx writes it; **you decide the `policy_fields`** |
| 2 | a NetworkPolicy pair — the proxy may reach the server, and only the proxy may | kmx, from the live Service |
| 3 | a `RemoteMCPServer` whose URL is the gateway and whose `headersFrom` names the agent-side Secret | kmx |
| 4 | a governed credential and its allowlist | kmx mints it; **you decide the allowlist** |
| 5 | the agent's `tools`, repointed at that seam | kmx |

kmx owns the mechanical four because they fail *silently*. A mistyped
gateway URL points an agent at a different upstream or at a 404 and says
nothing useful. A NetworkPolicy written against a Service's published
port when the container listens on another blocks every call while
reading as correct. Neither belongs in a human's fingers.

You own the two that are policy, and kmx will not guess either.

## What your server has to look like

Nothing exotic — but three things must be true:

- **It is in the cluster**, reachable at `http://<service>.<namespace>:<port>/mcp`.
  A server on the internet is a different path with a different threat
  model (a hardened dialer, an opt-in egress allowance): see
  [hosted-upstreams.md](hosted-upstreams.md).
- **Its Service selects its own pods.** kmx reads the Service's selector
  and its resolved `targetPort` and pins the NetworkPolicy pair to those.
  A Service with no selector is refused: a policy pinned to no labels
  selects every pod in the namespace, which is not a boundary.
- **It speaks streamable-HTTP MCP** — `initialize`,
  `notifications/initialized`, `tools/list`, `tools/call`. Those are the
  methods the gateway relays. `ping` is answered locally without
  reaching you; every other method is denied rather than relayed, and
  JSON-RPC batches are rejected outright.

If the Service targets a *named* port, kmx will not guess the number:
pass `--pod-port <n>`.

## Step 1 — decide what the arguments mean

This is the governance-critical moment of onboarding, and it is worth
more than the rest of this page put together. For every tool you name,
you declare which of its arguments are **policy-relevant**. That one
declaration does two jobs: the digest an approval is welded to
binds exactly those fields, and the audit's human-readable summary is
built from exactly those fields.

There are three answers, and they are not close to equivalent:

```bash
--tool stock_adjust:sku,delta   # those fields are policy-relevant
--tool stock_report:            # NO argument is policy-relevant
--tool legacy_thing:*           # do not declare it at all
```

| You wrote | What it means | What an approval then covers |
|---|---|---|
| `tool:a,b` | `a` and `b` are policy-relevant | **that call.** Approving `delta 5` on `SKU-1` cannot be spent on `delta 500`, or on another SKU. The audit row names both values. |
| `tool:` | `policy_fields: []` — no argument matters | **the verb, for any arguments**, until the grant is spent. The audit says only that the verb ran. |
| `tool:*` | no declaration at all | **the whole canonical argument object.** Exact, and brittle: an agent re-emitting a semantically identical call with one extra field produces a different digest, so the approval a human granted will not admit the retry. |

`tool:` is the **weakest** setting and it is the one an impatient
operator reaches for, because it is the shortest to type. It is correct
for a tool whose arguments genuinely do not matter — a status read, a
ping. It is wrong for anything that moves money, data or state: it turns
"a human approved this payment" into "a human approved payments".

kmx refuses a bare `--tool name` rather than defaulting, prints the three
consequences at the point of choosing, and — if you do declare a
verb-level binding — writes a `WEAKEST SETTING IN USE` banner into the
manifest, so it is visible to whoever reviews the file rather than only
to whoever typed the command.

## Step 2 — scaffold the upstream

```bash
kmx tools add warehouse \
  --url http://acme-warehouse.acme:8090/mcp \
  --tool stock_get:sku \
  --tool stock_adjust:sku,delta
```

This writes `upstreams/warehouse.yaml` — four documents, and they *are*
the onboarding, the same files you would have written by hand:

1. **the overlay fragment.** Your upstream goes into an overlay ConfigMap
   (`kaimahi-upstreams-extra` — one shared map, one `<name>.json` key per
   onboarded server), which the proxy merges over this repo's committed
   table at boot. Your entry is never inside our four,
   which is what stops the next `kmx plane` — which re-applies the
   committed table — from discarding it. An overlay that would redefine
   a committed entry is refused rather than resolved by precedence.
2. **the proxy's egress to your server**, pinned to your Service's own
   selector and its *container* port, as an additive policy — the plane's
   committed boundary is not edited to make room for it.
3. **your server's ingress, from the proxy alone.** This is the half that
   makes governance a boundary rather than a convention: without it any
   pod in the cluster could call your server directly, around the
   allowlist, the constraints, the grants and the audit. It is the only
   policy this path writes for those pods; it cannot bind one written
   elsewhere.
4. **the RemoteMCPServer**, whose URL is the gateway and whose
   `headersFrom` names an agent-side Secret. kmx accepts a credential in
   no form — no flag, no environment variable, no file — so this names a
   Secret and never holds one.

By default the ingress policy also gives your server **no egress at
all** (`policyTypes: [Ingress, Egress]` with no egress rules): the
strongest statement this file can make that it holds no credential and
reaches no other system. Additive again — another policy selecting these
pods can grant egress and this one cannot prevent it; what it guarantees
is that *it* grants none. If the server needs cluster DNS, use
`--server-egress dns`. If it already has egress you must not touch, use
`--server-egress keep` — and the manifest then says, in the file, that
its egress is unbounded and yours to bound.

Both directions are worth checking rather than assuming:
`kubectl -n <namespace> get networkpolicy` lists every policy selecting
those pods, not only this one.

**Before anything is written or applied**, kmx sends the candidate
**table** to the running plane, which merges it over the committed one
and parses it with the same `config.Parse` it booted with. That covers
the upstream entry and the declarations; the two NetworkPolicies and the
RemoteMCPServer are checked only by the Kubernetes API when they are
applied (`--dry-run` does that early). Their content comes from your
Service, so the thing worth reading before you apply is the pod selector
kmx printed. A malformed entry is
therefore refused *here*, with the plane's own message — not later, in a
rollout, by a pod that will not start. What the plane understood comes
back:

```
The plane validated the table: ado, erp, github, github-release, kagent-tools, slack, warehouse.
  stock_adjust: an approval binds sku, delta.
  stock_get: an approval binds sku.
```

Then it applies the file behind the [context guard](kmx.md#where-the-command-will-land)
and restarts the proxy, because the table is read at boot.

`--out -` prints the manifest and mutates nothing. `--no-apply` writes
the file and stops. Neither is offline: both read the Service from your
cluster and ask the plane to validate, because neither the pod selector
nor the container port can be derived from a URL, and a manifest that has
not been validated is the thing this command exists to stop you writing.

A `--no-apply` manifest is a **one-shot** artifact. It carries the
overlay's `resourceVersion`, so if anyone changes the overlay before you
apply it — another onboarding, a hand-added standing constraint —
`kubectl apply` refuses that document with a `Conflict` rather than
replacing their work with a snapshot taken before it existed. Scaffold
again to pick their change up.

Two things to know about that refusal. `kubectl apply -f` applies each
document **independently and does not roll back**, so a refused ConfigMap
still leaves the two NetworkPolicies created — an allowance to a server
that is not in the gateway's table, which nothing can use (the gateway
relays only to upstreams the table names) but which you should delete
along with the stale manifest. And it does not apply to kmx's own apply
path: `kmx tools add` re-reads the version immediately before applying
and refuses *before* anything is created.

## Step 3 — issue the credential and point the agent

Nothing can call the upstream yet: it has no credential, and an empty
allowlist means nothing is callable.

If you scaffolded with `--no-apply` or `--dry-run`, apply the manifest
before this step — the seam is your file, and `kmx tools govern` does not
re-apply it (it says so rather than waiting on an object that is not
there).

```bash
kmx tools govern \
  --server kaimahi-warehouse \
  --secret kaimahi-warehouse-token \
  --credential acme-agent \
  --agent acme-agent \
  --tools stock_get
```

Read the `--tools` list as the whole policy: **`stock_adjust` is
deliberately not on it.** A tool with consequences should reach an agent
through an approval or a standing constraint, not through the allowlist.
Naming it in the agent's own tool selection while leaving it off the
allowlist is the pattern the rest of this repo uses (`Makefile`'s
`AP_AGENT_TOOLS`): the agent can *attempt* it, the gateway denies it and
files a request carrying the exact call, and a human's approval makes it
work without anyone editing the agent.

The order matters and kmx keeps it: credential → allowlist →
RemoteMCPServer `Accepted` → agent patch. kagent's controller discovers
tools *through* the gateway with this credential, so
`status.discoveredTools` is the allowlist projection — the agent never
sees a tool its credential cannot call.

## Step 4 — verify it, from the audit

Not from the absence of errors. Three checks, in order:

```bash
# 1. The projection is the allowlist — the agent sees stock_get and nothing else.
kubectl -n kagent get remotemcpserver kaimahi-warehouse -o jsonpath='{.status.discoveredTools}'

# 2. An allowed call goes through, and lands in the audit with a name on it.
kmx agent chat acme-agent "What is the stock level of SKU-1?"
kmx audit tool acme-agent

# 3. A tool outside the allowlist is denied, audited, and files a request
#    carrying the exact call.
kmx audit tool acme-agent      # ... stock_adjust  denied  403 ... sku SKU-1, delta 5
kmx approvals                  # ... stock_adjust ... sku SKU-1, delta 5
```

The third is the one that matters, and it is why the `policy_fields`
choice was worth the paragraph above: `kmx approvals` shows the
**transaction**, not the verb. Approve it and the grant is welded to that
call:

```bash
kmx approve <id> --uses 1 --ttl 10m
kmx grants                     # ... yes ... call d55d6ddf6f8b
```

(the `binds` column carries `call <digest>` — the same digest the audit
row for the denied call carried, which is what "the approved call is the
call that ran" means concretely.)

A *different* `delta` is denied again and does not spend the grant — the
approved call is the call that runs.

And the boundary itself, which the audit cannot show you, because a call
that goes around the gateway leaves no row anywhere:

```bash
kubectl -n acme get networkpolicy    # your server's ingress: the proxy alone
TARGET=acme-warehouse.acme:8090 bash scripts/upstream-boundary-probe.sh
```

That probe asserts a **negative against a control**: a pod that is not
the proxy must fail to reach your server, *and* must succeed in reaching
something it may legitimately reach. Without the control a "blocked"
result proves nothing — a policy the CNI ignores, a dead target and a
probe pod with no network all look identical from outside. The other
direction is already proven by the governed call above.

NetworkPolicy is an API; the CNI enforces it. A cluster whose CNI ignores
it reports these objects as present while blocking nothing, which reads
as protection and is worse than none. kind enforces
([egress.md](egress.md)); AKS enforces only when the cluster was created
with a policy engine.

## Standing constraints — the calls that need no approval

An allowlist is per-tool. A **standing constraint** is per-credential
bounds on a tool's declared fields: a call inside them proceeds with no
approval; a call outside them is denied and files a request. That is how
"may adjust stock by at most 10 units" becomes a rule the plane enforces
rather than a sentence in a prompt.

kmx does not write these — they are policy, and the vocabulary is small
enough to write by hand. Add them to the fragment `kmx tools add` wrote,
in the overlay, then re-apply and roll:

```json
{
  "tool_upstreams": {
    "warehouse": { "...": "as scaffolded" }
  },
  "standing_constraints": {
    "acme-agent": {
      "stock_adjust": [
        {"field": "delta", "op": "lte", "value": 10},
        {"field": "sku", "op": "in", "values": ["SKU-1", "SKU-2"]}
      ]
    }
  }
}
```

Two things to know before you rely on one, both learned the hard way in
this repo:

- **Where a constraint exists, it BINDS.** It is a bound, not another way
  in: the gateway checks the constraint first and never falls through to
  the allowlist for that credential and tool. So allowlisting a
  constrained tool does not widen anything — it does something worse, it
  *reads* as though it does. Leave it off, as the accounts-payable demo
  leaves `payment_schedule` off.
- **A constraint bounds only what it names.** A rule on `delta` alone
  bounds the size of an adjustment and says nothing about *which SKU*.
  The accounts-payable demo shipped with exactly that gap and an agent
  walked straight through it ([ap-demo.md](ap-demo.md)). Bound every
  field whose value would make you refuse the call.

Every constraint field must appear in that tool's `policy_fields`, or the
config is refused at load — a constraint on an undeclared field would be
a rule that silently never applies. `kmx tools add --out -` plus an edit
is the reviewable way to land both at once.

## What this path does not cover

Stated plainly, because a generic path that quietly did something weaker
than our own demos would be worse than none:

- **A tool server that needs its own credential.** The gateway can inject
  an upstream's real credential from plane custody (the Slack MCP server
  works that way), but the mount for it is a volume on the proxy's own
  Deployment, and `kmx tools add` does not edit committed workloads.
  **The overlay refuses `credential_file`, `credential_header` and
  `extra_headers` outright**, so this is an enforced boundary and not only
  a convention:
  a ConfigMap that could name any path the proxy can read, and any host
  it may be sent to, would be a complete exfiltration primitive for the
  plane's own admin token. Add the `volumeMounts`/`volumes` pair to
  [`k8s/plane/proxy.yaml`](../k8s/plane/proxy.yaml) and the entry to the
  committed table, where both are reviewed as part of this repository.
  kmx will still never carry the value: an overlay is a file, and the
  only way a credential reaches kmx is typed at a prompt on a terminal
  (`kmx credential capture`), which writes it straight into a Secret.
- **A server outside the cluster.** `internet: true` and `ca_file` are
  refused in an overlay for the same reason — five fields in all, and the
  plane refuses them, not just kmx — and hosted upstreams are
  reached only through the plane's hardened dialer with an opt-in 443
  allowance rather than the in-cluster policy pair:
  [hosted-upstreams.md](hosted-upstreams.md).
- **Tool RESULTS.** Argument policy governs inputs. There is no filtering
  or redaction of what a tool returns, and none is implied.
- **Multi-tenancy.** The allowlist is per-credential, not
  per-(credential, upstream) — so onboarding a server that offers a tool
  NAME some credential is already allowlisted for makes that tool
  callable *on the new server* by that credential, with no allowlist
  change. `kmx tools add` checks for this and warns, naming the
  credentials; heed it, or rename the tool. Give each agent its own
  credential rather than sharing one across upstreams with different tool
  vocabularies.

## Verified how

Asserted keylessly in CI on every PR, against a server that is **not one
of ours**: `scripts/ci/plain-mcp-server.py`, deployed by
`scripts/ci/plain-upstream.sh` into its own namespace, over plain http,
publishing a Service port that deliberately differs from its container
port. CI runs exactly the steps on this page — scaffold, validate, apply,
issue, allowlist, call — and asserts the allowed call is audited
`allowed 200`, the tool outside the allowlist is audited `denied 403`
with a request carrying its arguments, the malformed table is refused
before apply, and the server's own record of what it served agrees with
the plane's audit.
