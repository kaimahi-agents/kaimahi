# Governing a runtime this repository did not write

Kaimahi is positioned as horizontal: a governance plane over an agent
runtime, where the runtime is somebody else's problem. Every agent it has
ever governed was deployed by kagent. This document is the result of
testing that positioning with a client that is not kagent — curl and `sh`
in a pod — against a live plane, and writing down the interface the
project actually offers a runtime it did not write.

**The result is a qualified confirmation.** The enforcement seam is
generic and the list of what a runtime must be told is short: six
things, none of them an object the runtime has to create or that the
plane reads through the Kubernetes API. Two of the four questions came
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
   `https://kaimahi-mcp-gateway.kaimahi:8081/upstream/<upstream>/mcp`.
   `POST` carries every JSON-RPC message, `DELETE` ends a session.
3. **The name of the upstream** it is allowed to call — an operator-owned
   key in the plane's table, not a URL. The real tool server's address
   never reaches the client, and an unknown name is a `403`.
4. **The model seam's URL**, if it spends model tokens:
   `https://kaimahi-proxy.kaimahi:8080/upstream/<name>/<path>`, where
   `<path>` must equal exactly the one path that upstream declares. The
   body must be a JSON object. `model` is what the ledger and the price
   gate read: a metered upstream under a cents budget refuses a model it
   has no price for, and nothing else requires the field.
5. **The plane's certificate authority.** Both seam URLs are `https`
   under an authority the plane mints for itself, so a client verifying
   against a system trust store is refused. `kmx plane` publishes the
   authority's certificate — and only its certificate — as Secret
   `kaimahi-plane-ca` in the agent namespace, key `ca.crt`. Read it with

   ```sh
   kubectl -n kagent get secret kaimahi-plane-ca \
     -o jsonpath='{.data.ca\.crt}' | base64 -d > plane-ca.crt
   ```

   and give it to the client (`curl --cacert`, `SSL_CERT_FILE`, whatever
   its HTTP library reads). It is public material: it says who to trust
   and confers nothing. The private half never leaves the plane's
   namespace and is mounted into no pod.

   **Do not skip verification instead.** A client that dials these seams
   with verification off pays for the whole certificate exercise and buys
   nothing from it, while looking from the outside exactly like one that
   verifies.
6. **A network position the plane admits.** Today that means a pod in a
   namespace named `kagent`. See below; this is the one that costs a
   change.

Not on the list, and this is the point: no CRD, no controller, no
sidecar, no pod label, no service account, no Kubernetes API access, no
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
  back as the upstream sent it, plain JSON or SSE; a `tools/list` that
  was projected always comes back as `application/json`. An upstream
  error on a listing is relayed unchanged, framing included, because
  there is nothing to project.

## What the operator must do on the plane side

Unchanged from the kagent path, because none of it is about the runtime:
deploy the plane; put the tool server in the upstream table (the
committed one, or the operator overlay `kmx tools add` writes); declare
each tool's `policy_fields`, which is what an approval's digest and the
audit summary bind to; issue the credential; set the tool allowlist —
with none set, nothing is callable except a tool covered by a standing
constraint or a live grant; optionally set a budget and those
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
independent: the gateway enforces admission on `tools/call`, and
separately projects onto `tools/list` what that credential can call right
now — its allowlist, plus tools live grants admit, plus tools it carries
a standing constraint on.

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

The proxy pod's namespace is default-deny, and the rule that gives the
two data ports — the model seam on 8080 and the tool seam on 8081 — back
to anybody admits exactly one place. Verbatim, from
`k8s/plane/network-policy.yaml`:

```yaml
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: kagent
      ports:
        - protocol: TCP
          port: 8080
        - protocol: TCP
          port: 8081
```

(The same pod carries a second ingress rule for the ops port, admitting
a scraper from a `monitoring` namespace. This one is about the data
seams.)

On a cluster whose CNI enforces NetworkPolicy — the file is explicit
that this is not a given — a runtime anywhere else is dropped before the
gateway sees the packet. It presents a perfectly valid credential and
gets a connection timeout, and **there is no audit row**, because
nothing arrived. That failure is silent from the plane's side and, from
the client's side, indistinguishable from the gateway being down.

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
agents live", and the name is a stand-in for that.

**The default is right and the wording is not.** Admitting one named
place and nothing else is the correct posture for a governance seam, and
widening it by default to admit any pod in the cluster would be worse
than the coupling it removes. What is wrong is that the rule reads as a
statement about kagent when it is a statement about a namespace, and
nothing tells an adopter that their runtime's namespace goes on that
line. A foreign runtime elsewhere is refused by the network, and the fix
is an operator's policy edit, not a product change.

### The inbound bridge is, deeply

The path where the plane *invokes* an agent is kagent-shaped end to end:
it dials `{base}/api/a2a/{namespace}/{agent}/`, sets kagent's
`x-user-id` — the header kagent turns into the session's actor — and
reads the token counts for the turn out of kagent's
`kagent_usage_metadata` envelope. A foreign runtime cannot be invoked by
the plane and cannot have a turn metered that way.

This matters less than it sounds, and it is important to be exact about
why: a foreign runtime's **own** model calls through the metering proxy
are metered normally, against the same budgets, with the same denial.
What is lost is the plane triggering the turn — and with it, the only
thing that produces an honest attribution.

### `kmx tools add` scaffolds into `kagent` and cannot be told otherwise

Every namespace the scaffolder writes is a compile-time constant except
one. The `RemoteMCPServer` goes into `kagent`; the overlay fragment and
the proxy-egress NetworkPolicy go into `kaimahi`; only the tool server's
own ingress policy takes its namespace from what the operator typed.
That is the right shape for everything except the CRD — and the CRD is
the one document a foreign runtime discards, since it exists to tell
kagent's controller where the seam is. So the pin is a rough edge in the
onboarding tool, not a hole in the enforcement path: what a foreign
runtime actually needs out of `kmx tools add` — the upstream entry and
the policy pair that make the tool server reachable only through the
proxy — lands in the right places already.

## What a foreign runtime cannot get today

Three things. The first is a defect, the second is a scope limit, the
third is a gap nobody has needed yet.

### 1. An honest attribution — and it gets a wrong one, not a blank

Every governed row carries `acted for`. Its vocabulary is closed and
deliberate: `slack:<user id>` is a person the plane's own door
authenticated, `unknown` means **the plane cannot say**, and `none`
means — in the plane's own words — *there is no person*, "a complete
answer, not a gap". (There is a fourth value, `legacy`, a closed
backfill class for rows written before any of this existed.)

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
so the same overclaim lands on the spend trail. (**Both halves of that
paragraph are now fixed**: the two trails carry `caller (claimed)` and
`from (observed)`. The client name in `initialize` is still not read,
and [identity.md](identity.md#who-called) says why — the gateway holds
no session state, and a client can skip the handshake entirely, as this
document proved.)

This was a real finding and it was not fixed here; this lane changed no
behaviour. **It has since been ruled on, and the outcome is written up
in [identity.md](identity.md#where-none-is-stretched-and-why-that-is-accepted).**
In short: the word does not change, because the honest answer is not
`unknown` either — the plane genuinely knows *nothing* about whether a
person was involved, and that is a third state the schema's CHECK
constraints do not allow. What changed instead is the second half of the
finding below: **every governed row now records who called**, so the
stretched `none` is visible on the row rather than invisible. The
imprecision is accepted, documented and bounded by there being no
supported way to reach it; if a foreign runtime becomes supported, the
ruling is void and the vocabulary gains a value.

Two things about the transcript below still stand and are worth reading
in that light: the rows show `none` for a curl client, which is the
overclaim, and they carry no caller columns, because they were recorded
before those existed. A trail captured today from the same run would
show a `ua:curl/…` claim and the client pod's address beside every one
of them. (The exact version is not recorded anywhere in this document,
so it is not stated here either.)

### 2. Being invoked by the plane, and turn-level spend metering

As above. A foreign runtime is a client of the plane, never a callee of
it.

### 3. Any position outside the cluster

The two data seams serve TLS, under a certificate authority the plane
mints for itself and no public trust store has heard of. The remaining
three listeners are plain HTTP and stay that way: the admin and
operations ports are on no Service at all, and the inbound bridge's one
public route terminates TLS at an edge.

That is a distribution problem rather than a routing one, and it is why a
runtime outside the cluster still has no supported route. Both seams are
`ClusterIP` Services with no ingress path of their own, so the address is
unreachable from outside before the certificate is even reached; and the
certificate is valid for in-cluster names and the loopback address, so it
would not answer to an external one. Nothing here is wrong — the plane
was built for in-cluster agents — but "runtime-agnostic" should not be
read as "location-agnostic".

## The transcript

Nothing in this repository reproduces what follows: no script, no
manifest and no CI job. It is the record of one run, kept because the
claims above rest on it, and every shape in it — the URLs, the ports,
the refusal messages, the constraint bounds, the ERP's nine tools — is
checkable against the tree.

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
--- reachability of https://kaimahi-mcp-gateway.kaimahi:8081/healthz
REACHED: ok [HTTP 200]

### from namespace foreign-runtime
--- reachability of https://kaimahi-mcp-gateway.kaimahi:8081/healthz
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
--- POST https://kaimahi-proxy.kaimahi:8080/upstream/ollama/v1/chat/completions
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

**Since then, on the word.** The column was ruled on rather than
changed: `none` keeps its meaning, and the row it sits on learned to say
who called, so the case where the word is stretched is visible instead of
invisible. That is the smaller of the two fixes and it was chosen
deliberately — see
[identity.md](identity.md#where-none-is-stretched-and-why-that-is-accepted)
for the reasoning, the bound that makes the remaining imprecision
acceptable, and the condition that voids it. The policy selector is
still an operator's edit and still undocumented for an adopter.
