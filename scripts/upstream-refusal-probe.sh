#!/usr/bin/env bash
# Prove the MCP gateway REFUSES a hosted upstream fail-closed: send a
# tools/call for an ALLOWLISTED tool (TOOL, default `echo`) to the named
# upstream with the governed kmh_ token and require exactly the expected
# HTTP status and a body naming the reason
# (docs/hosted-upstreams.md). The call is admitted by the allowlist and
# then refused at the dial or by the upstream's answer, so the refusal
# lands on an `allowed 502` audit row with the reason in its detail.
# Exits nonzero on any other outcome — including the call succeeding.
#
#   EXPECT_STATUS  502 (default) — the egress policy or the network refused it
#   EXPECT_BODY    substring the response body must carry, e.g.
#                    'refused by the egress policy'   (the dialer refused: private answer)
#                    'redirected (refused)'            (the upstream answered a 3xx)
#                    'unreachable'                     (the network refused: no allowance)
#
# Custody rules (docs/COORDINATION.md security guidance): the governed
# token travels only through pipes and 0600 files (curl reads the auth
# header from a file) — never argv, env listings, or logs.
#
# Usage: UPSTREAM=<name> [TOOL=echo] EXPECT_BODY='...' upstream-refusal-probe.sh
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-github-token}"
GATEWAY_PORT="${GATEWAY_PORT:-18083}"
UPSTREAM="${UPSTREAM:?set UPSTREAM to the tool_upstreams entry to probe}"
EXPECT_STATUS="${EXPECT_STATUS:-502}"
EXPECT_BODY="${EXPECT_BODY:?set EXPECT_BODY to the substring the refusal must carry}"
TOOL="${TOOL:-echo}"
case "$TOOL" in
  (*[!A-Za-z0-9._-]*|'') echo "invalid tool name '$TOOL'" >&2; exit 2 ;;
esac
case "$UPSTREAM" in
  (*[!A-Za-z0-9._-]*|'') echo "invalid upstream name '$UPSTREAM'" >&2; exit 2 ;;
esac

# Context safety: derived from $KUBECTL, never from an inherited
# KUBE_CTX — see scripts/tool-denial-probe.sh for why.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $AGENT_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $UPSTREAM"

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
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty (run make govern-github)" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-mcp-gateway "$GATEWAY_PORT:8081" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/healthz" \
  || { echo "gateway port-forward failed" >&2; exit 1; }

# A tools/call, not an initialize: only a tools/call is audited, and the
# refusal must land on the audit row of the call it refused.
printf '{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "%s", "arguments": {}}}\n' "$TOOL" > "$workdir/req"
# A refused dial answers at once; an unreachable one only after the
# dialer's connect timeout (10 s) — give the call room for that.
status=$(curl -sS --cacert "$workdir/plane-ca.crt" --max-time 60 -X POST -H @"$workdir/auth-header" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  --data @"$workdir/req" -o "$workdir/resp" -w '%{http_code}' \
  "https://127.0.0.1:$GATEWAY_PORT/upstream/$UPSTREAM/mcp")
[ "$status" = "$EXPECT_STATUS" ] || {
  echo "expected HTTP $EXPECT_STATUS from upstream '$UPSTREAM', got $status:" >&2
  head -c 600 "$workdir/resp" >&2; echo >&2; exit 1; }
grep -qF -- "$EXPECT_BODY" "$workdir/resp" || {
  echo "HTTP $status as expected, but the body does not say '$EXPECT_BODY':" >&2
  head -c 600 "$workdir/resp" >&2; echo >&2; exit 1; }
printf 'refused as expected: upstream %s -> HTTP %s: ' "$UPSTREAM" "$status"
head -c 200 "$workdir/resp"; echo
