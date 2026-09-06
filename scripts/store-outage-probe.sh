#!/usr/bin/env bash
# Prove a Postgres outage drops the proxies' READINESS and never their
# LIVENESS: scale Postgres to zero, require every replica's
# /readyz to answer 503 and its /livez 200 for longer than the liveness
# probe's restart threshold (3 × 10 s), then scale Postgres back and
# require /readyz 200 again — with every replica's restart count where
# it started. A plane that cannot reach its store is not routable; it is
# not broken.
#
# Usage: store-outage-probe.sh   (env: HOLD_SECONDS=45)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
HOLD_SECONDS="${HOLD_SECONDS:-45}"
BASE_PORT="${BASE_PORT:-18480}"

# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") (scales kaimahi-postgres to 0 and back)"

pf_pids=()
cleanup() {
  for p in "${pf_pids[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  # Whatever happened, leave Postgres running.
  $KUBECTL -n "$NAMESPACE" scale deploy/kaimahi-postgres --replicas=1 >/dev/null 2>&1 || true
}
trap cleanup EXIT

restarts() { # -> "pod=count pod=count"
  $KUBECTL -n "$NAMESPACE" get pods -l app=kaimahi-proxy \
    -o jsonpath='{range .items[*]}{.metadata.name}={.status.containerStatuses[0].restartCount} {end}'
}
mapfile -t pods < <(KUBECTL="$KUBECTL" bash "$(dirname "$0")/plane-pods.sh")
[ "${#pods[@]}" -ge 1 ] || { echo "no running kaimahi-proxy pods" >&2; exit 1; }
ports=()
for i in "${!pods[@]}"; do
  port=$((BASE_PORT + i))
  $KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 "pod/${pods[$i]}" "$port:9092" >/dev/null 2>&1 &
  pf_pids+=("$!")
  ports+=("$port")
done
probe() { curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/$2" 2>/dev/null || echo 000; }
for port in "${ports[@]}"; do
  for _ in $(seq 1 150); do [ "$(probe "$port" livez)" = 200 ] && break; sleep 0.2; done
  [ "$(probe "$port" readyz)" = 200 ] || { echo "replica on :$port not ready before the outage" >&2; exit 1; }
done
before=$(restarts)
echo "restart counts before: $before"

echo "scaling Postgres to 0"
$KUBECTL -n "$NAMESPACE" scale deploy/kaimahi-postgres --replicas=0 >/dev/null
$KUBECTL -n "$NAMESPACE" wait --for=delete pod -l app=kaimahi-postgres --timeout=120s >/dev/null

# Readiness must drop on every replica (the pool's ping fails once the
# server is gone; allow the probe interval to notice), liveness must not.
for port in "${ports[@]}"; do
  ok=0
  for _ in $(seq 1 60); do
    [ "$(probe "$port" readyz)" = 503 ] && { ok=1; break; }
    sleep 1
  done
  [ "$ok" = 1 ] || { echo "replica on :$port stayed ready without Postgres" >&2; exit 1; }
done
echo "every replica reports not-ready; holding the outage for ${HOLD_SECONDS}s (liveness threshold is 30s)"
end=$((SECONDS + HOLD_SECONDS))
while [ "$SECONDS" -lt "$end" ]; do
  for port in "${ports[@]}"; do
    [ "$(probe "$port" livez)" = 200 ] || { echo "liveness failed on :$port during a store outage" >&2; exit 1; }
  done
  sleep 5
done
during=$(restarts)
[ "$during" = "$before" ] || { echo "a proxy restarted during the outage: $before -> $during" >&2; exit 1; }
# The pods stayed, and the Service dropped them: no ready endpoints.
endpoints=$($KUBECTL -n "$NAMESPACE" get endpoints kaimahi-proxy -o jsonpath='{.subsets[*].addresses[*].ip}')
[ -z "$endpoints" ] || { echo "Service still has ready endpoints during the outage: $endpoints" >&2; exit 1; }

echo "scaling Postgres back to 1"
$KUBECTL -n "$NAMESPACE" scale deploy/kaimahi-postgres --replicas=1 >/dev/null
$KUBECTL -n "$NAMESPACE" rollout status deploy/kaimahi-postgres --timeout=180s >/dev/null
for port in "${ports[@]}"; do
  ok=0
  for _ in $(seq 1 60); do
    [ "$(probe "$port" readyz)" = 200 ] && { ok=1; break; }
    sleep 1
  done
  [ "$ok" = 1 ] || { echo "replica on :$port did not become ready again" >&2; exit 1; }
done
$KUBECTL -n "$NAMESPACE" rollout status deploy/kaimahi-proxy --timeout=120s >/dev/null
after=$(restarts)
echo "restart counts after: $after"
[ "$after" = "$before" ] || { echo "a proxy restarted around the outage: $before -> $after" >&2; exit 1; }
echo "store-outage: readiness dropped and returned on every replica; no proxy restarted"
