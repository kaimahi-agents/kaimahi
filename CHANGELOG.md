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

### Changed

- **The prerequisite list is one item: a container engine.** It was five (Go,
  Docker or Podman, kind, kubectl, Helm) plus make and curl. Go is now needed
  only by `kmx plane`, which builds the plane's image locally.
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

### Fixed

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

### Breaking

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
