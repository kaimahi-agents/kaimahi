# Changelog

Kaimahi is **pre-1.0 and incubating**. Versions are semantic with the pre-1.0
reading of that word:

- **Patch** (`v0.1.0` → `v0.1.1`) — fixes only. No schema change, no flag
  removed, no behaviour an operator was relying on.
- **Minor** (`v0.1.0` → `v0.2.0`) — anything else, including a breaking one.
  Below 1.0 the minor number is where breaking changes live, so every entry
  that breaks something says so under **Breaking** and says what to do about
  it.
- **Pre-release** (`v0.2.0-rc.1`) — a candidate for the version it names. Go's
  `@latest` ignores these, so a release candidate cannot become anybody's
  default install by accident.

There is no 1.0 promise yet and no support window. What there *is*: the
release job refuses to publish a tag that has no section here, so a version
without notes cannot exist.

Each entry names what changed, and — where it matters — what an operator has
to do. Sections: **Added**, **Changed**, **Fixed**, **Breaking**, **Upgrading**.

## Unreleased

### Added

- **`scripts/check-repository-map.py`** — `docs/repository-map.md` asserted
  several dozen facts about this tree and nothing checked any of them. CI now
  runs the map against the tree on every pull request: the file counts, the
  paths, the membership lists, the callers, and — the assertion that catches
  the most — that every tracked file under `cmd/`, `internal/`, `k8s/`,
  `scripts/`, `docs/`, `brand/` and the repository root is named by exactly
  one list in the map. Adding a file fails the check until somebody has said
  what it is.

  Nothing here checks the *classification*. Whether a manifest is product or
  demonstration is a judgement, and a checker enforcing one would freeze an
  opinion the tree is allowed to change; what is enforced is that a
  classification exists. The three cases the map calls genuinely unclear stay
  a standing question by design — the check requires the section to survive,
  to say how many cases it holds, and to name paths that are still there.

  Every list is derived rather than copied: the embedded manifests come out of
  `embed.go`'s own `go:embed` patterns, the manifests that must not ride along
  out of the Go test that names them, every count out of `git ls-files`.

### Changed

- **The prerequisite list is one item: a container engine.** It was five (Go,
  Docker or Podman, kind, kubectl, Helm) plus make and curl. Go is now needed
  only by the two commands that build the plane's image: `kmx plane`, and
  then only when it is run from outside a checkout, and `kmx lift`, which
  demands it whenever its plane phase runs.
- Measured on a clean machine (no tooling, no checkout), time from one command
  to an agent's answer: **246s → 178s**. Against the same measurement of `kmx
  up` from this branch's parent, 217s → 178s; the first-answer kagent profile
  is 33s where the full one is 64s.
- `kmx` now uses one Cobra command tree for nested commands, flags,
  command-specific help, validation, and Bash/Zsh/Fish completion. Operational
  behavior, context guards, Make delegation, and machine-readable stdout remain
  in the existing application layer.
- Documentation and code comments now say what a thing does rather than
  citing the planning identifier that tracked it. A lane or decision number
  is a coordination artifact nobody reading the code can resolve, so each
  one is replaced by the mechanism or the rule it stood for — and where the
  reference was carrying the argument, the reason is written out instead of
  cited. Trailing pointers to a capability document are unchanged; the
  planning board keeps its own identifiers.
- **`kmx agent create` and the blueprint parser refuse a wider set of
  credential shapes**, and both now read one shared list rather than each
  keeping its own — so a shape added in one place is refused in all of them,
  including the repository-wide scan CI runs on every change. Neither refuses
  less than it did before. The list gained Slack's app-level token, which is
  not an `xox` shape and so was covered nowhere, and an Azure DevOps personal
  access token.

### Fixed

- **Eight claims in `docs/repository-map.md` were wrong on the day it merged**,
  found by writing the checker above. `scripts/` holds 67 tracked files and
  not 65; there are ten `check-*` scripts and not nine, so the checker bucket
  is 13 and the summary table's scaffolding column was 40 where the tree said
  42; ten mutation specifications and not nine, and ten checkers the mutation
  harness proves rather than nine; twelve files under `scripts/` name a
  `k8s/` path and not eleven. Two sentences were also unfalsifiable as
  written and are now precise: `docs/reviews/` is referenced once *outside the
  map* rather than once in the repository, and `verify-chat.py`'s occurrences
  in Go are comments except one, which is a test's own failure message.

- **`kmx credential capture <upstream> <repository|organization>`** — the last
  step of the journey that still needed a checkout. Installing kmx, standing a
  cluster up, deploying the plane and governing an agent all work with no
  clone; handing the thing a token was `make release-secret` /
  `make ado-secret` / `make github-secret`, which are make targets in a
  repository. The credential is now typed at a prompt, checked against the
  upstream, and written straight into the Secret the gateway reads.

  **This is the one path on which kmx accepts credential material, and it is
  fenced.** The value is read from a terminal or not at all: there is no flag,
  no environment variable, no file, and a pipe or a redirect is refused rather
  than read, because a credential that can arrive through a pipe can arrive
  from a shell history or a CI log. The terminal's echo is off while it is
  typed. It travels to exactly two places: the upstream, in an
  `Authorization` header, because a capture that stored an unchecked value
  would be worse than the make target it replaced; and the Secret. Not argv
  (`--from-literal` would put it in the process table, so the Secret is
  rendered in memory and piped to `kubectl apply -f -`), not a temporary file,
  not a log, not this command's own output — and the buffers holding it are
  cleared when the write returns. The context guard
  runs first, so the cluster about to hold the credential is named before
  anything is typed. Everything else in kmx keeps refusing credential
  material: the agent wizard still screens its input against credential
  shapes, and a blueprint carrying a credential-shaped key is still refused
  before the document is decoded.

  The checks the shell scripts made are kept, and so is their honesty about
  what cannot be checked. GitHub: refused unless the token is fine-grained,
  reads the repository named, announces no OAuth scopes, and GitHub's answer
  is about that repository; the expiry is REPORTED, and whether the token
  reaches only one repository is not proven at all, because GitHub exposes no
  endpoint that says so. Azure DevOps: refused unless it is an access token
  with an expiry claim that has not passed and the hosted server accepts it on
  a real handshake; the audience is REPORTED, because Entra writes it two ways
  for the same request and refusing on it refused correct tokens. A capture
  that is refused stores nothing.

  An upstream that already holds a credential is not overwritten silently:
  the capture stops and names `--replace`.

- **`kmx workflow` and blueprints** — one declarative file says what a
  governed workflow reaches, which of its calls need no human, which need one,
  and in what order; `kmx workflow govern` applies the governance and
  `kmx workflow run` executes the steps. A blueprint NAMES seams that already
  exist in the plane's table and asserts the `policy_fields` it depends on —
  it cannot create a hosted or keyed upstream, because the overlay refuses
  the custody fields that would make one — and it carries no credential in
  any form. kmx carries `blueprints/release.yaml`, which reproduces the
  release agent's governance exactly: the same tool allowlist `make
  release-allow` sets and the same standing constraints
  `scripts/release-bind.sh` writes, proven by a test that runs those and
  diffs the result. Steps can be conditional (`when: <parameter>`), so one
  blueprint covers a release that builds on GitHub Actions, on Azure DevOps,
  or on both; `kmx workflow show` and `kmx workflow run` describe the same
  run for the same `--set`, and a guard on a parameter that carries a default
  is refused rather than silently always-on. See
  [docs/workflows.md](docs/workflows.md).
- **The release agent** — the first thing in this repository that is not a
  demonstration. One command reads what merged since the last release,
  drafts the notes, and proposes the release branch and the builds. Cutting
  the branch and publishing are denied by default, filed naming the version
  and the repository, approved by a person, and admitted under a grant
  welded to that call — approving "cut release/v1.2.3" cannot be spent on
  the next one. Build dispatch runs under a standing constraint bounded to
  named pipelines instead, because a human approving every build is a human
  who stops reading. It reaches two hosted seams: a write-scoped GitHub
  credential, and Microsoft's hosted Azure DevOps server, which is
  authenticated by Microsoft Entra rather than by a key. The agent never
  carries a byte — the workflows and pipelines it dispatches do the
  building. The one exception is the final publish, where no CI system is
  in both networks: the DECISION is governed like every other consequential
  call, but the transfer runs on the operator's machine under their own
  credentials, which is weaker than the rest of the path and is written
  down rather than glossed. See
  [docs/release-agent.md](docs/release-agent.md).

- **`kmx lift`** — the same agent you have been running locally, on AKS, in
  one command. It creates the cluster (or acts on one you already have with
  `--byo`, which it never creates, deletes or adopts), builds the plane's
  image in a private registry, renders the manifest for it, deploys the
  plane and the agents, and wires Azure-managed Prometheus, Container
  Insights and a workbook. `--plan` prints what it would create and stops;
  `--step` runs one phase, so a run that stopped can be resumed where it
  stopped rather than from the beginning. `kmx lift down` removes what the
  lift created — and on a cluster you brought, only that.

  Two refusals worth knowing before you plan around them. **A cluster with
  no NetworkPolicy engine is refused on both paths**, before the boundary
  phase writes anything: AKS accepts every NetworkPolicy on such a cluster
  and enforces none, so the boundary would be present and inert, which
  reads as protection and is worse than having none. On a cluster the lift
  creates, the creation itself already refused anything but an enforcing
  engine; on one you bring, the control plane is asked and an unreadable
  answer is refused too, rather than assumed either way. An engine being
  present is still not enforcement, so the boundary phase then runs the
  existing negative probe against the live boundary. **The second refusal
  is `--byo`-only**: a cluster whose identity cannot pull from the registry
  is refused with the `az aks update --attach-acr` for its owner to run,
  because granting that role means writing a role assignment on your
  subscription, which a demo has no business doing quietly. And the model credential is yours: a managed cluster runs a
  hosted model, a provider token is not one of the three upstream
  credentials `kmx credential capture` can check a value against, and the
  lift stops and names `make plane-copilot-secret` rather than storing
  something it cannot vet. That is the one step on the managed path that
  still needs a checkout.

- **`kmx tools add <name>`** — point Kaimahi at an MCP server this
  repository did not write. It reads the server's own Service to derive the
  pod selector and the resolved container port, scaffolds four reviewable
  documents (the gateway's table entry as an overlay fragment, the proxy's
  egress to that server, that server's ingress from the proxy alone, and
  the `RemoteMCPServer` whose URL is the gateway), sends the candidate
  table to the running plane to be parsed by the same code the proxy booted
  with, and applies it behind the context guard. A tool named without a
  `policy_fields` declaration is refused rather than defaulted, and the
  weakest declaration announces itself in the file. The committed table is
  never edited: onboarded upstreams live in a separate ConfigMap merged
  over it at boot, and an overlay that would redefine a committed entry is
  refused rather than resolved by precedence.

- **`kmx flow [credential]`** — the ledger, the tool audit, the approvals
  trail and the inbound trail merged into one timeline, so "what happened
  in that run" is one command instead of four reads and a mental join.

- **`kmx tools sandbox`** — installs a WASM runtime class for tool
  execution, and `kmx tools sandbox status` reports whether it is installed
  and what uses it. Read the banner before running it: this writes a
  containerd shim onto every Linux node with a privileged, hostPID,
  host-root-mounted DaemonSet, which nothing else in Kaimahi asks for.

- **Bring-your-own agents.** `kmx agent create --image` scaffolds an agent
  that runs your container serving A2A on :8080 instead of a declarative
  one, carrying the governed seams across as environment (the proxy's
  base URL, the gateway's endpoint, the credential reference) where the
  cluster has a governance plane — and saying plainly that it cannot verify
  your image honours them; the ledger is where that becomes proven.
  `--isolation virtual-node` places it on an ACI virtual node, and is
  refused without `--image`, because a declarative agent cannot be
  VM-isolated. `--run-as-user` states the UID the image runs as, or `root`
  to say it needs one, rather than kmx guessing a UID that is not yours.

- **`kmx agent chat --interactive`** keeps one streamed session open
  instead of one question per process, `--session` resumes a kagent session
  by id, and `--json` forces the raw A2A task at a terminal. At a terminal
  chat prints the reply, the tools the agent called and the token cost; a
  pipe still gets the raw task, byte for byte, because things parse it.

- **`kmx status` counts how much of the system is actually governed** —
  model seams, tool seams and credentials, as "1 of 2 governed, 1 direct",
  read from the cluster objects with no plane, credential or internet
  needed. It never invents a zero: a count it could not take reads
  `unknown`, which is a different word from `none`. `-o json|yaml` gives
  the same document for automation.

- **The plane reports its version, and kmx refuses to guess across a
  gap.** `/admin/version` returns the plane's version and an integer admin
  contract; kmx checks it once per session and refuses **per operation** —
  a read that the older plane can still serve is served, and only the
  operation that needs the newer contract is refused, naming both versions.
  A plane too old to answer at all is named as such rather than assumed
  current.

- **`kmx quickstart`** — one command from a machine that has a container
  engine to an agent that has answered a question. It runs `kmx up`'s steps in
  `kmx up`'s order, with the same waits and fail-closed checks, but defers
  everything a first question cannot reach: kagent's console, its bundled tool
  server, the MCP controller, the second agent and the whole governance plane.
  `--output json` makes the result machine-readable (`ok`, `answer`,
  `governed`, `elapsed_seconds`, `next`) for an agent driving kmx from inside
  a harness; the command is safe to run twice.
- **`install.sh`** — `curl -fsSL .../install.sh | sh` downloads the release
  binary for the platform, verifies its published sha256 before installing it,
  and puts it in `~/.local/bin` without sudo. `--quickstart` carries on into
  `kmx quickstart`, so one command ends with a working agent.
- **kmx fetches kind, kubectl and Helm** when the machine does not have them,
  pinned and checksum-verified into `~/.config/kmx`, exactly as it has always
  fetched the pinned kagent CLI — the digest is re-verified on every use, not
  only at download. A copy already on PATH is always preferred and never
  shadowed. `KMX_TOOLCHAIN=off` restores the previous behaviour, where a
  missing tool is an error naming its install page.


- **A stale `bin/kmx` could apply the previous version of twelve embedded
  files.** The manifests, blueprints and scripts kmx carries are inside the
  binary, so editing one has to relink it — but the Makefile's list of those
  prerequisites had drifted twelve files behind `embed.go`. Editing the WASM
  runtime, the release blueprint, any observability manifest or any of the
  five scripts the managed path ships left a binary built before the edit,
  which `make sandbox`, `make lift` or a blueprint run then applied. CI never
  saw it, because a fresh runner builds once. The test guarding this asserted
  two hard-coded filenames and stayed green throughout; it now derives the
  list from `embed.go`'s own directives, so the two cannot drift again.
  Affects checkouts only — an installed kmx was never stale.

- **`make ap-ask` downloaded a kagent CLI it did not use.** It delegated to
  `make chat`, which is kmx, which fetches its own pinned copy. The
  prerequisite is gone, and the comment that explained the duplicate away —
  it claimed a checkout hands kmx the make-side binary — has been corrected
  to say what actually happens.

- **`kmx` no longer acts silently on a cluster nobody chose.** Context
  resolution used to fall through to `kind-kaimahi-p1`, label the result as
  coming from a `KIND_CLUSTER` variable that did not exist, and never print
  where the choice came from. It cost a real cluster: `kmx down` announced
  "context not created yet" and then deleted one that existed. Two things
  changed. The banner now names the **source** of the context it chose, on
  every command. And the guard **refuses** the specific case that caused
  that loss: a context reached only by falling through to the default, on a
  kubeconfig that has contexts, whose current-context is something else.
  The fallback itself is still there and still right for the fresh-machine
  case — an empty kubeconfig gets `kind-kaimahi-p1`, which is what `kmx up`
  is about to create — so this is a narrowed refusal plus an honest label,
  not the removal of a default.
- **A stale `Accepted` condition is reported as `unknown`, not as a pass.**
  A Kubernetes condition records when a verdict last *changed*, not when it
  was last checked, so after a credential is replaced the old pass stands
  until kagent looks again — measured at 49 seconds on a live cluster, and
  forever if the new credential is good, because nothing changed. kmx waits
  90 seconds for a verdict reached after the write. That wait can confirm a
  REJECTION and can never confirm a pass, which is the right way round: the
  case worth blocking on is the broken credential, and a timeout is
  reported as `unknown` rather than as success.
- **The clone-free path waits the Go module proxy out instead of failing.**
  A freshly pushed revision is not immediately servable by proxy.golang.org,
  and `kmx plane` on a just-merged commit failed rather than retrying.
- **The agent pods are hardened like the rest of the tree.** The pods that
  actually execute model output were the only workload setting no security
  context at all, while the proxy and the fixture ERP set `runAsNonRoot`,
  no privilege escalation, dropped capabilities and a read-only root
  filesystem. Five of the six committed agents and the `kmx` scaffolder now
  carry the same posture, so an agent an adopter creates is hardened too —
  the scaffolder is the half that matters, because without it every agent
  an adopter creates is unhardened. `k8s/release-agent.yaml` landed after
  this change with no security context at all — the agent holding the
  write-scoped GitHub credential — and now carries the same posture as the
  other five. Two
  things this took a cluster to learn are written into the manifests:
  `runAsNonRoot` alone fails when the image names its user by name rather
  than by number (kagent's image says `python`, so the numeric `1001` has
  to be stated alongside it), and a read-only root filesystem needs
  somewhere to write, which is an `emptyDir` on `/tmp`.

- A one-shot `kmx agent chat` no longer reports nothing when the agent asks a
  question instead of answering. kagent's runtime gives every agent a built-in
  `ask_user` tool that no manifest declares and none can remove; a small model
  occasionally calls it, and the task then ends `input-required` with an empty
  reply that nothing in a script can answer. kmx now re-asks — at most twice,
  only when the agent did nothing but ask, never in a resumed session, and
  saying so on stderr every time. A pending human APPROVAL is the same
  `input-required` state and is never re-asked: that decision is a person's.
  Neither case can become a success — `scripts/verify-chat.py` still fails
  closed on both, and now names which one it was.
- `kmx workflow govern` no longer writes the standing-bounds fragment and
  restarts the proxy before discovering that the credential its governance is
  written for does not exist. The credential is checked first, and a missing
  one is refused with the command that creates it rather than
  `tool-allow failed (HTTP 404): no such credential` after the cluster has
  already been changed.
- The documented by-hand install used `sha256sum --ignore-missing`, a GNU
  coreutils flag that macOS (no `sha256sum`) and BusyBox (Alpine, slim images)
  both reject — so the verification step failed on two of the platforms the
  release publishes for. `docs/releases.md` now shows a portable comparison,
  and `install.sh` uses whichever of `sha256sum`, `shasum` or `openssl` exists.
- `kmx workflow run --dry-run` no longer writes to the cluster. It printed
  "Nothing was created" and then re-minted every seam credential the workflow
  declares a `refresh:` for, applying a Secret into plane custody — a turn step
  names no upstream, so the refresh ran on the FIRST step of any run, including
  one whose opening step only reads and drafts. A dry run now rides whatever
  credential is already in custody.

  **What an operator gets in exchange for that:** a dry run whose stored
  credential has since expired will fail where it previously refreshed itself,
  and the symptom is the agent reporting the seam's tools missing from its
  toolset rather than anything naming a credential. So the run now says, before
  it starts, which seams it is deliberately not refreshing and what an expired
  one will look like. A live run is unchanged and still refreshes.

- **`kmx down` no longer deletes a cluster under a banner saying it does
  not exist.** `kind delete cluster` deletes by **container** name and never
  opens the kubeconfig, so a kubeconfig that has never heard of the context
  is not evidence there is nothing to delete — but the guard's "context not
  created yet" allowance, which exists so `kmx up` can name the cluster it
  is about to create, was accepted here too. A stale or re-pointed
  `KUBECONFIG` plus `kmx down` therefore destroyed a real cluster, with no
  confirmation, under a banner saying it had never been created. It cost a
  lane a cluster.

  `kmx down` now asks the container engine first. No cluster by that name:
  it says so and deletes nothing. A cluster the kubeconfig describes: the
  banner and no question, exactly as before, so CI and every ordinary
  teardown are unchanged. A cluster the kubeconfig cannot vouch for: the
  banner says so, and it takes `KAIMAHI_CONFIRM=<context>` or a typed
  confirmation — which is also how a half-created cluster is removed after
  a `kmx up` that died before writing its kubeconfig entry.
- **`kmx workflow run` and `kmx credential renew` go through the context
  guard.** The documentation said every mutating command did; these two did
  not. `kmx workflow run` is the one that matters: it files approval
  requests, drives turns that cut branches and dispatch builds, and executes
  whatever an `ungoverned:` step names, and it could do all of that on a
  cluster nobody had looked at. It is guarded **once**, before the
  port-forward and before the first request is filed, because every
  consequential step already stops for a human to approve the exact call —
  and each of those now names the cluster next to the call, so the one
  banner scrolling away does not take the target with it.
- **Every agent manifest's `runAsUser` is checked against the image kagent
  ships.** Five manifests said the number was "pinned by a CI assertion".
  There was no such assertion. There is now
  (`scripts/check-agent-uid.py`): it resolves the agent image from the
  pinned chart — this repository's values file layered over the chart's
  defaults, so a moved registry moves what is tested — reads the uid by
  running `id -u` inside it, and fails if any manifest disagrees, names no
  uid at all, or if it found no agent manifests to check. Keyless: the
  chart and the image are both public.

### Breaking

- **`kmx status -o json` is no longer a `kubectl apply` document.** The
  top-level kubectl `apiVersion` and `kind` are gone, because the output is
  now kmx's own envelope — `context`, `contextSource`, a `governance` block,
  and the kubectl objects verbatim under `items`. Anything piping this into
  `kubectl apply` breaks; `jq '.items[]'` is unchanged.

- **A credential can no longer be piped into the capture.** `az account
  get-access-token … | make ado-secret ADO_ORG=<organization>` used to work
  and now refuses: the value is read from a terminal only. Run the `az`
  command, then paste its output at the prompt. The release driver's automatic
  refresh is unaffected — it mints and stores the token without a human and
  never goes through this path. `make github-secret`, `make release-secret`
  and `make ado-secret` still exist and run the new command; re-capturing an
  upstream that already has a credential now needs `--replace`.

- Subcommand `--help` now succeeds and prints command-specific help instead of
  the former inconsistent `flag.FlagSet` error path. Unknown and extra
  arguments use Cobra’s standard errors rather than being ignored by a few
  commands. Bare command groups such as `kmx agent` now print their hierarchical
  help and exit successfully instead of returning a one-line legacy usage error.

## v0.1.0 — 2026-09-03

The first tagged release: everything the project has built since the first
hello-world agent, at a version you can name. Most of what is below is
release plumbing — the product can now be installed and upgraded without a
commit hash — plus the last capability to land before the tag was cut.

### Added

- **An agent's calls record who it acted for, and credentials expire.** A run
  opened at the inbound door (where a Slack signature has already proved who
  typed the message) is what joins a governed call to a person, so the ledger
  and the tool audit carry an actor the plane itself observed — never a claim
  made by the thing being governed. Every credential issued from now on has a
  deadline (default 30 days, no way to ask for "never"), enforced at the LLM
  proxy, the MCP gateway and the inbound door, failing closed and audited.
  `make credentials` shows what is expiring; `kmx credential renew` moves the
  date without touching the token, so custody is unchanged
  ([docs/identity.md](docs/identity.md)).
- **Tagged releases, built by CI from the tag.** `kmx` binaries for
  linux/amd64, linux/arm64, darwin/amd64 and darwin/arm64, with a
  `checksums.txt` published beside them. The job refuses to publish a build
  that does not report its own tag, and refuses a tag with no entry in this
  file.
- **`go install …/cmd/kmx@latest`** as the install line — no sha, no
  package-manager namespace claimed, and no new tooling to trust: the module
  proxy and the Go checksum database already stand behind it.
- **`kmx version` reports the build's own version**, not only the versions it
  installs. A release binary reports its tag; a `go install` reports the
  version you asked for; a checkout build says it is a checkout build.
- **A released binary deploys the plane at its tag.** `kmx plane` used to
  depend entirely on VCS stamping surviving the build; the tag is now the
  first source it reads, and the release refuses to publish unless the
  plane module's matching `plane/vX.Y.Z` tag exists at the same commit.
- **A documented upgrade path** ([docs/releases.md](docs/releases.md)),
  including what happens when a migration fails halfway (the plane does not
  start), and a CI job that upgrades a plane across a real schema gap with
  live data in it and proves the data survives.

### Upgrading

- From an untagged `go install …@<sha>` build: install `@latest` and run
  `kmx version`. There is no state in `kmx` itself to migrate.
- For the **plane**, see [docs/releases.md](docs/releases.md#upgrading-the-plane).
  Migrations are additive and run at startup under a lock. Two behaviours are
  worth knowing before you upgrade, and both follow the same rule — an
  upgrade never silently widens or voids what an operator already had:
  - past `00008`: tool grants minted before argument binding existed keep
    their old verb-level meaning, and no new one of that kind can be created.
  - past `00010`: credentials that already exist keep a NULL expiry and keep
    working. Expiring a running estate at migration time would be an outage,
    not a control. `kaimahi_credentials_without_expiry` is the gauge for
    shrinking that set; re-issue or renew at your own pace.

### Not in this release

- **No container image is published.** `kmx plane` still builds the plane's
  image locally from the Go module proxy at kmx's own revision, so Go remains
  a prerequisite for the governed half even if you installed a binary. The
  reasoning is in [docs/releases.md](docs/releases.md#why-no-published-image-yet).
- **No package-manager namespace is claimed** — no Homebrew tap, no npm, no
  crates, no PyPI. The name is provisional and no trademark opinion has been
  obtained; claiming namespaces would raise the cost of a rename that may
  still happen. See [docs/NAMING.md](docs/NAMING.md).
