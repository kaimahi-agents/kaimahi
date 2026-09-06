#!/usr/bin/env bash
# Make one REAL tools/call through the MCP gateway with the governed
# kmh_ token, doing the full MCP streamable-HTTP handshake (initialize →
# notifications/initialized → tools/call, session header relayed) — and
# require it to SUCCEED: a JSON-RPC result with no error and no
# isError:true content. The approvals demo's positive half; the negative half
# is tool-denial-probe.sh.
#
# Custody rules (docs/COORDINATION.md): the token travels only through
# pipes and 0600 files (curl -H @file) — never argv, env listings, logs.
#
# Usage: tool-call-probe.sh <tool-name> [json-arguments]
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-tools-token}"
GATEWAY_PORT="${GATEWAY_PORT:-18082}"
UPSTREAM="${UPSTREAM:-kagent-tools}"

tool="${1:?usage: tool-call-probe.sh <tool-name> [json-arguments]}"
args="${2:-{\}}"
case "$tool" in
  (*[!A-Za-z0-9._-]*|'') echo "invalid tool name '$tool'" >&2; exit 2 ;;
esac

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

$KUBECTL -n "$AGENT_NAMESPACE" get secret "$GOVERNED_SECRET" \
  -o jsonpath='{.data.api-key}' | base64 -d > "$workdir/token"
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty (run make govern-tools)" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-mcp-gateway "$GATEWAY_PORT:8081" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS -o /dev/null "http://127.0.0.1:$GATEWAY_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS -o /dev/null "http://127.0.0.1:$GATEWAY_PORT/healthz" \
  || { echo "gateway port-forward failed" >&2; exit 1; }

mcp_url="http://127.0.0.1:$GATEWAY_PORT/upstream/$UPSTREAM/mcp"
mcp_post() { # body-file extra-header-file|- -> status; resp in $workdir/resp, headers in $workdir/resp-headers
  local body=$1 extra=$2
  local args=(-sS -X POST -H @"$workdir/auth-header" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    --data @"$body" -D "$workdir/resp-headers" -o "$workdir/resp" -w '%{http_code}' "$mcp_url")
  [ "$extra" = - ] || args+=(-H @"$extra")
  status=$(curl "${args[@]}")
}

# 1. initialize — capture the session the upstream assigns.
printf '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "kaimahi-probe", "version": "0"}}}\n' > "$workdir/req"
mcp_post "$workdir/req" -
[ "$status" = 200 ] || { echo "initialize failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
session=$(tr -d '\r' < "$workdir/resp-headers" | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2; exit}')
session_header=-
if [ -n "$session" ]; then
  printf 'Mcp-Session-Id: %s\n' "$session" > "$workdir/session-header"
  session_header="$workdir/session-header"
fi

# 2. notifications/initialized — completes the lifecycle handshake.
printf '{"jsonrpc": "2.0", "method": "notifications/initialized"}\n' > "$workdir/req"
mcp_post "$workdir/req" "$session_header"
case "$status" in (2*) ;; (*) echo "initialized notification failed (HTTP $status)" >&2; exit 1 ;; esac

# 3. tools/call — must succeed.
printf '{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "%s", "arguments": %s}}\n' \
  "$tool" "$args" > "$workdir/req"
mcp_post "$workdir/req" "$session_header"
[ "$status" = 200 ] || { echo "tools/call failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
python3 - "$workdir/resp" "$tool" <<'EOF'
import json, sys
raw = open(sys.argv[1]).read()
# The response may be SSE-framed: find the data event that IS the
# JSON-RPC response to our id (other frames — server log notifications
# and the like — must not satisfy the probe).
d = None
if raw.lstrip().startswith("{"):
    d = json.loads(raw)
else:
    for line in raw.splitlines():
        if not line.startswith("data:"):
            continue
        try:
            m = json.loads(line[5:].lstrip(" "))
        except ValueError:
            continue
        if isinstance(m, dict) and m.get("id") == 2:
            d = m
assert isinstance(d, dict) and d.get("id") == 2, f"no JSON-RPC response with id=2 in: {raw[:500]}"
assert "error" not in d, f"tools/call for {sys.argv[2]} returned an error: {d['error']}"
assert "result" in d, f"response carries no result: {d}"
result = d["result"]
assert not result.get("isError"), f"tool execution failed: {result}"
text = "".join(c.get("text", "") for c in result.get("content", []) if isinstance(c, dict))
print(f"tools/call {sys.argv[2]} succeeded through the gateway")
print(text[:2000])
EOF
