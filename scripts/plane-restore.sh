#!/usr/bin/env bash
# Restore a `make backup` file into the running plane's database,
# REPLACING its contents: the dump drops and recreates every table.
#
# psql runs INSIDE the Postgres pod over its unix socket and reads the
# file from stdin through kubectl exec -i — the same custody as the
# backup (no password leaves the pod, nothing lands on disk in the
# cluster, no local client). ON_ERROR_STOP makes a partial restore a
# failure rather than a silently half-restored ledger; the whole file
# runs in one transaction so a failure leaves the database as it was.
#
# Traffic is QUIESCED for the restore: the proxies are scaled to zero
# (their in-flight calls drain first), the tables are replaced, and the
# proxies are scaled back. A proxy admitting calls during a --clean
# restore could write ledger rows the restore then discards, or decide
# a budget against a half-loaded ledger — a restore is a short outage,
# never a concurrent write. A fresh cluster's migrations already created
# the schema and the dump carries goose's version table, so nothing is
# re-migrated; the restored token hashes mean the agent-side Secrets
# from the backed-up cluster work again.
#
# Usage: KUBECTL="kubectl --context ..." FILE=path plane-restore.sh
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
FILE="${FILE:?FILE=<backup.sql> is required}"
test -s "$FILE" || { echo "plane-restore: $FILE is missing or empty" >&2; exit 1; }
grep -q '^-- PostgreSQL database dump complete' "$FILE" || {
  echo "plane-restore: $FILE is not a complete pg_dump (no trailer); refusing" >&2
  exit 1
}

# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
replicas=$($KUBECTL -n "$NAMESPACE" get deploy kaimahi-proxy -o jsonpath='{.spec.replicas}')
case "$replicas" in ''|*[!0-9]*) echo "plane-restore: cannot read the proxy's replica count: '$replicas'" >&2; exit 1 ;; esac
# Whatever happens below, the proxies come back.
trap '$KUBECTL -n "$NAMESPACE" scale deploy/kaimahi-proxy --replicas="$replicas" >/dev/null 2>&1 || true' EXIT
echo "plane-restore: quiescing the plane (scaling kaimahi-proxy $replicas -> 0; in-flight calls drain)" >&2
$KUBECTL -n "$NAMESPACE" scale deploy/kaimahi-proxy --replicas=0 >/dev/null
$KUBECTL -n "$NAMESPACE" wait --for=delete pod -l app=kaimahi-proxy --timeout=120s >/dev/null

$KUBECTL -n "$NAMESPACE" exec -i deploy/kaimahi-postgres -- \
  psql -U kaimahi -d kaimahi -q -v ON_ERROR_STOP=1 --single-transaction < "$FILE"
# Well-formed positive: the ledger table answers a count after the restore.
n=$($KUBECTL -n "$NAMESPACE" exec deploy/kaimahi-postgres -- \
  psql -U kaimahi -d kaimahi -tAc 'SELECT count(*) FROM ledger_entry')
case "$n" in
  ''|*[!0-9]*) echo "plane-restore: ledger unreadable after restore: '$n'" >&2; exit 1 ;;
esac
echo "plane-restore: restored $FILE; ledger_entry has $n rows; scaling kaimahi-proxy back to $replicas" >&2
$KUBECTL -n "$NAMESPACE" scale deploy/kaimahi-proxy --replicas="$replicas" >/dev/null
trap - EXIT
$KUBECTL -n "$NAMESPACE" rollout status deploy/kaimahi-proxy --timeout=300s >/dev/null
echo "plane-restore: restored $FILE; ledger_entry has $n rows; plane serving again"
