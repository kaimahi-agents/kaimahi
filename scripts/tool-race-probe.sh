#!/usr/bin/env bash
# Prove grant consumption is EXACT across replicas: run M
# complete MCP sequences (initialize → notifications/initialized →
# tools/call) AT ONCE against the gateway, spread across every running
# replica (a port-forward per pod), with the governed kmh_ token, for a
# tool that is outside the static allowlist and covered by a live grant
# with EXPECT_ADMITTED uses. Require that exactly EXPECT_ADMITTED calls
# come back with a JSON-RPC result and every other one with the
# gateway's -32001 "not permitted" error. Anything else fails.
#
# Custody rules (docs/COORDINATION.md): the token travels only through
# pipes and 0600 files (curl -H @file) — never argv, env listings, logs.
#
# Usage: tool-race-probe.sh <tool-name> [M] [json-arguments]
#        (env: EXPECT_ADMITTED=1 GOVERNED_SECRET=kaimahi-tools-token UPSTREAM=kagent-tools)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-tools-token}"
UPSTREAM="${UPSTREAM:-kagent-tools}"
EXPECT_ADMITTED="${EXPECT_ADMITTED:-1}"
BASE_PORT="${BASE_PORT:-18280}"

tool="${1:?usage: tool-race-probe.sh <tool-name> [M] [json-arguments]}"
m="${2:-8}"
args="${3:-{\}}"
case "$tool" in
  (*[!A-Za-z0-9._-]*|'') echo "invalid tool name '$tool'" >&2; exit 2 ;;
esac

# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $AGENT_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $tool"

workdir=$(mktemp -d)
pf_pids=()
cleanup() {
  for p in "${pf_pids[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
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

mapfile -t pods < <(KUBECTL="$KUBECTL" bash "$(dirname "$0")/plane-pods.sh")
[ "${#pods[@]}" -ge 1 ] || { echo "no running kaimahi-proxy pods" >&2; exit 1; }
ports=()
for i in "${!pods[@]}"; do
  port=$((BASE_PORT + i))
  $KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 "pod/${pods[$i]}" "$port:8081" >/dev/null 2>&1 &
  pf_pids+=("$!")
  ports+=("$port")
done
for port in "${ports[@]}"; do
  for _ in $(seq 1 150); do
    curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$port/healthz" 2>/dev/null && break
    sleep 0.2
  done
  curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$port/healthz" || { echo "port-forward to $port failed" >&2; exit 1; }
done

printf '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "kaimahi-race", "version": "0"}}}\n' > "$workdir/init"
printf '{"jsonrpc": "2.0", "method": "notifications/initialized"}\n' > "$workdir/initialized"
printf '{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "%s", "arguments": %s}}\n' \
  "$tool" "$args" > "$workdir/call"

# One sequence per worker: the handshake first (its own session with
# the upstream), then — released together with every other worker at
# the gate — the governed call.
mkfifo "$workdir/gate"
workers=()
for i in $(seq 1 "$m"); do
  port="${ports[$(( (i - 1) % ${#ports[@]} ))]}"
  url="https://127.0.0.1:$port/upstream/$UPSTREAM/mcp"
  (
    d="$workdir/w$i"; mkdir -p "$d"
    st=$(curl -sS --cacert "$workdir/plane-ca.crt" -X POST -H @"$workdir/auth-header" -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' --data @"$workdir/init" \
      -D "$d/init-headers" -o "$d/init-resp" -w '%{http_code}' "$url" || echo 000)
    [ "$st" = 200 ] || { echo "init:$st" > "$d/outcome"; exit 0; }
    session=$(tr -d '\r' < "$d/init-headers" | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2; exit}')
    hdr=(); [ -n "$session" ] && { printf 'Mcp-Session-Id: %s\n' "$session" > "$d/session"; hdr=(-H @"$d/session"); }
    curl -sS --cacert "$workdir/plane-ca.crt" -o /dev/null -X POST -H @"$workdir/auth-header" -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' "${hdr[@]}" --data @"$workdir/initialized" "$url" || true
    # Handshake done: report ready, then block on the gate.
    : > "$d/ready"
    read -r _ < "$workdir/gate" || true
    st=$(curl -sS --cacert "$workdir/plane-ca.crt" -X POST -H @"$workdir/auth-header" -H 'Content-Type: application/json' \
      -H 'Accept: application/json, text/event-stream' "${hdr[@]}" --data @"$workdir/call" \
      -o "$d/call-resp" -w '%{http_code}' "$url" || echo 000)
    python3 - "$d/call-resp" "$st" > "$d/outcome" <<'EOF'
import json, sys
raw = open(sys.argv[1]).read(); st = sys.argv[2]
d = None
if raw.lstrip().startswith("{"):
    try: d = json.loads(raw)
    except ValueError: d = None
else:
    for line in raw.splitlines():
        if line.startswith("data:"):
            try: m = json.loads(line[5:].lstrip(" "))
            except ValueError: continue
            if isinstance(m, dict) and m.get("id") == 2: d = m
if not isinstance(d, dict) or d.get("id") != 2:
    print(f"nojsonrpc:{st}")
elif "error" in d:
    print(f"error:{d['error'].get('code')}")
elif d.get("result", {}).get("isError"):
    print("tool-error")
else:
    print("result")
EOF
    echo "$port" > "$d/port"
  ) &
  workers+=("$!")
done
# Every worker must have finished its handshake and be waiting at the
# gate before it opens: a worker that reached the gate after the writer
# closed would block forever, and one released alone would not be in
# the race. Bounded — a handshake that never completes fails the probe.
for _ in $(seq 1 300); do
  [ "$(find "$workdir" -name ready -type f | wc -l)" -ge "$m" ] && break
  sleep 0.2
done
[ "$(find "$workdir" -name ready -type f | wc -l)" -ge "$m" ] || {
  echo "tool-race: only $(find "$workdir" -name ready -type f | wc -l)/$m workers completed the MCP handshake" >&2
  kill "${workers[@]}" 2>/dev/null || true; exit 1; }
exec 3> "$workdir/gate"
for _ in $(seq 1 "$m"); do echo go >&3; done
# The workers only — the port-forwards are background jobs too and
# never exit on their own. The write end stays open until they are all
# done, so a reader that opens the gate late still finds a writer and
# its line rather than blocking forever.
wait "${workers[@]}"
exec 3>&-

admitted=0; denied=0; other=0
declare -A per_port
for i in $(seq 1 "$m"); do
  d="$workdir/w$i"
  out=$(cat "$d/outcome" 2>/dev/null || echo missing); port=$(cat "$d/port" 2>/dev/null || echo '?')
  per_port["$port"]="${per_port[$port]:-}$out "
  case "$out" in
    result) admitted=$((admitted + 1)) ;;
    error:-32001) denied=$((denied + 1)) ;;
    *) other=$((other + 1)); echo "worker $i via :$port -> $out" >&2 ;;
  esac
done
for i in "${!pods[@]}"; do
  echo "replica ${pods[$i]} (:${ports[$i]}): ${per_port[${ports[$i]}]:-}"
done
echo "tool-race: $m concurrent $tool -> admitted=$admitted denied(-32001)=$denied other=$other"
[ "$other" -eq 0 ] || { echo "tool-race: non-grant outcomes seen" >&2; exit 1; }
[ "$admitted" -eq "$EXPECT_ADMITTED" ] || {
  echo "tool-race: expected exactly $EXPECT_ADMITTED admitted, got $admitted" >&2; exit 1; }
[ "$denied" -eq $((m - EXPECT_ADMITTED)) ] || { echo "tool-race: denied count off" >&2; exit 1; }
for port in "${ports[@]}"; do
  [ -n "${per_port[$port]:-}" ] || { echo "tool-race: replica on :$port saw no request" >&2; exit 1; }
done
