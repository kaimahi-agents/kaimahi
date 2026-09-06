# Approvals and bounded grants

Budgets govern what an agent spends and the gateway governs what it
does. Approvals add the human to the loop: **a denied action files a
pending approval request, and a CLI approval mints a bounded grant**
that widens enforcement exactly as far as the human said, for exactly
as long. The bound can be an expiry, a use count, or both.

Assumes: the plane from [spend.md](spend.md), and for the tool demo the
gateway wiring from [tool-governance.md](tool-governance.md).

The model is **deny-and-retry**: no held-open calls, no approval flow
inside MCP itself. The agent's denial says a request was filed; the
operator decides; the agent (or the operator) tries again.

An approval is about a **call**, not a verb: "may pay invoice
INV-88134, 32,550 cents, to MER-4471" rather than "may call
pay_invoice for the next ten minutes". And a credential can carry
**standing constraints** so routine calls need no approval at all. Both
are below.

## The cycle

```text
  agent / probe                    plane                        operator
       │  tools/call k8s_get_events  │                              │
       │ ───────────────────────────▶│ denied (allowlist)           │
       │ ◀─── JSON-RPC -32001 ────── │ + approval request FILED     │
       │      "request filed"        │   (deduped while pending)    │
       │                             │                              │ make approvals
       │                             │ ◀──── make approve ID=… ──── │ TTL=60s USES=1
       │  tools/call k8s_get_events  │       (bounded grant)        │
       │ ───────────────────────────▶│ ALLOWED via grant            │
       │ ◀────── tool result ─────── │ (use consumed, audited)      │
       │  …bound expires/exhausts…   │                              │
       │  tools/call k8s_get_events  │                              │
       │ ───────────────────────────▶│ denied again — an expired    │
       │                             │ grant is not a grant         │
```

## The approval binds the call

A denied `tools/call` files a request carrying two things derived from
the call's canonical arguments (the gateway parses each message once,
into one normalized form that feeds the policy decision, the digest, the
audit and the bytes forwarded upstream alike):

- a **digest** — the sha256 of the tool name and its *policy-relevant*
  argument fields, which the tool declares in the upstream table
  ([tool-governance.md](tool-governance.md#declaring-what-arguments-mean));
- a **summary** — the line a human reads:
  `pay_invoice: invoice_id INV-88134, amount_cents 3255000, payee_id MER-4471`.

Approving mints a grant **welded to that digest**. The gateway admits a
call only when the digest of its declared policy fields matches a live
grant for that credential and tool. A mismatch is a denial that files
its **own** request — never a silent pass, never a re-use of the earlier
grant, and the earlier grant's uses are untouched.

```text
  agent ──▶ pay_invoice 48,000 to MER-4471   ▶ DENIED, request A filed
  human ──▶ make approve ID=A                ▶ grant welded to call A
  agent ──▶ pay_invoice 48,000 to EVIL-1     ▶ DENIED (grant A is not this call),
                                               request B filed, A untouched
  agent ──▶ pay_invoice 48,000 to MER-4471   ▶ ADMITTED under grant A
```

Consequences worth stating plainly:

- **Two attempts with different policy-relevant arguments file two
  requests.** They once deduped into one, and one approval covered both.
  Genuine repeats of the *same* call still collapse into a single pending
  request.
- **`make approvals`, `make grants`, `make tool-audit` and `make
  approval-audit` all show the call**, and the audit records the digest
  and summary on the denial *and* on the admitted call — so the call a
  human approved and the call that ran are provably the same one.
- **The audit never carries undeclared arguments.** The summary is built
  from declared fields only, scalars only, one bounded printable line:
  these tables are in every `make backup`. (`internal/redact` scrubs
  known secret *values* from logs; it is not a business-data redactor and
  is not used here.)
- **A tool request that names no call cannot be approved.** `make
  request KIND=tool SUBJECT=… ARGS='{"…"}'` names one; without `ARGS` it
  files the *argument-less* call, never "any call" — with one exception,
  and it comes from the declaration rather than the request: a tool that
  declares `policy_fields: []` has said no argument is policy-relevant,
  so every call to it has the same digest and a grant for one admits any
  arguments. Grants minted before approvals bound arguments (the
  migration's closed legacy class) stay verb-level and keep working;
  nothing can create another, and `make grants` labels them
  `verb-level (legacy)`.

## Standing constraints: the calls that need no approval

An approval per routine call would be theatre. A credential may instead
carry declarative **bounds** on a tool's declared fields — the accounts-
payable case is *"may call `payment_schedule` when `amount_cents` is at
most 1,000,000 and the payee is one we know, and never otherwise"*
(the shipped one is the amount clause alone; the demo it drives is
[ap-demo.md](ap-demo.md)):

```json
"standing_constraints": {
  "ap-agent": {
    "payment_schedule": [
      {"field": "amount_cents", "op": "lte", "value": 1000000},
      {"field": "payee_id", "op": "in", "values": ["MER-4471"]}
    ]
  }
}
```

- A call **inside** the bounds proceeds with **no approval and no
  grant**, audited as allowed with `within standing constraint` and the
  call's own summary.
- A call **outside** them is denied and files a request, exactly as an
  unlisted tool does — and that request is welded to the call, so the
  human approves *that* transaction.
- Where a constraint exists it **binds**: the static allowlist no longer
  admits that tool for that credential, because "and never otherwise" is
  the point. A grant is still the way through, one call at a time.
- The vocabulary is small on purpose — `eq`, `ne`, `lt`, `lte`, `gt`,
  `gte`, `in`, `not_in` over a tool's declared fields, all rules ANDed.
  There is no expression language, and there will not be one.
- Everything is refused at **load**: an unknown op, a non-integer numeric
  bound, an empty rule list, or a constraint on a field the tool does not
  declare. A rule the plane cannot enforce is never quietly ignored.
- Evaluation **fails closed**: a missing field, a wrong-typed value, or a
  non-integer where an integer bound applies all count as outside.

## Grants are bounded, or they are not grants

- **At least one bound is required**: expiry (`TTL=`) and/or use count
  (`USES=`). An unbounded grant is a config change (edit the allowlist
  or the budget), not an approval, and the plane refuses to mint one.
- **Expiry and exhaustion are enforced at decision time, in SQL.** The
  gateway and meter never act on a cached grant, and no cleanup job is
  needed for correctness: an expired grant stops matching the liveness
  predicate.
- **Budget grants carry an `AMOUNT`** (tokens or cents, matching the
  exceeded cap). The effective cap is `cap + Σ(live grant amounts)`. A
  use is consumed only by a request that needed the grant; under-cap
  traffic never burns one.
- **Tool-grant uses are consumed per admitted `tools/call`, before the
  forward.** An upstream failure therefore burns the use. That is the
  conservative direction, and it has a visible consequence on the Slack
  path ([slack.md](slack.md)).

## Demo 1: widening a tool allowlist

The tool server stays read-only throughout. Grants widen *which* read
tools a credential may call, never the server's posture.

```sh
make govern-tools                                   # gateway wiring in place
bash scripts/tool-denial-probe.sh k8s_get_events    # denied; request filed
make approvals                                      # copy the ID
make approve ID=<uuid> TTL=60s USES=1
bash scripts/tool-call-probe.sh k8s_get_events '{"namespace": "default"}'   # succeeds
bash scripts/tool-denial-probe.sh k8s_get_events    # denied again (use consumed)
make tool-audit                                     # allowed row says: granted <grant-id>
```

`tool-call-probe.sh` does the full MCP handshake (initialize,
initialized, tools/call) through the gateway with the `hello-tools`
credential. The `tools/list` projection includes live-granted tools, so
visible means callable right now. The agent's own `toolNames` selection
is untouched by grants, which is why a granted tool is exercised via the
probe here: the static allowlist plus `make govern-tools TOOLS=…`
remains the way to widen what the *agent* wields.

## Demo 2: budget overage

```sh
make budget CAP_TOKENS=1        # below any real chat
make chat                       # fails: "monthly token budget reached;
                                #  approval request filed — run 'make approvals'"
make approvals                  # copy the ID (kind=budget, subject=tokens)
make approve ID=<uuid> TTL=5m USES=1 AMOUNT=100000
make chat                       # completes; make ledger shows the overage rows
make chat                       # denied again — the single use is consumed
make budget CAP_TOKENS=100      # restore whatever cap you actually want
```

## Queue mechanics

- **Auto-filed**: a gateway tool denial files `(credential, tool, call)`;
  a budget-cap denial files `(credential, tokens|cents)`. Deduped per
  `(credential, kind, subject, call digest)` while pending, so retry
  loops cannot spam the queue — but two different calls are two
  requests. A filing failure never un-denies (denial is the safe
  state), and the enforcement audit row still writes.
- **Explicit**: `make request KIND=tool SUBJECT=k8s_get_events
  ARGS='{"namespace": "default"}'`. Tool requests default to the
  `hello-tools` credential, budget requests to `hello-world`; override
  with `CRED=`. `ARGS` names the call to pre-approve (the plane computes
  the digest with the gateway's own code, so this request and the
  agent's retry are the same call); omitted, it files the argument-less
  call. A tool declaring `policy_fields: []` is verb-level by
  declaration, so its grants admit any arguments.
- **Decide**: `make approvals`, then `make approve ID=… [TTL=…] [USES=…]
  [AMOUNT=…]` or `make deny ID=…`; or, from Slack, `@kaimahi approve <id>`
  ([below](#deciding-from-slack)). A decided request is immutable; fresh
  denials file fresh requests.
- **Inspect**: `make grants` (liveness computed by the same predicate
  enforcement uses) and `make approval-audit` (requested / approved /
  denied, with bounds and **who decided**: `admin` for the CLI, `slack:<user
  id>` for a Slack command; the approvals' own append-only trail). Approve
  and deny commit the grant, the status change and the audit row in one
  transaction, so a decision that cannot be recorded does not happen.

## Deciding from Slack

The queue is no longer CLI-only. When a request is filed, by any of the
sites that file one (a gateway tool denial, a budget-cap denial, the
inbound door, `make request`), the plane posts into the pinned Slack
channel that a decision is waiting, and an approver decides it from
Slack, as themselves:

```text
  hello-slack ──▶ gateway: conversations_add_message DENIED, request filed
                     │
                     ▼  (the plane's OWN credential, through its own gateway)
  #channel  ◀── "Kaimahi approval request `3f1c…`: credential hello-slack was
                 denied tool conversations_add_message. To decide, mention
                 the bot: @kaimahi approve 3f1c… [uses=N] [ttl=15m] …"
  approver  ──▶ "@kaimahi approve 3f1c9a2e uses=1 ttl=15m"
                     │ signature ✓, channel ✓, approver list ✓
                     ▼
  #channel  ◀── (in the thread) "approved request 3f1c…: grant 7b0d… uses=1
                 expires=…"          make grants → decided by slack:U…
```

**The notification** goes through the governed posting path. The plane
holds a credential of its own, `kaimahi-plane`, issued by `make
notify-slack` the way `make govern-slack` issues the agent's, and
allowlisted to the posting tool only. That is configuration rather than
a grant, because the plane is the trust root; what it buys is that the
plane's own message is authenticated, allowlisted, pinned to the one
channel (the same Secret key that restricts the MCP server and bounds
the inbound hook) and **audited** like any agent's: `make tool-audit
CRED_TOOLS=kaimahi-plane` shows a row per attempt. The message carries
the request id, the credential, the kind and subject, and the command to
type. It is asynchronous and a convenience: a post that fails never
un-files the request. A refusal the plane can see (the gateway refused
before forwarding, the gateway could not be dialled, Slack said no) is
retried, three attempts in all; anything that may have happened after
the post went out (a timeout, a reset, the gateway's own 502, which it
answers for a dial failure and for a reset after delivery alike) is
recorded and **not** retried, because a notification posted twice is
the double-post the rest of this repo is careful to avoid, and `make
approvals` is always there.

**The command.** `@kaimahi approve <id> [uses=N] [ttl=D] [amount=N]` or
`@kaimahi deny <id>`, as an `app_mention` on the existing `slack-events`
hook ([inbound.md](inbound.md#slack-events-the-loop)). `<id>` is the
request id or its first block (8 characters; a longer prefix if that is
ambiguous). The command is recognised after Slack's signature and the
channel allowlist have been checked and *before* the grant gate: deciding
a request needs no inbound grant (or approving would need an approval),
runs no agent, and spends nothing. Bounds omitted get the hook's defaults,
**one use and fifteen minutes** (`slack_default_uses` /
`slack_default_ttl` in the hook table): an approval typed into a chat is
the least deliberate form an approval takes, so its default is the
tightest grant that still lets the retried action through. `uses=` or
`ttl=` on the command wins; a budget request needs `amount=`; the store
still refuses a grant with no bound at all. A decided request is
immutable: the same command again answers "already approved by
slack:U… at …". The outcome is posted back in the mention's thread
through the same governed path.

**Who may decide** is a Secret-mounted file of Slack user ids,
`make slack-approvers` (stdin only: a user id is a workspace identifier
and never lands in this repo), named in the hook table as
`slack_approvers_file` next to the channel file and read per command.
Channel membership is not authority: anyone in the room can ask the
agent a question; only a listed user can decide. A command from anyone
else is refused (403) and audited with their id; a list that is missing,
empty or malformed fails every command closed (503) and changes nothing
about questions. A bot-authored command, including the plane's own
notification arriving back as an event, is ignored by the loop guard
before it is ever parsed.

**Identity.** `decided_by` is on the request, the grant and the audit
row: `admin` for the CLI (the admin bearer, the only writer that port
admits) and `slack:<user id>` for Slack. `make grants` and `make
approval-audit` show it.

```sh
make slack-approvers          # paste the approver ids on stdin
make notify-slack             # the plane's own credential, posting tool only
# then, in the channel, after a denial has been announced:
#   @kaimahi approve 3f1c9a2e uses=1 ttl=15m
make grants                   # decided by slack:U…
make approval-audit
make inbound-audit HOOK=slack-events   # the command row, and any refused ones
```

On a kind cluster there is no Slack to type into; `make slack-mention
SLACK_USER=U0EXAMPLE COMMAND='approve 3f1c9a2e'` fires a correctly signed
synthetic mention at the hook the way `make inbound-fire` does, which is
how CI asserts the whole path keyless. The notification's post then
answers 502 (the Slack upstream does not exist there), once, and its
audit row under `kaimahi-plane` is the proof it went through the
governed path.

## Operational notes

- Approvals live in the same process, Postgres and admin port as the
  rest of the plane. Nothing new to deploy; re-run `make plane` to roll
  a new image, and migrations run idempotently at startup.
- Grant rows are never deleted. Exhausted and expired grants stay
  visible in `make grants` as `live=no` history. Demo-durable via the
  PVC, like the ledger.
- `make up` preserves a governed agent's modelConfig and gateway wiring
  across re-runs, so a grant demo survives a re-provision.
  `make use PRESET=ollama` and `make ungovern-tools` are the explicit
  ways back.

## What this promises, and what it does not

The plane does **not** stop an agent being manipulated. A prompt-injected
agent can and will attempt whatever it was talked into. What the plane
stops is a manipulated agent **acting outside the call a human
approved**: the attempt is denied, it files a request whose summary shows
the changed amount or the changed payee, and it is audited. It cannot
ride an earlier approval, because that grant is welded to a different
call.

Two more honest edges. Argument policy governs *inputs*, not outputs —
there is no filtering or redaction of tool RESULTS in this project, and
none is implied. And a tool that declares nothing binds its whole
canonical argument object: exact, but brittle, since an LLM re-emitting a
semantically identical call is not byte-stable. Declaring the fields that
matter is what makes "approve, then it proceeds" deterministic.

## Limitations

- **Routing is Slack only.** No email or ticket routing; a request is
  announced in one pinned channel, and only when `make notify-slack` has
  issued the plane's credential. The notification is best-effort by
  design (a known refusal retried three times, an ambiguous failure
  recorded and left); `make approvals` is the queue of record.
- **Identity is a Slack user id, not a name.** `decided_by` records
  `slack:<user id>`; display names are not resolved, and the CLI path
  records `admin`, since the admin bearer is not a person.
- **A grant does not widen the agent's own `toolNames`.** For a kagent
  agent, a granted tool becomes usable through discovery, and what the
  agent *sees* lags: enforcement is immediate, but the agent's
  discovered tool list updates on kagent's next RemoteMCPServer
  reconcile. [slack.md](slack.md) explains this in full, because it is
  where the lag shows up on the demo path.
- **A burned use does not guarantee a delivered result**, since the use
  is consumed before the forward. The audit row says what happened.
- **Constraints and declarations are config, not inference.** They live
  in the committed table, are refused at load when malformed, and the
  plane never guesses which arguments matter. Fields are top-level
  argument names; a value nested inside an object is not addressable.

The single table of what is and is not governed across the whole plane
is in [README.md](README.md#what-is-governed-today-and-what-is-not).
