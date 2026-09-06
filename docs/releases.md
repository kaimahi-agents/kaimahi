# Releases, versions and upgrades

Kaimahi is **pre-1.0 and incubating**. This page is the whole contract: how a
version is numbered, how to install one, how to check what you got, how to
upgrade, and what happens when an upgrade goes wrong.

Nothing here claims a trademark, and nothing here claims a package-manager
namespace. The name is provisional ([NAMING.md](NAMING.md)).

## Versions

Tags are `vMAJOR.MINOR.PATCH`, and **the tag is the source of truth** — the
binary reports it, the release is named after it, and the notes come from
[CHANGELOG.md](../CHANGELOG.md).

| | Below 1.0 that means |
|---|---|
| **patch** `v0.1.0` → `v0.1.1` | fixes only: no schema change, no removed flag, no behaviour change an operator was relying on |
| **minor** `v0.1.0` → `v0.2.0` | everything else, breaking changes included — below 1.0 this is where they live, and the changelog says so under **Breaking** |
| **pre-release** `v0.2.0-rc.1` | a candidate for the version it names. `go install …@latest` ignores pre-releases, so a candidate never becomes somebody's default by accident |

There is no 1.0 promise and no support window yet. What there is: CI refuses
to publish a tag whose version has no section in the changelog, and refuses
to publish a binary that does not report its own tag.

Two tags are pushed for each version, at the same commit:

```
v0.1.0          the repository, and the kmx binary
plane/v0.1.0    the plane, which is a separate Go module under plane/
```

Both are needed. `kmx plane` installs the plane through the Go module proxy at
kmx's own version, and Go resolves a nested module's version from a
`plane/`-prefixed tag. The release job refuses to publish without it.

## Install

The one-line install, and the one the [README](../README.md) quickstart uses:

```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
```

It resolves the latest tag, downloads the binary for your platform, verifies
its published sha256 **before** installing it, and puts it in `~/.local/bin`
without sudo. `--quickstart` goes straight on to a running agent;
`KMX_VERSION=v0.1.0` pins a version; `KMX_BIN_DIR=DIR` installs elsewhere.

The other route, if you have a Go toolchain:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
```

`@latest` is the newest tagged release. Pin instead when you want a build you
can name: `@v0.1.0`. Both go through the public Go module proxy and the Go
checksum database, so the bytes you get are the bytes the sum database
recorded — no namespace of ours is involved, and there is nothing new to
trust.

### By hand

`install.sh` above does exactly this, and doing it yourself is a reasonable
preference. Every release carries binaries and a `checksums.txt`:

```bash
version=v0.1.0
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

Do not skip the digest check. This project verifies the pinned kagent CLI's
digest before it will execute it, and now kind's, kubectl's and Helm's too
([internal/kmx/toolchain](../internal/kmx/toolchain)); applying less care to
its own binary would be indefensible.

**Platforms**: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.
kmx drives a Linux container runtime, so the machine running it is a Linux
host or a Mac running one in a VM — and those are the same four platforms the
pinned kagent CLI publishes, which kmx downloads for the same machine.
Windows is served through WSL, which is `linux/amd64`; a native
`windows/amd64` build would be an untested claim rather than a platform.

**Go is still a prerequisite for the governed half.** `kmx up`, `kmx agent`,
`kmx status` and the operator verbs work from a downloaded binary alone.
`kmx plane` builds the plane's image on your machine and uses `go install` to
do it — see [below](#why-no-published-image-yet).

### What did I install?

```console
$ kmx version
kmx v0.1.0 (release build)
  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md
  kagent   0.9.12
  model    qwen2.5:3b
  plane    kaimahi-proxy:p10, built from v0.1.0
```

The first line is the binary's own identity and it names its source, because
the same version string means different things depending on where it came
from:

| First line says | You have |
|---|---|
| `v0.1.0 (release build)` | a binary from the release for `v0.1.0` |
| `v0.1.0 (installed with go install)` | `go install …@v0.1.0` — the same code, built on your machine |
| `v0.0.0-2026…-fb456eb (development build from a checkout)` | a `go build` from a clone; not a release |
| `v0.0.0-dev+fb456eb.dirty (development build from a MODIFIED checkout)` | a clone with uncommitted changes |

## Upgrading kmx

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
kmx version
```

kmx itself holds no state: it reads your kubeconfig and writes agent YAML you
own. Re-installing is the whole upgrade. Read the changelog for the versions
you skipped — below 1.0 a minor bump may change behaviour.

The cluster is a separate question. A newer kmx does not touch a running
cluster until you ask it to; `kmx up` is idempotent and re-applies the pinned
kagent chart and the agents.

## Upgrading the plane

The governance plane holds everything that matters: the ledger, budgets,
allowlists, approvals and grants, all in Postgres. Upgrading it is
`kmx plane` again with the newer kmx:

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

**Proven, not asserted.** CI's `plane-upgrade` job
([scripts/plane-upgrade-probe.sh](../scripts/plane-upgrade-probe.sh)) installs
a plane several migrations old straight from the module proxy, seeds it through
its own admin API with a credential, a budget, a tool allowlist, a grant a
human approved and a priced ledger row, then starts the current plane on the
same database and asserts every one of those survived and that the upgraded
plane serves a fresh governed call.

### One behaviour change worth knowing: grants minted before argument binding

Migration `00008` welded tool approvals to the exact call. Grants minted
before it carry no argument digest, and that class is **closed**: those grants
keep their old verb-level meaning — still bounded by the expiry and use count
their approver set — and the store will not mint another one. So an upgrade
neither widens an old grant nor silently voids it. Every grant minted after
the upgrade admits exactly one call.

### And one more: credentials that already exist keep working

Migration `00010` gave credentials an expiry. Credentials that predate it
carry a NULL one and are **not** expired by the upgrade — expiring a running
estate at migration time would be an outage, not a control. The class can only
shrink: every credential issued afterwards has a deadline, and
`kaimahi_credentials_without_expiry` is the gauge whose job is to trend to
zero. Renew or re-issue at your own pace ([identity.md](identity.md)).

Both of these follow one rule, which is the rule to expect from any future
migration here: **an upgrade never silently widens or voids what an operator
already had.**

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
version. CI proves this path too — the same probe seeds a second database,
makes a migration impossible, and asserts the plane never serves, exits
non-zero, and leaves the rows and the schema version untouched.

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
- **A registry namespace is a namespace.** The name is provisional and no
  trademark opinion has been obtained; every namespace claimed raises the cost
  of a rename that may still happen.

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
   git tag v0.2.0 && git tag plane/v0.2.0
   git push origin v0.2.0 plane/v0.2.0
   ```

4. Watch the `release` workflow. It refuses to publish if: the version is not
   semantic, `plane/vX.Y.Z` is missing or points somewhere else, the changelog
   has no section, the built binary does not report the tag, or the checksums
   do not verify.
5. Check the result: `go install …/cmd/kmx@vX.Y.Z && kmx version`.

To rehearse without spending a version number, run the `release` workflow
manually (`workflow_dispatch`) from a branch: it builds and checksums exactly
the same artifacts, publishes nothing, and uploads them as workflow artifacts.
