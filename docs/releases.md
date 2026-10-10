# Releases, versions and upgrades

Kaimahi is **pre-1.0 and incubating**. This page is the whole contract: how a
version is numbered, how to install one, how to check what you got, how to
upgrade, and what happens when an upgrade goes wrong.

`@latest` and the installer's default select the newest stable tagged release;
v0.4.1 includes the Orka v0.2.0 Helm installer, Kubernetes Tool and bundle
lifecycle commands. Pin `@v0.4.1` for a repeatable build; `@main` is the moving
development option. Orka's own installation and upgrade limits are separate:
see [orka.md](orka.md). This release pins the **Orka v0.2.0** chart; the
historical Kaimahi v0.2.0 release reported Orka v0.1.3.

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
| Kaimahi v0.4.1 release | v0.2.0 verified Helm chart, harness-v2 (`fullnameOverride=orka-api`) | v0.2.0 by default; explicit v0.1.3 or old `main` snapshot still readable |
| Historical Kaimahi v0.2.0 release | v0.1.3 pinned manifest | historical behavior; not an upgrade path to the current chart |

There is no 1.0 promise and no support window yet. What there is: CI refuses
to publish a tag whose version has no section in the changelog, and refuses
to publish a binary that does not report its own tag.

Releases use the root `vX.Y.Z` tag for the repository and CLI. The root is the
only Go module; no paired module release tag is needed.

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
the same release. Docker or Podman remains required for local kind workflows.

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
without sudo. `KMX_VERSION=v0.4.1` pins this release; `KMX_BIN_DIR=DIR`
installs elsewhere. `install.sh --quickstart` installs the latest release and
then launches the non-interactive Orka quickstart. An explicit v0.1.0
`--quickstart` is still refused because that release predates Orka.

The other route, if you have a Go toolchain:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest
```

`@latest` is the newest tagged release. Pin instead when you want a build you
can name: `@v0.4.1`. Both go through the public Go module proxy and the Go
checksum database, so the bytes you get are the bytes the sum database
recorded — no namespace of ours is involved, and there is nothing new to
trust.

### By hand

`install.sh` above does exactly this, and doing it yourself is a reasonable
preference. Every release carries binaries and a `checksums.txt`:

```bash
version=v0.4.1
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

A downloaded CLI needs no Go toolchain for native setup and agent workflows;
it still requires the external tools described in [getting started](getting-started.md#prerequisites).

### What did I install?

```console
$ kmx version
kmx v0.4.1 (release build)
  kaimahi is pre-1.0 and incubating: minor versions may break behaviour, and say so in CHANGELOG.md
  orka     v0.2.0
  model    qwen2.5:3b
```

The first line is the binary's own identity and it names its source, because
the same version string means different things depending on where it came
from:

| First line says | You have |
|---|---|
| `v0.4.1 (release build)` | a binary from the release for `v0.4.1` |
| `v0.4.1 (installed with go install)` | `go install …@v0.4.1` — the same code, built on your machine |
| `v0.0.0-2026…-fb456eb (development build from a checkout)` | a `go build` from a clone; not a release |
| `v0.0.0-dev+fb456eb.dirty (development build from a MODIFIED checkout)` | a clone with uncommitted changes |

## Upgrading kmx

Use `@latest` for the latest stable release, or pin the version you want:

```bash
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.1
kmx version
```

Re-installing upgrades the CLI without changing a running cluster. Bundle
workflows also keep local target selections and deployment/evaluation receipts;
keep those files alongside the agent YAML you own when moving machines. Read
the changelog for the versions you skipped — below 1.0 a minor bump may change
behaviour.

The cluster is a separate question. A newer kmx does not touch a running
cluster until you ask it to. The v0.4.1 release installs the pinned
Orka v0.2.0 Helm chart using Helm on PATH or a pinned/checksum-verified toolchain
binary. `kmx up` reuses a matching kmx-owned Orka release, but refuses an
existing v0.1.3 manifest installation or foreign release rather than upgrading
it. For local kind replacement, export needed data before `kmx down` (which
deletes the whole cluster: Tasks, Secrets, PVC-backed data, model data and any
historical database still present), then run `kmx up` for a fresh installation.
On AKS, plan a fresh installation after backing up Orka resources, volumes/PVCs and Secrets,
including any existing agent-execution snapshot key. Back up the new chart's
`orka-api-agent-execution-snapshot` key with its controller volume and Orka
resources without printing its value. Do not use `helm upgrade --force`: [Orka v0.2.0 supports new installations only](https://github.com/orka-agents/orka/blob/v0.2.0/website/docs/operations/upgrading.md).
See [safe kind and AKS replacement boundaries](orka.md#limits-stated).
KMX still does not install or upgrade Kagent. Its explicit exact-v0.10.2 create
path targets an installation the operator already owns; the old chart and broad
runtime surface are not restored.

## Cutting a release

For maintainers. The point of this list is that the second release is cheaper
than the first.

1. Write the section. Move what is under `## Unreleased` in
   [CHANGELOG.md](../CHANGELOG.md) into a `## vX.Y.Z — <date>` heading, and
   leave `## Unreleased` empty behind it. Anything breaking goes under
   **Breaking** with what to do about it; anything that changes an operator's
   day goes under **Upgrading**.
2. Merge that to `main`. Releases are cut from `main`.
3. Tag the root CLI release and push:

   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

4. Watch the `release` workflow. It builds through
   [GoReleaser](../.goreleaser.yaml), checks that exact artifact set, then
   publishes those same files together as one GitHub release. It refuses to
   publish if: the version is not semantic, the changelog has no section,
   the built binary does not report the tag, or the checksums do not verify. The
   release also carries `kmx.rb`, GoReleaser's rendered formula for those exact checksums
   (`brews.skip_upload: true` keeps it from being pushed to the tap on its own
   — that stays a human's job, next). A failed asset upload can leave a GitHub
   draft; inspect and remove that incomplete draft before rerunning the tag
   workflow. For a stable release, download the exact formula asset and submit
   it as the change in the official tap:

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
   pull request to `kaimahi-agents/homebrew-tap`. Its CI
   (`.github/workflows/tests.yml` in that repo) runs `brew style`,
   `brew audit --strict --online`, `brew readall`, `brew install`, and
   `brew test` on macOS and Linux — that is what "review and the tap's
   formula checks pass" means, and it is the actual gate; GoReleaser
   rendering cleanly is necessary but not sufficient. To rehearse the same
   checks locally before opening the PR (catches issues without waiting on
   that CI). Run this from the candidate tap checkout created in step 4. It
   refuses to replace an installed `kmx` keg, preserves any existing tap
   checkout, and keeps the temporary trust entry outside your normal Homebrew
   configuration:

   ```bash
   (
     set -euo pipefail
     candidate=$PWD
     tap_name=kaimahi-agents/tap
     formula="$tap_name/kmx"
     tap_parent="$(brew --repository)/Library/Taps/kaimahi-agents"
     tap_dir="$tap_parent/homebrew-tap"
     if brew list --formula --versions kmx 2>/dev/null | grep -q .; then
       echo "refusing to replace installed kmx; use a clean Homebrew installation" >&2
       exit 1
     fi
     mkdir -p "$tap_parent"
     backup_root=$(mktemp -d "$tap_parent/.kmx-tap-backup.XXXXXX")
     saved_tap="$backup_root/homebrew-tap"
     trust_home=$(mktemp -d)
     had_tap=false
     candidate_linked=false
     install_started=false
     cleanup() {
       local status=$?
       if [ "$install_started" = true ]; then
         if ! brew uninstall --force "$formula" >/dev/null 2>&1; then
           echo "failed to uninstall candidate $formula; remove it manually" >&2
           status=1
         fi
       fi
       if [ "$candidate_linked" = true ] && [ -L "$tap_dir" ]; then rm "$tap_dir"; fi
       if [ "$had_tap" = true ] && { [ -e "$saved_tap" ] || [ -L "$saved_tap" ]; }; then
         mv "$saved_tap" "$tap_dir"
       fi
       rm -rf "$backup_root" "$trust_home"
       exit "$status"
     }
     trap cleanup EXIT
     if [ -e "$tap_dir" ] || [ -L "$tap_dir" ]; then
       mv "$tap_dir" "$saved_tap"
       had_tap=true
     fi
     mkdir -p "$tap_parent"
     ln -s "$candidate" "$tap_dir"
     candidate_linked=true
     export XDG_CONFIG_HOME="$trust_home"
     brew trust --tap "$tap_name"
     brew style "$formula"
     brew audit --strict --online "$formula"
     brew readall --os=all --arch=all "$tap_name"
     install_started=true
     brew install "$formula"
     kmx_prefix=$(brew --prefix "$formula")
     installed_version=$("$kmx_prefix/bin/kmx" version | sed -n '1s/^kmx v\([^ ]*\) (release build)$/\1/p')
     formula_version=$(brew info --json=v2 "$formula" | brew ruby -rjson -e 'puts JSON.parse(STDIN.read).fetch("formulae").first.fetch("versions").fetch("stable")')
     test -n "$installed_version"
     test "$installed_version" = "$formula_version"
     brew test "$formula"
   )
   ```

   Merge the PR only after review and the tap's actual CI (not just this
   rehearsal) passes. Prerelease formula assets are inspection evidence and
   do not replace the stable formula.
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
