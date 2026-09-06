#!/usr/bin/env bash
# Prove the MCP gateway ADMITTED a tools/call — the enforcement decision
# a bounded grant buys — in the case where the upstream tool server is
# NOT deployed. The gateway decides before it forwards, so the admit is
# fully exercised while the call itself goes nowhere.
#
# This exists so keyless CI can assert the whole approval cycle WITHOUT a
# Slack token: an admitted call to an absent upstream answers 502, and
# the audit row reads "allowed 502 granted <id>". A 200 would mean the
# call reached a tool server, which CI must never do for Slack — so a
# 200 FAILS this probe. The counterpart for a reachable upstream is
# tool-call-probe.sh.
#
# Fail closed on the status alone is NOT enough: the gateway also answers
# 503 from four PRE-forward DENIAL paths (credential store unavailable,
# audit breaker tripped, allowlist unreadable, grant check failed). Those
# are refusals, not admissions — a Postgres blip must never read as
# "admitted". So 503 is accepted only with the one body that means the
# decision was made and the forward then failed on the UPSTREAM's own
# credential; every other 503 is a denial and fails this probe.
#
# Custody rules (docs/COORDINATION.md): the governed token travels only
# through pipes and 0600 files — never argv, env listings, or logs.
#
# Usage: UPSTREAM=slack GOVERNED_SECRET=kaimahi-slack-token \
#          tool-admit-probe.sh <tool-name> [json-arguments]
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-slack-token}"
GATEWAY_PORT="${GATEWAY_PORT:-18083}"
UPSTREAM="${UPSTREAM:-slack}"

tool="${1:?usage: tool-admit-probe.sh <tool-name> [json-arguments]}"
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
test -s "$workdir/token" || { echo "$GOVERNED_SECRET missing/empty" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

# --address pins IPv4 explicitly (see plane-admin.sh for why).
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-mcp-gateway "$GATEWAY_PORT:8081" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS -o /dev/null "http://127.0.0.1:$GATEWAY_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS -o /dev/null "http://127.0.0.1:$GATEWAY_PORT/healthz" \
  || { echo "gateway port-forward failed" >&2; exit 1; }

printf '{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "%s", "arguments": %s}}\n' \
  "$tool" "$args" > "$workdir/req"
status=$(curl -sS -X POST -H @"$workdir/auth-header" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  --data @"$workdir/req" -o "$workdir/resp" -w '%{http_code}' \
  "http://127.0.0.1:$GATEWAY_PORT/upstream/$UPSTREAM/mcp")

# The only 503 that means "admitted, then the forward failed": the
# gateway could not read the UPSTREAM's own credential. Every other 503
# is a pre-forward denial.
admitted_503='tool upstream credential unavailable'

case "$status" in
  502)
    # Admitted by policy; the gateway dialed the upstream and could not
    # reach it. The ALLOWLIST/GRANT decision was made and audited.
    echo "gateway ADMITTED $tool on upstream '$UPSTREAM' (HTTP 502: dialed, unreachable)"
    echo "the upstream was not reached — nothing was sent. Check the audit trail for the granted row."
    ;;
  503)
    body=$(tr -d '\n' < "$workdir/resp")
    if [ "$body" != "$admitted_503" ]; then
      # A denial wearing a 503: the credential store, the audit breaker,
      # the allowlist read or the grant check failed. Nothing was
      # admitted and no use was consumed — fail closed.
      echo "NOT admitted: the gateway REFUSED $tool before forwarding (HTTP 503: $body)" >&2
      exit 1
    fi
    echo "gateway ADMITTED $tool on upstream '$UPSTREAM' (HTTP 503: $body)"
    echo "the upstream was not reached — nothing was sent. Check the audit trail for the granted row."
    ;;
  200)
    # A JSON-RPC denial also rides a 200; distinguish it from a real
    # tool result, and fail on both — a result means CI touched a live
    # tool server.
    if grep -q '"error"' "$workdir/resp"; then
      echo "NOT admitted: the gateway denied $tool" >&2
      cat "$workdir/resp" >&2
      exit 1
    fi
    echo "the call REACHED a tool server and returned a result — this probe asserts the" >&2
    echo "admit decision against an ABSENT upstream; use tool-call-probe.sh instead." >&2
    exit 1
    ;;
  *)
    echo "unexpected HTTP $status:" >&2; cat "$workdir/resp" >&2; exit 1 ;;
esac
