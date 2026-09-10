#!/usr/bin/env bash
# Prove the plane survives losing a replica mid-cycle: with two
# replicas Running, start a governed chat on replica A, delete A while
# that call is in flight, send the next call to the survivor B, and
# require BOTH to be answered 200 — the in-flight one because the
# process drains before it exits, the next because B needs nothing from
# A — and the ledger to gain exactly two rows (no lost record). Then
# wait for the Deployment to be back at full strength.
#
# Custody rules (docs/COORDINATION.md): the token travels only through
# pipes and 0600 files (curl -H @file) — never argv, env listings, logs.
#
# Usage: replica-kill-probe.sh   (env: GOVERNED_SECRET=kaimahi-governed-token
#        SECRET_NAMESPACE=kagent UPSTREAM=ollama MODEL=qwen2.5:3b CRED=hello-world)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
SECRET_NAMESPACE="${SECRET_NAMESPACE:-kagent}"
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-governed-token}"
UPSTREAM="${UPSTREAM:-ollama}"
MODEL="${MODEL:-qwen2.5:3b}"
CRED="${CRED:-hello-world}"
PORT_A="${PORT_A:-18380}"
PORT_B="${PORT_B:-18381}"

# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $SECRET_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") (deletes one kaimahi-proxy pod)"

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

ledger_rows() { # the credential's allowed rows, counted in Postgres (not through a newest-N view)
  local n
  n=$($KUBECTL -n "$NAMESPACE" exec deploy/kaimahi-postgres -- psql -U kaimahi -d kaimahi -tAc \
    "SELECT count(*) FROM ledger_entry WHERE credential_name = '$CRED' AND upstream = '$UPSTREAM' AND status = 200 AND cost_source <> 'denied'")
  case "$n" in ''|*[!0-9]*) echo "ledger count unreadable: '$n'" >&2; return 1 ;; esac
  echo "$n"
}

mapfile -t pods < <(KUBECTL="$KUBECTL" bash "$(dirname "$0")/plane-pods.sh")
[ "${#pods[@]}" -eq 2 ] || { echo "expected exactly 2 running kaimahi-proxy pods, found ${#pods[@]}" >&2; exit 1; }
a="${pods[0]}"; b="${pods[1]}"
for pair in "$a:$PORT_A" "$b:$PORT_B"; do
  pod="${pair%%:*}"; port="${pair##*:}"
  $KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 "pod/$pod" "$port:8080" >/dev/null 2>&1 &
  pf_pids+=("$!")
  for _ in $(seq 1 150); do
    curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$port/healthz" 2>/dev/null && break
    sleep 0.2
  done
  curl -fsS --cacert "$workdir/plane-ca.crt" -o /dev/null "https://127.0.0.1:$port/healthz" || { echo "port-forward to $pod failed" >&2; exit 1; }
done

before=$(ledger_rows)
printf '{"model": "%s", "messages": [{"role": "user", "content": "Reply with the single word OK."}], "max_tokens": 8}\n' \
  "$MODEL" > "$workdir/body"
chat() { # port -> status
  curl -sS --cacert "$workdir/plane-ca.crt" -o "$workdir/resp-$1" -w '%{http_code}' -X POST -H @"$workdir/auth-header" \
    -H 'Content-Type: application/json' --data @"$workdir/body" \
    "https://127.0.0.1:$1/upstream/$UPSTREAM/v1/chat/completions" 2>/dev/null || echo 000
}

# 1. A call in flight on A...
chat "$PORT_A" > "$workdir/status-a" &
inflight=$!
sleep 0.2
# Was the call still open when A went? curl writes the status only on
# completion, so an empty file means yes. A host fast enough to finish
# the generation first has not exercised the drain — say so rather
# than claim it (the survivor and ledger assertions still hold either
# way; CI's 2-CPU runner is slow enough that the drain is exercised).
drained=1
if [ -s "$workdir/status-a" ]; then
  drained=0
  echo "NOTE: the call on $a completed before the delete ($(cat "$workdir/status-a")); the drain is not exercised on this host"
fi
# 2. ...A is deleted (a direct delete: no eviction, no PDB — the harsher case)...
echo "deleting replica $a"
$KUBECTL -n "$NAMESPACE" delete pod "$a" --wait=false >/dev/null
# 3. ...and the next call goes to the survivor.
st_b=$(chat "$PORT_B")
wait "$inflight" || true
st_a=$(cat "$workdir/status-a")
echo "call on the deleted replica: $st_a (drain exercised: $drained); next call on the survivor: $st_b"
[ "$st_b" = 200 ] || { echo "survivor answered $st_b: $(head -c 300 "$workdir/resp-$PORT_B")" >&2; exit 1; }
[ "$st_a" = 200 ] || { echo "the in-flight call was not drained (got $st_a)" >&2; exit 1; }

# The ledger gained exactly the two rows — nothing lost with the replica.
after=$(ledger_rows)
echo "ledger rows for $CRED: $before -> $after"
[ "$after" -eq $((before + 2)) ] || { echo "expected exactly 2 new ledger rows" >&2; exit 1; }

# Back to full strength.
$KUBECTL -n "$NAMESPACE" rollout status deploy/kaimahi-proxy --timeout=180s >/dev/null
ready=$($KUBECTL -n "$NAMESPACE" get deploy kaimahi-proxy -o jsonpath='{.status.readyReplicas}')
[ "$ready" = 2 ] || { echo "deployment not back at 2 ready replicas ($ready)" >&2; exit 1; }
if [ "$drained" = 1 ]; then
  echo "replica-kill: survivor served, in-flight call drained, ledger complete, 2/2 ready again"
else
  echo "replica-kill: survivor served, ledger complete, 2/2 ready again (drain not exercised on this host)"
fi
