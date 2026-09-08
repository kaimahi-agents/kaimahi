# What is actually in this repository

`docs/development.md` says where things live. This says what they are
FOR — which is a different question, and the one a new reader actually
has. Three kinds of thing sit side by side in this tree with nothing
marking the boundary:

1. **Product** — it ships to a user, or it runs in their cluster.
2. **Demonstration** — it exists to show the product working. A fixture
   vendor system, a scenario driver, a walkthrough.
3. **Scaffolding** — it exists to build or verify. Checkers, probes,
   CI fixtures.

A fourth category is a result rather than a gap: **unclear**, where the
evidence does not settle it. Those are listed at the end, named, rather
than rounded to the nearest confident answer.

**How each row was decided.** Not by reading names. For every file the
question was *who invokes this* — greps for the path and the symbol
across the Makefile, `.github/workflows/`, `scripts/`, the Go tree,
`embed.go` and `docs/`. Two pieces of evidence override everything else:

- **Embedded in the binary is product.** `embed.go` names what travels
  inside `kmx`. An operator who ran `go install` and has no checkout can
  only use what is in there, so anything embedded is reachable by a real
  user by definition — including six shell scripts, which is not where
  you would look for product code.
- **A demo's own words.** `internal/demo/erp`'s package comment says
  "the gateway in front of it is what the demo is about", and
  `docs/ap-demo.md` lists the ERP under **Simulated**. Where the code
  says what it is, that is the answer.

---

## The short version

| Area | Product | Demonstration | Scaffolding |
|---|---|---|---|
| `cmd/` | `kmx` | `demo/kaimahi-erp` | — |
| `internal/` | `kmx/` (15 packages) | `demo/erp` | `kmx/delegation` (tests only) |
| `plane/` | all of it | — | test fakes inside packages |
| `k8s/` | the embedded set, the plane, the model presets | the AP, Slack, GitHub and release scenarios | — |
| `scripts/` | 20 (6 embedded in the binary, 14 operator) | 5 | 40 (checkers, probes, CI fixtures, mutation specs) |
| `docs/` | the capability docs | `ap-demo.md`, `demo.md`, `release-agent.md` | `COORDINATION.md`, `reviews/`, `development.md` |
| `brand/` | 6 assets used by the README and the org profile | — | its own checker |

---

## `cmd/` — two binaries, and only one of them is the product

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (15 files) | **Product** | The CLI. `go install .../cmd/kmx@latest` is the documented front door. |
| `cmd/demo/kaimahi-erp` (2 files) | **Demonstration** | A fake accounts-payable ERP. Applied by `k8s/erp-mcp.yaml` via `scripts/erp-deploy.sh`; `docs/ap-demo.md` lists it under "Simulated" — "no vendor, no bank, no payment rail". |

Until this change both sat directly under `cmd/`, as peers, and nothing
distinguished them.

## `internal/` — the product's packages, and one fixture

`internal/kmx/` is fifteen packages. Every one is product, and the line
between them is consistent enough to state as a rule: **anything that
can be decided without reaching a cluster lives in its own package;
`app` is what shells out.**

`lift` holds the naming, validation and teardown rules of the AKS path
and talks to no cloud, so they are tested without a subscription — the
five `lift*.go` files in `app` are the half that runs `az`. `blueprint`
parses and validates a governed workflow; `workflow_run.go` in `app`
executes it. `scaffold` generates agent YAML; `guard` decides whether a
context may be written to; `seam`, `secretshapes`, `toolchain` and
`version` are each one decidable question. That is why `app` is 38
files: it is not a grab bag, it is everything left after the decidable
parts were taken out, and what remains all shares one receiver holding
a kubectl and the operator's terminal.

| Package | Non-test files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 38 | Product | Every kmx command. The shell-out orchestration layer. |
| `kmx/admin` | 5 | Product | Talks to the plane's admin API. |
| `kmx/blueprint` | 5 | Product | The declarative governed-workflow file. |
| `kmx/scaffold` | 7 | Product | Generates the reviewable Agent YAML. |
| `kmx/guard` | 1 | Product | The context-safety net; refuses a non-kind cluster. |
| `kmx/seam` | 1 | Product | What kmx knows about each upstream credential. |
| `kmx/toolchain` | 2 | Product | Fetches kind/kubectl/helm, pinned and checksum-verified. |
| `kmx/kagentcli` | 1 | Product | Fetches the pinned kagent CLI. |
| `kmx/planebuild` | 1 | Product | Builds the plane's image. |
| `kmx/lift` | 2 | Product | The cloud-free half of the AKS lift. |
| `kmx/config` | 1 | Product | Settings resolution. |
| `kmx/run` | 1 | Product | The shell-out layer. |
| `kmx/secretshapes` | 1 | Product | The one list of credential shapes. |
| `kmx/version` | 1 | Product | Version and upgrade answers. |
| `kmx/delegation` | **0** | **Scaffolding** | A package with no source at all — only `delegation_test.go`. It exists to hold the test that make and kmx are one implementation. |
| `demo/erp` | 2 | **Demonstration** | The fixture ERP. |

`internal/kmx/delegation` is worth naming because it looks like a
product package from the outside and contains no product code. That is
deliberate and correct — a test needs a package to live in — but a
reader counting packages will miscount without being told.

## `plane/` — all product

A separate Go module: the governance proxy that runs in the user's
cluster. Thirteen internal packages and one binary, all of them product.
The root module CANNOT import it — that is the point of the module
boundary, and it is why `kmx plane` fetches the plane's source from the
public Go proxy at kmx's own revision rather than embedding it.

`proxy` (LLM data path and admin API), `gateway` (MCP), `inbound`
(webhooks), `egress`, `meter` (budgets), `pricing`, `store` and `db`
(Postgres and ten migrations), `config`, `redact`, `metrics`, `notify`,
`ops`. Nothing here is a demonstration or a fixture; the synthetic
upstreams live inside the test files rather than as separate packages.

## `k8s/` — where product and demonstration are most interleaved

This is the directory where the boundary is least visible, and the
embedded set is the line that matters.

**Product — embedded in `kmx`, so a user with no checkout applies them:**
`ollama.yaml`, `kagent-values.yaml`, `hello-world.yaml`,
`tools-agent.yaml`, `kaimahi-tools.yaml`, `egress-hosted.yaml`,
`egress-copilot.yaml`, `wasm/runtime.yaml`, all five of `plane/`, all
nine of `models/`, and all three of `observability/`.

**Product — applied from a checkout only:** `inbound-edge.yaml`,
`slack-mcp.yaml`, `kaimahi-slack.yaml`, `kaimahi-github.yaml`,
`kaimahi-release-github.yaml`, `kaimahi-release-ado.yaml`. These are the
connector seams for documented capabilities; they need a credential the
operator supplies, which is why they are not carried.

**Demonstration:** `ap-agent.yaml`, `erp-mcp.yaml`, `erp-fixtures.json`,
`kaimahi-erp.yaml` (the AP scenario), `slack-agent.yaml`,
`github-agent.yaml`, `release-agent.yaml` (the agents each capability
doc walks through).

The distinction that matters for a reader: `hello-world.yaml` and
`tools-agent.yaml` are inside the binary and are what `kmx up` creates;
`ap-agent.yaml` and `release-agent.yaml` are not, and exist for a
walkthrough. All six are "an agent manifest in `k8s/`" and look alike.

## `scripts/` — 65 tracked files, three different jobs

Every one is referenced from outside itself; none is orphaned. The
counts below come from a classification of `git ls-files scripts` in
which all 65 files land in exactly one bucket — not from reading the
directory and estimating.

| Class | Count | Files |
|---|---|---|
| **Product** — embedded in the kmx binary | 6 | `aks-up.sh`, `aks-down.sh`, `plane-deploy.sh`, `netpol-probe.sh`, `kube-guard.sh`, `release-publish.sh` |
| **Product** — operator scripts, reached through make or kmx | 14 | `plane-admin.sh`, `plane-secrets.sh`, `plane-backup.sh`, `plane-restore.sh`, `plane-metrics.sh`, `plane-pods.sh`, `slack-secret.sh`, `slack-approvers.sh`, `copilot-secret.sh`, `inbound-secret.sh`, `inbound-expose.sh`, `release-bind.sh`, `release-run.sh`, `exposure-scan.sh` |
| **Demonstration** | 5 | `erp-deploy.sh`, `ap-demo.sh`, `ap-injection.sh`, `await-approval.sh`, `show-turn.py` |
| **Scaffolding** — checkers and their self-tests | 12 | the nine `check-*` files, `kube-guard-test.sh`, `release-notes.py`, `verify-chat.py` |
| **Scaffolding** — live-cluster probes | 13 | `*-probe.sh`, minus the one that is embedded |
| **Scaffolding** — CI fixtures and synthetic upstreams | 6 | `scripts/ci/`: `synthetic-upstream.sh`, `plain-upstream.sh`, `mcp-echo-server.py`, `plain-mcp-server.py`, `status-unknown-probe.sh`, `workflow-fixture.yaml` |
| **Scaffolding** — mutation specifications | 9 | `scripts/mutations/*.json`, one per checker, declaring how it must be broken |

Two things a reader would get wrong from the directory listing alone:

- **Six of these shell scripts are product.** They are inside the kmx
  binary, extracted at runtime into a temporary tree shaped like this
  repository. Editing one changes what a user who never cloned this
  repository runs. Shell in `scripts/` reads like tooling; these are not.
- **`scripts/ci/` is a separate world.** Six synthetic upstreams and
  fixtures — a fake MCP server, a fake LLM upstream, a workflow fixture —
  that exist so CI can prove a path without a real vendor. Nothing
  outside CI reaches them, and nothing in them is product.

Two files are genuinely dual-role and are counted once above:
`kube-guard.sh` is embedded product AND is one of the nine checkers the
mutation harness breaks on purpose; `verify-chat.py` is a checker AND is
invoked by make and kmx on a real run.

## `docs/` — 30 files, two audiences

**Product documentation** (a user or operator reads it): `README.md`,
`getting-started.md`, `kmx.md`, `aks.md`, `approvals.md`, `spend.md`,
`egress.md`, `identity.md`, `inbound.md`, `isolation.md`, `models.md`,
`operations.md`, `tools.md`, `tool-governance.md`, `workflows.md`,
`hosted-upstreams.md`, `govern-your-agent.md`, `releases.md`, `FAQ.md`.

**Demonstration walkthroughs:** `ap-demo.md`, `demo.md`,
`release-agent.md`, `slack.md`, `SCENARIOS.md`. These describe a
scenario being run, not a capability being configured. `ap-demo.md` is
explicit that its ERP is simulated; the others are less so.

**Maintainer and process:** `development.md`, this file,
`COORDINATION.md` (the coordination board, single-writer, and by a wide
margin the largest file in `docs/`), `reviews/`, `CLI-PROPOSAL.md`,
`NAMING.md`, `entry-point-principles.md`.

## `brand/` — assets, and a checker that holds them to a spec

Six image files plus a README. `README.md`'s hero and architecture
images reference them, and `scripts/check-brand-assets.py` asserts each
one's exact dimensions and transparency, and fails on an asset in the
directory that no requirement names. Product, in the sense that the
front door uses them.

---

## Genuinely unclear — four, and this is a result

1. **`k8s/kaimahi-erp.yaml`.** Referenced only from the Makefile —
   nothing in `scripts/`, CI, the Go tree or `docs/` names it, which is
   unlike every other connector seam. It is demonstration-shaped, but
   the evidence for how it is actually reached is thinner than for its
   neighbours.
2. **`scripts/exposure-scan.sh`.** The only script with exactly one
   caller and no documentation of its own — a make target and nothing
   else. Whether it is an operator tool or a maintainer's one-off is not
   answerable from the tree.
3. **`scripts/show-turn.py`.** Called only from another script. Not
   dead, but no reader would find it, and nothing says what it is for.
4. **The connector seams and their agents as a class** —
   `kaimahi-slack.yaml`, `kaimahi-github.yaml`, `kaimahi-release-*.yaml`
   and the `slack-agent.yaml` / `github-agent.yaml` /
   `release-agent.yaml` manifests beside them. Each supports a
   documented capability AND is the fixture that capability's
   walkthrough deploys. They are honestly both. The table above files
   them under demonstration because that is how a first-time reader
   meets them, but a maintainer should read them as the reference
   wiring for a real connector — and nothing in the tree says which
   reading is intended.

## What moved, and what did not

**Moved:** the fixture ERP, to `internal/demo/erp` and
`cmd/demo/kaimahi-erp`. It makes `cmd/` show one product binary and a
`demo/` directory, so "is this thing real?" is answerable from the path.

**Not moved, deliberately:**

- **`k8s/`.** The demonstration manifests are interleaved with the
  product ones, and a `k8s/demo/` split would be the largest change in
  this lane for the smallest gain: the paths appear in the Makefile,
  five scripts, CI's inline Python assertions, and a dozen docs, and
  `embed.go`'s patterns cannot climb out of their own directory. The
  boundary is real but the naming already carries most of it
  (`ap-*`, `erp-*`, `*-agent.yaml`), and this table carries the rest.
- **`scripts/`.** Same reasoning, more strongly: six of these files are
  inside the binary at paths `embed.go` names literally, so moving them
  changes what `kmx lift` extracts at runtime. The caller table above is
  the cheaper way to answer the same question.
