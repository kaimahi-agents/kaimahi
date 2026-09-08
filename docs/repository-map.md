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

**Unclear** is the fourth answer, and it is a result rather than a gap.
It is a flag, not a fourth bucket: every file still gets a best reading in
the tables — a map with holes is less useful than a map with question
marks — and the list at the end names the cases where that reading rests
on thin evidence. A file can therefore appear in a table *and* at the end.
`scripts/exposure-scan.sh`, `scripts/show-turn.py` and the connector
seams do.

**How each row was decided.** Not by reading names. For every file the
question was *who invokes this* — greps for the path and the symbol
across the Makefile, `.github/workflows/`, `scripts/`, the Go tree,
`embed.go` and `docs/`. Two pieces of evidence override everything else:

- **Embedded in the binary is product.** `embed.go` names what travels
  inside `kmx`. An operator who ran `go install` and has no checkout can
  only use what is in there, so anything embedded is reachable by a real
  user by definition — including six shell scripts, which is not where
  you would look for product code.
- **A demo's own words.** The comment at the top of
  `internal/demo/erp/server.go` (below the package clause, so not a
  package doc comment) says "the gateway in front of it is what the demo
  is about", and `docs/ap-demo.md` lists the ERP under **Simulated**.
  Where the code says what it is, that is the answer.

**There is a second, stricter sense of "product" in this repository, and
it disagrees with the one above.** `docs/release-agent.md:2-5` says:
*"Kaimahi's first real user. Everything else in this repository is a
demonstration — a fixture ERP, a hello-world agent, a Slack channel made
for the purpose."* By that reading, `hello-world.yaml` is a demonstration
too, even though it is inside the binary and is what `kmx up` creates.

Both readings are true of different questions. This map answers **"will a
user encounter this?"**, so `hello-world.yaml` is product. The release
agent's doc answers **"is anyone depending on this?"**, and by that test
almost nothing here is. Neither is wrong; a reader should know both exist,
because the tree does not say which one a given file was written under.

**What here is checked, and what rests on review.**
`scripts/check-repository-map.py` runs this document against the tree on
every pull request. It checks what the tree can settle: the counts, the
paths, the membership lists, the callers, the arithmetic between them —
and, more usefully than any single number, that every tracked file under
`cmd/`, `internal/`, `k8s/`, `scripts/`, `docs/`, `brand/` and the root is
named by exactly one list here. Add a file and the check fails until
somebody has said what it is.

It does NOT check the classifications themselves. Whether a manifest is
product or demonstration is a judgement, and a checker enforcing one would
either be wrong or would freeze an opinion this tree is allowed to change.
That column is defended by review, and by the evidence each row cites. The
three cases at the end are neither: they are a standing question, and what
is enforced is that the question survives — the section exists, it says
how many cases it holds, it holds that many, and every path it rests on is
still in the tree.

---

## The short version

| Area | Product | Demonstration | Scaffolding |
|---|---|---|---|
| `cmd/` | `kmx` | `demo/kaimahi-erp` | — |
| `internal/` | `kmx/` (15 packages) | `demo/erp` | `kmx/delegation` (tests only) |
| `plane/` | all of it | — | test fakes inside packages |
| `k8s/` | the embedded set, the plane, the model presets, the release agent and its seams | the AP, Slack and GitHub scenarios | — |
| `scripts/` | 22 (6 embedded in the binary, 16 operator) | 3 | 47 (checkers, probes, CI fixtures, mutation specs) |
| `docs/` | 22 capability docs, incl. `release-agent.md` | `ap-demo.md`, `demo.md` | `COORDINATION.md`, `reviews/`, `development.md`, this file |
| `brand/` | 6 assets used by the README and the org profile | — | its own checker |

---

## `cmd/` — two binaries, and only one of them is the product

| Path | Class | Evidence |
|---|---|---|
| `cmd/kmx` (15 files) | **Product** | The CLI. The documented front door is `install.sh`, piped from `curl` to `sh`; `go install .../cmd/kmx@latest` is the stated alternative. |
| `cmd/demo/kaimahi-erp` (2 files) | **Demonstration** | A fake accounts-payable ERP. Applied by `k8s/erp-mcp.yaml` via `scripts/erp-deploy.sh`; `docs/ap-demo.md` lists it under "Simulated" — "no vendor, no bank, no payment rail". |

Until this change both sat directly under `cmd/`, as peers, and nothing
distinguished them.

## `internal/` — the product's packages, and one fixture

`internal/kmx/` is fifteen packages. All but one are product — the
exception, `delegation`, is below — and the line between them is
consistent enough to state as a rule: **anything that
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

| Package | Non-test source files | Class | What it is |
|---|---|---|---|
| `kmx/app` | 38 | Product | Every kmx command. The shell-out orchestration layer. |
| `kmx/admin` | 5 | Product | Talks to the plane's admin API. |
| `kmx/blueprint` | 5 | Product | The declarative governed-workflow file. |
| `kmx/scaffold` | 7 | Product | Generates the reviewable Agent YAML. |
| `kmx/guard` | 1 | Product | The context-safety net. A local kind context proceeds with a banner; any other requires confirmation naming it; no confirmation, unknown context or unreadable kubeconfig refuses. |
| `kmx/seam` | 1 | Product | What kmx knows about each upstream credential. |
| `kmx/toolchain` | 2 | Product | Fetches kind/kubectl/helm, pinned and checksum-verified. |
| `kmx/kagentcli` | 1 | Product | Fetches the pinned kagent CLI. |
| `kmx/planebuild` | 1 | Product | Builds the plane's image. |
| `kmx/lift` | 2 | Product | The cloud-free half of the AKS lift. |
| `kmx/config` | 1 | Product | Settings resolution. |
| `kmx/run` | 1 | Product | The shell-out layer. |
| `kmx/secretshapes` | 2 | Product | The one list of credential shapes — `shapes.json` is the list, `shapes.go` reads it. The only package here whose non-test files are not all Go. |
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
The root module cannot import it — no `require`, and no `plane/...`
import anywhere in root `cmd/` or `internal/`. That is the point of the
module boundary, and it is why `kmx plane` fetches the plane's source
from the public Go proxy at kmx's own revision rather than embedding it.

But the two are coupled, just not at compile time:
`internal/kmx/planebuild` holds the plane's module path as string
constants and shells out to `go install …@<revision>`. That dependency is
real and load-bearing, and no import graph or build tool will show it —
worth knowing before anyone moves or renames the plane's package paths.

`proxy` (LLM data path and admin API), `gateway` (MCP), `inbound`
(webhooks), `egress`, `meter` (budgets), `pricing`, `store` and `db`
(Postgres and ten migrations), `config`, `redact`, `metrics`, `notify`,
`ops`. Nothing here is a demonstration or a fixture; the synthetic
upstreams live inside the test files rather than as separate packages.

## `k8s/` — where product and demonstration are most interleaved

This is the directory where the boundary is least visible, and the
embedded set is the line that matters.

**The repository has its own ledger for this**, and it is better than any
grep: `TestTheConnectorFamiliesAreNotEmbedded`
(`internal/kmx/app/manifests_test.go`) names the twelve manifests that must
NOT ride along, and — because an exclusion passes for free once the thing
it excludes stops existing — it `os.Stat`s each one and fails if it moves.
Twenty-five of `k8s/`'s 38 files are embedded, twelve are named by that
test, and the thirteenth is `k8s/erp-fixtures.json`, a JSON corpus rather
than a manifest.

**Product — embedded in `kmx`, so a user with no checkout applies them
(25):** `ollama.yaml`, `kagent-values.yaml`, `hello-world.yaml`,
`tools-agent.yaml`, `kaimahi-tools.yaml`, `egress-hosted.yaml`,
`egress-copilot.yaml`, `wasm/runtime.yaml`, all five of `plane/`, all nine
of `models/`, and all three of `observability/`.

**Product — applied from a checkout only (6):** `inbound-edge.yaml`,
`slack-mcp.yaml`, `kaimahi-slack.yaml`, `kaimahi-github.yaml`, and the
release agent's two seams `kaimahi-release-github.yaml` and
`kaimahi-release-ado.yaml`. These need a credential the operator supplies,
which is why they are not carried.

**Product — the release agent (1):** `release-agent.yaml`. Filed here and
not under demonstration on the authority of `docs/release-agent.md:2-5`:
*"Kaimahi's first real user. Everything else in this repository is a
demonstration."* It cuts releases of a real project on a real repository.

**Demonstration (6):** `ap-agent.yaml`, `erp-mcp.yaml`,
`erp-fixtures.json`, `kaimahi-erp.yaml` (the AP scenario),
`slack-agent.yaml` and `github-agent.yaml` (the agents the Slack and
GitHub walkthroughs deploy).

The distinction that matters for a reader: `hello-world.yaml` and
`tools-agent.yaml` are inside the binary and are what `kmx up` creates;
`ap-agent.yaml`, `slack-agent.yaml` and `github-agent.yaml` are not, and
exist for a walkthrough; `release-agent.yaml` is also not embedded but is
not a walkthrough either — it is the one agent this project depends on.
All six are "an agent manifest in `k8s/`" and look alike.

## `scripts/` — 72 tracked files, three different jobs

**None is orphaned**, but "orphaned" needs care: 60 of the 72 are named
by something outside themselves, and the twelve `scripts/mutations/*.json`
are named by nothing at all — `check-mutations.py` finds them by globbing
the directory. That is deliberate (a checker added without mutations is
meant to be a failure, so the harness must not read a list someone can
forget to update), and it means a grep for references is the wrong test
for that one directory.

The counts below come from a classification of `git ls-files scripts` in
which all 72 files land in exactly one bucket — not from reading the
directory and estimating.

| Class | Count | Files |
|---|---|---|
| **Product** — embedded in the kmx binary | 6 | `aks-up.sh`, `aks-down.sh`, `plane-deploy.sh`, `netpol-probe.sh`, `kube-guard.sh`, `release-publish.sh` |
| **Product** — operator scripts, reached through make, kmx, or another product script | 16 | `plane-admin.sh`, `plane-secrets.sh`, `plane-backup.sh`, `plane-restore.sh`, `plane-metrics.sh`, `plane-pods.sh`, `slack-secret.sh`, `slack-approvers.sh`, `copilot-secret.sh`, `inbound-secret.sh`, `inbound-expose.sh`, `release-bind.sh`, `release-run.sh`, `exposure-scan.sh`, `await-approval.sh`, `show-turn.py` |
| **Demonstration** | 3 | `erp-deploy.sh`, `ap-demo.sh`, `ap-injection.sh` |
| **Scaffolding** — checkers and their self-tests | 15 | the twelve `check-*` files, `kube-guard-test.sh`, `release-notes.py`, `verify-chat.py` |
| **Scaffolding** — live-cluster probes | 13 | `*-probe.sh`, minus the one that is embedded |
| **Scaffolding** — CI fixtures and synthetic upstreams | 6 | `scripts/ci/`: `synthetic-upstream.sh`, `plain-upstream.sh`, `mcp-echo-server.py`, `plain-mcp-server.py`, `status-unknown-probe.sh`, `workflow-fixture.yaml` |
| **Scaffolding** — mutation specifications | 12 | `scripts/mutations/*.json`, one per checker, declaring how it must be broken |
| **Scaffolding** — a checker's record of what it has been told about | 1 | `board-open-drift.json`, the disagreements in the coordination board that `check-board.py` found, plus those found by hand where it could not look, and a record of how each was closed |

**Two of those look like demo scripts and are not.** `await-approval.sh`
was renamed out of the AP demo — its own comment says "so the release
driver can reuse" it — and `scripts/release-run.sh` calls it twice.
`show-turn.py` renders one agent turn and is called only by
`release-run.sh`. Both serve the release agent, which
`docs/release-agent.md` calls "Kaimahi's first real user", so both are
product with an `ap-`-shaped history.

Three things a reader would get wrong from the directory listing alone:

- **Six of these shell scripts are product.** They are inside the kmx
  binary, extracted at runtime into a temporary tree shaped like this
  repository. Editing one changes what a user who never cloned this
  repository runs. Shell in `scripts/` reads like tooling; these are not.
- **`scripts/ci/` is a separate world.** Six synthetic upstreams and
  fixtures — a fake MCP server, a fake LLM upstream, a workflow fixture —
  that exist so CI can prove a path without a real vendor. Nothing
  outside CI *runs* them — though two product docs,
  `docs/hosted-upstreams.md` and `docs/govern-your-agent.md`, link them
  as worked examples a reader can copy, which is the one way a user meets
  them.
- **A `check-` prefix does not mean CI-only, and a name does not mean
  ownership.** `kube-guard.sh` is shipped inside the binary and is also
  mutation-tested as a checker; `verify-chat.py` looks like it belongs to
  `make chat` because a dozen comments name it, but nothing in the
  Makefile or in Go actually runs it.

One file is genuinely dual-role and is counted once above:
`kube-guard.sh` is embedded product — `kmx lift` writes it into a
temporary tree and executes it — AND is one of the twelve checkers the
mutation harness breaks on purpose.

`verify-chat.py` is a checker. Neither make nor kmx runs it: every
occurrence in the Makefile is a comment line rather than a recipe, and
every occurrence in Go is a comment or, in one case, the text of a test's
own failure message. Its real callers are
`.github/workflows/ci.yml` (fourteen invocations among nineteen
mentions — the other five are comments, which is the trap),
`scripts/release-run.sh`,
and `docs/tools.md`, which gives it as a step a reader runs by hand.

## `docs/` — 34 tracked files, two audiences and two assets

**Product documentation (22)** — a user or operator reads it: `README.md`
(the index), `getting-started.md`, `kmx.md`, `aks.md`, `models.md`,
`tools.md`, `spend.md`, `tool-governance.md`, `approvals.md`,
`govern-your-agent.md`, `egress.md`, `inbound.md`, `hosted-upstreams.md`,
`identity.md`, `operations.md`, `releases.md`, `workflows.md`, `FAQ.md`,
`isolation.md`, `slack.md`, `foreign-runtime.md`, and `release-agent.md`.

`release-agent.md` sits here rather than under demonstration on its own
authority — it documents the one agent this project actually depends on.
`slack.md` is a borderline case kept here deliberately: the capability is
product, the walkthrough uses a channel made for the purpose.

**Demonstration walkthroughs (2):** `ap-demo.md` and `demo.md`. These
describe a scenario being run rather than a capability being configured.
`ap-demo.md` says its ERP is simulated in its own second table row;
`demo.md` is less explicit.

**Maintainer and process (8):** `development.md`, `repository-map.md`
(this file),
`COORDINATION.md` (the coordination board, single-writer, and by a wide
margin the largest file in `docs/` — enough that any tool measuring
"documentation" over this directory is mostly measuring it),
`reviews/2026-09-07-drift-review.md`, `CLI-PROPOSAL.md` (self-labelled
superseded), `SCENARIOS.md` (self-labelled a working concept),
`entry-point-principles.md`, `NAMING.md`.

**Assets (2):** `docs/assets/architecture.mmd` (the Mermaid source) and
`docs/assets/architecture.svg` (the rendered diagram the root README
embeds). Worth one line of warning: the `.svg` has no trailing newline, so
`wc -l` reports it as 0 — a checker using a line count as a proxy for
"this file has content" would read a healthy 48KB asset as empty.

**Two docs are effectively unfindable**, which is a legibility problem of
the same family this map exists to fix. `isolation.md` appears nowhere in
`docs/README.md` — neither the by-task table nor the project-docs section
— and is reachable only from one table cell in `kmx.md`. `docs/reviews/` is
referenced exactly once anywhere in `docs/` outside this map, from the
coordination board, and never from the documentation index.

## `brand/` — assets, and a checker that holds them to a spec

Six image files plus a README. Only one is *used* by anything in the
tree: `README.md:2` embeds `brand/hero.png`. (The architecture picture
beside it is `docs/assets/architecture.svg`, which is not a brand asset.)
The other five — `mark.svg`, `mark.png`, `wordmark.svg`,
`social-preview.png`, `mascot.png` — are consumed *outside* this
repository: the org avatar, the favicon, GitHub's social preview, and a
design source. They are not unreferenced, though: `brand/README.md` gives
each one its canvas and its use, and `scripts/check-brand-assets.py`
enforces that table. Which is the point of the checker — an asset nothing
in the tree renders cannot go wrong visibly, so it is held to a written
spec instead, and an asset in the directory that no requirement names is a
failure.

Precisely what it checks, because "validates the brand assets" oversells
it: exact pixel dimensions and whether transparency is present, for the
four PNGs only; a title-string match for the two SVGs; and — worth
knowing if you go looking in `brand/` for everything it guards — it also
covers `docs/assets/architecture.svg`, which is not a brand asset.

Product, in the sense that the front door and the organisation's identity
use them.

## `blueprints/`, `.github/` and the root files

Small, but they were missing from the first version of this map, which is
its own kind of misleading — a reader checking whether something is
covered needs the map to cover everything.

| Path | Class | Evidence |
|---|---|---|
| `blueprints/release.yaml` | **Product** | Embedded — `//go:embed blueprints` is a directory pattern, so it travels in the binary, and `kmx workflow list/show/run` loads it from there. It carries no repository, organisation or token; those arrive as `--set` parameters. |
| `install.sh` | **Product** | The documented front door. `curl … \| sh` downloads the pinned release binary, verifies its sha256, and optionally runs `kmx quickstart`. Exercised in CI. |
| `README.md` | **Product** | The front door's front door. Its ordering is machine-checked by `check-readme-front-door.py`. |
| `CHANGELOG.md` | **Product** | A build input, not just prose: the release workflow EXTRACTS its notes from here via `release-notes.py`, and refuses to publish a tag with no section. |
| `CONTRIBUTING.md`, `LICENSE` | **Product** | The contributor entry point and the MIT licence. |
| `embed.go` | **Product** | The module root's only job: the one file from which a `go:embed` directive can reach `k8s/`. |
| `Makefile` | **Scaffolding** | The operator interface *from a checkout*. Someone who installed kmx never sees it; it delegates the developer journey to the binary and keeps the demo and connector targets kmx does not own. |
| `.github/workflows/ci.yml`, `release.yml` | **Scaffolding** | The gates, and the tag-driven release. |
| `.github/actions/classify-change/` | **Scaffolding** | Decides whether a change is docs-only so cluster jobs can short-circuit. Fails closed. |
| `staticcheck.conf` | **Scaffolding** | Both modules' lint configuration, and the written reason for the one check that is off. |
| `go.mod`, `go.sum` | **Product** | The root module. |
| `.gitignore` | **Scaffolding** | — |
| `.dockerignore` | **Demonstration** | Its own first line says why: "The ERP image is built from the repository root." `plane/` has its own Dockerfile and context and never reads it. The root file that looks like general build hygiene exists for the demo. |

---

## Genuinely unclear — three, and this is a result

1. **`scripts/exposure-scan.sh`.** One caller — a make target — and no
   documentation of its own. Whether it is an operator tool or a
   maintainer's one-off is not answerable from the tree. (It is not
   unique in having one caller: `scripts/ci/status-unknown-probe.sh` is
   reached only from CI, but that one's home makes its purpose obvious.)
2. **`scripts/show-turn.py`.** Called only from another script. Not
   dead, but no reader would find it, and nothing says what it is for.
3. **The connector seams and their agents as a class** —
   `kaimahi-slack.yaml`, `kaimahi-github.yaml`, `kaimahi-release-*.yaml`
   and the `slack-agent.yaml` / `github-agent.yaml` /
   `release-agent.yaml` manifests beside them. Each supports a
   documented capability AND is the fixture that capability's
   walkthrough deploys. They are honestly both. The tables above file
   the Slack and GitHub agents under demonstration because that is how a
   first-time reader meets them, and the release family under product on
   its own doc's authority — but a maintainer should read all of them as
   the reference wiring for a real connector, and nothing in the tree
   says which reading is intended.

## What moved, and what did not

**Moved:** the fixture ERP, to `internal/demo/erp` and
`cmd/demo/kaimahi-erp`. It makes `cmd/` show one product binary and a
`demo/` directory, so "is this thing real?" is answerable from the path.

**Not moved, deliberately:**

- **`k8s/`.** The demonstration manifests are interleaved with the
  product ones, and a `k8s/demo/` split would be the largest change in
  this lane for the smallest gain: the demonstration manifests are named
  by the Makefile, three scripts (`erp-deploy.sh`, `ap-demo.sh`,
  `ap-injection.sh`), CI's inline Python assertions and several docs —
  fourteen tracked files under `scripts/` contain the literal `k8s/` — and
  `embed.go`'s patterns cannot climb out of their own directory. The
  boundary is real but the naming already carries most of it
  (`ap-*`, `erp-*`, `*-agent.yaml`), and this table carries the rest.
- **`scripts/`.** Same reasoning, more strongly: six of these files are
  inside the binary at paths `embed.go` names literally, so moving them
  changes what `kmx lift` extracts at runtime. The caller table above is
  the cheaper way to answer the same question.
