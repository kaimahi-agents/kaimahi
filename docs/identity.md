# Who the agent acted for, and credentials that expire

Two questions this project could not answer in a security review, and
what answering them changed.

1. **"Who did the agent do that for?"** The ledger and the tool audit
   keyed on the credential and nothing else. We could say *ap-agent
   spent $4.10* and never *on whose behalf*. The only human anywhere in
   the data was `decided_by` on an approval — the **approver**, never
   the requester.
2. **"When does that token stop working?"** It didn't. A governed
   credential was bounded by allowlist, budget and standing constraint,
   but never by **time**, and lived until someone deleted its row —
   which is the one thing nobody does.

Assumes: a plane from [spend.md](spend.md). The identity half is most
visible on the inbound path ([inbound.md](inbound.md)), where a person
demonstrably triggers a run.

## Part 1: identity on the call

### Where the identity comes from, and what it is worth

The plane vouches for what **the plane itself observed at its own
door**, and for nothing else.

The one door where a person is visible is inbound. A Slack
`app_mention` names the user who typed it, and Slack's request
signature — which the bridge verifies before it does any work — is what
makes that name a claim worth recording. That is the same evidence the
approver list is checked against, so this reuses the vocabulary approvals
from Slack already established rather than inventing a second one:

| Value | Means |
|---|---|
| `slack:U0123ABC` | a person, vouched for by the signature the bridge verified |
| `none` | **there is no person.** No run was open for this credential (an operator typed `kmx agent chat`), or the run that was open came from a source that names nobody (a signed webhook). A complete answer |
| `unknown` | **the plane cannot say.** Two runs overlapped on one credential, or the attribution read failed. The attribution was lost — this is never a claim that nobody was there |
| `legacy` | the row was written before attribution existed. Backfill only; migration `00009` closed the class |

Those last three are deliberately different words. An empty column that
could mean *nobody* or *we lost it* would be worse than no column at
all, which is why there is no empty case: `acted_for` is `NOT NULL`, its
values are constrained by the schema, and a writer that resolves nothing
gets `unknown` — never a false `none`.

**What is deliberately not done:** the agent is not asked. A header the
agent set would be a claim by the thing being governed, and forking
kagent to add one the plane could trust is exactly what the prime
directive exists to stop. Nothing here is an identity the plane cannot
substantiate.

### How a call is joined to a person

The agent pod authenticates to the proxy and to the gateway with its
credential and nothing else, so the correlation has to be one the plane
owns end to end. It is a **run**:

```text
Slack mention (signed)          the plane                         the agent
       │                             │                                 │
       │  app_mention, user U0123    │                                 │
       ├────────────────────────────▶│ verify signature, channel,      │
       │                             │ grant, budget                   │
       │                             │                                 │
       │                             │ OPEN run(s): acted_for=slack:U0123
       │                             ├── A2A message/send ────────────▶│
       │                             │                                 │ model call  ──▶ ledger row  (slack:U0123)
       │                             │                                 │ tool call   ──▶ tool audit  (slack:U0123)
       │                             │◀── task completed ──────────────┤
       │                             │ CLOSE run(s)                    │
```

The bridge opens the run before the A2A call and closes it when that
call returns, so every governed call the agent makes in between falls
inside the window. At each enforcement seam the resolution is:

- no run open for this credential → `none`
- exactly one → that run's actor, plus its id as provenance (`run_id`)
- two or more → `unknown`

**One run per credential, not per turn.** An agent authenticates with
*two* credentials: it spends model tokens under its budget credential
and calls tools under its gateway credential. A hook therefore declares
both (`budget_credential` and `tool_credential`), and the bridge opens a
run for each — all or nothing. A run opened on only one of them would
put the person's name on half the turn and `none` on the rest, which
reads as *nobody did that part*: precisely the false claim this exists
to remove.

A run expires a minute past the invoke timeout, so a replica that dies
mid-turn cannot leave an open run poisoning every later call for that
credential — the same discipline spend reservations use.

### Reading it

`acted for` is the last column on the ledger, the tool audit and the
inbound trail:

```console
$ make ledger
created (UTC)       credential   upstream  model                in    out  cents source   status acted for
2026-09-03T20:55:41 hello-world  ollama    qwen2.5:3b          724     35      0 free     200    slack:U0123ABC
2026-09-03T20:54:12 hello-world  ollama    qwen2.5:3b          371     25      0 free     200    none
```

```console
$ make tool-audit CRED_TOOLS=hello-tools
created (UTC)       credential   upstream     method       tool                     decision status detail                                       call                                         acted for
2026-09-03T20:55:36 hello-tools  kagent-tools tools/call   k8s_get_resources        allowed     200                                              k8s_get_resources: (…) [3494fcafa57a]        slack:U0123ABC
```

Both rows above are the same agent turn, under two different
credentials, and both name the person who triggered it.

### What this makes possible, and what it is not

Per-**person attribution** is what shipped: you can ask what one person's
requests cost, and every denial names who was refused.

Per-person **budgets** are not built. The schema makes them possible —
`ledger_entry.acted_for` is what you would group by — but making them
*exact* would mean carrying the actor into the locked check-and-reserve
transaction that admits spend, so caps could be enforced per actor the
way they are enforced per credential today. That is a piece of work in
its own right, not a side effect, and nothing here pretends otherwise.

### Privacy

The Slack user id and nothing else. No name, no email, no profile, and
no lookup against Slack to get one. These tables are in every `pg_dump`
(`make backup`), so an identifier is what belongs in them; a profile
does not. The schema enforces the shape.

## Part 2: credentials that expire

### The rules

- Every credential issued from now on **has a deadline**. `make govern`
  and `make govern-tools` apply the plane's default (30 days); the admin
  surface offers **no way to ask for "never"**. Only `kmx govern --ttl`
  names a lifetime at issue — `kmx tools govern` has no `--ttl`, so a
  gateway credential always takes the default and is moved afterwards
  with `kmx credential renew`.
- A credential with **no** expiry is the **legacy class**: issued before
  this existed, and still valid. Expiring a running estate at migration
  time would be an outage, not a control. The class can only shrink.
- Expiry is enforced at **every seam that authenticates a credential** —
  the LLM proxy, the MCP gateway, and the inbound door (both for the
  hook's own credential and for the target agent's, so an event is never
  admitted and a grant use never burned for a turn that could not
  spend). Each refusal is **audited on that seam's own trail**, exactly
  like every other refusal.
- The expired credential still **resolves**. It is not filtered out of
  the lookup, which would answer *unknown token* and send an operator
  hunting the wrong problem.

### Seeing it coming

A credential that expires silently at 3am is an outage nobody
diagnosed. Three places say it first:

```console
$ make credentials
credential       cap cents  cap tokens   expires (UTC)          state     created (UTC)
hello-world      -          -            2026-09-03T22:56:34    EXPIRING  2026-09-03T20:52:41
hello-tools      -          -            2026-10-03T20:53:10    ok        2026-09-03T20:53:10
inbound-demo     -          -            -                      no expiry 2026-08-30T09:14:02
```

Soonest deadline first, so the one about to strand an agent is at the
top. `EXPIRING` is the week's warning window; `no expiry` is the legacy
class, named rather than left blank so it does not read as a bug.

`make grants` carries the credential's deadline as its last column too:
a grant that outlives the credential it was given on is a promise the
plane cannot keep, so the two are read side by side.

And in Prometheus ([operations.md](operations.md#metrics)):
`kaimahi_credential_expires_in_seconds{credential="…"}` (negative once
it already has) and `kaimahi_credentials_without_expiry` — a gauge whose
job is to trend to zero.

### The refusal

```console
$ make chat
… expired credential "hello-world": it expired at 2026-09-03T19:56:30Z;
  renew it with 'make credential-renew NAME=hello-world TTL=720h', or
  re-issue the credential and re-point its Secret
```

The fault, the time, and the fix, in the message an operator will
actually be looking at. The denial is a ledger row (`denied 403`) under
that credential, so it is in the audit trail and in the metrics.

### Renewing, and rotating

```bash
make credential-renew NAME=hello-world TTL=720h     # or kmx credential renew
```

Renewal moves a **date**, not material. The token does not change, so no
Secret has to be rewritten and no credential bytes travel.

**Rotating the material** is what it always was: issue the credential
again (`make govern`), which mints a fresh token and pipes it straight
into the agent-side Secret. Renewal is not a substitute for rotation;
it buys time on the same token.

### After you write a credential, "Accepted" is not yet an answer

kagent's `Accepted` condition is a **cached reconcile result**. kagent records
it when it last tried to reach an upstream and does not retry on its own, and a
Secret mounted into a pod is not updated the moment it is written — the kubelet
refreshes projected Secrets on its own sync period. So for some minutes after a
credential is rotated, that condition still carries the verdict reached against
the credential *before* it.

Written wrongly, that reads as `Accepted: yes` for a credential that cannot be
used at all, and several minutes of "it worked" is worse than an error, because
you move on.

kmx does not guess whether the new credential works. It declines to reuse an
answer that was about something else:

- **After kmx writes a credential**, it records what the seam said *before*
  the write, asks kagent to look again, and then requires the verdict to have
  **moved past** that — the API server's clock compared only with itself, so no
  disagreement between your machine's clock and the cluster's can make a
  verdict kagent never revisited look like a new one. If kagent rejects the
  credential, you get kagent's own words. If it has not looked in time, you get
  `unknown` — not `accepted`, which would be the lie, and not `rejected`, which
  would send you to fix something that may be fine.
- **`kmx status` changed nothing**, so it has no write to compare against and
  must not invent one. It reports the verdict as it stands, and publishes *when*
  it was reached:

  ```
  NAME         READY  ACCEPTED         MODEL CONFIG     TOOL SERVER
  hello-world  yes    yes (12s ago)    governed-ollama  kaimahi-tools

  Accepted is what kagent decided when it last looked, not a live check: a credential
  written since then has not been tested, however old that answer is.
  ```

  A condition kagent has recorded nothing for reads `unknown` rather than `-`:
  it has not said no, it has said nothing. That is the same distinction the
  governance counts draw between `none` and `unknown`.

A pod's `Ready` condition is genuinely live and is left alone; only the cached
kagent verdicts carry an age.

## Verified how

| Claim | How |
|---|---|
| The three attribution answers stay distinguishable; an unclosed run stops counting; both trails carry the actor; the schema refuses an actor outside the vocabulary | Postgres-backed store tests, CI on every PR (`go-plane`) |
| An expired credential is refused, audited and named at the proxy, the gateway and the inbound door; a lost attribution reads `unknown` | package tests, CI on every PR |
| An operator-driven turn is `none`, and no row is ever `unknown` or `legacy` | e2e (`e2e-spend`), CI on every PR |
| A credential's deadline is visible; an expired one refuses `make chat` with the operator message; the refusal is ledgered; a NULL expiry still works; renewal restores service | e2e (`e2e-spend`), CI on every PR |
| A signed Slack mention from a person triggers a run, and the ledger, the tool audit and the inbound trail all name them — across two credentials — then read `none` once the run closes | e2e (`e2e-tools`), CI on every PR |
| `make backup` / `make restore` round-trip across both migrations | e2e (`e2e-resilience`), CI on every PR |

## Limitations

- **Only the inbound door names a person.** An operator-driven turn is
  `none`, honestly and by design: `kmx agent chat` is authenticated by
  cluster credentials, not by an identity the plane holds. Workload or
  OIDC identity is not built.
- **Overlapping runs on one credential resolve to `unknown`.** Two
  events for the same agent in flight together cannot be told apart from
  the data plane, so the plane says so instead of guessing. Nothing is
  denied because of it — attribution describes a call, it does not
  authorise one.
- **Attribution is a window, not a token.** It is exact for a turn the
  plane itself started and held open. A call an agent makes after its
  turn has returned is `none`, correctly.
- **Renewal does not rotate.** A compromised token is not fixed by
  moving its deadline; re-issue it.
- **No per-person budgets, and no per-person policy.** Attribution only.
