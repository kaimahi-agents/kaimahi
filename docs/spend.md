# Legacy reference: model proxy, budgets and ledger

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This page remains because the
> proxy still runs and CLI messages reference it. It documents existing
> behavior, not a proposal to preserve a separate governance platform.
> For current onboarding use the [index](README.md) and [migration](migrate.md).

In the migration path, governance means **governed model traffic**.
The application keeps its owner-managed Deployment. Tool-call governance,
whole-workload containment and human attribution do not follow from routing
model requests through a seam. Removing the bridge when upstream makes it
unnecessary is a successful outcome.

## Existing model seam

The proxy serves TLS on 8080 and accepts configured routes under
`/upstream/<name>/<client-path>`. A table entry names one forwarded method,
path, upstream base URL, classification and protocol. `client_path`, when
set, can differ from the forwarded path for an explicit translation.
Other routes are refused before forwarding.

The [committed table](../k8s/plane/upstreams.yaml) contains `ollama`,
`copilot`, `orka` and `orka-coordinator`. The Orka entries currently
translate Responses requests to chat completions; `orka` adds
`X-Orka-Tools: disabled`, while `orka-coordinator` leaves the coordinator
behavior enabled. That second entry is a comparison path, not an onboarding
recommendation. Neither entry imposes an agent-authoring contract.

The caller's `kmh_` credential is stored as a hash in Postgres. Provider
credentials remain in proxy-mounted Secrets, are read per request and
replace caller credential-slot headers. Rotations take effect after Secret
projection; keyed requests do not follow redirects. Direct provider routes
are outside this ledger and budget enforcement.

## Existing operator commands

For an already deployed legacy plane and kagent example:

```sh
kmx govern hello-world
kmx ledger hello-world
kmx budget hello-world --tokens 100000
kmx credentials
```

`kmx govern` issues/reissues credential material and repoints the configured
agent; it is not a read. `kmx budget` **replaces both caps**: omitted flags
mean no cap for that unit, and no flags removes both. Set `--cents` and
`--tokens` together to retain both. Defaults and context handling are in
[commands.go](../cmd/kmx/commands.go), not the retired make walkthroughs.

## The ledger

Rows record credential, upstream, model, token counts, cents, HTTP status,
caller observations and [actor attribution](identity.md). `source` means:

| Source | Meaning |
|---|---|
| `free` | explicitly classified free; zero cost is a configuration claim |
| `priced` | configured price applied to reported usage |
| `unpriced` | metered but no price for this model; counts retained, cost zero |
| `denied` | request did not go upstream; usage zero |
| `unmetered` | request went upstream but usable counts could not be read |

Zero cents is not proof of zero real cost. No Copilot per-token price is
bundled. A metered, unpriced model is refused under a cents cap; use a token
cap or supply a reviewed price rather than inventing one.

## Protocols and missing usage

`chat_completions` reads `usage.prompt_tokens`/`completion_tokens`;
`responses` reads `usage.input_tokens`/`output_tokens`. A recognizable path
can infer the protocol; a contradictory declaration or an unrecognized path
with no explicit protocol is refused at load. Separate paths need separate
entries. See [protocol.go](../plane/internal/proxy/protocol.go) and
[translation](../plane/internal/proxy/translate.go) for supported wire shapes.

A non-streamed success with no readable usage is discarded with 502 and
ledgered `unmetered`. An unsolicited SSE stream is refused before relay.
For a requested stream, bytes may already have reached the caller before
missing usage is known: they cannot be recalled. The row and decision metric
say `unmetered`; repeated missing usage does not trip the whole plane closed.
Operators must investigate or disable that route rather than trust zero counts.

## Budgets and failure behavior

Caps are per credential and calendar month in UTC. Admission locks the
credential row and counts ledger usage plus live reservations across all
replicas. Each admitted capped call holds one token and, when priced, one
cent until recording finishes; abandoned holds stop counting after ten minutes.

This is an exact admission check but a **soft stop on final spend**: admitted
calls can each finish above the cap by their eventual usage. Reservations
are not estimates or hard maximum-cost guarantees. Live bounded
[budget grants](approvals.md) add headroom; under-cap calls do not burn uses.

Exhausted budgets return 429 before forwarding. An unreadable admission
store returns 403 `metering unavailable`; a failed ledger write trips the
replica to 503 until another write succeeds. Rows for forwarded calls land
after the response; a crash can lose that record. Unknown-token failures
cannot be attributed. See [meter](../plane/internal/meter/meter.go),
[spend transactions](../plane/internal/store/spend.go) and
[handler](../plane/internal/proxy/handler.go).

## Adding a model upstream

The retained `kmx models add` command handles keyless in-cluster endpoints:

```sh
kmx models add house --url http://vllm.demo:8000/v1/responses \
  --classification metered
```

It reads the live Service and validates the candidate table, then emits
an overlay fragment plus proxy egress and server ingress. The shared
`kaimahi-upstreams-extra` survives redeployment and refuses name collisions.
The overlay cannot set custody fields, hosted dialing, custom headers/CAs
or prices; those remain reviewed committed configuration.

**Every issued credential can reach every configured model upstream**;
there is no per-credential model-upstream allowlist. `classification` is the
operator's unverified claim: declaring an in-cluster paid router `free`
makes cents caps ineffective even if the router itself holds a provider key.
`metered` without a price remains usable under token caps, not cents caps.

TLS renewal, backup/restore, the single-Postgres availability limit and
per-replica breakers are retained in [operations](operations.md).
Tests in [proxy](../plane/internal/proxy/), [meter](../plane/internal/meter/)
and [store](../plane/internal/store/) remain; no live verification is implied
by this documentation retirement.
