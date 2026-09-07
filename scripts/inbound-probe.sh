#!/usr/bin/env bash
# Deliver ONE event to an inbound hook and assert exactly what the
# plane did with it. The demo's `make inbound-fire` and CI's fail-closed
# assertions both run through here.
#
# Fail closed, in the probe as in the product: the probe passes ONLY
# when the HTTP status equals $EXPECT and, for 202, the body is the
# plane's well-formed admission (status "admitted" with an event id). A
# 202 when a denial was expected fails; a denial when 202 was expected
# fails; anything else fails.
#
# Auth modes (AUTH):
#   hmac    (default) sign with the hook's key from the plane-side
#           Secret kaimahi-inbound-signing — Kaimahi's v1 scheme
#   bearer  present the hook credential's kmh_ token from Secret
#           $INBOUND_TOKEN_SECRET (default kaimahi-inbound-token)
#   none    send NO proof at all (the unauthenticated-event assertion)
#   forged  sign with a WRONG key (the forged-signature assertion)
#   stale   sign correctly with a timestamp ten minutes old (the
#           replay-window assertion)
#
# Custody rules (docs/COORDINATION.md): the signing key and any token
# travel only through pipes and 0600 files (python reads the key from a
# file; curl reads headers from a file) — never argv, env listings, logs.
#
# Usage: inbound-probe.sh <hook> <text>
#   env: EXPECT=202|401|403|409|413|429|503 (default 202)
#        DELIVERY=<id>   reuse a delivery id (the replay assertion);
#                        default: a fresh random id
#        AUTH=hmac|bearer|none|forged|stale
#        INBOUND_PORT=<local port> (default 18084)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
INBOUND_PORT="${INBOUND_PORT:-18084}"
SIGNING_SECRET="${INBOUND_SIGNING_SECRET:-kaimahi-inbound-signing}"
TOKEN_SECRET="${INBOUND_TOKEN_SECRET:-kaimahi-inbound-token}"
EXPECT="${EXPECT:-202}"
AUTH="${AUTH:-hmac}"

hook="${1:?usage: inbound-probe.sh <hook> <text>}"
text="${2:?usage: inbound-probe.sh <hook> <text>}"
case "$hook" in
  (*[!a-z0-9-]*|'') echo "invalid hook name '$hook' (want [a-z0-9-]+)" >&2; exit 2 ;;
esac
case "$EXPECT" in
  (*[!0-9]*|'') echo "invalid EXPECT '$EXPECT' (want an HTTP status)" >&2; exit 2 ;;
esac
case "$AUTH" in (hmac|bearer|none|forged|stale) ;; (*) echo "invalid AUTH '$AUTH'" >&2; exit 2 ;; esac
delivery="${DELIVERY:-probe-$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')}"
case "$delivery" in
  (*[!A-Za-z0-9._:-]*|'') echo "invalid DELIVERY '$delivery'" >&2; exit 2 ;;
esac

# Context safety: run directly, so resolve the effective context
# from $KUBECTL and guard it — see scripts/tool-call-probe.sh for why an
# ambient KUBE_CTX is deliberately NOT honoured here.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $AGENT_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $hook"

workdir=$(mktemp -d)
pf_pid=""
cleanup() {
  [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT

# The event body: a JSON object with the text, built by python so the
# text is escaped rather than interpolated.
python3 -c 'import json,sys; sys.stdout.write(json.dumps({"text": sys.argv[1]}))' "$text" > "$workdir/body"

# Proof headers into a 0600 file (curl -H @file).
: > "$workdir/headers"
printf 'X-Kaimahi-Delivery: %s\n' "$delivery" >> "$workdir/headers"
case "$AUTH" in
  hmac|forged|stale)
    if [ "$AUTH" = forged ]; then
      printf 'not-the-real-key' > "$workdir/key"
    else
      $KUBECTL -n "$NAMESPACE" get secret "$SIGNING_SECRET" -o jsonpath="{.data.$hook}" | base64 -d > "$workdir/key"
      test -s "$workdir/key" || { echo "no signing key for hook '$hook' in $SIGNING_SECRET (run make inbound-secret HOOK=$hook)" >&2; exit 1; }
    fi
    ts=$(date +%s)
    [ "$AUTH" = stale ] && ts=$((ts - 600))
    python3 - "$workdir/key" "$ts" "$delivery" "$workdir/body" >> "$workdir/headers" <<'EOF'
import hashlib, hmac, sys
key = open(sys.argv[1], "rb").read().strip()
ts, delivery = sys.argv[2], sys.argv[3]
body = open(sys.argv[4], "rb").read()
base = b"v1:" + ts.encode() + b":" + delivery.encode() + b":" + body
sig = hmac.new(key, base, hashlib.sha256).hexdigest()
print(f"X-Kaimahi-Timestamp: {ts}")
print(f"X-Kaimahi-Signature: v1={sig}")
EOF
    ;;
  bearer)
    $KUBECTL -n "$NAMESPACE" get secret "$TOKEN_SECRET" -o jsonpath='{.data.api-key}' | base64 -d > "$workdir/token"
    test -s "$workdir/token" || { echo "$TOKEN_SECRET missing/empty (run make inbound-credential CRED_INBOUND=... INBOUND_SECRET=$TOKEN_SECRET)" >&2; exit 1; }
    { printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } >> "$workdir/headers"
    ;;
  none) ;;
esac

# --address pins IPv4 explicitly (see plane-admin.sh for why).
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-inbound "$INBOUND_PORT:8082" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS -o /dev/null "http://127.0.0.1:$INBOUND_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS -o /dev/null "http://127.0.0.1:$INBOUND_PORT/healthz" \
  || { echo "inbound port-forward failed" >&2; exit 1; }

status=$(curl -sS -X POST -H @"$workdir/headers" -H 'Content-Type: application/json' \
  --data-binary @"$workdir/body" -o "$workdir/resp" -w '%{http_code}' \
  "http://127.0.0.1:$INBOUND_PORT/hook/$hook")

if [ "$status" != "$EXPECT" ]; then
  echo "hook '$hook' (auth=$AUTH, delivery=$delivery): expected HTTP $EXPECT, got HTTP $status:" >&2
  cat "$workdir/resp" >&2; echo >&2
  exit 1
fi
if [ "$status" = 202 ]; then
  python3 - "$workdir/resp" "$hook" "$delivery" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
assert d.get("status") == "admitted" and d.get("event_id") and d.get("hook") == sys.argv[2], f"malformed admission: {d}"
print(f"ADMITTED on hook {sys.argv[2]} (delivery {sys.argv[3]}): event {d['event_id']} -> agent {d.get('agent')} under grant {d.get('grant')}")
print("The agent runs asynchronously; `make inbound-audit` shows the outcome when it lands.")
EOF
else
  echo "REFUSED as expected (HTTP $status, auth=$AUTH, delivery=$delivery): $(tr -d '\n' < "$workdir/resp")"
fi
