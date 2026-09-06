#!/usr/bin/env bash
# Print one proxy replica's Prometheus exposition. The ops port is
# on no Service, so this port-forwards to a POD (cluster credentials
# gate it, like the admin port; the port itself has no auth — see
# k8s/plane/network-policy.yaml for what may reach it in-cluster).
#
# Usage: KUBECTL="kubectl --context ..." [POD=name] plane-metrics.sh
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
OPS_PORT="${OPS_PORT:-19092}"
POD="${POD:-}"

if [ -z "$POD" ]; then
  # shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
  POD=$(KUBECTL="$KUBECTL" bash "$(dirname "$0")/plane-pods.sh" | head -1)
fi
test -n "$POD" || { echo "plane-metrics: no running kaimahi-proxy pod" >&2; exit 1; }

pf_pid=""
cleanup() { [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true; }
trap cleanup EXIT
# shellcheck disable=SC2086
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 "pod/$POD" "$OPS_PORT:9092" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS -o /dev/null "http://127.0.0.1:$OPS_PORT/livez" 2>/dev/null && break
  sleep 0.2
done
echo "# replica: $POD" >&2
curl -fsS "http://127.0.0.1:$OPS_PORT/metrics"
