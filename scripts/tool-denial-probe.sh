#!/usr/bin/env bash
# Prove the MCP gateway denies a NOT-allowlisted tools/call fail-closed:
# port-forward the gateway Service, call the given tool with the governed
# kmh_ token, and require a JSON-RPC error (no result) naming the
# allowlist. Exits nonzero on any other outcome — including the call
# unexpectedly succeeding.
#
# Custody rules (docs/COORDINATION.md security guidance): the governed
# token travels only through pipes and 0600 files (curl reads the auth
# header from a file) — never argv, env listings, or logs.
#
# A denial is about a CALL, not a verb: the optional
# json-arguments name the call to attempt, and the approval request the
# denial files is welded to it. Two attempts with different
# policy-relevant arguments therefore file two requests, and the retry
# after an approval must carry the SAME arguments to be admitted.
#
# Usage: tool-denial-probe.sh <tool-name> [json-arguments]
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-tools-token}"
GATEWAY_PORT="${GATEWAY_PORT:-18081}"
UPSTREAM="${UPSTREAM:-kagent-tools}"

tool="${1:?usage: tool-denial-probe.sh <tool-name> [json-arguments]}"
args="${2:-{\}}"
case "$tool" in
  (*[!A-Za-z0-9._-]*|'') echo "invalid tool name '$tool'" >&2; exit 2 ;;
esac
python3 -c 'import json,sys
d = json.loads(sys.argv[1])
assert isinstance(d, dict), "arguments must be a JSON object"' "$args" \
  || { echo "invalid json-arguments '$args'" >&2; exit 2; }

# Context safety: unlike a make target, this script is run directly,
# so nothing has resolved a context for it — see the "run directly" note in
# scripts/kube-guard.sh.
#
# The context is derived from $KUBECTL and NOT from an inherited KUBE_CTX,
# so that the cluster the guard vouches for is the cluster this script
# actually acts on. Honouring an ambient KUBE_CTX here would re-open the
# very hole the guard was added to close: KUBE_CTX is a documented knob,
# and someone who exported KUBE_CTX=kind-... and then ran `make aks-cluster`
# (whose `az aks get-credentials --overwrite-existing` flips
# current-context to AKS) would get a silent kind-shaped pass while every
# call below went to the managed cluster.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $AGENT_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $tool"

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
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty (run kmx tools govern)" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

# --address pins IPv4 explicitly so a stale listener cannot receive the request.
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-mcp-gateway "$GATEWAY_PORT:8081" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$GATEWAY_PORT/healthz" \
  || { echo "gateway port-forward failed" >&2; exit 1; }

printf '{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "%s", "arguments": %s}}\n' \
  "$tool" "$args" > "$workdir/req"
status=$(curl -sS --cacert "$workdir/plane-ca.crt" -X POST -H @"$workdir/auth-header" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  --data @"$workdir/req" -o "$workdir/resp" -w '%{http_code}' \
  "https://127.0.0.1:$GATEWAY_PORT/upstream/$UPSTREAM/mcp")
[ "$status" = 200 ] || { echo "expected HTTP 200 carrying a JSON-RPC error, got $status:" >&2; cat "$workdir/resp" >&2; exit 1; }
python3 - "$workdir/resp" "$tool" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
err = d.get("error")
assert "result" not in d, f"tools/call for {sys.argv[2]} unexpectedly returned a result"
assert err and "not permitted" in err.get("message", ""), f"unexpected response: {d}"
print(f'denied as expected: {sys.argv[2]} -> JSON-RPC error {err["code"]}: {err["message"]}')
EOF
