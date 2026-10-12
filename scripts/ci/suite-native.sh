#!/usr/bin/env bash
# Checkout-only real native qualification; terminal output is metadata only.
set +x +v
set -euo pipefail
unset PYTHONOPTIMIZE
umask 077
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
exec python3 "$root/scripts/ci/suite-native.py" "$@"
