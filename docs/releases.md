# Releases, versions and upgrades

Kaimahi is **pre-1.0 and incubating**. This page is the whole contract: how a
version is numbered, how to install one, how to check what you got, how to
upgrade, and what happens when an upgrade goes wrong.

`@latest` and the installer's default select the newest stable tagged release;
v0.4.0 includes the Orka v0.2.0 Helm installer, Kubernetes Tool and bundle
lifecycle commands. Pin `@v0.4.0` for a repeatable build; `@main` is the moving
development option. Orka's own installation and upgrade limits are separate:
see [orka.md](orka.md). This release pins the **Orka v0.2.0** chart; the
historical Kaimahi v0.2.0 release reported Orka v0.1.3.
The plane-upgrade sections below apply only to the retained legacy plane.

The official Homebrew namespace is limited to the public
`kaimahi-agents/homebrew-tap` repository and its `kmx` formula. No trademark is
claimed; the remaining publication constraints are recorded in
[NAMING.md](NAMING.md).

## Versions

Tags are `vMAJOR.MINOR.PATCH`, and **the tag is the source of truth** — the
binary reports it, the release is named after it, and the notes come from
[CHANGELOG.md](../CHANGELOG.md).

| | Below 1.0 that means |
|---|---|
| **patch** `v0.1.0` → `v0.1.1` | fixes only: no schema change, no removed flag, no behaviour change an operator was relying on |
| **minor** `v0.1.0` → `v0.2.0` | everything else, breaking changes included — below 1.0 this is where they live, and the changelog says so under **Breaking** |
| **pre-release** `v0.2.0-rc.1` | a candidate for the version it names. `go install …@latest` ignores pre-releases, so a candidate never becomes somebody's default by accident |

Orka runtime pins are independent of kmx tags:

| kmx build | Orka install | Offline schema target |
|---|---|---|
| Kaimahi v0.4.0 release | v0.2.0 verified Helm chart, harness-v2 (`fullnameOverride=orka-api`) | v0.2.0 by default; explicit v0.1.3 or old `main` snapshot still readable |
| Historical Kaimahi v0.2.0 release | v0.1.3 pinned manifest | historical behavior; not an upgrade path to the current chart |

There is no 1.0 promise and no support window yet. What there is: CI refuses
to publish a tag whose version has no section in the changelog, and refuses
to publish a binary that does not report its own tag.

Two tags are pushed for each version, at the same commit:

```
v0.4.0          the repository, and the kmx binary
plane/v0.4.0    the plane, which is a separate Go module under plane/
```

Both are needed. `kmx plane` installs the plane through the Go module proxy at
kmx's own version, and Go resolves a nested module's version from a
`plane/`-prefixed tag. The release job refuses to publish without it.

## Install

With Homebrew on macOS or Linux:

```bash
brew install kaimahi-agents/tap/kmx
```

The fully qualified name automatically adds the tap and trusts only this
formula. It selects the matching one of the four release binaries for the
current OS and architecture, then verifies that asset's published SHA-256. The
formula checksum detects a changed or truncated download;
like `install.sh`, it is not an independent signature because both originate in
the same release. Docker or Podman remains required for local kind workflows,
and `kmx plane` still needs Go.

Upgrade the stable formula with:

```bash
brew upgrade kaimahi-agents/tap/kmx
```

The tap follows stable releases only. Use the versioned installer or pinned Go
route below when you need an older release or a prerelease.

The one-line installer for the latest **tagged** CLI:

```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
```

It resolves the latest tag, downloads the binary for your platform, verifies
its published sha256 **before** installing it, and puts it in `~/.local/bin`
without sudo. `KMX_VERSION=v0.4.0` pins this release; `KMX_BIN_DIR=DIR`
installs elsewhere. `install.sh --quickstart` installs the latest release and
then launches the non-interactive Orka quickstart. An explicit v0.1.0
`--quickstart` is still refused because that release predates Orka.

The other route, if you have a Go toolchain:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
```

`@latest` is the newest tagged release. Pin instead when you want a build you
can name: `@v0.4.0`. Both go through the public Go module proxy and the Go
checksum database, so the bytes you get are the bytes the sum database
recorded — no namespace of ours is involved, and there is nothing new to
trust.

### By hand

`install.sh` above does exactly this, and doing it yourself is a reasonable
preference. Every release carries binaries and a `checksums.txt`:

```bash
version=v0.4.0
base=https://github.com/kaimahi-agents/kaimahi/releases/download/$version
curl -fsSLO "$base/kmx-linux-amd64"
curl -fsSLO "$base/checksums.txt"

want=$(grep ' kmx-linux-amd64$' checksums.txt | cut -d' ' -f1)
got=$(sha256sum kmx-linux-amd64 | cut -d' ' -f1)   # macOS: shasum -a 256
[ "$want" = "$got" ] || { echo "checksum mismatch"; exit 1; }

install -m 0755 kmx-linux-amd64 /usr/local/bin/kmx
```

The comparison is written out rather than left to `sha256sum -c` on purpose.
`--ignore-missing` is a GNU coreutils flag: macOS has no `sha256sum` at all
(it has `shasum`), and BusyBox — Alpine, most slim container images — has one
that rejects the flag. The instruction that used to be here failed on both,
which is a poor first impression from a project whose whole argument is
fail-closed verification. `install.sh` picks whichever of `sha256sum`,
`shasum` and `openssl` the machine has, and refuses to install if it finds
none.

Do not skip the digest check. The installer verifies the kmx binary before
execution, and kmx independently verifies its pinned kind and kubectl downloads
([internal/kmx/toolchain](../internal/kmx/toolchain)); applying less care to
its own binary would be indefensible.

**Platforms**: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.
kmx drives a Linux container runtime, so the machine running it is a Linux
host or a Mac running one in a VM. Those are the four release platforms the
installer and pinned toolchain support. Windows is served through WSL, which
is `linux/amd64`; a native
`windows/amd64` build would be an untested claim rather than a platform.

**Go is still a prerequisite for the plane.** `kmx up`, `kmx agent`,
`kmx status` and the operator verbs work from a downloaded binary alone.
`kmx plane` builds the plane's image on your machine and uses `go install` to
do it — see [below](#why-no-published-image-yet).

### What did I install?

```console
$ kmx version
kmx v0.4.0 (release build)
  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md
  orka     v0.2.0
  model    qwen2.5:3b
  plane    kaimahi-proxy:p15, built from v0.4.0
```

The first line is the binary's own identity and it names its source, because
the same version string means different things depending on where it came
from:

| First line says | You have |
|---|---|
| `v0.4.0 (release build)` | a binary from the release for `v0.4.0` |
| `v0.4.0 (installed with go install)` | `go install …@v0.4.0` — the same code, built on your machine |
| `v0.0.0-2026…-fb456eb (development build from a checkout)` | a `go build` from a clone; not a release |
| `v0.0.0-dev+fb456eb.dirty (development build from a MODIFIED checkout)` | a clone with uncommitted changes |

## Upgrading kmx

Use `@latest` for the latest stable release, or pin the version you want:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.0
kmx version
```

Re-installing upgrades the CLI without changing a running cluster. Bundle
workflows also keep local target selections and deployment/evaluation receipts;
keep those files alongside the agent YAML you own when moving machines. Read
the changelog for the versions you skipped — below 1.0 a minor bump may change
behaviour.

The cluster is a separate question. A newer kmx does not touch a running
cluster until you ask it to. The v0.4.0 release installs the pinned
Orka v0.2.0 Helm chart using Helm on PATH or a pinned/checksum-verified toolchain
binary. `kmx up` reuses a matching kmx-owned Orka release, but refuses an
existing v0.1.3 manifest installation or foreign release rather than upgrading
it. For local kind replacement, export needed data before `kmx down` (which
deletes the whole cluster: Tasks, Secrets, PVC-backed data, model data and the
plane ledger), then run `kmx up` for a fresh installation. On AKS, plan a
fresh installation after backing up Orka resources, volumes/PVCs and Secrets,
including any existing agent-execution snapshot key. Back up the new chart's
`orka-api-agent-execution-snapshot` key with its controller volume and Orka
resources without printing its value. Do not use `helm upgrade --force`: [Orka v0.2.0 supports new installations only](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md).
See [safe kind and AKS replacement boundaries](orka.md#limits-stated).
The retired legacy runtime remains untouched; its old chart is not installed
by kmx.

## Upgrading the plane

The retained model plane stores its ledger, budgets and reservations in Postgres,
alongside historical requests, grants and audit data. This is not Orka's storage
or upgrade contract. Upgrading the legacy plane is
`kmx plane` again with the newer kmx, **after** the
[retirement review](operations.md#upgrading-after-approval-retirement):

```bash
kmx backup plane-before-upgrade.sql   # take one; it is one command
kmx plane                             # builds and rolls out the newer plane
kmx ledger                            # the rows are still there
```

What happens under that:

- The new proxy runs the migrations at startup, under a Postgres advisory
  lock, so a rollout of N replicas is its own migration step and the replicas
  do not race each other.
- Migrations are additive. Every column added since the first schema has a
  default, which
  is what lets a backup taken before an upgrade restore after one.
- A rollout is a Kubernetes rolling update: a new pod does not take traffic
  until it is ready, and it is not ready until its migrations have applied.
  **Old replicas can still consume grants during that overlap.** Declare
  retirement effective only when every replica reports the new build, not
  when one new pod is Ready.

CI's `plane-upgrade` job
([scripts/plane-upgrade-probe.sh](../scripts/plane-upgrade-probe.sh)) installs
a plane several migrations old from the module proxy, seeds it through its admin
API with a credential, budget, bounded budget grant and priced ledger row, then
starts the current plane on the same database. The retirement check requires
that state survive unchanged while old grants cannot admit over-cap model calls.
Historical requests/grants/audits are SQL/backup data, not served through the
removed APIs or used as authority. All twelve SQL migrations remain unchanged;
no pending-request normalization, grant exhaustion or expiry rewriting occurs.

## When kmx and the plane are different versions

Installing kmx changes no running plane. Admin commands read
`GET /admin/version`, once per command, before their operation. The reported
**admin contract** marks API revisions; it is not release-version ordering or
compatibility negotiation.

Contract **5** removes the remaining custom request/approval/grant APIs and
approval audit; contract 4 removed the gateway/tool-policy APIs and contract 3
removed inbound interfaces. Lower-bound checks remain useful for surviving
operations: model-overlay validation still requires contract 2, not 5. They
cannot prove that every route used by an older CLI exists on a newer plane.

A newer plane can pass an older CLI's numeric check while its removed endpoints
return errors. Upgrading one side alone therefore does not establish a working
operator path. Use matched CLI and plane revisions.

### The promise the contract rests on

The original grow-only promise no longer applies across governance retirement.
**Upgrade kmx and the plane together.** Older binaries may still print their
compiled-in grow-only reassurance. Their custom approval/tool commands and
multi-trail flow/watch calls fail against this plane. The current CLI reads
**only the model ledger** in flow/watch, and warns that a newer contract is not a
compatibility guarantee. See the [retirement upgrade procedure](operations.md#upgrading-after-approval-retirement)
for rolling-update/rollback risks and retained data; earlier gateway/inbound
cleanup still covers rejected configuration, stale resources and owner references.

Two consequences worth stating:

- A plane that answers `/admin/version` but reports a contract no release ever
  served is a **fault in the plane**, not a version gap, and kmx says so rather
  than sending you to reinstall over something an upgrade cannot fix.
- A plane between `v0.1.0` and the release that added `/admin/version` is
  treated as contract 0 even where it could in fact serve more. Those are
  unreleased revisions, the misjudgement is conservative, and the fix is one
  `kmx plane`.

**Proven, not asserted.** The same `plane-upgrade` job drives the current kmx
against the genuinely old plane it already has running, and asserts the version
gap is named rather than reported as a 404 — and, at the other end, that a plane
built from the checkout reports a usable contract.

### One behaviour change worth knowing: historical grants

All custom grants, including budget grants and tool grants predating argument
binding in migration `00008`, are **inactive on this build**. Their digests,
summaries, expiry and use counts remain unchanged. Historical pending requests
are not normalized or deniable through the retired API; SQL/backups preserve the
history without a new archive interface. Ordinary monthly caps, reservations,
accounting and credential lifecycle remain. Cap denials no longer file requests
or advise approval; recovery is an operator's deliberate budget change or the
UTC month reset.

**Rolling back to an approval-capable binary can reactivate stored grants.**
An old replica still serving during rollout can consume them too. Preserve the
database, but do not mistake preserved history for a revocation enforced by old
code. Check every replica's build before declaring retirement effective.

### And one more: credentials that already exist keep working

Migration `00010` gave credentials an expiry. Credentials that predate it
carry a NULL one and are **not** expired by the upgrade — expiring a running
estate at migration time would be an outage, not a control. The class can only
shrink: every credential issued afterwards has a deadline, and
`kaimahi_credentials_without_expiry` is the gauge whose job is to trend to
zero. Renew or re-issue at your own pace ([identity.md](identity.md)).

Credential compatibility preserves the model seam; it does not restore retired
tool authority. Retirement does not drop stored audit data or revoke credentials
used by surviving model routes. Review obsolete tool-only Secrets and external
revocation separately rather than resetting the database.

### When a migration fails halfway

**The plane does not start.** That is the designed answer, and it is what you
should expect to see:

- goose applies each migration in its own transaction, so a migration that
  fails is rolled back whole. Migrations before it stay applied; the schema
  version stops at the last one that succeeded.
- The proxy retries startup for 90 seconds and then exits non-zero
  (`database startup failed` in its log). Under Kubernetes the pod
  crash-loops.
- Because the new pod never becomes ready, the rolling update does not retire
  the old replicas: **the previous version keeps serving** while you work out
  what happened.
- Nothing is half-served. The plane refuses traffic on a schema it could not
  migrate rather than guessing which columns exist.

To recover: fix the conflict, or restore the backup you took
(`kmx restore plane-before-upgrade.sql`) and roll back to the previous
version, explicitly reviewing the grant-reactivation risk above. CI proves this
path too — the same probe seeds a second database, makes a migration impossible,
and asserts the plane never serves, exits non-zero, and leaves the rows and the
schema version untouched.

## Why no published image yet

`kmx plane` builds the proxy image locally: it `go install`s the plane from
the module proxy at kmx's own version and packages the resulting static binary
onto a distroless base. No container image is published for this release, on
purpose:

- **The provenance is already better than a tag.** The Go module proxy and the
  checksum database stand behind that fetch. An unsigned image tag in a
  registry would be a weaker claim wearing a stronger costume.
- **kind's side-load stays honest.** The local path loads the image into the
  kind node and `k8s/plane/proxy.yaml` pins `imagePullPolicy: Never`, so a
  locally built tag can never silently fall back to pulling a squattable
  public name (see [scripts/plane-deploy.sh](../scripts/plane-deploy.sh)).
  Publishing an image is exactly the change that would put pressure on that
  pin.
- **A registry namespace is a namespace.** No trademark opinion has been
  obtained; claiming a distribution namespace remains a separate decision.

The cost, stated plainly: Go remains a prerequisite for `kmx plane` even if
you installed a downloaded binary. Registry-backed clusters (AKS) already have
their own road — the operator builds the image into their own registry
([aks.md](aks.md)) — and that is unchanged.

This is a decision for this release, not a principle. The case for publishing
gets stronger the moment someone needs the plane on a machine with no Go
toolchain.

## Cutting a release

For maintainers. The point of this list is that the second release is cheaper
than the first.

1. Write the section. Move what is under `## Unreleased` in
   [CHANGELOG.md](../CHANGELOG.md) into a `## vX.Y.Z — <date>` heading, and
   leave `## Unreleased` empty behind it. Anything breaking goes under
   **Breaking** with what to do about it; anything that changes an operator's
   day goes under **Upgrading**.
2. Merge that to `main`. Releases are cut from `main`.
3. Tag both modules at the same commit and push:

   ```bash
   git tag v0.4.0 && git tag plane/v0.4.0
   git push --atomic origin v0.4.0 plane/v0.4.0
   ```

4. Watch the `release` workflow. It refuses to publish if: the version is not
   semantic, `plane/vX.Y.Z` is missing or points somewhere else, the changelog
   has no section, the built binary does not report the tag, or the checksums
   do not verify. The release also carries `kmx.rb`, rendered from those exact
   checksums. For a stable release, download that exact asset and submit it as
   the formula change in the official tap:

   ```bash
   tap=$(mktemp -d)
   git clone https://github.com/kaimahi-agents/homebrew-tap.git "$tap"
   formula=$(mktemp)
   gh release download vX.Y.Z --repo kaimahi-agents/kaimahi \
     --pattern kmx.rb --output "$formula" --clobber
   mv "$formula" "$tap/Formula/kmx.rb"
   cd "$tap"
   ```

5. From that tap checkout, create a branch, commit `Formula/kmx.rb`, and open a
   pull request to `kaimahi-agents/homebrew-tap`. Merge it only after review and
   the tap's macOS and Linux formula checks pass. Prerelease formula assets are
   inspection evidence and do not replace the stable formula.
6. Check the result with both routes: `go install …/cmd/kmx@vX.Y.Z && kmx version`,
   then the following on a clean Homebrew installation:

   ```bash
   brew install kaimahi-agents/tap/kmx &&
     kmx_prefix="$(brew --prefix kaimahi-agents/tap/kmx)" &&
     "$kmx_prefix/bin/kmx" version
   ```

To rehearse without spending a version number, run the `release` workflow
manually (`workflow_dispatch`) from a branch: it builds and checksums exactly
the same artifacts, renders the candidate formula, publishes nothing, and
uploads them as workflow artifacts. Prereleases publish their own formula asset
for inspection but never replace the tap's stable formula.
