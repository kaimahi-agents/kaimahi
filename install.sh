#!/bin/sh
# Install kmx: download the pinned release binary for this machine, verify its
# published sha256, and put it somewhere you can run it.
#
#   curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart
#
# The second form is the whole distance: it installs kmx and then runs
# `kmx quickstart`, which ends with an agent answering a question.
#
# What this script needs: curl (or wget), tar-free — the release is a bare
# binary — and one of sha256sum, shasum or openssl to check the digest. It
# installs into your home directory and never uses sudo.
#
# What it does NOT do, deliberately: accept, read or write any credential.
# kmx holds none, and an install script piped into a shell is the last
# place a key should be typed.
#
# On trust, stated plainly rather than implied: the binary and its checksum
# come from the same GitHub release over TLS, so this proves the download was
# not corrupted or truncated — it is not an independent signature, and a
# compromised release would publish a matching digest. If you would rather
# verify by a different route, `go install github.com/kaimahi-agents/kaimahi/cmd/kmx@latest`
# goes through the Go module proxy and the Go checksum database instead.
set -eu

REPO="kaimahi-agents/kaimahi"
VERSION="${KMX_VERSION:-latest}"
BIN_DIR="${KMX_BIN_DIR:-$HOME/.local/bin}"
RUN_QUICKSTART=no

for arg in "$@"; do
  case "$arg" in
    --quickstart) RUN_QUICKSTART=yes ;;
    --version=*) VERSION="${arg#--version=}" ;;
    --bin-dir=*) BIN_DIR="${arg#--bin-dir=}" ;;
    -h|--help)
      sed -n '2,25p' "$0" 2>/dev/null || echo "usage: install.sh [--quickstart] [--version=vX.Y.Z] [--bin-dir=DIR]"
      exit 0 ;;
    *) echo "install.sh: unknown option $arg" >&2; exit 2 ;;
  esac
done

say() { printf '%s\n' "$*" >&2; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# ---- what this machine is ---------------------------------------------------
os=$(uname -s)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "unsupported operating system '$os'. kmx drives a Linux container runtime; on Windows use WSL." ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture '$arch' (kmx publishes amd64 and arm64)" ;;
esac
asset="kmx-$os-$arch"

# ---- how to fetch -----------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
  resolve() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
  # wget prints the redirect chain with --server-response; the last Location
  # is the tag URL. --spider means no body is downloaded to learn it.
  resolve() {
    wget -q --max-redirect=10 --server-response --spider "$1" 2>&1 \
      | awk 'tolower($1) == "location:" { last = $2 } END { print last }'
  }
else
  die "neither curl nor wget is installed"
fi

# ---- how to check a digest --------------------------------------------------
# Not every machine has GNU coreutils. macOS ships shasum, BusyBox ships a
# sha256sum without --ignore-missing, and openssl is nearly everywhere. The
# comparison is done here rather than by `sha256sum -c` so that the same code
# runs on all three.
if command -v sha256sum >/dev/null 2>&1; then
  digest_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  digest_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
elif command -v openssl >/dev/null 2>&1; then
  digest_of() { openssl dgst -sha256 "$1" | sed 's/.*= *//'; }
else
  die "no sha256 tool found (looked for sha256sum, shasum, openssl). Refusing to install a binary it cannot verify."
fi

# ---- which version ----------------------------------------------------------
# KMX_DOWNLOAD_BASE points the download at somewhere other than the GitHub
# release — used by this repository's CI to prove the install path against the
# binary a pull request just built, which by definition has no release yet.
# The checksum is still checked against a checksums.txt served from the same
# place, so the mechanism being tested is the real one.
BASE_OVERRIDE="${KMX_DOWNLOAD_BASE:-}"
if [ -n "$BASE_OVERRIDE" ]; then
  say "downloading from $BASE_OVERRIDE (KMX_DOWNLOAD_BASE is set)"
elif [ "$VERSION" = latest ]; then
  # The /releases/latest URL redirects to the tag. Reading the redirect keeps
  # this off the GitHub API, which rate-limits unauthenticated callers — and a
  # rate-limited install is a confusing first impression.
  effective=$(resolve "https://github.com/$REPO/releases/latest" || true)
  VERSION="${effective##*/}"
  case "$VERSION" in
    v*) ;;
    *) die "could not work out the latest version. Pass one: --version=v0.1.0" ;;
  esac
fi

base="https://github.com/$REPO/releases/download/$VERSION"
[ -n "$BASE_OVERRIDE" ] && base="$BASE_OVERRIDE"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/kmx-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

say "kmx $VERSION for $os/$arch"
fetch "$base/$asset" "$tmp/$asset" || die "cannot download $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || die "cannot download $base/checksums.txt"

want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1 || true)
[ -n "$want" ] || die "the release checksums do not mention $asset"
got=$(digest_of "$tmp/$asset")
if [ "$want" != "$got" ]; then
  die "checksum mismatch for $asset: got $got, want $want. Nothing was installed."
fi
say "checksum verified: $got"

mkdir -p "$BIN_DIR"
# Install to a temporary name in the destination and rename, so an install
# over a running kmx cannot leave a half-written binary behind.
chmod 0755 "$tmp/$asset"
mv "$tmp/$asset" "$BIN_DIR/.kmx.new"
mv "$BIN_DIR/.kmx.new" "$BIN_DIR/kmx"
say "installed $BIN_DIR/kmx"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    say ""
    say "$BIN_DIR is not on your PATH. Add it:"
    say "  export PATH=\"$BIN_DIR:\$PATH\""
    PATH="$BIN_DIR:$PATH"
    export PATH
    ;;
esac

"$BIN_DIR/kmx" version >&2 || true

if [ "$RUN_QUICKSTART" = yes ]; then
  say ""
  exec "$BIN_DIR/kmx" quickstart
fi

say ""
say "Next:  kmx quickstart      # a cluster and an agent that answers a question"
say "       kmx quickstart -o json   # the same, for something driving kmx"
