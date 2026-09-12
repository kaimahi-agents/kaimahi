#!/usr/bin/env bash
# Prove budget enforcement is EXACT across replicas: fire N
# chat completions at the proxy AT ONCE, spread across every running
# replica (a port-forward per pod, so both replicas are in the race by
# construction), with the governed kmh_ token, and require that exactly
# EXPECT_ADMITTED of them are answered 200 and every other one 429 —
# the meter's denial, taken before any upstream contact. Any other
# status (a 5xx, a 502, a 403) fails the probe: those are not budget
# decisions.
#
# Drives the proxy directly rather than through kagent because an
# agent's OpenAI client retries a 429 on its own, which would make the
# denied count a property of the client, not of the plane.
#
# Custody rules (docs/COORDINATION.md): the token travels only through
# pipes and 0600 files (curl -H @file) — never argv, env listings, logs.
#
# Usage: spend-race-probe.sh [N]   (env: EXPECT_ADMITTED=1
#        GOVERNED_SECRET=kaimahi-governed-token SECRET_NAMESPACE=kagent
#        UPSTREAM=ollama MODEL=qwen2.5:3b)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
SECRET_NAMESPACE="${SECRET_NAMESPACE:-kagent}"
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-governed-token}"
UPSTREAM="${UPSTREAM:-ollama}"
MODEL="${MODEL:-qwen2.5:3b}"
EXPECT_ADMITTED="${EXPECT_ADMITTED:-1}"
BASE_PORT="${BASE_PORT:-18180}"
n="${1:-8}"

# Context safety: run directly, so guard the effective context of
# $KUBECTL rather than trusting an ambient KUBE_CTX.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $SECRET_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $n"

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

$KUBECTL -n "$SECRET_NAMESPACE" get secret "$GOVERNED_SECRET" \
  -o jsonpath='{.data.api-key}' | base64 -d > "$workdir/token"
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty (run kmx govern)" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

# One port-forward per running replica.
mapfile -t pods < <(KUBECTL="$KUBECTL" bash "$(dirname "$0")/plane-pods.sh")
[ "${#pods[@]}" -ge 1 ] || { echo "no running kaimahi-proxy pods" >&2; exit 1; }
ports=()
for i in "${!pods[@]}"; do
  port=$((BASE_PORT + i))
  $KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 "pod/${pods[$i]}" "$port:8080" >/dev/null 2>&1 &
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

printf '{"model": "%s", "messages": [{"role": "user", "content": "Reply with the single word OK."}], "max_tokens": 8}\n' \
  "$MODEL" > "$workdir/body"

# Release every request together: each waits on the same fifo.
mkfifo "$workdir/gate"
workers=()
for i in $(seq 1 "$n"); do
  port="${ports[$(( (i - 1) % ${#ports[@]} ))]}"
  (
    : > "$workdir/ready-$i"
    read -r _ < "$workdir/gate" || true
    curl -sS --cacert "$workdir/plane-ca.crt" -o "$workdir/resp-$i" -w '%{http_code}' -X POST -H @"$workdir/auth-header" \
      -H 'Content-Type: application/json' --data @"$workdir/body" \
      "https://127.0.0.1:$port/upstream/$UPSTREAM/v1/chat/completions" > "$workdir/status-$i" 2>/dev/null \
      || echo "000" > "$workdir/status-$i"
    echo "$port" > "$workdir/port-$i"
  ) &
  workers+=("$!")
done
# Every worker must be at the gate before it opens (bounded).
for _ in $(seq 1 100); do
  [ "$(find "$workdir" -maxdepth 1 -name 'ready-*' | wc -l)" -ge "$n" ] && break
  sleep 0.1
done
[ "$(find "$workdir" -maxdepth 1 -name 'ready-*' | wc -l)" -ge "$n" ] || {
  echo "spend-race: workers did not all reach the gate" >&2; kill "${workers[@]}" 2>/dev/null || true; exit 1; }
# Open the fifo for writing once; every reader is released. The write
# end stays open until the workers are done, so a reader that opens the
# gate late still finds a writer and its line rather than blocking.
exec 3> "$workdir/gate"
for _ in $(seq 1 "$n"); do echo go >&3; done
# The workers only — the port-forwards are background jobs too and
# never exit on their own.
wait "${workers[@]}"
exec 3>&-

admitted=0; denied=0; other=0
declare -A per_port
for i in $(seq 1 "$n"); do
  st=$(cat "$workdir/status-$i"); port=$(cat "$workdir/port-$i")
  per_port["$port"]="${per_port[$port]:-}$st "
  case "$st" in
    200) admitted=$((admitted + 1)) ;;
    429) denied=$((denied + 1)) ;;
    *) other=$((other + 1)); echo "request $i via :$port -> $st: $(head -c 200 "$workdir/resp-$i")" >&2 ;;
  esac
done
for i in "${!pods[@]}"; do
  echo "replica ${pods[$i]} (:${ports[$i]}): ${per_port[${ports[$i]}]:-}"
done
echo "spend-race: $n concurrent -> admitted=$admitted denied(429)=$denied other=$other"
[ "$other" -eq 0 ] || { echo "spend-race: non-budget outcomes seen" >&2; exit 1; }
[ "$admitted" -eq "$EXPECT_ADMITTED" ] || {
  echo "spend-race: expected exactly $EXPECT_ADMITTED admitted, got $admitted" >&2; exit 1; }
[ "$denied" -eq $((n - EXPECT_ADMITTED)) ] || { echo "spend-race: denied count off" >&2; exit 1; }
# Both replicas took part (when there are two): each port saw traffic.
for port in "${ports[@]}"; do
  [ -n "${per_port[$port]:-}" ] || { echo "spend-race: replica on :$port saw no request" >&2; exit 1; }
done
