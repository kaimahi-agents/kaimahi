#!/usr/bin/env bash
# Drive the tool seam the way a SPEC-COMPLIANT MCP client does: read the
# capabilities the handshake advertises, and call only what is advertised.
#
# That is the case the seam used to fail. The gateway relayed the upstream's
# advertisement verbatim while enforcing a tools-only method set, so a client
# that guards `prompts/list` on `capabilities.prompts` — which is what the
# specification tells it to do — called a method the gateway then refused,
# fatally, at startup. This probe fails if that advertisement is anything but
# `tools`, and it fails if a client that trusted it could not then work.
#
# It proves the projection is the GATEWAY's doing by asking the upstream
# directly first: if the server itself advertised only tools, a passing probe
# would prove nothing.
#
# Custody rules (docs/COORDINATION.md): the token travels only through pipes
# and 0600 files (curl -H @file) — never argv, env listings, logs.
#
# Env: UPSTREAM (table name), GOVERNED_SECRET, SECRET_NAMESPACE,
#      SERVER_NAMESPACE + SERVER_DEPLOY + SERVER_PORT (the upstream's own
#      endpoint, for the direct read), GATEWAY_PORT.
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
SECRET_NAMESPACE="${SECRET_NAMESPACE:-kagent}"
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-warehouse-token}"
GATEWAY_PORT="${GATEWAY_PORT:-18084}"
UPSTREAM="${UPSTREAM:-warehouse}"
SERVER_NAMESPACE="${SERVER_NAMESPACE:-acme}"
SERVER_DEPLOY="${SERVER_DEPLOY:-acme-warehouse}"
SERVER_PORT="${SERVER_PORT:-9090}"
TOOL="${TOOL:-stock_get}"
TOOL_ARGS="${TOOL_ARGS:-{\"sku\": \"SKU-1\"\}}"

# Context safety: unlike a make target, this script is run directly, so
# nothing has resolved a context for it — see scripts/kube-guard.sh.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
probe_ns=$(printf '%s\n%s\n%s\n' "$NAMESPACE" "$SECRET_NAMESPACE" "$SERVER_NAMESPACE" \
  | awk '!seen[$0]++' | paste -sd, - | sed 's/,/, /g')
KUBE_NS="$probe_ns" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $UPSTREAM"

workdir=$(mktemp -d)
pf_pid=""
cleanup() {
  [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT

# 1. What the SERVER advertises, read from inside its own pod. Without this
#    the probe cannot tell a projection from a server that never offered
#    anything to project.
$KUBECTL -n "$SERVER_NAMESPACE" exec "deploy/$SERVER_DEPLOY" -- python3 -c '
import json, urllib.request
r = urllib.request.urlopen(urllib.request.Request(
    "http://127.0.0.1:'"$SERVER_PORT"'/mcp",
    data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                     "params": {"protocolVersion": "2025-03-26"}}).encode(),
    headers={"Content-Type": "application/json"}), timeout=10)
raw = r.read().decode()
# The upstream may answer as plain JSON or as an event; this read is about
# what it OFFERS, not how it frames the offer.
if raw.lstrip().startswith("{"):
    msg = json.loads(raw)
else:
    msg = next(json.loads(line[5:].lstrip(" "))
               for line in raw.splitlines() if line.startswith("data:"))
print(json.dumps(msg["result"]["capabilities"]))
' > "$workdir/upstream-caps"
python3 - "$workdir/upstream-caps" <<'EOF'
import json, sys
caps = json.load(open(sys.argv[1]))
extra = sorted(k for k in caps if k != "tools")
assert extra, ("the upstream advertises only tools, so this probe could not "
               "tell a projection from a server with nothing to project: %s" % caps)
print("the upstream itself advertises: %s" % ", ".join(sorted(caps)))
EOF

# 2. Both seams serve TLS under the plane's own authority; fetch what to
#    verify against. Never --insecure.
# shellcheck source=scripts/seam-tls.sh
. "$(dirname "$0")/seam-tls.sh"
seam_ca "$workdir/plane-ca.crt"

$KUBECTL -n "$SECRET_NAMESPACE" get secret "$GOVERNED_SECRET" \
  -o jsonpath='{.data.api-key}' | base64 -d > "$workdir/token"
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty" >&2; exit 1; }
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

# The trailing slash a client's URL normaliser appends. The seam answers
# both forms, and this probe uses the one that used to 404.
mcp_url="https://127.0.0.1:$GATEWAY_PORT/upstream/$UPSTREAM/mcp/"
mcp_post() { # body-file extra-header-file|- -> status; resp in $workdir/resp
  local body=$1 extra=$2
  local args=(-sS --cacert "$workdir/plane-ca.crt" -X POST -H @"$workdir/auth-header" \
    -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
    --data @"$body" -D "$workdir/resp-headers" -o "$workdir/resp" -w '%{http_code}' "$mcp_url")
  [ "$extra" = - ] || args+=(-H @"$extra")
  status=$(curl "${args[@]}")
}

# 3. initialize, through the gateway.
printf '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "kaimahi-capability-probe", "version": "0"}}}\n' > "$workdir/req"
mcp_post "$workdir/req" -
[ "$status" = 200 ] || { echo "initialize failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
session=$(tr -d '\r' < "$workdir/resp-headers" | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2; exit}')
session_header=-
if [ -n "$session" ]; then
  printf 'Mcp-Session-Id: %s\n' "$session" > "$workdir/session-header"
  session_header="$workdir/session-header"
fi

# 4. The projection: what the client is told it may use.
python3 - "$workdir/resp" "$workdir/upstream-caps" <<'EOF'
import json, sys
raw = open(sys.argv[1]).read()
if raw.lstrip().startswith("{"):
    msg = json.loads(raw)
else:
    msg = None
    for line in raw.splitlines():
        if line.startswith("data:"):
            candidate = json.loads(line[5:].lstrip(" "))
            if isinstance(candidate, dict) and candidate.get("id") == 1:
                msg = candidate
assert msg is not None, "no JSON-RPC handshake in: %s" % raw[:500]
assert "error" not in msg, "the gateway refused the handshake: %s" % msg["error"]
caps = msg["result"]["capabilities"]
offered = sorted(caps)
assert offered == ["tools"], (
    "the gateway advertises %s but relays tools only — a client that trusts "
    "this dies on the first call it is told it may make" % offered)
# The VALUE too, not just the key. `tools.listChanged` promises
# notifications/tools/list_changed on a server-initiated stream, and this
# gateway offers none — GET on the seam is a 405. Checking only the key set
# would let that same lie through one level down.
assert caps["tools"] == {}, (
    "the gateway advertises tools%s, and every one of those is a promise it "
    "has to be able to keep" % caps["tools"])
# The rest of the handshake is the upstream's and must survive the rewrite.
assert msg["result"]["serverInfo"]["name"], "serverInfo did not survive the projection"
assert msg["result"]["protocolVersion"], "protocolVersion did not survive the projection"
dropped = sorted(k for k in json.load(open(sys.argv[2])) if k != "tools")
print("advertised through the gateway: tools (dropped: %s)" % ", ".join(dropped))
EOF

# 5. What the client does next, and this is the whole point: it calls only
#    what it was offered. prompts/list is never sent, because the
#    advertisement no longer promises it.
printf '{"jsonrpc": "2.0", "method": "notifications/initialized"}\n' > "$workdir/req"
mcp_post "$workdir/req" "$session_header"
case "$status" in (2*) ;; (*) echo "initialized notification failed (HTTP $status)" >&2; exit 1 ;; esac

printf '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}\n' > "$workdir/req"
mcp_post "$workdir/req" "$session_header"
[ "$status" = 200 ] || { echo "tools/list failed (HTTP $status)" >&2; cat "$workdir/resp" >&2; exit 1; }

printf '{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "%s", "arguments": %s}}\n' \
  "$TOOL" "$TOOL_ARGS" > "$workdir/req"
mcp_post "$workdir/req" "$session_header"
[ "$status" = 200 ] || { echo "tools/call failed (HTTP $status)" >&2; cat "$workdir/resp" >&2; exit 1; }
python3 - "$workdir/resp" <<'EOF'
import json, sys
raw = open(sys.argv[1]).read()
d = json.loads(raw) if raw.lstrip().startswith("{") else None
if d is None:
    for line in raw.splitlines():
        if line.startswith("data:"):
            m = json.loads(line[5:].lstrip(" "))
            if isinstance(m, dict) and m.get("id") == 3:
                d = m
assert d and "error" not in d, "the governed tool call failed: %s" % raw[:500]
assert not d["result"].get("isError"), "tool execution failed: %s" % d["result"]
print("a client that read the advertisement and trusted it ran to a real tool call")
EOF

# 6. And the method set is UNCHANGED. The projection is what stops a client
#    asking; it does not widen what the gateway will relay.
for refused in prompts/list resources/list; do
  printf '{"jsonrpc": "2.0", "id": 9, "method": "%s"}\n' "$refused" > "$workdir/req"
  mcp_post "$workdir/req" "$session_header"
  grep -q 'method not relayed by the Kaimahi gateway' "$workdir/resp" \
    || { echo "$refused was not refused (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
done
echo "prompts/list and resources/list are still refused — advertised nowhere, relayed nowhere"
