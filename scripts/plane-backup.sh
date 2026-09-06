#!/usr/bin/env bash
# Back up the plane's Postgres database to a local file.
#
# pg_dump runs INSIDE the Postgres pod, over its unix socket, and streams
# to stdout through kubectl exec into a local file: the database
# password never leaves the pod (the socket authenticates the postgres
# OS user), nothing is written to disk in the cluster, and no local
# Postgres client is needed. --clean --if-exists makes the file a
# complete replacement (every table dropped and recreated), which is
# what `make restore` needs on a fresh cluster whose migrations already
# created the schema.
#
# What the file holds: credential NAMES and token HASHES (never a
# token), caps, the ledger, tool/inbound/approval audit trails (which
# carry whatever ids those trails record — a Slack user id on a
# decision, a delivery id), grants and open spend holds. Keep it as you
# would the database.
#
# Usage: KUBECTL="kubectl --context ..." [FILE=path] plane-backup.sh
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
FILE="${FILE:-}"
if [ -z "$FILE" ]; then
  mkdir -p backups
  FILE="backups/kaimahi-$(date -u +%Y%m%dT%H%M%SZ).sql"
fi

# Fail closed: a dump that stops half way must not look like a backup.
# pg_dump writes its trailer last, so its presence is the well-formed
# positive; the file is written to a temp name and renamed only then.
tmp="$FILE.partial"
trap 'rm -f "$tmp"' EXIT
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
$KUBECTL -n "$NAMESPACE" exec deploy/kaimahi-postgres -- \
  pg_dump -U kaimahi -d kaimahi --clean --if-exists --no-owner --no-privileges > "$tmp"
grep -q '^-- PostgreSQL database dump complete' "$tmp" || {
  echo "plane-backup: dump did not complete (no trailer); nothing written to $FILE" >&2
  exit 1
}
mv "$tmp" "$FILE"
rows=$(grep -c '^COPY public\.' "$FILE" || [ $? = 1 ])
echo "plane-backup: wrote $FILE ($(wc -c < "$FILE") bytes, $rows tables)"
