#!/usr/bin/env bash
# One governed MODEL call, made directly at the seam, and the answer
# printed — the model seam's counterpart to `tool-call-probe.sh`.
#
# Why it exists as its own probe: the other way to make a governed model
# call is `kmx agent chat`, which goes through an agent, and an agent's OpenAI
# client speaks exactly one protocol and retries on its own. Neither is
# usable for proving that an upstream speaking the Responses API is
# metered — the client would never send that shape, and a retry would
# make the ledger's row count a property of the client.
#
# So this posts a body you give it to a path you give it, under the
# governed kmh_ token, and prints what came back. Everything about which
# protocol is being spoken lives in those two arguments, which is why one
# probe covers both seams' protocols and will cover the next one.
#
# Custody rules (docs/COORDINATION.md): the token travels only through
# pipes and 0600 files (curl -H @file) — never argv, env listings, logs.
#
# Usage: model-seam-probe.sh '<json body>'
#   env: UPSTREAM=ollama            the upstreams-table entry to call
#        SEAM_PATH=v1/chat/completions  the one path that upstream allows
#        EXPECT=200                 the status this call must produce
#        GOVERNED_SECRET=kaimahi-governed-token
#        SECRET_NAMESPACE=kagent
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
SECRET_NAMESPACE="${SECRET_NAMESPACE:-kagent}"
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-governed-token}"
UPSTREAM="${UPSTREAM:-ollama}"
SEAM_PATH="${SEAM_PATH:-v1/chat/completions}"
EXPECT="${EXPECT:-200}"
PORT="${PORT:-18190}"
body="${1:?usage: model-seam-probe.sh '<json body>'  (env: UPSTREAM, SEAM_PATH, EXPECT)}"

# Context safety: run directly, so guard the effective context of
# $KUBECTL (see scripts/tool-call-probe.sh for why not an ambient KUBE_CTX).
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $SECRET_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $UPSTREAM/$SEAM_PATH"

workdir=$(mktemp -d)
pf_pid=""
cleanup() {
  if [ -n "$pf_pid" ]; then kill "$pf_pid" 2>/dev/null || true; fi
  rm -rf "$workdir"
}
trap cleanup EXIT

# The seam serves TLS under the plane's own authority; fetch what to verify
# against. Never --insecure: a probe that skipped verification would keep
# passing on the day the certificate stopped being valid.
# shellcheck source=scripts/seam-tls.sh
. "$(dirname "$0")/seam-tls.sh"
seam_ca "$workdir/plane-ca.crt"

$KUBECTL -n "$SECRET_NAMESPACE" get secret "$GOVERNED_SECRET" \
  -o jsonpath='{.data.api-key}' | base64 -d > "$workdir/token"
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"
printf '%s' "$body" > "$workdir/body"

$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  deploy/kaimahi-proxy "$PORT:8080" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$PORT/healthz" ||
  { echo "port-forward to $PORT failed" >&2; exit 1; }

status=$(curl -sS --cacert "$workdir/plane-ca.crt" -o "$workdir/resp" -w '%{http_code}' \
  -X POST -H @"$workdir/auth-header" -H 'Content-Type: application/json' \
  --data @"$workdir/body" \
  "https://127.0.0.1:$PORT/upstream/$UPSTREAM/$SEAM_PATH" || echo 000)

echo "model-seam: POST /upstream/$UPSTREAM/$SEAM_PATH -> $status"
cat "$workdir/resp"
echo
[ "$status" = "$EXPECT" ] || { echo "model-seam: expected $EXPECT, got $status" >&2; exit 1; }
