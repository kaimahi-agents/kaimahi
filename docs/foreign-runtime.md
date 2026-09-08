# Governing a runtime this repository did not write

Kaimahi is positioned as horizontal: a governance plane over an agent
runtime, where the runtime is somebody else's problem. Every agent it has
ever governed was deployed by kagent. This document is the result of
testing that positioning with a client that is not kagent — curl and `sh`
in a pod — against a live plane, and writing down the interface the
project actually offers a runtime it did not write.

**The result is a qualified confirmation.** The enforcement seam is
generic and the list of what a runtime must be told is short: five
things, none of them a Kubernetes object. Two of the four questions came
back clean. One came back as a single line of YAML. One came back as a
real defect: a call the plane cannot attribute is recorded as a call with
**no person behind it**, which is a claim the plane cannot support.

Everything below was measured on a kind cluster running the plane, the
fixture ERP and **no kagent at all** — no controller, no `Agent`, no
`RemoteMCPServer`, no CRDs installed. The transcript is at the end.

## What a runtime must be given

This is the whole list. It has never been written down before, and it is
the interface this project offers a foreign runtime.

1. **A credential.** A `kmh_` bearer token the plane issued —
   `kmh_` plus 64 hex characters, shown exactly once at issue, stored by
   the plane only as a sha256. It goes in `Authorization`; the `Bearer `
   prefix is optional on both seams.
2. **The tool seam's URL.**
   `http://kaimahi-mcp-gateway.kaimahi:8081/upstream/<upstream>/mcp`.
   `POST` carries every JSON-RPC message, `DELETE` ends a session.
3. **The name of the upstream** it is allowed to call — an operator-owned
   key in the plane's table, not a URL. The real tool server's address
   never reaches the client, and an unknown name is a `403`.
4. **The model seam's URL**, if it spends model tokens:
   `http://kaimahi-proxy.kaimahi:8080/upstream/<name>/<path>`, where
   `<path>` must equal exactly the one path that upstream declares. The
   body is OpenAI-shaped and must carry `model`.
5. **A network position the plane admits.** Today that means a pod in a
   namespace named `kagent`. See below; this is the one that costs a
   change.

Not on the list, and this is the point: no CRD, no controller, no
sidecar, no label, no service account, no Kubernetes API access, no
Kaimahi library, no SDK. The client used here is about a hundred lines
of `sh`, and `kubectl get crds` on the test cluster returned nothing at
all.

Two smaller facts a client author needs:

- **The protocol scope is tools only.** `initialize`,
  `notifications/initialized`, `tools/list` and `tools/call` are relayed;
  `ping` is answered by the gateway itself; every other method is
  refused. JSON-RPC batches are refused. A duplicated JSON key anywhere
  in the message is refused rather than collapsed.
- **The client must tolerate either framing.** A relayed response comes
  back as the upstream sent it, plain JSON or SSE; a projected
  `tools/list` always comes back as `application/json`.

## What the operator must do on the plane side

Unchanged from the kagent path, because none of it is about the runtime:
deploy the plane; put the tool server in the upstream table (the
committed one, or the operator overlay `kmx tools add` writes); declare
each tool's `policy_fields`, which is what an approval's digest and the
audit summary bind to; issue the credential; set the tool allowlist —
absent means nothing is callable; optionally set a budget and standing
constraints. The admin surface is on a port no Service exposes, so
cluster credentials gate every one of those operations before the admin
token does.

One thing that is worth saying plainly because the kagent path hides it:
**the credential's Secret can live anywhere.** The plane has no notion of
where a token is stored — it looks up a hash. `kagent` is the default
`kmx` writes to, and the scripts take `SECRET_NAMESPACE`. The Secret is
a custody convention for a runtime that resolves headers from Secrets; a
runtime that does not needs only the token string.

## Which parts are kagent-shaped

### Discovery is not

`discovered ∩ toolNames` — the rule that makes a kagent agent's tool list
a selection rather than a grant — is kagent's controller reconciling a
CRD. There is no implementation of it in this repository, and nothing in
the plane depends on it. What the plane does is different and
independent: the gateway enforces the allowlist on `tools/call` and
separately projects it onto `tools/list`.

A foreign client gets that projection with no CRD and no controller. In
the transcript, a client holding a credential allowlisted for six tools
asked a nine-tool server for its tools and was told about seven — the six
plus the one its credential carries a standing constraint on, which is
callable right now and therefore visible.

**And a client that never discovers is governed anyway.** The gateway
holds no session state; every message is decided on its own. A bare
`tools/call` with no `initialize`, no session and no listing was allowed
when the tool was on the allowlist and refused when it was not, both
audited. Skipping discovery buys a client nothing. That is the strongest
single piece of evidence for the horizontal claim.

### Placement is, by exactly one selector

The plane's NetworkPolicy admits ingress to both data ports from one
place:

```yaml
ingress:
  - from:
      - namespaceSelector:
          matchLabels:
            kubernetes.io/metadata.name: kagent
    ports: [8080, 8081]
```

A runtime anywhere else is dropped by the CNI before the gateway sees the
packet. It presents a perfectly valid credential and gets a connection
timeout — and **there is no audit row**, because nothing arrived. That
failure is silent from the plane's side and, from the client's side,
indistinguishable from the gateway being down.

The experiment was two identical pods — same image, same script, same
mounted credential shape — one in `kagent`, one in `foreign-runtime`.
The first reached the gateway and made governed calls. The second timed
out, while reaching the Kubernetes API server from the same shell to
prove its networking worked. Adding a second `namespaceSelector` to that
one rule made the second pod behave exactly like the first; removing it
again restored the block.

Two things follow. First, **the cost of moving a runtime out of the
`kagent` namespace is one line of YAML**, which is good news for the
positioning. Second, the boundary is a namespace *name*, not a runtime:
the `kagent` namespace on the test cluster contained no kagent whatsoever
— just a pod running curl — and the plane governed it happily. The
network rule is not identifying kagent. It is saying "only from where
agents live", and the name is a stand-in for that. Whether the default is
right is an operator's call, but the file should say what it is doing.

### The inbound bridge is, deeply

The path where the plane *invokes* an agent is kagent-shaped end to end:
it dials `{base}/api/a2a/{namespace}/{agent}/`, sets kagent's session
header, and reads the token counts for the turn out of kagent's
`kagent_usage_metadata` envelope. A foreign runtime cannot be invoked by
the plane and cannot have a turn metered that way.

This matters less than it sounds, and it is important to be exact about
why: a foreign runtime's **own** model calls through the metering proxy
are metered normally, against the same budgets, with the same denial.
What is lost is the plane triggering the turn — and with it, the only
thing that produces an honest attribution.

### `kmx tools add` scaffolds into `kagent` and cannot be told otherwise

The namespace the scaffolder writes agent-side documents into is a
compile-time constant. The documents it generates that *matter* to a
foreign runtime — the upstream overlay fragment and the two
NetworkPolicies that make the tool server reachable only through the
proxy — are namespace-parameterised and fine. The one that is pinned is
the `RemoteMCPServer`, which a foreign runtime discards. It is a rough
edge in the onboarding tool, not a hole in the enforcement path.

## What a foreign runtime cannot get today

Three things. The first is a defect, the second is a scope limit, the
third is a gap nobody has needed yet.

### 1. An honest attribution — and it gets a wrong one, not a blank

Every governed row carries `acted for`. Its vocabulary is closed and
deliberate: `slack:<user id>` is a person the plane's own door
authenticated, `unknown` means **the plane cannot say**, and `none`
means — in the plane's own words — *there is no person*, "a complete
answer, not a gap".

`none` is resolved by one query: is a **run** open for this credential?
A run is a window the inbound bridge opens around an agent turn it
triggered. Nothing else in the system can open one. There is no endpoint
on any port, no admin call, no `kmx` verb, and no ops path that opens a
run; the inbound bridge is the only writer.

So a foreign runtime — triggered by its own human, through a door the
plane never saw — makes calls that are recorded as having no person
behind them. Every row in the transcript below says `none`, and the
plane's own definition of that word is a positive claim it cannot
support here. The word that would be true is `unknown`, and the code
cannot reach it in this case: absence-of-run is wired directly to `none`.

Two things make this worse than it first looks. Nothing in a tool-audit
row distinguishes a kagent agent's call from a curl's — not the client
name in `initialize`, which the gateway relays without reading, not the
user agent, not the source address. And the ledger behaves identically,
so the same overclaim lands on the spend trail.

This is a real finding and it is not fixed here; this lane changes no
behaviour. Whoever fixes it should note that the honest answer is not
obviously `unknown` either: the plane genuinely knows *nothing* about
whether a person was involved, which is a third state the schema's CHECK
constraints do not currently allow.

### 2. Being invoked by the plane, and turn-level spend metering

As above. A foreign runtime is a client of the plane, never a callee of
it.

### 3. Any position outside the cluster

All five of the plane's listeners are plain HTTP; there is no TLS
listener anywhere, and the two data seams are `ClusterIP` Services with
no ingress path of their own. A runtime that is not in the cluster has no
supported route to either seam. Nothing here is wrong — the plane was
built for in-cluster agents — but "runtime-agnostic" should not be read
as "location-agnostic".

## The transcript

A kind cluster with the plane and the fixture ERP; no kagent. Two
namespaces, `kagent` and `foreign-runtime`, each with one pod running
`curlimages/curl` and a shell script, each holding its own `kmh_`
credential allowlisted for the ERP's six read tools. The credential used
from the `kagent` namespace is named `ap-agent`, because that is the name
the committed standing constraint on `payment_schedule` binds to; nothing
about it is kagent, and no kagent object exists on the cluster.

**Placement.** Identical client, two namespaces:

```console
### from namespace kagent
--- reachability of http://kaimahi-mcp-gateway.kaimahi:8081/healthz
REACHED: ok [HTTP 200]

### from namespace foreign-runtime
--- reachability of http://kaimahi-mcp-gateway.kaimahi:8081/healthz
NOT REACHED: curl: (28) Connection timed out after 10016 milliseconds

### control, from the same shell in foreign-runtime
control: kubernetes.default.svc:443 -> HTTP 200
```

**Discovery, with no CRD and no controller.** The ERP offers nine tools;
the credential is allowlisted for six and constrained on a seventh:

```console
--- initialize
HTTP 200
--- notifications/initialized: HTTP 202
--- tools/list: HTTP 200
contract_get  invoice_get  invoice_list  payment_policy_get
payment_schedule  po_get  receiving_get
```

`dispute_open` and `vendor_notify` are not in the answer.

**The calls.** Allowed, allowed-inside-a-constraint, refused-outside-it,
refused-by-the-allowlist:

```console
--- tools/call invoice_get {"invoice_id":"INV-88134"}: HTTP 200
    "total_cents": 4800000 …

--- tools/call payment_schedule {"amount_cents":900000,"payee_id":"MER-4471"}: HTTP 200
    {"payment_id":"PAY-INV-88134","status":"scheduled"}

--- tools/call payment_schedule {"amount_cents":4800000,"payee_id":"MER-4471"}
{"error":{"code":-32001,"message":"tool call not permitted: outside the
 standing constraint (amount_cents lte 1000000); approval request filed"}}

--- tools/call vendor_notify {"vendor_id":"MER-4471"}
{"error":{"code":-32001,"message":"tool not permitted by the Kaimahi
 allowlist for this call; approval request filed"}}
```

**The audit trail**, unmodified:

```console
$ make tool-audit CRED_TOOLS=ap-agent
created (UTC)       credential upstream method     tool             decision status detail                                    call                                                              acted for
2026-09-08T13:58:39 ap-agent   erp      tools/call vendor_notify    denied      403 tool not permitted by the Kaimahi allowl… vendor_notify: vendor_id MER-4471 [9d99a3a4e9e4]                  none
2026-09-08T13:58:38 ap-agent   erp      tools/call payment_schedule denied      403 outside the standing constraint (amount…  payment_schedule: invoice_id INV-88134, amount_cents 4800000, …    none
2026-09-08T13:58:33 ap-agent   erp      tools/call payment_schedule allowed     200 within standing constraint                payment_schedule: invoice_id INV-88134, amount_cents 900000, …     none
2026-09-08T13:58:28 ap-agent   erp      tools/call invoice_get      allowed     200                                           invoice_get: invoice_id INV-88134 [ebb1d47dba1e]                   none
```

Both denials filed approval requests carrying the exact call, as they
would for a kagent agent.

**No handshake at all** — a client that never discovers:

```console
--- bare tools/call invoice_get (no handshake): HTTP 200
--- bare tools/call vendor_notify (no handshake)
{"error":{"code":-32001,"message":"tool not permitted by the Kaimahi allowlist for this call…"}}
```

**The model seam**, same client, same credential, a zero token budget:

```console
--- POST http://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1/chat/completions
monthly token budget reached; approval request filed — run 'make approvals'
HTTP 429

$ make ledger CRED=foreign-runtime
created (UTC)       credential      upstream model       in out cents source status acted for
2026-09-08T14:02:01 foreign-runtime ollama   qwen2.5:3b   0   0     0 denied 429    none
```

The budget denial stops the call and is ledgered, for a caller the plane
has never seen a runtime for.

## What this changes

Nothing, yet — this was an investigation and it deliberately built no
adapter, shim or compatibility layer, because the finding is that none of
those is what is missing. What is missing is one policy selector an
operator can already edit, and one honest word in a column.
