# Governing spend: budgets, the ledger, and key custody

This is the part kaimahi actually adds. A metering and enforcing LLM
proxy sits at kagent's ModelConfig `baseUrl` seam. Every model call an
agent makes through a governed preset is authenticated and
budget-checked before it is forwarded, and ledgered (denials
immediately; forwarded calls once the response reveals their usage).
The real upstream credential lives only with the proxy. **Keys never
reach the agent.**

Assumes: a cluster from [getting-started.md](getting-started.md). The
plane you deploy here is also what [tool-governance.md](tool-governance.md),
[approvals.md](approvals.md) and [slack.md](slack.md) build on.

> **Every credential the plane issues now expires**, and every ledger
> row names who the call was made for. Both are
> [identity.md](identity.md); the ledger's last column and
> `make credentials` are where you see them.

> **This governs LLM traffic only, and only through `governed-*`
> presets.** Tool calls are governed separately
> ([tool-governance.md](tool-governance.md)), approvals separately
> ([approvals.md](approvals.md)); each is its own opt-in. The plain
> hosted presets from [models.md](models.md) (`make use PRESET=openai`
> and friends) still exist and are still ungoverned. Nothing meters
> them.

## Architecture

```text
Agent pod (kagent)                         namespace kaimahi
┌─────────────────────┐   opaque token   ┌──────────────────┐    real creds
│ governed-ollama      │ ───────────────▶ │  kaimahi-proxy   │ ──────────────▶ upstream
│ ModelConfig          │  (kmh_…, issued  │  authn → route → │  (ollama: none; │
│ baseUrl = proxy      │   by the plane)  │  budget → fwd →  │   copilot: Secret
│ apiKeySecret = token │                  │  ledger          │   mounted to proxy)
└─────────────────────┘                  └────────┬─────────┘
                                                  │
                                         ┌────────▼─────────┐
                                         │ Postgres 16      │  credentials (hashes),
                                         │ (durable store)  │  budgets, spend ledger
                                         └──────────────────┘
```

- **The plane** ([`k8s/plane/`](../k8s/plane/)): namespace `kaimahi`,
  the proxy Deployment/Service, Postgres 16 with a 1 Gi PVC. Migrations
  are embedded in the proxy binary and run idempotently at startup, so a
  rollout is its own migration step.
- **The mount** (`k8s/models/governed-*.yaml`): ModelConfigs whose
  `openAI.baseUrl` points at the proxy and whose `apiKeySecret` is the
  agent-side token Secret. kagent needs no changes. This is the seam
  working as designed.
- **Upstreams** ([`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml)):
  where the proxy will forward to. Two committed entries: in-cluster
  ollama (the free demo tier) and the Copilot subscription endpoint. One
  base URL, **one allowed (method, path)** and one declared protocol per
  upstream is the whole blast radius; any other request is denied before
  any upstream contact. Your own endpoints are added beside these rather
  than into them, with `kmx models add` — see
  [Adding a model upstream](#adding-a-model-upstream). The same file also
  carries `tool_upstreams`, which is the MCP gateway's table and belongs
  to [tool-governance.md](tool-governance.md).
- **Admin plane**: a second port (9091) that the Service deliberately
  does not expose. `kmx govern`/`kmx ledger` (and `make govern`,
  `make budget`, `make ledger`) reach
  it via `kubectl port-forward` plus a bearer token read from the
  `kaimahi-admin` Secret, so cluster credentials gate every admin
  operation.

## Credential custody

- The agent-side Secret (`kagent/kaimahi-governed-token`) holds a
  **kaimahi-issued opaque token** (`kmh_…`), minted by `make govern` and
  shown exactly once. The plane stores only its sha256.
- The **real** Copilot token lives in `kaimahi/kaimahi-copilot-token`,
  mounted to the proxy pod and read per request. Rotate it with
  `make plane-copilot-secret`; no restart needed. Ollama has no
  credential at all and is forwarded bare: nothing is injected, by
  contract rather than by accident.
- The proxy strips the opaque token (and any other credential-slot
  header) before injecting the real credential upstream. Keyed calls
  never follow redirects. Logs pass through a redactor.

You can check custody yourself without printing the whole token:

```bash
kubectl -n kagent get secret kaimahi-governed-token -o jsonpath='{.data.api-key}' | base64 -d | cut -c1-4
```

prints `kmh_`, not `sk-` or a GitHub token prefix.

## From zero

```sh
make up          # cluster, ollama, kagent, agents
make plane       # build the proxy image, load into kind, deploy proxy + Postgres
make govern      # issue the credential, switch hello-world through the proxy
make chat        # works as before, but now authenticated, metered, ledgered
make ledger      # see the row the chat just wrote
```

Or without a clone, which is the same code — on kind these `make` targets
are one-line recipes that call `kmx` ([kmx.md](kmx.md)):

```sh
kmx up
kmx plane                # fetches the plane at kmx's own revision; no clone, no registry
kmx govern hello-world
kmx agent chat hello-world "Who are you?"
kmx ledger
```

On a managed cluster the plane needs a registry, a rendered manifest and a
captured model key. `kmx lift` does the first two and stops at the third,
naming `make plane-copilot-secret`; `TARGET=aks make plane` /
`TARGET=aks make govern` do the same work step by step — see
[aks.md](aks.md). Budgets and approvals are `kmx` commands —
`kmx budget`, `kmx approvals`/`approve`/`deny`/`request` — and they act on
whichever context you point them at. It is the *Makefile* that is split:
`make budget` and `make approve` delegate to `kmx` on `TARGET=kind` and run
`scripts/plane-admin.sh` on `TARGET=aks`.

`make govern` leaves `hello-world` on the `governed-ollama` preset and
also applies `governed-copilot`. Switch to the latter with
`make use PRESET=governed-copilot` once `make plane-copilot-secret` has
given the proxy the real token. (On AKS there is no ollama, so `make
govern` switches to `governed-copilot` directly. See [aks.md](aks.md).)

## The ledger

From a real run:

```text
created (UTC)       credential   upstream  model                in    out  cents source   status caller (claimed)   from (observed) acted for
2026-09-01T03:41:45 hello-world  ollama    qwen2.5:3b          371     27      0 free     200    ua:kagent/0.9.12   10.244.1.7      none
```

`source` says why the cost is what it is:

- **free**: the upstream is explicitly classified `free` in
  `upstreams.yaml` (in-cluster ollama). $0 is a classification, never an
  inference from a $0-looking URL.
- **priced**: a real price row (`prices` in `upstreams.yaml`, cents per
  1M tokens) was applied.
- **unpriced**: a metered upstream with no price row for that model.
  Tokens are still counted honestly; cost stays 0. Under a **cents**
  budget an unpriced model is **denied**, because spend that can't be
  charged against the budget can't be admitted. Use a token budget for
  Copilot unless you configure a price. No Copilot per-token price is
  bundled: subscription usage has no public per-token price and kaimahi
  never invents one.
- **denied**: the request never went upstream.
- **unmetered**: the call DID go upstream, and the token counts on this
  row are not its counts — the plane could not read them. This is the
  only source that is not a statement about cost, and it exists because
  the alternative was a row that looked ordinary: a call read by the
  wrong protocol's reader used to be ledgered `0 in / 0 out, free`,
  indistinguishable in every sum from a call that genuinely cost nothing.
  A non-streamed call in this state is **refused** (502) and the answer
  never reaches the caller; a streamed one has already been flushed and
  cannot be recalled, so it is relayed and the row says so. Never priced:
  pricing a count that was never read would invent the cost.

The FAQ has the longer answer to
[why ollama is free but still budgeted in tokens](FAQ.md#ollama-is-free--why-is-it-budgeted-in-tokens).

## The two protocols

An upstream declares which wire protocol it speaks, because the two
differ in the only thing the plane reads out of a response:

| `protocol` | path a client posts to | where the meter reads |
|---|---|---|
| `chat_completions` | `v1/chat/completions` | `usage.prompt_tokens` / `usage.completion_tokens` |
| `responses` | `v1/responses` | `usage.input_tokens` / `usage.output_tokens` |

Both are OpenAI-compatible surfaces, which is why "point it at an
OpenAI-compatible endpoint" was never a sufficient answer: one current
agent framework speaks the Responses API **by default**, its model client
is the Responses client, and it offers no switch. Against a table that
only knew the other shape, every one of its calls was refused on the path
— and once a path was added, metered as zero.

The field is optional only where the path already names it: a path
ending `chat/completions` or `responses` IS that protocol and the plane
fills it in. A path that names neither must declare one, and a
declaration that disagrees with its own path is **refused at load**
rather than resolved, because whichever of the two is wrong the meter
would read the wrong field. An upstream whose protocol the plane cannot
name never serves a request: the plane will not boot a seam it could
only forward unmetered.

A client that speaks both protocols against one endpoint needs two
entries — one path each — which keeps "one allowed path per upstream"
exactly as it was, and keeps each entry's blast radius readable.

## Adding a model upstream

The committed table is this repository's, and `kmx plane` re-applies it:
an entry added there is an entry the next deploy discards. Your own
endpoints go in the operator overlay instead, the same ConfigMap
`kmx tools add` writes tool servers into:

```bash
kmx models add house \
  --url http://vllm.demo:8000/v1/responses \
  --classification free
```

That URL is split into the two halves the table stores separately (base
`http://vllm.demo:8000`, path `v1/responses`), the protocol is resolved
from the path, and three documents are written for you to read before
they are applied: the overlay fragment, the proxy's egress to that one
destination and port, and the endpoint's ingress from the proxy alone.
The command validates the candidate table against the **running plane's
own parser** before writing anything.

`--classification` has no default. `free` says the endpoint costs
nothing, so only a token budget can ever exhaust it; `metered` counts
tokens always and applies a cost only where a price is configured. A $0
by inference is a budget nothing can exhaust.

Three things the overlay deliberately cannot express, refused by the
plane and not merely by kmx:

- **a credential** — `credential_file` and `credential_header`. An
  overlay entry is keyless.
- **an endpoint outside the cluster** — `internet`, `ca_file`, and
  `extra_headers` with them. A hosted model endpoint holds a real API
  key; that is a reviewed entry in the committed table
  ([hosted-upstreams.md](hosted-upstreams.md)).
- **a price** — `prices`. A price is the multiplier a cents budget is
  measured with and the one number in the table the plane can never
  check, so it stays on the reviewed file. A metered overlay upstream
  still works under a token budget, and under a cents budget the
  priced-pair gate refuses it, which is the correct outcome.

And one thing it *can* express that deserves saying out loud, because
the obvious inference from the list above is wrong: **`classification` is
the operator's claim, and the plane cannot check it.** An overlay entry
being keyless does not make the *endpoint* keyless — the ordinary
in-cluster shape for a paid model is a router (LiteLLM, an
OpenAI-compatible gateway) that holds the key itself. Declaring such an
endpoint `free` means every call through it is ledgered as costing
nothing and no cents budget can ever bind it. That is exactly as true of
the committed table, and it is not something enforcement can fix; what
`kmx models add` does is refuse to let it pass silently — it prints the
consequence whenever `free` is chosen.

**There is no allowlist on this seam.** Unlike a tool upstream — which
nothing can call until a credential allowlists a tool on it — a model
upstream is reachable by every credential the plane has issued the moment
it is in the table. What bounds them is the budget each one carries, and
the fact that an overlay upstream is in-cluster and keyless. `kmx models
add` says this before it applies anything.

## Budgets

Caps are monthly (calendar month, UTC), per credential:

```bash
make budget CAP_TOKENS=100000    # token cap — the only lever for the free tier
make budget CAP_CENTS=500        # cents cap
make budget                      # remove all caps
```

### What a denial looks like

Set a cap below what any chat costs and try:

```bash
make budget CAP_TOKENS=1
make chat
```

The task fails with the reason in plain text. This is a real run:

```text
"status":{"state":"failed","message":{... "parts":[{"kind":"text",
"text":"monthly token budget reached"}] ...}}
```

The denial is a 429 from the proxy, sent before any upstream contact,
and it lands in the ledger too. In our runs each attempt produced three
denied rows, because the agent runtime retried the call:

```text
2026-09-01T03:42:26 hello-world  ollama    qwen2.5:3b            0      0      0 denied   429
```

Since approvals landed, the denial message also says an approval
request was filed. A human can mint a bounded overage instead of
raising the cap; see [approvals.md](approvals.md).

### Enforcement properties

All unit-tested and live-verified:

- An exhausted budget denies with **429** and a clear error before any
  upstream contact; the agent surfaces it as a failed task.
- The check is **exact under concurrency**, across replicas. Admission
  is one Postgres transaction under a lock on the credential's row: it
  counts the ledger *plus the calls already admitted but not yet
  recorded* (each holds the least it can spend — one token, one cent if
  the model is priced — until its own ledger write lands), so N
  concurrent calls against a cap with room for one admit exactly one,
  whichever replicas they land on. CI fires eight at both replicas at
  once and asserts one 200 and seven 429s. The hold bounds *admissions*,
  not overshoot: what remains is the accepted soft stop — every call
  admitted while the cap had room for its hold may still finish above
  the cap by its own usage, so with plenty of headroom several in-flight
  calls can each end over it. A hold that a crashed replica never
  released stops counting after ten minutes (longer than any call the
  proxy allows).
- If the ledger store is unreadable, budgeted credentials are denied
  (**403 "metering unavailable"**): no spend visibility, no spend. A
  credential with no caps skips the budget read, but a failed ledger
  *write* trips the whole data plane closed (**503 "spend ledger
  unavailable"**) until a write succeeds again. Spend that cannot be
  recorded must not happen.
- Every attributable outcome is ledgered: success, upstream failure,
  and denial. Unauthenticated requests have no credential to attribute.
  Billed usage is recorded even when the surrounding request fails.
- Forwarded traffic meters through the `usage` object, and **which
  usage object depends on the upstream's declared protocol** — see
  [The two protocols](#the-two-protocols) above. Denials are fixed
  zero-usage rows.
- **A success the plane cannot meter is refused, not relayed.** If a
  response carries no usage the declared protocol can read, the answer is
  discarded, the caller gets a 502, and the row is written with
  `source=unmetered`. Only one of the two available answers — refuse it,
  or hand it over with a zero beside it — is consistent with a plane that
  exists to meter, and the second one was the measured failure that
  produced this rule. Token counts are never invented.
- **A stream the request did not ask for is refused too**, before a byte
  of it is relayed. `stream_options.include_usage` is only sent when the
  request said `stream`, so an upstream that answers `text/event-stream`
  anyway is answering in a shape the plane deliberately did not prepare
  to meter — and letting it through would land in the one place a
  refusal is no longer possible.
- **That one place is a stream the client DID ask for.** Its bytes have
  already been flushed by the time the missing usage is known, so it is
  relayed, logged at ERROR, and ledgered `unmetered` — visible in the
  trail rather than a plausible zero. Repeated `unmetered` rows do not
  trip the plane closed the way a failed ledger write does: a ledger that
  cannot be written is the plane's own fault and affects every credential,
  while an upstream that will not report usage is one upstream, and
  taking the whole data plane down for it would refuse traffic that is
  being metered correctly. Watch the row, and the
  `reason="unmetered"` decision metric.

What each status code from the plane means, and what to do about it, is
in the [FAQ](FAQ.md#what-the-planes-status-codes-mean).

## Verification status

- **governed-ollama**: live-verified end to end (chat, ledger row,
  budget denial, restore), and exercised keylessly in CI on every PR.
- **governed-copilot**: schema-valid, and the custody fail-closed path
  is live-verified on kind (no proxy-side token means 503 "upstream
  credential unavailable"; the request never leaves the cluster). A full
  governed Copilot chat needs an interactive device login
  (`make plane-copilot-secret`), so it is not part of the kind
  verification or CI. It **was** completed for real once, on AKS on
  2026-09-01, with its ledger row and a budget denial ([aks.md](aks.md)).

## Operational notes

- On kind the proxy image is side-loaded (`imagePullPolicy: Never`).
  `make plane` always restarts the proxy after applying, because a
  rebuilt image under the same tag leaves the spec unchanged and apply
  alone would keep the old binary running. On a real cluster the image
  goes through a registry; see [aks.md](aks.md).
- `scripts/plane-secrets.sh` generates the Postgres password and admin
  token idempotently. Existing Secrets are kept: regenerating the pg
  password under a live database would lock the proxy out.
- The admin API answers 503 if its token file is unreadable and 401 when
  otherwise unauthenticated. Credential issuance shows the token exactly
  once; if the agent-side Secret is lost, follow
  [the FAQ](FAQ.md#i-lost-the-governed-token).
- `make up` **preserves** a governed agent across re-runs: it warns and
  restores a non-default modelConfig instead of silently resetting it.
  `make use PRESET=ollama` is the explicit way back. If you are on an
  older checkout, or something else re-applied the agent YAML, see
  [the FAQ](FAQ.md#my-governed-agent-stopped-showing-up-in-the-ledger).
- Re-run `make plane` after editing `upstreams.yaml`: the config is read
  at boot (the ConfigMap mounts via subPath, which never live-updates).
- Postgres data survives pod restarts via the PVC; `make down` destroys
  it. `make backup` writes a `pg_dump` of the whole plane database to a
  local file and `make restore FILE=…` loads it back, proven on a fresh
  cluster in CI ([operations.md](operations.md#backup-and-restore),
  [FAQ](FAQ.md#where-did-my-ledger-go)).
- The proxy runs as two replicas; `make plane` rolls them one at a time
  and neither the ledger nor a budget decision depends on which one a
  call lands on ([operations.md](operations.md)).

## Limitations

- Only LLM calls through `governed-*` presets are governed. The plain
  hosted presets are a live credit card with nothing in front of them.
- Egress other than the two configured upstreams is not reachable
  *through the plane*, but pod-level network egress is not enforced by
  the plane.
- No Copilot price is bundled, so Copilot can only be capped by tokens.
- Postgres is one replica. The plane is stateless and survives a
  replica loss; the database is durable (PVC, backup, restore) but not
  highly available — while it restarts, every replica drops readiness
  and nothing is admitted ([operations.md](operations.md)).

The single table of what is and is not governed across the whole plane
is in [README.md](README.md#what-is-governed-today-and-what-is-not).
