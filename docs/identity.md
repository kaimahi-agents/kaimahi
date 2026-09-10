# Legacy reference: attribution and credential expiry

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This page preserves the meaning
> and limits of existing database columns and credentials. It does not
> define Orka identity or make legacy attribution a migration prerequisite.
> Current onboarding starts at the [documentation index](README.md).

## Identity on the call

The ledger, tool audit and inbound audit carry `acted_for`. The inbound
bridge can verify a Slack signature and record the event's user identifier;
it opens run windows around the kagent turn it invokes. It does not obtain
a verified human identity from an arbitrary model or MCP client.

[store/identity.go](../plane/internal/store/identity.go) resolves the window:

| State | Stored result |
|---|---|
| No live run for the credential | `none` |
| Exactly one live run | its actor (`slack:<user id>` or `none`) and `run_id` |
| Overlapping runs, or attribution lookup failure | `unknown` |
| Rows predating attribution | `legacy`, a closed migration backfill class |

**`none` is not evidence that no human was involved.** It is also written
for operator-driven turns and independently triggered applications whose
human the plane never saw. The code's older “there is no person” wording
must not be treated as a security conclusion. This limitation is especially
important on [foreign-runtime](foreign-runtime.md) and migration traffic;
it cannot be excused by assuming such clients never reach the seam.

## Correlation is a window, not caller identity

The bridge opens a run for the hook's `budget_credential` and, when set,
`tool_credential`, all-or-nothing, then closes them after A2A returns.
Runs expire a minute past the invoke timeout so a crashed worker cannot
leave an unlimited window. No public/admin endpoint or `kmx` command opens
such a run for arbitrary clients.

A call is correlated by **credential and time**, not by a unique invocation
token. A separate caller sharing that credential during the window can be
associated with it too. Calls after closure resolve without that run, and
concurrent windows resolve `unknown`. Attribution failure alone neither
admits nor denies traffic; budget and tool policy are separate decisions.

An approver is different from a requester: approvals record `decided_by`
as `admin` or `slack:<user id>`. An approval does not retrospectively prove
who initiated all calls using the grant.

## Who called

The ledger and tool audit also carry two deliberately different columns:

| Column/value | What can be concluded |
|---|---|
| `caller (claimed)` / `ua:<value>` | caller-supplied User-Agent, unverified and spoofable |
| `from (observed)` | socket peer address, not a person or stable workload identity |
| claimed `none` | caller supplied no identification |
| observed `unknown` | peer address could not be read |
| `unrecorded` | writer did not provide the observation |
| `legacy` | row predates these columns |

Source: [store/caller.go](../plane/internal/store/caller.go). Claims are
bounded to 160 bytes and reduced to printable single-line text; renderers
sanitize cells too. `X-Forwarded-For` is ignored. MCP `clientInfo.name` is
not carried across requests because the gateway maintains no session state
and clients may skip the handshake.

Neither column controls authorization or actor attribution. A User-Agent
that says kagent does **not** distinguish a genuine agent from a script
imitating it. Pod addresses are reused and port-forwarding can show a
loopback peer. These columns aid investigation, not identity verification.

## Credentials that expire

New plane credentials have an expiry, default 30 days. Issuance offers no
“never expires” option. NULL expiry is the compatibility class issued before
expiry existed, and remains valid until changed. Credential expiry applies
at model, MCP and inbound authentication, including the inbound target's
budget credential, before burning an unusable trigger grant.

Expired credentials still resolve by hash so the refusal can name the
credential and deadline, rather than misleadingly report an unknown token.
Expiry refusals are recorded on the corresponding trail. A live grant does
not override expiry of its credential.

```sh
kmx credentials
kmx grants
kmx credential renew hello-world --ttl 720h
kmx ledger hello-world
kmx audit tool hello-tools
```

Renewal moves a deadline **without changing token material**. For suspected
compromise, reissue the credential and repoint its Secret; renewal is not
rotation. `kmx govern --ttl` can set a lifetime at issuance, while
`kmx tools govern` uses the default and renewal can change it afterward.
Tokens are shown once and stored in the database only as hashes.

## Recognizing stale credentials and certificates

`kmx credentials` shows deadlines, the one-week `EXPIRING` warning and the
legacy no-expiry class. Grants display the credential deadline alongside
permission lifetime. [Metrics](operations.md#metrics) expose expiry gauges.

A kagent `Accepted` condition is a cached reconcile verdict, not a live
credential check. Secret projection is asynchronous. After writing a
credential, kmx asks the seam to reconcile and requires a verdict newer
than the pre-write baseline; failure to observe one is `unknown`, not
accepted or rejected. `kmx status` reports the cached verdict and its age.
Pod readiness is a separate signal. TLS certificate expiry can also surface
as a generic connection failure; see [certificate renewal](operations.md#the-seam-certificate).

## Privacy and evidence

The attribution path stores Slack IDs, not profiles, names or emails;
caller strings and observed addresses also enter the database and backups.
Treat dumps as sensitive even though opaque tokens and upstream keys are
not included. There are no per-person budgets, per-person policy, workload
identity or OIDC guarantees in this legacy mechanism.

[Identity store tests](../plane/internal/store/identity_pg_test.go),
[caller tests](../plane/internal/store/caller_test.go) and
[inbound identity tests](../plane/internal/inbound/identity_test.go) retain
behavioral evidence. Historical transcripts and arguments for accepting an
overclaim have been removed; no schema or runtime behavior changes here.
