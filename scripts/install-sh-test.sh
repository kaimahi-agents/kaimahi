#!/usr/bin/env bash
# Hermetic tests for install.sh — the one command a new machine runs.
#
# No network and no release: the download is served from a directory this
# script builds, through KMX_DOWNLOAD_BASE (the same hook CI uses to prove
# the install path against the binary a pull request just built), and the
# "kmx" it installs is a shell script that records how it was called.
#
# What that records is the point. `--quickstart` is the supported first
# answer and it is Orka-only, so nothing on it may name the retired runtime;
# a `kmx version` banner ran before quickstart had even started and put its
# pinned component list into the transcript CI greps. The recorded argv is
# the evidence for both the ordering and the absence.
#
# Run:  bash scripts/install-sh-test.sh
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
installer="$here/install.sh"
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

os=$(uname -s)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "SKIP: install.sh does not support $os"; exit 0 ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "SKIP: install.sh does not support $arch"; exit 0 ;;
esac

# The "release": a fake kmx that appends its argv to $KMX_CALLS, plus the
# checksums.txt install.sh verifies it against. Anything that changes the
# binary changes the digest, so the fixture cannot drift from the check.
release="$workdir/release"
mkdir -p "$release"
cat > "$release/kmx-$os-$arch" <<'FAKE'
#!/bin/sh
printf '%s\n' "$*" >> "$KMX_CALLS"
exit 0
FAKE
chmod 0755 "$release/kmx-$os-$arch"
( cd "$release" && { sha256sum "kmx-$os-$arch" 2>/dev/null \
    || shasum -a 256 "kmx-$os-$arch"; } > checksums.txt )

fails=0
# install <label> [args...] -> records the argv of every kmx the run invoked
# into $workdir/calls, and the run's own output into $workdir/out.
install_run() {
  local label=$1
  shift
  local rc=0
  : > "$workdir/calls"
  KMX_CALLS="$workdir/calls" KMX_DOWNLOAD_BASE="file://$release" \
    KMX_BIN_DIR="$workdir/bin" \
    sh "$installer" "$@" >"$workdir/out" 2>&1 </dev/null || rc=$?
  if [ "$rc" -ne 0 ]; then
    fails=$((fails + 1))
    echo "FAIL [$label]: install.sh exited $rc"
    sed 's/^/    | /' "$workdir/out"
    return 1
  fi
  return 0
}

check() {
  local label=$1 condition=$2
  if [ "$condition" = ok ]; then
    echo "ok   [$label]"
  else
    fails=$((fails + 1))
    echo "FAIL [$label]"
    echo "    | kmx was called as:"
    sed 's/^/    |   kmx /' "$workdir/calls"
  fi
}

# --quickstart: quickstart is the ONLY thing this path runs kmx for. A
# `kmx version` here opens the transcript with a pinned component banner
# instead of with the answer the reader asked for.
if install_run "--quickstart installs and runs quickstart" --quickstart; then
  first=$(head -n 1 "$workdir/calls")
  check "quickstart is the first thing kmx is asked to do" \
    "$([ "$first" = quickstart ] && echo ok || echo no)"
  check "the quickstart path never runs 'kmx version'" \
    "$(grep -q '^version' "$workdir/calls" && echo no || echo ok)"
  check "the quickstart path says nothing about the legacy runtime" \
    "$(grep -qi 'k''agent' "$workdir/out" && echo no || echo ok)"
fi

# The published v0.1.0 cannot provide the new Orka path. Refuse before
# downloading or running any binary rather than launching its old quickstart.
: > "$workdir/calls"
if KMX_VERSION=v0.1.0 KMX_BIN_DIR="$workdir/legacy-bin" \
  sh "$installer" --quickstart >"$workdir/out" 2>&1 </dev/null; then
  fails=$((fails + 1))
  echo "FAIL [v0.1.0 quickstart should be refused]"
else
  check "v0.1.0 quickstart explains the release gap" \
    "$(grep -q 'does not include Orka quickstart' "$workdir/out" && echo ok || echo no)"
  check "v0.1.0 quickstart installs no binary" \
    "$([ ! -e "$workdir/legacy-bin/kmx" ] && echo ok || echo no)"
fi

# A plain install still reports what it installed: that line is how an
# operator learns which build landed, and it is not on the Orka path.
if install_run "plain install still prints the version"; then
  check "a plain install runs 'kmx version'" \
    "$(grep -q '^version' "$workdir/calls" && echo ok || echo no)"
  check "a plain install runs no quickstart" \
    "$(grep -q '^quickstart' "$workdir/calls" && echo no || echo ok)"
fi

if [ "$fails" -ne 0 ]; then
  echo "install.sh: $fails check(s) failed" >&2
  exit 1
fi
echo "install.sh: all checks passed"
