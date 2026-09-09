#!/usr/bin/env bash
# Post ONE raw JSON-RPC body at the MCP gateway and require an exact
# outcome. The other probes build well-formed messages; this one exists
# for the messages a well-behaved client cannot produce — a duplicated
# key, a batch, a malformed body — where what matters is that the
# gateway refuses them fail-closed and audits the refusal.
#
# Custody rules (docs/COORDINATION.md security guidance): the governed
# token travels only through pipes and 0600 files (curl reads the auth
# header from a file) — never argv, env listings, or logs.
#
# Usage: EXPECT=400 EXPECT_BODY='duplicate JSON key' \
#          tool-raw-probe.sh '<json-rpc body>'
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
# Where the governed token's Secret lives. `kagent` is where kmx writes it
# on the kagent path; a runtime this project did not deploy holds it in its
# own namespace, and there is no kagent namespace to read.
AGENT_NAMESPACE="${SECRET_NAMESPACE:-kagent}"
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-tools-token}"
GATEWAY_PORT="${GATEWAY_PORT:-18086}"
UPSTREAM="${UPSTREAM:-kagent-tools}"
EXPECT="${EXPECT:-400}"
EXPECT_BODY="${EXPECT_BODY:-}"

body="${1:?usage: tool-raw-probe.sh '<json-rpc body>'}"

# Context safety: run directly, so nothing has resolved a context
# for this script — see the "run directly" note in scripts/kube-guard.sh.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $AGENT_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0")"

workdir=$(mktemp -d)
pf_pid=""
cleanup() {
  [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT
# Both seams serve TLS under the plane's own authority; fetch what to verify
# against. Never --insecure: a probe that skipped verification would keep
# passing on the day the certificate stopped being valid.
# shellcheck source=scripts/seam-tls.sh
. "$(dirname "$0")/seam-tls.sh"
seam_ca "$workdir/plane-ca.crt"

$KUBECTL -n "$AGENT_NAMESPACE" get secret "$GOVERNED_SECRET" \
  -o jsonpath='{.data.api-key}' | base64 -d > "$workdir/token"
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

# --address pins IPv4 explicitly (see plane-admin.sh for why).
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-mcp-gateway "$GATEWAY_PORT:8081" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/healthz" \
  || { echo "gateway port-forward failed" >&2; exit 1; }

printf '%s' "$body" > "$workdir/req"
status=$(curl -sS --cacert "$workdir/plane-ca.crt" -X POST -H @"$workdir/auth-header" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  --data @"$workdir/req" -o "$workdir/resp" -w '%{http_code}' \
  "https://127.0.0.1:$GATEWAY_PORT/upstream/$UPSTREAM/mcp")
if [ "$status" != "$EXPECT" ]; then
  echo "expected HTTP $EXPECT, got $status:" >&2
  cat "$workdir/resp" >&2
  exit 1
fi
if [ -n "$EXPECT_BODY" ] && ! grep -qF "$EXPECT_BODY" "$workdir/resp"; then
  echo "response body does not carry '$EXPECT_BODY':" >&2
  cat "$workdir/resp" >&2
  exit 1
fi
echo "refused as expected: HTTP $status${EXPECT_BODY:+ — $EXPECT_BODY}"
cat "$workdir/resp"
