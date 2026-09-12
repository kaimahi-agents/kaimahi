#!/usr/bin/env bash
# Prove that ONLY the plane's proxy can reach an onboarded model endpoint.
# Without the scaffolded NetworkPolicy pair, a pod could bypass model
# budgets and metering by calling the endpoint directly.
#
# It asserts a NEGATIVE against a CONTROL, because a "blocked" result
# means nothing on its own: a policy the CNI ignores, a dead target, a
# broken probe pod and a typo'd address all look the same from here.
#
#   from a pod that is NOT the proxy:   TARGET   must be BLOCKED
#                                       CONTROL  must SUCCEED
#
# The control is a service the same pod may legitimately reach, so a
# successful control proves this pod has working DNS and networking, that
# `nc` is behaving, and that the block is therefore policy. That last one
# matters: busybox does not advertise `nc -z` in its usage text (it works
# — measured on busybox 1.36.1, exit 0 to an open port and 1 to a closed
# one) and if a future image dropped it, EVERY probe would report
# "blocked". The control is what makes that a loud failure rather than a
# false pass. The other direction — the proxy CAN reach
# the endpoint — is proven by the governed model call itself
# (scripts/model-seam-probe.sh), which is a stronger positive than
# anything this script could open.
#
# Env:
#   KUBECTL      kubectl invocation incl. --context
#   TARGET       host:port that must be unreachable (default acme-model.acme-model:8000)
#   CONTROL      host:port that must be reachable  (default kagent-tools.kagent:8084)
#   PROBE_NS     where the probe pod runs (default kagent — where agents live)
#   PROBE_IMAGE  default busybox:1.36
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
TARGET="${TARGET:-acme-model.acme-model:8000}"
CONTROL="${CONTROL:-kagent-tools.kagent:8084}"
PROBE_NS="${PROBE_NS:-kagent}"
PROBE_IMAGE="${PROBE_IMAGE:-busybox:1.36}"
TIMEOUT="${TIMEOUT:-8}"

# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$PROBE_NS" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0")"

pod="upstream-boundary-$(od -An -N3 -tx1 /dev/urandom | tr -d ' ')"
cleanup() { $KUBECTL -n "$PROBE_NS" delete pod --ignore-not-found --wait=false "$pod" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# BestEffort (no requests) so it schedules on a full CI node, non-root,
# and gone on exit.
$KUBECTL -n "$PROBE_NS" run "$pod" --image="$PROBE_IMAGE" --restart=Never \
  --overrides='{"spec":{"securityContext":{"runAsNonRoot":true,"runAsUser":65534}}}' \
  --command -- sleep 300 >/dev/null
$KUBECTL -n "$PROBE_NS" wait --for=condition=Ready "pod/$pod" --timeout=180s >/dev/null

reach() { # host:port -> 0 if a TCP connection completes
  $KUBECTL -n "$PROBE_NS" exec "$pod" -- \
    sh -c "nc -w $TIMEOUT -z ${1%:*} ${1##*:}" >/dev/null 2>&1
}

if ! reach "$CONTROL"; then
  echo "the CONTROL $CONTROL is unreachable from $PROBE_NS/$pod — this probe cannot prove anything." >&2
  echo "Fix the control before reading the target result: a probe pod with no working network" >&2
  echo "would report the target 'blocked' whatever the NetworkPolicy said." >&2
  exit 1
fi
echo "control ok: $PROBE_NS/$pod reaches $CONTROL"

if reach "$TARGET"; then
  echo "BOUNDARY FAILED: $PROBE_NS/$pod reached $TARGET directly." >&2
  echo "A pod that is not the proxy can bypass model budgets and metering." >&2
  exit 1
fi
echo "boundary holds: $PROBE_NS/$pod cannot reach $TARGET; only the proxy may."
