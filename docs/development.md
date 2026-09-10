# Development

For engineers working **on** Kaimahi. If you want to *use* it, start at
[README.md](README.md) and [getting-started.md](getting-started.md); this
page assumes you are changing code.

Contribution process and PR expectations live in
[../CONTRIBUTING.md](../CONTRIBUTING.md). This page is the mental model, the
build, and the traps.

## The one-paragraph model

kagent runs the agents. Kaimahi does not replace it, wrap it, or fork it —
it **sits in two seams kagent already has** and makes the traffic through
them accountable:

- an agent's `ModelConfig.baseUrl` can point anywhere, so Kaimahi points it
  at its own proxy → every model call is authenticated, budget-checked and
  ledgered;
- an agent's tools come from an MCP server URL, so Kaimahi points it at its
  own gateway → every tool call is authenticated, allowlisted and audited.

Nothing else changes. The agent is still a plain kagent `Agent` CRD, and
pointing it back at the direct URLs makes it ungoverned again. **Governance
is opt-in per agent, and that is a deliberate property, not an oversight** —
it is also the first thing to state honestly in any doc you write.

## Repository layout

This table is where things live. [What is actually in this
repository](repository-map.md) is what they are FOR — every area
classified as product, demonstration or scaffolding, with the evidence.
Read it before assuming a file you have found is part of the product.

| Path | What it is |
|---|---|
| `plane/` | The Go governance plane, its own module. `kmx plane` fetches and builds it from the public Go proxy at kmx's own revision — `go:embed` cannot cross into it, precisely because it is a separate module. |
| `cmd/kmx`, `internal/kmx/`, `embed.go` | `kmx`: the developer journey and the kind governance path as one binary, in the root module. `k8s/` travels inside it. |
| `cmd/demo/`, `internal/demo/` | Fixtures that exist to SHOW the product working, never to ship with it. Today that is `kaimahi-erp`, the accounts-payable demo's fake ERP. If you are asking whether something is real, `demo/` in the path is the answer. |
| `plane/cmd/kaimahi-proxy/` | One binary, five listeners (below). |
| `plane/internal/` | `proxy` (LLM data path + admin), `gateway` (MCP), `inbound` (webhooks), `meter` (budgets), `pricing` (tokens→cents), `store`/`db` (Postgres + migrations), `config` (upstream table), `redact` (log scrubbing), `metrics` (Prometheus, fixed label vocabularies), `ops` (metrics listener + probes). |
| `k8s/` | Everything applied to a cluster: agents, model presets, the plane, network policy, scenario fixtures. |
| `k8s/models/` | One `ModelConfig` per preset. `governed-*` point at the proxy; the rest go direct. |
| `scripts/` | Key-handling and check scripts. A credential is handled here or in `kmx credential capture` — never in a make recipe. |
| `docs/` | User docs by capability, plus the board (`COORDINATION.md`). |
| `Makefile` | The operator interface. Every mutating target depends on `guard`. |

## Build and verify

Everything below is runnable from a clean checkout with no cluster.

```bash
# Two Go modules: kmx at the root, the plane under plane/.
# `gofmt -l` LISTS unformatted files but still exits 0, so it has to be
# wrapped to actually fail the chain — the same trap as `! grep` below.
test -z "$(gofmt -l cmd internal embed.go embed_test.go)" && go vet ./... && go build ./... && go test ./...
(cd plane && test -z "$(gofmt -l .)" && go vet ./... && go build ./... && go test ./...)

# Every plane/internal/store test SKIPS without a real Postgres, so on a
# machine that has none the line above runs gofmt, vet, build and every
# other package for real and covers the store not at all. Point them at a
# throwaway database to actually run them — CI's `go-plane` job uses a
# service container and fails if they skip:
#   docker run --rm -d -p 127.0.0.1:5432:5432 -e POSTGRES_PASSWORD=throwaway \
#     -e POSTGRES_USER=kaimahi -e POSTGRES_DB=kaimahi postgres:16
#   (the loopback prefix matters: a bare -p 5432:5432 publishes on every
#    interface, which puts a trivially-credentialled database on the
#    network you happen to be on)
#   (cd plane && KAIMAHI_TEST_PG_DSN='postgres://kaimahi:throwaway@127.0.0.1:5432/kaimahi?sslmode=disable' \
#      go test -count=1 ./...)

# Repository checks. The CI hygiene job runs each checker, each checker's
# self-test, and a set of inline meta-checks over CI's own guards (that
# every cluster step carries the docs-only guard, that the required check
# covers every shard, the network-policy shape, the registry render, the
# release job staying keyless, and more). The job is the authority on what
# runs, not this list.
python3 scripts/check-doc-links.py --selftest && python3 scripts/check-doc-links.py
python3 scripts/check-readme-front-door.py
python3 scripts/check-readme-front-door-test.py
python3 scripts/check-brand-assets.py
# Credential shapes, from ONE list the manifest scaffolder and the
# blueprint parser read too — so a shape added in one place is refused
# everywhere. The self-test runs first.
python3 scripts/check-secret-shapes.py --selftest && python3 scripts/check-secret-shapes.py
# The two documents about this repository that are checkable rather than
# reviewable: the map's counts and membership lists against the tree, and
# the coordination board against itself — no two rows for one lane, no
# ready-to-paste prompt for a lane the table says shipped. Whether a lane
# is done is a judgement and neither checker asks it. The board checker
# reads `git log`, so a shallow clone makes it name the claims it skipped.
python3 scripts/check-repository-map.py --selftest && python3 scripts/check-repository-map.py
python3 scripts/check-board.py --selftest && python3 scripts/check-board.py
# Every checker, broken on purpose and required to notice. A checker that
# has never been watched saying no is not known to work; this applies each
# declared breakage to a copy and fails if the checker stays quiet.
python3 scripts/check-mutations.py
# Azure identifiers, by SHAPE: GUIDs (subscription/tenant), AKS API-server
# hostnames, literal container-registry login servers, literal public
# load-balancer DNS labels, public IPv4 addresses. The self-test runs
# first. With no arguments it scans what git would commit
# (tracked + unignored files), so a transcript you are about to paste
# into a PR must be saved to a file and passed by path. A bare resource
# group or cluster NAME is just a string and is not detected: read for
# those yourself before pasting.
bash   scripts/check-no-azure-ids-test.sh && bash scripts/check-no-azure-ids.sh
bash   scripts/check-no-azure-ids.sh path/to/transcript.txt
bash   scripts/kube-guard-test.sh
python3 scripts/release-notes.py --selftest

# Container image
docker build -t kaimahi-proxy:dev plane/
```

The image is ~18 MB: a static binary on `distroless/static-debian12:nonroot`
— no shell, no package manager, non-root by default.

### The local loop

```bash
make up KIND_CLUSTER=<your-name>      # kind + Ollama + kagent + agents
make plane KIND_CLUSTER=<your-name>   # build, load, deploy the plane + Postgres
make govern KIND_CLUSTER=<your-name>  # issue a credential, switch the agent onto it
make chat KIND_CLUSTER=<your-name> AGENT=hello-world TASK="..."
make ledger KIND_CLUSTER=<your-name>
make down KIND_CLUSTER=<your-name>
```

Podman works too — pass `CONTAINER_ENGINE=podman` to every target for that
cluster (see
[getting-started.md](getting-started.md#using-podman-instead-of-docker)).
The Makefile derives `KIND_EXPERIMENTAL_PROVIDER` from it, and swaps the
image load to `podman save` + `kind load image-archive`, because `kind load
docker-image` cannot see podman's images.

**Always pass your own `KIND_CLUSTER`.** `kaimahi-p1` is the shared demo
cluster and lanes have collided over it before. The board records this as a
rule, not a suggestion.

A full `make up` is 5–10 minutes, most of it pulling the model. Expect a
laptop node to be CPU-bound once Ollama, kagent, Postgres and the plane are
all running: a 3B model on a saturated single node can exceed the kagent
controller's A2A timeout even though the governed calls themselves succeed.
If a chat times out, check `make ledger` before assuming the plane is broken.

### What CI proves, and where a new probe goes

Three required checks gate every PR: `hygiene` (repository checks, kmx's
own tests), `go-plane` (the proxy module) and `e2e-hello-world`.

`e2e-hello-world` owns no cluster. It is an aggregator over **six** shard
jobs that run at the same time, each bringing up its own kind cluster and
running one part of the end-to-end proof. The aggregator's `needs` list is
the authority: a shard added there and nowhere else still gates merges.

| shard | what it proves |
|---|---|
| `e2e-runtime` | the runtime a developer gets — cluster, agent, model path, MCP tools — and hosted tool upstreams |
| `e2e-spend` | metering, budgets, the budget approval cycle, the network boundary, the inbound bridge |
| `e2e-tools` | the tool gateway, tool approvals, the governed Slack path, approvals from Slack, the exact races and metrics |
| `e2e-resilience` | a replica killed mid-cycle, a Postgres outage, both replicas restarted, backup and restore |
| `e2e-ap` | the accounts-payable demo: the fixture ERP reaches nothing, a routine invoice pays itself, the exception needs a named human, and the injected call is denied and spends no approval |
| `e2e-quickstart` | one command from a machine with only a container engine to an agent that answered, run against the release this branch would publish; safe to run twice, `kmx up` restores the full profile, a later quickstart preserves it, and the elapsed time is asserted not to have doubled |

Other jobs run beside them:

| job | what it proves |
|---|---|
| `plane-upgrade` | a plane several migrations old, with a credential, a budget, an allowlist, an approved grant and a priced ledger row in it, upgraded to this checkout's plane on the same database: the data survives, the plane serves, and a migration that cannot apply leaves the plane refusing to start with the rows untouched ([releases.md](releases.md#when-a-migration-fails-halfway)). It needs no cluster |
| `kmx-clone-free` | the whole journey from a `kmx` installed out of the Go proxy at this commit, with no checkout anywhere. It brings up its own cluster, but it is **not** a shard and does not gate PRs: it runs on pushes to `main` and on manual dispatch |
| `release` (own workflow, tags only) | the tagged build: four platforms, checksums, and a binary that reports its own tag ([releases.md](releases.md#cutting-a-release)) |

`plane-upgrade` is not a shard and must not become one: it holds no cluster,
so the docs-only short-circuit and the aggregator's `needs` do not apply to
it. If you want it to gate merges, add it to the branch ruleset's required
checks — the workflow cannot do that for you.

Adding a probe: put it in the shard whose state it needs. The shards are
drawn along state lineage — a denial in one step is what files the approval
the next step approves — so a probe that reads what another step wrote
belongs in that step's shard, or it has to set that state up itself.

Two rules the `hygiene` job enforces, so a mistake fails on the PR that
makes it rather than on someone else's:

- every step in every shard carries `if: steps.changes.outputs.docs_only !=
  'true'`, the docs-only short-circuit (a docs-only PR reports all three
  checks in seconds without booting a cluster);
- `e2e-hello-world` names every shard in `needs` and runs with `always()`.
  A shard nothing needs would fail while the required check stayed green,
  and a required check that never reports blocks every merge.

## How the plane actually works

One binary (two replicas), five listeners, one Postgres. The two DATA
seams serve TLS; the other three are plain HTTP and stay that way — the
admin and ops ports are on no Service, and the inbound bridge's one
public route terminates TLS at an edge:

| Port | Listener | Carries |
|---|---|---|
| 8080 | **data** | OpenAI-compatible model traffic from agents — **TLS**, under the plane's own authority ([operations.md](operations.md)) |
| 8081 | **MCP gateway** | JSON-RPC tool traffic from agents — **TLS**, same certificate |
| 8082 | **inbound** | authenticated webhooks from outside |
| 9091 | **admin** | issuing credentials, budgets, approvals — bearer-token, cluster-internal |
| 9092 | **ops** | Prometheus `/metrics`, `/readyz`, `/livez` — no auth, on no Service ([operations.md](operations.md)) |

The admin port is deliberately separate from every data path. `kmx budget`,
`kmx approve` and the other admin commands reach it through a port-forward;
the corresponding Make targets are compatibility aliases, and the port is
not exposed. The ops port is on no Service either; kubelet probes it, `kmx
metrics` (or its `make plane-metrics` alias) port-forwards to a pod, and a
scraper gets in only through the NetworkPolicy allowance.

The process holds no governance state. Every decision that must be
exact — a budget admission, a grant use, a replay check, a filing, an
approval — is one Postgres transaction under a lock on the credential's
row (`lockCredential` in `store/`), so the replicas agree by
construction; what stays per replica (the pre-auth rate limiter, the
bounded queues, the fail-closed breakers) is listed in
[operations.md](operations.md) with the reason.

### The credential is the unit of governance

Every governed call carries a Kaimahi-issued opaque token (`kmh_…`), not a
provider key. The plane stores only its SHA-256. That one token is what a
budget is attached to, what a tool allowlist is attached to, and what shows
up in the ledger and audit rows.

The property worth protecting: **the agent never holds a real upstream
credential.** For a governed Copilot preset the agent's Secret holds a `kmh_`
token; the real Copilot token is mounted only into the proxy pod. Breaking
that is the most serious kind of regression in this repo.

### Where the durable state lives

Postgres, migrated from `plane/internal/db/migrations/`:

| Table | Holds |
|---|---|
| `credential` | issued tokens (hashed), their budgets, and their **expiry** — NULL is the closed legacy class ([identity.md](identity.md)) |
| `ledger_entry` | one row per billed model call — tokens, cents, status |
| `spend_reservation` | calls admitted under a cap whose ledger row has not landed yet — the hold that makes budgets exact under concurrency; the ledger write deletes it |
| `tool_allowlist` | which tools a credential may call |
| `tool_audit` | every tool call, allowed **and denied** |
| `approval_request`, `permit_grant`, `approval_audit` | the deny → approve → bounded grant cycle |
| `inbound_audit` | append-only, and doubles as the webhook replay guard |
| `agent_run` | one agent turn the plane triggered and held open — the window that lets `ledger_entry.acted_for` and `tool_audit.acted_for` name WHO a call was made for ([identity.md](identity.md)) |

### What is configuration, not code

`k8s/plane/upstreams.yaml` is the upstream table: base URL plus **exactly
one** allowed forwarded path per upstream, for models and for MCP servers.

`k8s/plane/network-policy.yaml` is a **complementary** control, not the same
rule restated. They constrain different things and neither substitutes for
the other:

| | Enforces | Cannot see |
|---|---|---|
| `upstreams.yaml` | which base URL, and the one permitted path on it | anything below the process — a pod can still open sockets the table never mentions |
| `network-policy.yaml` | which pods, namespaces, IP blocks and ports are reachable at all | URLs, paths, or HTTP at all — it is L3/L4 |

So the policy stops the pod reaching a host that is not allowed; the
upstream table stops the proxy forwarding to a path that is not allowed on a
host it *can* reach. If you are adding a destination you are editing both —
and writing neither a new client nor a new egress path.

## Invariants

These are the things that get a PR sent back. Most were learned the
expensive way.

1. **Do not rebuild what kagent ships.** Agent runtime, CRDs, CLI,
   dashboard, MCP servers. Net-new components need a written survey in the
   PR justifying them. Ignoring this caused a full project restart once.
2. **Fail closed.** A verify path accepts only a well-formed positive. WAFs
   return HTML with a 200; gateway-style services return 200 with an error
   envelope for a bad key. `! grep` is not a gate — distinguish "no match"
   from "the scanner failed to run".
3. **Keys are typed, and go into a Secret.** Never argv, env listings,
   YAML, ConfigMaps, or logs. There are two capture paths and each has its
   own rule. `kmx credential capture` — the three tool upstreams whose
   tokens can be checked against the upstream — reads from a **terminal
   only**, echo off, and refuses a pipe or a redirect rather than reading
   it, because a value that can arrive through a pipe can arrive from a
   shell history or a CI log. Everything else (model keys, the Slack bot
   token, inbound signing keys) is captured by a script in `scripts/` that
   reads **stdin**, with `set -euo pipefail`. Neither lives in a make
   recipe — make runs recipes without pipefail, and a failed pipe stage can
   fail *open*. That exact bug once stored an empty Secret after a failed
   token exchange.
4. **Record spend before honouring a failure.** A billed call gets a ledger
   row even when the surrounding operation errors.
5. **Never infer that something is free.** Free is an explicit
   classification in the upstream table, not a guess from a URL.
6. **Mutating make targets depend on `guard`.** `scripts/kube-guard.sh`
   checks the context name *and* the API server address, because a context
   named `kind-prod` can point at production. Confirm non-interactively with
   `KAIMAHI_CONFIRM=$KUBE_CTX`.
7. **Say what is actually verified.** The repo's status vocabulary is
   continuously tested / demonstrated once / schema-valid / proposed /
   unbuilt. "It should work" is not one of them.
8. **A governance decision is a Postgres transaction under the credential
   lock, never a read-then-act in Go.** The plane runs two replicas that
   share nothing but the database; anything decided from an unlocked read
   or from process memory is a race between them. New limits get a
   concurrent test in `store_pg_test.go` (real Postgres, goroutines
   racing the real SQL), not an argument.
9. **No identifier is a metric label.** Label values come from the fixed
   vocabularies in `plane/internal/metrics` or the two public name shapes
   (credential name, upstream name); the label-set test fails on anything
   else. A token, a channel, a user, a request or a delivery id never
   becomes a series.

## Traps

- **`.gitignore` rules are unanchored by default.** `bin/` matches at every
  depth. A tracked-looking file can be silently skipped by `git add -A`,
  and you will not be told. Verify the committed tree
  (`git ls-files --error-unmatch <path>`), not the working directory.
- **macOS ships Python 3.9.** Repo scripts must not use PEP 604 (`str |
  None`) at import time without `from __future__ import annotations`, or
  they die before running a single check on a contributor's laptop while
  passing in CI.
- **`git stash -u` will take files that were ignored a moment ago.** Change
  an ignore rule, stash, and the newly-visible file goes with it.
- **A missing `ModelConfig` is admitted, then fails to reconcile.** The
  Agent reports `Accepted=False` in a status condition you only see if you
  look. Check conditions, not just `READY`.
- **`make up` does not run `govern` on kind.** A fresh cluster has no
  governed presets until you ask for them.
- **Re-running `make up` re-applies the agent** and can quietly drop it back
  onto an ungoverned preset.
- **The proxy image is distroless: there is no shell and no `cat` to
  `kubectl exec`.** A wait loop built on `exec … cat` never succeeds; it
  burns its full timeout and moves on (CI carried one for two phases).
  Wait on the plane's own behaviour, or restart the Deployment so pods
  start with a freshly created Secret already projected.
- **`kubectl port-forward svc/…` sticks to one pod.** With two replicas,
  a probe through a Service exercises whichever pod it happened to pick.
  The race and kill probes port-forward each `pod/` separately for that
  reason; do the same when the claim is "both replicas".
- **A bare `wait` in a script waits on the port-forwards too**, which
  never exit. Collect worker PIDs and `wait` on those.
- **`go install` refuses to cross-compile while `GOBIN` is set** — and mise,
  asdf and `go env -w GOBIN=…` all set it. The plane's binary is built for
  Linux (the kind node's platform), so on macOS every `kmx plane` build is a
  cross-compile. kmx removes `GOBIN` from the environment it hands the
  toolchain; a `GOBIN` in Go's own environment file survives that, and kmx
  says so and names `go env -u GOBIN` or `kmx plane --source .`.
- **A container engine's clusters are invisible to the other engine.** `kind
  get clusters` under docker will not list a podman cluster, so the Makefile
  cheerfully tries to create one that already exists. Keep
  `CONTAINER_ENGINE` consistent for a given `KIND_CLUSTER`.
- **Restarting the podman machine stops kind's node container.** `make
  cluster CONTAINER_ENGINE=podman` — and `kmx up`, which it delegates to —
  starts every node belonging to the named cluster and waits for both the API
  server and CoreDNS before returning, so the rest of `make up` can safely
  continue. If kind lists the cluster but podman has no nodes for it, that is
  a disagreement rather than a recovery, and it refuses.
- **First `make up` on podman can fail at the ollama rollout.** Pulling the
  ~1.9GB image through the podman VM took 5m04s here, past the target's
  300s `rollout status` timeout, so make stops even though the pull
  succeeds. Re-run `make up`; it is idempotent and continues.
- **A podman machine with no volume mounts cannot read your checkout**, so
  `podman build` fails with `faccessat <path>: connection refused`. Volumes
  are fixed at `podman machine init` time — `podman machine set` has no flag
  for them — so the machine has to be recreated.

## Where to look when something is wrong

```bash
kubectl --context <context> -n kagent get agents.kagent.dev             # Accepted / Ready
kubectl --context <context> -n kagent describe agents.kagent.dev <name>  # the real error
kubectl --context <context> -n kagent logs deploy/<agent> --tail=50     # what it called
kubectl --context <context> -n kaimahi logs -l app=kaimahi-proxy --prefix   # governance decisions, both replicas
make ledger    CRED=<name>                          # was the call metered?
make tool-audit CRED_TOOLS=<name>                   # was the tool allowed?
make plane-metrics                                  # one replica's counters, queue depths, breakers
```

The ledger and the audit are the source of truth for "did governance
actually happen". A passing conversation proves nothing on its own — the
rows do.
