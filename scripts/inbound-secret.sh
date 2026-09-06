#!/usr/bin/env bash
# Store the signing secret for one HMAC-authenticated inbound hook
# as a key of the plane-side Secret kaimahi-inbound-signing, which the
# proxy pod mounts at /etc/kaimahi/inbound/<hook> and reads per request.
#
# Two ways to obtain the secret, chosen explicitly:
#   stdin      the SOURCE's secret — e.g. the signing secret Slack shows
#              in an app's Basic Information page. Pasted stdin-only.
#   --generate a fresh 32-byte secret for a Kaimahi-scheme caller. The
#              caller must be told it; retrieve it with
#                kubectl -n kaimahi get secret kaimahi-inbound-signing \
#                  -o jsonpath='{.data.<hook>}' | base64 -d
#              (cluster credentials gate that, like every other Secret).
#
# Custody rules (docs/COORDINATION.md security guidance): secret bytes
# travel only through pipes and 0600 files — never argv, env listings,
# YAML on disk, or logs. Other hooks' keys in the Secret are preserved
# by re-reading them into files and re-applying the whole Secret from
# files (kubectl create --from-file); the manifest exists only inside
# the apply pipe.
#
# Usage: HOOK=demo bash scripts/inbound-secret.sh [--generate]
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${INBOUND_SECRET_NAMESPACE:-kaimahi}"
SECRET="${INBOUND_SIGNING_SECRET:-kaimahi-inbound-signing}"
hook="${HOOK:-}"
mode="${1:-stdin}"

case "$hook" in
  (*[!a-z0-9-]*|'') echo "usage: HOOK=<hook-name> bash scripts/inbound-secret.sh [--generate]  (hook: [a-z0-9-]+)" >&2; exit 2 ;;
esac
case "$mode" in
  (stdin|--generate) ;;
  (*) echo "unknown argument '$mode' (want nothing, or --generate)" >&2; exit 2 ;;
esac

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
mkdir "$workdir/keys"

if [ "$mode" = --generate ]; then
  od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$workdir/keys/$hook"
  test -s "$workdir/keys/$hook" || { echo 'entropy read failed' >&2; exit 1; }
else
  echo "Paste the signing secret for hook '$hook', press Enter, then Ctrl-D:" >&2
  tr -d '\r\n' < /dev/stdin > "$workdir/keys/$hook"
  test -s "$workdir/keys/$hook" || { echo 'no secret read on stdin' >&2; exit 1; }
  # A signing secret is a shared key, not a Slack token: refuse the
  # token shapes so a bot token pasted here by mistake is not stored
  # where it would sign nothing and leak on rotation.
  if grep -qE '^xox[bpca]-' "$workdir/keys/$hook"; then
    echo "that is a Slack TOKEN (xox?-...), not a signing secret. Slack's signing secret is on the app's Basic Information page." >&2
    exit 1
  fi
fi

# Preserve the other hooks' keys: decode each existing key into its own
# 0600 file, then re-create the Secret from files. Only a genuine
# NotFound may skip this: an unreachable API server, an expired
# credential or an RBAC denial on `get` would otherwise read as "absent"
# and the apply below would silently drop every other hook's key.
set +e
$KUBECTL -n "$NAMESPACE" get secret "$SECRET" -o json > "$workdir/existing.json" 2> "$workdir/get.err"
get_rc=$?
set -e
if [ "$get_rc" -ne 0 ] && ! grep -q NotFound "$workdir/get.err"; then
  echo "cannot read Secret $NAMESPACE/$SECRET (refusing to overwrite other hooks' keys blind):" >&2
  cat "$workdir/get.err" >&2
  exit 1
fi
if [ "$get_rc" -eq 0 ]; then
  python3 - "$workdir/existing.json" "$workdir/keys" "$hook" <<'EOF'
import base64, json, os, sys
d = json.load(open(sys.argv[1]))
for k, v in (d.get("data") or {}).items():
    if k == sys.argv[3]:
        continue  # replaced by the new value
    with open(os.path.join(sys.argv[2], k), "wb") as f:
        f.write(base64.b64decode(v))
EOF
fi

args=()
for f in "$workdir/keys"/*; do
  args+=(--from-file="$(basename "$f")=$f")
done
$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || $KUBECTL create namespace "$NAMESPACE"
$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET" "${args[@]}" \
  --dry-run=client -o yaml | $KUBECTL -n "$NAMESPACE" apply -f - >/dev/null
echo "Secret $NAMESPACE/$SECRET: key '$hook' stored (${mode#--})." >&2
echo "The proxy reads it per request from /etc/kaimahi/inbound/$hook; a Secret mounted" >&2
echo "for the first time can take kubelet up to a minute to project." >&2
