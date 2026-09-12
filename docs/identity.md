# Legacy reference: attribution and credential expiry

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This page preserves the meaning
> and limits of existing database columns and credentials. It does not
> define Orka identity or make legacy attribution a migration prerequisite.
> Current onboarding starts at the [documentation index](README.md).

## Identity on the call

The ledger and historical tool/inbound audit carry `acted_for`.
The retired inbound bridge recorded signed Slack event user identifiers and
opened run windows around kagent turns. That producer is removed; its stored
records are history. Retirement adds no verified human identity for model
clients; the gateway no longer accepts MCP traffic.

The retained [store lookup](../plane/internal/store/identity.go) interprets
existing run windows as follows:

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

Historically, the bridge opened runs for the hook's budget and tool credentials,
then closed them after A2A returned. Runs expired a minute past the invoke timeout.
The remaining process opens no inbound runs; any still-live window from an old
replica is bounded by that expiry. No public/admin endpoint or `kmx` command
opens such a run for arbitrary clients.

Those calls were correlated by **credential and time**, not by a unique
invocation token. A separate caller sharing the credential during the window
could be associated with it too. Calls without a live run resolve `none`, and
concurrent windows resolve `unknown`. Attribution failure alone neither admits
nor denies model traffic; budget admission is a separate decision.

Historical approval rows distinguish a requester from `decided_by`: `admin`
was not a verified person, and older `slack:<user id>` decisions remain stored.
The approval subsystem and its history API are removed; SQL/backups retain those
rows. A historical approval does not prove who initiated calls using its grant.

## Who called

The ledger and historical tool audit carry two deliberately different columns:

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
sanitize cells too. `X-Forwarded-For` is ignored. The retired gateway did not
carry MCP `clientInfo.name` across calls; historical rows do not establish it.

Neither column controls authorization or actor attribution. A User-Agent
that says kagent does **not** distinguish a genuine agent from a script
imitating it. Pod addresses are reused and port-forwarding can show a
loopback peer. These columns aid investigation, not identity verification.

## Credentials that expire

New plane credentials have an expiry, default 30 days. Issuance offers no
“never expires” option. NULL expiry is the compatibility class issued before
expiry existed, and remains valid until changed. Credential expiry applies
at model authentication. Gateway retirement does not invalidate previously
issued model credentials or reset their stored expiry.

Expired credentials still resolve by hash so the refusal can name the
credential and deadline, rather than misleadingly report an unknown token.
Expiry refusals are recorded in the model ledger. Historical grants have no
authority on this build; credential expiry remains an independent boundary.

```sh
kmx credentials
kmx credential renew hello-world --ttl 720h
kmx ledger hello-world
```

Renewal moves a deadline **without changing token material**. For suspected
compromise, reissue the credential and repoint its Secret; renewal is not
rotation. `kmx govern --ttl` can set a lifetime at issuance. Model credentials
are separate from the removed tool-governance commands. Tokens are shown once
and stored in the database only as hashes.

## Recognizing stale credentials and certificates

`kmx credentials` shows deadlines, the one-week `EXPIRING` warning and the
legacy no-expiry class. The retired grant view is not an expiry inspection path.
[Metrics](operations.md#metrics) retain credential and certificate expiry gauges.

A kagent `Accepted` condition is a cached reconcile verdict, not a live
credential check. Secret projection is asynchronous. `kmx status` reports cached
conditions and their age; pod readiness is a separate signal. The retired
RemoteMCPServer credential-acceptance flow is not a model authentication test.
TLS certificate expiry can also surface as a generic connection failure; see [certificate renewal](operations.md#the-seam-certificate).

## Privacy and evidence

Historical attribution records contain Slack IDs, not profiles, names or emails;
caller strings and observed addresses also enter the database and backups.
Treat dumps as sensitive even though opaque tokens and upstream keys are
not included. There are no per-person budgets, per-person policy, workload
identity or OIDC guarantees in this legacy mechanism.

[Identity store tests](../plane/internal/store/identity_pg_test.go) and
[caller tests](../plane/internal/store/caller_test.go) retain behavioral evidence.
Historical SQL migrations and stored audit/attribution data remain intact;
retiring gateway/inbound producers is not destructive schema cleanup.
