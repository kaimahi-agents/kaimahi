# Governing tool calls: the enforcing MCP gateway

Assumes the governance plane is deployed (`make plane`, see
[spend.md](spend.md)) and the tools agent exists ([tools.md](tools.md)).

Spend governance controls what an agent *spends*; this controls what it
*does*. The gateway mounts at kagent's tool-server seam, the
RemoteMCPServer URL, and every MCP call a governed agent makes is
authenticated, scope-checked, allowlist-enforced, and audited before it
reaches a tool server. kagent still runs the tools. Kaimahi ships no MCP
runtime; the gateway relays the protocol and enforces.

> **What this is, and is not.** The allowlist here is static policy, and
> it is joined by ARGUMENT policy: which argument fields a tool
> declares policy-relevant, and the standing constraints a credential
> carries on them. The consent flow on top of both, where a denial files
> an approval request welded to the exact call and a human mints a
> bounded grant, is [approvals.md](approvals.md).
> And the gateway is an *application-layer* egress rule: a pod that
> ignores its RemoteMCPServer wiring can still open arbitrary
> connections. Cluster-level NetworkPolicy is not built as of this doc;
> a parallel lane is building it.

> The tool audit's last column is `acted for`: who the call was made
> for, where the plane can substantiate it ([identity.md](identity.md)).
> The gateway also refuses an expired credential, audited like every
> other refusal.

> **Onboarding a server this repo did not write** — yours — is
> [govern-your-agent.md](govern-your-agent.md): `kmx tools add` scaffolds
> the table entry, the NetworkPolicy pair and the gateway seam as
> reviewable YAML, validates them against the running plane's own parser
> before applying, and leaves the two decisions that are policy — the
> `policy_fields` declaration and the allowlist — to you.

## Architecture

```text
namespace kagent                             namespace kaimahi
┌──────────────────────┐  Authorization:   ┌─────────────────────┐
│ hello-tools agent    │  kmh_… (Secret-   │ kaimahi-proxy pod   │
│   tools[] ──▶ kaimahi-│  resolved via    │  :8080 LLM proxy    │   committed
│   tools               │  headersFrom)    │  :8081 MCP GATEWAY ─┼──▶ tool_upstreams
│ RemoteMCPServer       │ ───────────────▶ │  authn → scope →    │   table
│   url = gateway       │                  │  allowlist → relay  │        │
└──────────────────────┘                  │  → audit            │        ▼
                                           └─────────┬───────────┘  http://kagent-tools
kagent controller discovery                          │            .kagent:8084/mcp
(initialize + tools/list) rides            ┌─────────▼──────────┐  (chart-managed,
the same path — discoveredTools            │ Postgres 16        │   read-only lockdown)
IS the allowlist projection                │ tool_allowlist,    │
                                           │ tool_audit         │
                                           └────────────────────┘
```

- **Placement.** The gateway is a second data listener (`:8081`) in the
  existing `kaimahi-proxy` process. It reuses the plane's Postgres pool,
  credential model, log redactor and fail-closed machinery, and adds
  zero CPU requests (the CI node runs nearly full). A dedicated Service,
  `kaimahi-mcp-gateway`, gives the tool seam its own address. The admin
  port stays off every Service.
- **The seam.** [`k8s/kaimahi-tools.yaml`](../k8s/kaimahi-tools.yaml) is
  a Kaimahi-owned RemoteMCPServer whose URL is the gateway. The
  chart-managed `kagent-tool-server` is neither shadowed nor mutated.
  This is a second, governed front door.
- **Upstreams.** The `tool_upstreams` table in
  [`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml) lists the
  only places the gateway will relay to. Three entries: `kagent-tools`
  (kagent's tool server) and `slack` (the server deployed in
  [slack.md](slack.md)), both in-cluster, and `github` (GitHub's hosted
  MCP server), the one marked `internet: true` and reached only through
  the hardened dialer ([hosted-upstreams.md](hosted-upstreams.md)).

## Declaring what arguments mean

A tool server knows what its arguments do; the plane has to be told. A
`tool_upstreams` entry may therefore declare, per tool, which argument
fields are **policy-relevant** — and that one declaration does two jobs:
the digest an approval is welded to binds exactly those fields,
and the audit's human-readable summary is built from exactly those
fields.

```json
"tool_upstreams": {
  "kagent-tools": {
    "url": "http://kagent-tools.kagent:8084/mcp",
    "tools": {
      "k8s_get_events":  {"policy_fields": ["namespace"]}
    }
  },
  "erp": {
    "url": "http://kaimahi-erp-mcp.kaimahi:8085/mcp",
    "tools": {
      "payment_schedule":   {"policy_fields": ["invoice_id", "amount_cents", "payee_id"]},
      "payment_policy_get": {"policy_fields": []}
    }
  }
}
```

Both of those are live entries in `k8s/plane/upstreams.yaml`; the ERP one
is what the accounts-payable demo runs on ([ap-demo.md](ap-demo.md)).

- **Choosing it is the governance-critical moment of onboarding**, and
  [govern-your-agent.md](govern-your-agent.md#step-1--decide-what-the-arguments-mean)
  is the table of what each of the three answers costs you.
- **`policy_fields` is required** in a declaration. `[]` is a real answer
  — "no argument of this tool is policy-relevant", a verb-level binding —
  and it is different from forgetting the key, which is refused at load.
- **Fields are top-level argument names** (`[A-Za-z0-9_-]{1,64}`). A value
  nested inside an object is not addressable, which keeps the vocabulary
  small and the summary flat.
- **A tool with no declaration binds its WHOLE canonical argument
  object.** That is exact, and it is the brittle case: an LLM re-emitting
  a semantically identical call — one extra field, a reordered object, a
  different trace id — produces a different digest, so the approval a
  human granted will not admit the retry. Declare the fields that matter
  for any tool an approval cycle runs through.
- **Declarations are per tool NAME across the table.** The allowlist is
  per-credential and not per-upstream, so a tool name means one thing
  here too; two upstreams declaring the same name differently is refused
  at load, which is what stops a constrained tool being reached
  unconstrained through another route.
- **Numbers**: an integer literal has one canonical form, so `48000` and
  `48000` agree however they were spaced. `48000.0` is a different value
  — policy fields should be integers, as money in this plane already is
  (cents).

Standing constraints — the per-credential bounds that let routine calls
through without an approval — are declared alongside, and are documented
with the rest of the consent flow in
[approvals.md](approvals.md#standing-constraints-the-calls-that-need-no-approval).

## Credential custody

`make govern-tools` mints a separate `hello-tools` credential. The
agent-side Secret `kagent/kaimahi-tools-token` holds only the `kmh_…`
opaque token (the plane stores its sha256), and the RemoteMCPServer sends
it via `headersFrom`. The gateway strips it, and every other
credential-slot header, before relaying, so the token never reaches a
tool server. The kagent tool server is in-cluster and unauthenticated. A
keyed tool server gets its real credential injected from proxy-side
custody, exactly like the LLM upstreams; the Slack server was the first
one wired that way, and the GitHub token is held the same way
([hosted-upstreams.md](hosted-upstreams.md#custody)).

## From zero

```sh
make up             # cluster, ollama, kagent, agents
make plane          # proxy + gateway + Postgres
make govern-tools   # credential, allowlist, gateway wiring for hello-tools
make chat AGENT=hello-tools TASK='What pods run in the ollama namespace?'
make tool-audit     # the call you just made, in the audit trail
```

An upstream an operator onboarded lives in a separate ConfigMap,
`kaimahi-upstreams-extra`, merged over the committed table at boot: this
repo's four entries are never edited by onboarding, an overlay that would
redefine one is refused rather than resolved by precedence, and
`make plane` cannot discard somebody's added server. `POST
/admin/config/validate` decides whether a candidate overlay would load,
using the same `config.Parse` the proxy boots with — so a malformed entry
is refused before it is applied rather than by a pod that will not start.
An overlay entry may not set `credential_file`, `credential_header`,
`internet` or `ca_file`: those decide what credential the proxy reads and
which host outside the cluster it may be sent to, and belong in the
committed table. Keyed and hosted upstreams are therefore committed-table
only, by enforcement rather than by convention.

`make ungovern-tools` restores the direct, ungoverned wiring by
re-applying `k8s/tools-agent.yaml`. Re-run `make plane` after editing
`upstreams.yaml`: the config is read at boot, and the ConfigMap mounts
via subPath, which never live-updates.

## Enforcement, all fail-closed

- **Egress.** Only `tool_upstreams` entries are reachable. Any other
  upstream name answers 403 before any network contact.
- **Canonicalization.** Every message is parsed ONCE into a canonical
  form, and the policy decision, the approval digest, the audit summary
  and the bytes relayed upstream all come from it — they cannot disagree
  about what the call was. A **duplicated JSON key at any depth is
  refused** (HTTP 400, audited), not collapsed: Go reads last-wins and an
  upstream parser may read first-wins, so `{"amount": 42000, "amount":
  48000}` inside `arguments` is a smuggling vector, not a typo. Nesting
  depth and node count are bounded, and a message past either bound is
  refused rather than walked.
- **Protocol scope.** Tools only. `initialize` and
  `notifications/initialized` (the mandatory lifecycle handshake) relay;
  `tools/list` and `tools/call` relay under governance; `ping` is
  answered locally without upstream contact; **every other method is
  denied, not relayed** (JSON-RPC error, audited). JSON-RPC batches are
  rejected outright, since a batch could smuggle a denied method.
- **Argument policy.** A `tools/call` is admitted by, in order: a
  standing constraint the call is INSIDE (no approval, no grant burned,
  audited `within standing constraint`); otherwise the allowlist — but
  only where no constraint exists for that credential and tool, since a
  constraint is a bound and not merely another way in; otherwise a live
  grant welded to this call's digest. Everything else is denied and files
  a request carrying the call. Arguments that are not a JSON object are
  refused rather than forwarded unexamined.
- **Allowlist.** Enforced on `tools/call` and **projected** onto
  `tools/list`. kagent's controller discovers through the gateway, so
  `status.discoveredTools` on `kaimahi-tools` shows exactly what the
  credential may call, and the agent never sees the rest. **Empty or
  missing allowlist means nothing is callable.** The one governed
  exception is a live bounded grant ([approvals.md](approvals.md)), which
  admits calls and joins the projection while it lasts; a tool a
  credential is constrained on joins it too, since it is callable right
  now for arguments inside those bounds. Allowlist edits
  enforce immediately on calls; the projection an agent *sees* refreshes
  on kagent's next RemoteMCPServer reconcile.
- **Audit.** Every `tools/call` outcome and every attributable denial is
  appended to `tool_audit` (credential, upstream, method, tool, decision,
  status). Pre-auth 401/503 refusals have no credential to attribute.
  Allowed rows are written after the response they describe, and **a
  failed audit write trips the gateway to 503 for all subsequent
  traffic** until a write succeeds. Unrecordable actions must not keep
  happening.
- **Auth.** Same as the LLM proxy: unknown token 401, credential store
  unreadable 503, no upstream contact either way.

## Changing the allowlist, and watching a denial

```sh
make tool-allow TOOLS=k8s_get_resources,k8s_get_events   # widen
make tool-allow TOOLS=-                                  # nothing callable
make tool-allowlist                                      # show
bash scripts/tool-denial-probe.sh k8s_describe_resource  # watch a denial
```

The denial probe calls a non-allowlisted tool with the governed token
and requires the JSON-RPC `-32001` "not permitted" error. The attempt
lands in `make tool-audit` as a `denied 403` row.

Two levers, and they differ: `make govern-tools TOOLS=…` sets the
gateway allowlist **and** keeps the agent's `toolNames` aligned with it.
`make tool-allow` alone changes only the gateway policy. Re-run
`govern-tools` (or widen `toolNames` yourself) if the agent should *use*
newly allowed tools. The allowlist is the governance boundary either
way.

The probe scripts read `GOVERNED_SECRET` (default `kaimahi-tools-token`)
and `UPSTREAM` (default `kagent-tools`) from the environment, and
port-forward on `GATEWAY_PORT` (`18081` for the denial probe). If you
run two clusters at once, see the port note in [aks.md](aks.md).

## Verified how

Asserted keylessly in CI on every PR: the governed tool call through the
gateway with the probe-ConfigMap proof from [tools.md](tools.md), the
`allowed 200` audit row, a non-allowlisted call denied `-32001` and
audited `denied 403`, and custody (the agent-side Secret matches
`^kmh_[0-9a-f]{64}$`, the RemoteMCPServer references the Secret via
`headersFrom` with no inline value, and discovery projects exactly the
allowlist). Additionally live-verified and unit-tested: an empty
allowlist denying even the default tool, and the projection hiding 7 of
the 8 tools the upstream offers.

## Operational notes

- The gateway shares the proxy's lifecycle. `make plane` rebuilds and
  rolls both. The image tag in
  [`k8s/plane/proxy.yaml`](../k8s/plane/proxy.yaml) moves with each
  release of the plane, so a stale side-loaded image can never satisfy a
  newer manifest under `imagePullPolicy: Never`, and `make plane` always
  restarts the deployment so a same-tag rebuild takes effect.
- `make govern-tools` is idempotent and ordered so discovery never sees
  an empty projection by accident: credential → allowlist → the
  RemoteMCPServer (waits Accepted) → agent patch (waits Ready).
- If the RemoteMCPServer sits at `Accepted=False` right after
  `make plane`, the first reconcile raced the proxy rollout. It
  self-heals within a minute, the same behaviour the chart's own server
  shows.
- Argument declarations and standing constraints are read at boot from
  the same ConfigMap as the rest of the table, so `make plane` after
  editing them (the mount is subPath, which never live-updates). A
  malformed declaration or constraint refuses the config at load — the
  pod says so and the old replicas keep serving.
- The allowlist is per-credential, not per-(credential, upstream). With
  two upstreams in the table, a credential's allowlist applies across
  both; scope credentials per upstream (the Slack agent has its own) and
  do not share one credential across upstreams with different tool
  vocabularies.
- The audit trail is demo-durable like the ledger: it survives pod
  restarts via the Postgres PVC, and `make down` destroys it.

## Limitations

- Application-layer only. A pod that bypasses the gateway is not
  constrained by it; NetworkPolicy is unbuilt as of this doc.
- Argument policy governs INPUTS. There is no filtering or redaction of
  tool RESULTS in this project, and none is implied: a governed call's
  answer is relayed as the upstream wrote it.
- An internet-facing upstream is reached only through the hardened
  dialer, and only when marked `internet: true` in the table; the
  network allowance for it is opt-in. What that does and does not bound
  is in [hosted-upstreams.md](hosted-upstreams.md#the-egress-sentence).
- Discovery lag: enforcement is immediate, but what an agent sees changes
  on kagent's next reconcile.
- The consolidated status of every governed and ungoverned surface is in
  [README.md](README.md#what-is-governed-today-and-what-is-not).

## Narrowing the server itself

The allowlist decides what a credential may call. It cannot decide what a
server OFFERS — and for a hosted server this repository did not write,
that matters: GitHub's exposes 61 write tools including
`delete_repository`, and Azure DevOps' consolidates four operations into
one `pipelines_write`.

A `tool_upstreams` entry may therefore carry `extra_headers`: committed,
non-secret headers set on every forwarded call. Both of those servers
read them (`X-MCP-Toolsets`, `X-MCP-Tools`, `X-MCP-Exclude-Tools`;
Azure DevOps also `X-MCP-Readonly`), and the release agent's seams use them
to exclude every destructive tool at the source.

This is the outer of two rings, and it is the stronger one: a tool the
server never offers is not discovered by kagent's controller, not
projected onto `tools/list`, and **not reachable even by an approval** —
which is a guarantee the allowlist cannot make, because the allowlist can
be widened by a grant and this cannot. The allowlist still applies
underneath; use both.

Two rules, both enforced at config load rather than at the first call:
an extra header naming the credential slot (or `Authorization` on a
keyless upstream) is refused, and the credential is injected last
regardless. A committed header must never be able to displace a
credential held in custody.
