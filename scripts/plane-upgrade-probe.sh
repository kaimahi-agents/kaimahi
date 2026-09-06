#!/usr/bin/env bash
# Prove that upgrading the governance plane across a real schema gap
# keeps the data, and that a half-applied migration cannot serve traffic.
#
# The plane has run goose migrations from the beginning and had never once
# been tested going from an older version to a newer one. Every e2e shard starts
# from an empty database, so "the migrations apply" had only ever been
# proven on an empty database — which is the case where they cannot lose
# anything.
#
# What this probe does instead:
#
#   1. installs an OLD plane straight from the Go module proxy
#      (`go install .../plane/cmd/kaimahi-proxy@<rev>` — the same
#      clone-free path `kmx plane` uses, so the old version needs no
#      checkout and no image),
#   2. puts real governance state in it through its OWN admin API: a
#      credential, a budget, a tool allowlist, a live grant a human
#      approved, and a ledger row from a metered call it actually
#      forwarded,
#   3. stops it, starts the NEW plane built from this checkout against
#      the SAME database, and
#   4. asserts every one of those survived, the schema moved, and the new
#      plane serves — a fresh governed call lands in the same ledger,
#      after the rows the old version wrote.
#
# Then, on a second database seeded the same way, it proves the failure
# mode documented in docs/releases.md: when a migration cannot apply, the
# plane does NOT start and does not serve, and the data it could not
# migrate is untouched.
#
# No cluster and no container: the proxy is env-configured and file-fed,
# so both versions run as plain processes against a plain Postgres. That
# is what makes this affordable to run on every PR.
#
# Env:
#   PGHOST/PGPORT/PGUSER/PGDATABASE/PGPASSWORD  the server to work on; the
#                                               probe creates and drops its
#                                               OWN databases on it (PG*
#                                               defaults match the CI service)
#   OLD_REV      the revision to upgrade FROM (default below)
#   KEEP_WORKDIR set to keep the scratch directory for inspection
set -euo pipefail
umask 077

# The version we upgrade FROM: the last commit (#41) before the
# spend-reservation table landed. Everything since is crossed in one go — at
# the time of writing 00007 through 00010 — and those migrations alter
# tables that already hold the seeded rows, which is the case worth
# testing. The gap therefore WIDENS on its own as migrations land; the
# assertion below pins the floor (the old plane must stop at
# OLD_MIGRATIONS) so it can never quietly shrink to nothing. Bump it only
# when this floor stops being reachable, and never to the commit before
# HEAD, or this proves nothing.
OLD_REV="${OLD_REV:-109e08d6e0763c9111b6fe50f0fffebcc4fce412}"
OLD_MIGRATIONS=6

PGHOST="${PGHOST:-127.0.0.1}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-kaimahi}"
# The database PG* points at is used only to CREATE and DROP the two the
# probe actually works in. Owning them is what makes this rerunnable: an
# upgrade probe that assumed a pristine schema would pass once and then
# refuse ("OLD_REV is no longer a real gap") on the second run against the
# same Postgres, which is every local run after the first.
PGDATABASE="${PGDATABASE:-kaimahi}"
UPGRADE_DB="${UPGRADE_DB:-kaimahi_upgrade_probe}"
BROKEN_DB="${BROKEN_DB:-kaimahi_upgrade_broken}"
PGPASSWORD="${PGPASSWORD:-ci-throwaway}"
export PGHOST PGPORT PGUSER PGPASSWORD

here=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
stub_port=18179
data_port=18180
mcp_port=18181
inbound_port=18182
admin_port=19191
ops_port=19192

cleanup() {
  local rc=$?
  stop_proxy || true
  [ -n "${stub_pid:-}" ] && kill "$stub_pid" 2>/dev/null || true
  if [ -n "${KEEP_WORKDIR:-}" ]; then
    echo "workdir kept at $work" >&2
  else
    rm -rf "$work"
  fi
  return $rc
}
trap cleanup EXIT

say() { printf '\n== %s\n' "$*" >&2; }
fail() { printf 'plane-upgrade: %s\n' "$*" >&2; exit 1; }

psql_q() { # psql_q <database> <sql> -> one value
  psql --no-psqlrc -qtAX -d "$1" -c "$2"
}

# ---------------------------------------------------------------- fixtures

for db in "$UPGRADE_DB" "$BROKEN_DB"; do
  psql_q "$PGDATABASE" "drop database if exists $db" >/dev/null
  psql_q "$PGDATABASE" "create database $db" >/dev/null
done

mkdir -p "$work/secrets"
printf '%s' "$PGPASSWORD" > "$work/secrets/pgpassword"
admin_token="upgrade-probe-admin-token"
printf '%s' "$admin_token" > "$work/secrets/admin-token"

# One metered, PRICED upstream so a forwarded call produces a ledger row
# with a real cost, not just a token count — cost is the column an upgrade
# would be most embarrassing to lose. 127.0.0.1 is in-cluster-shaped as far
# as the config's egress rules are concerned (a loopback address is not a
# public one), so no `internet` marking is needed and none is implied.
cat > "$work/upstreams.json" <<EOF
{
  "upstreams": {
    "stub": {
      "base_url": "http://127.0.0.1:$stub_port",
      "path": "v1/chat/completions",
      "classification": "metered",
      "prices": {
        "upgrade-probe-model": {"in_cents_per_1m": 1000, "out_cents_per_1m": 2000}
      }
    }
  }
}
EOF

# The upstream itself: an OpenAI-shaped response carrying usage, which is
# the only part of an upstream the meter reads.
cat > "$work/stub.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Stub(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("content-length", 0))
        self.rfile.read(length)
        body = json.dumps({
            "id": "upgrade-probe",
            "object": "chat.completion",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1000, "completion_tokens": 500,
                      "total_tokens": 1500},
        }).encode()
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), Stub).serve_forever()
PY

python3 "$work/stub.py" "$stub_port" &
stub_pid=$!

# ------------------------------------------------------------ proxy control

proxy_pid=""
start_proxy() { # start_proxy <binary> <database> <logfile>
  PGDATABASE="$2" \
  DATA_ADDR="127.0.0.1:$data_port" \
  MCP_ADDR="127.0.0.1:$mcp_port" \
  INBOUND_ADDR="127.0.0.1:$inbound_port" \
  ADMIN_ADDR="127.0.0.1:$admin_port" \
  OPS_ADDR="127.0.0.1:$ops_port" \
  CONFIG_FILE="$work/upstreams.json" \
  ADMIN_TOKEN_FILE="$work/secrets/admin-token" \
  PGPASSWORD_FILE="$work/secrets/pgpassword" \
    "$1" > "$3" 2>&1 &
  proxy_pid=$!
}

stop_proxy() {
  [ -n "$proxy_pid" ] || return 0
  kill "$proxy_pid" 2>/dev/null || true
  wait "$proxy_pid" 2>/dev/null || true
  proxy_pid=""
}

wait_serving() { # wait_serving <logfile> — admin /healthz answers
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "http://127.0.0.1:$admin_port/healthz" 2>/dev/null; then
      return 0
    fi
    if ! kill -0 "$proxy_pid" 2>/dev/null; then
      echo "--- proxy log ---" >&2; cat "$1" >&2
      fail "the proxy exited before it served"
    fi
    sleep 1
  done
  echo "--- proxy log ---" >&2; cat "$1" >&2
  fail "the proxy never served on the admin port"
}

admin() { # admin <method> <path> [body]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" -H "Authorization: Bearer $admin_token" \
      -H 'Content-Type: application/json' -d "$body" \
      "http://127.0.0.1:$admin_port$path"
  else
    curl -fsS -X "$method" -H "Authorization: Bearer $admin_token" \
      "http://127.0.0.1:$admin_port$path"
  fi
}

governed_call() { # governed_call <token> — one metered call through the plane
  curl -fsS -X POST -H "Authorization: Bearer $1" \
    -H 'Content-Type: application/json' \
    -d '{"model":"upgrade-probe-model","messages":[{"role":"user","content":"hi"}]}' \
    "http://127.0.0.1:$data_port/upstream/stub/v1/chat/completions"
}

seed() { # seed <database> — the state an upgrade must not lose
  local db="$1"
  token=$(admin POST /admin/credentials '{"name":"upgrade-probe"}' |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')
  [ -n "$token" ] || fail "no credential token"
  admin PUT /admin/budgets '{"credential":"upgrade-probe","cap_cents":5000,"cap_tokens":null}'
  admin PUT /admin/tool-allowlist '{"credential":"upgrade-probe","tools":["k8s_get_resources","k8s_get_pod_logs"]}'
  # A human-approved, bounded grant: the row whose meaning the argument-
  # binding migration changes, and therefore the one worth carrying across.
  admin POST /admin/requests '{"credential":"upgrade-probe","kind":"tool","subject":"k8s_get_resources"}' >/dev/null
  request_id=$(admin GET /admin/approvals |
    python3 -c 'import json,sys; p=json.load(sys.stdin)["pending"]; print(p[0]["id"] if p else "")')
  [ -n "$request_id" ] || fail "the seeded approval request is not pending"
  admin POST "/admin/approvals/$request_id/approve" '{"ttl_seconds":86400,"max_uses":5}' >/dev/null
  # A real metered call: the ledger row, with a cost.
  governed_call "$token" >/dev/null
  seeded_cents=$(psql_q "$db" "select coalesce(sum(cost_cents),0) from ledger_entry")
  seeded_rows=$(psql_q "$db" "select count(*) from ledger_entry")
  [ "$seeded_rows" -ge 1 ] || fail "the seeded call left no ledger row"
  [ "$seeded_cents" -gt 0 ] || fail "the seeded call recorded no cost"
  echo "seeded: $seeded_rows ledger row(s), ${seeded_cents}c, a grant, an allowlist, a budget" >&2
}

# ------------------------------------------------------- 1: the old version

say "installing the OLD plane at $OLD_REV from the module proxy (no clone)"
GOBIN="$work/oldbin" GOFLAGS=-mod=mod \
  go install "github.com/kaimahi-agents/kaimahi/plane/cmd/kaimahi-proxy@$OLD_REV"
old_bin="$work/oldbin/kaimahi-proxy"
test -x "$old_bin" || fail "the old proxy did not install"

say "starting the OLD plane against $UPGRADE_DB"
start_proxy "$old_bin" "$UPGRADE_DB" "$work/old.log"
wait_serving "$work/old.log"

applied=$(psql_q "$UPGRADE_DB" "select max(version_id) from goose_db_version")
[ "$applied" = "$OLD_MIGRATIONS" ] ||
  fail "expected the old plane to stop at migration $OLD_MIGRATIONS, found $applied — OLD_REV is no longer a real gap"
echo "old schema: migration $applied" >&2

say "seeding governance state through the OLD plane's own admin API"
seed "$UPGRADE_DB"

stop_proxy

# ------------------------------------------------------- 2: the new version

say "building the NEW plane from this checkout"
(cd "$here/plane" && go build -o "$work/kaimahi-proxy" ./cmd/kaimahi-proxy)

say "starting the NEW plane on the SAME database"
start_proxy "$work/kaimahi-proxy" "$UPGRADE_DB" "$work/new.log"
wait_serving "$work/new.log"

now=$(psql_q "$UPGRADE_DB" "select max(version_id) from goose_db_version")
[ "$now" -gt "$applied" ] || fail "the new plane applied nothing: still at migration $now"
grep -q 'migrations: applied' "$work/new.log" ||
  fail "the new plane did not log the migrations it applied"
echo "new schema: migration $now (was $applied)" >&2

say "the data the old version wrote is intact"
after_rows=$(psql_q "$UPGRADE_DB" "select count(*) from ledger_entry")
after_cents=$(psql_q "$UPGRADE_DB" "select coalesce(sum(cost_cents),0) from ledger_entry")
[ "$after_rows" = "$seeded_rows" ] || fail "ledger rows changed: $seeded_rows -> $after_rows"
[ "$after_cents" = "$seeded_cents" ] || fail "ledger cost changed: $seeded_cents -> $after_cents"

admin GET '/admin/tool-allowlist?credential=upgrade-probe' > "$work/allowlist.json"
python3 - "$work/allowlist.json" <<'PY'
import json, sys
tools = json.load(open(sys.argv[1]))["tools"]
assert sorted(tools) == ["k8s_get_pod_logs", "k8s_get_resources"], tools
print("allowlist intact:", tools)
PY

# The grant a human approved before argument binding existed: still there,
# still live, and still bounded by what the approver said. Its arg_digest is
# NULL — the closed legacy class 00008 documents — so it keeps its old
# verb-level meaning rather than being silently widened or silently voided.
admin GET '/admin/grants?credential=upgrade-probe' > "$work/grants.json"
python3 - "$work/grants.json" <<'PY'
import json, sys
grants = json.load(open(sys.argv[1]))["grants"]
live = [g for g in grants if g.get("live")]
assert len(live) == 1, grants
g = live[0]
assert g["subject"] == "k8s_get_resources", g
assert g["max_uses"] == 5 and g["uses"] == 0, g
print("grant intact and live:", g["subject"], "uses", g["uses"], "of", g["max_uses"])
PY
legacy=$(psql_q "$UPGRADE_DB" "select count(*) from permit_grant where arg_digest is null")
[ "$legacy" = "1" ] ||
  fail "expected the pre-upgrade grant to keep a NULL arg_digest (the closed legacy class), found $legacy"

# The same rule, one migration later: a credential issued before expiry
# existed keeps a NULL expiry and KEEPS WORKING. Expiring a running estate at
# migration time would be an outage, not a control — so the closed legacy
# class is asserted here rather than trusted, on both halves: the column is
# still NULL, and the governed call below still authenticates with that
# token.
legacy_cred=$(psql_q "$UPGRADE_DB" "select count(*) from credential where name = 'upgrade-probe' and expires_at is null")
[ "$legacy_cred" = "1" ] ||
  fail "expected the pre-upgrade credential to keep a NULL expiry (the closed legacy class), found $legacy_cred"

budget=$(admin GET '/admin/ledger?credential=upgrade-probe' |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["month_cents"])')
[ "$budget" = "$seeded_cents" ] || fail "month-to-date changed: $seeded_cents -> $budget"

say "the upgraded plane serves: a fresh governed call, on the migrated schema, with the legacy credential"
governed_call "$token" >/dev/null
final_rows=$(psql_q "$UPGRADE_DB" "select count(*) from ledger_entry")
[ "$final_rows" -gt "$after_rows" ] || fail "the new plane recorded nothing: $after_rows -> $final_rows"
echo "served: ledger $after_rows -> $final_rows rows" >&2

stop_proxy

# ------------------------------- 3: a migration that cannot apply, halfway

say "a migration that fails halfway: the plane must not start, and must not lose data"
broken_db="$BROKEN_DB"

start_proxy "$old_bin" "$broken_db" "$work/old-broken.log"
wait_serving "$work/old-broken.log"
seed "$broken_db"
broken_rows="$seeded_rows"
broken_cents="$seeded_cents"
stop_proxy

# Make migration 00007 impossible without touching the migration: the name
# it creates is already taken, by something else. This is exactly the shape
# of a real halfway failure — an object that conflicts with a migration the
# schema has not applied yet.
psql_q "$broken_db" "create table spend_reservation (squatter text)" >/dev/null

start_proxy "$work/kaimahi-proxy" "$broken_db" "$work/new-broken.log"
# The proxy retries a failing startup for 90 seconds before exiting (main.go
# retryConnect), so this wait is deliberately longer than that budget.
exited=false
for _ in $(seq 1 150); do
  if ! kill -0 "$proxy_pid" 2>/dev/null; then exited=true; break; fi
  if curl -fsS -o /dev/null "http://127.0.0.1:$admin_port/healthz" 2>/dev/null; then
    fail "the plane SERVED on a schema it could not migrate"
  fi
  sleep 1
done
wait "$proxy_pid" 2>/dev/null && rc=0 || rc=$?
proxy_pid=""
[ "$exited" = true ] || fail "the plane neither served nor exited on a failed migration"
[ "$rc" -ne 0 ] || fail "the plane exited 0 after a failed migration"
grep -qi 'database startup failed' "$work/new-broken.log" ||
  fail "the plane did not say why it would not start"
echo "refused to start, exit $rc, after a migration it could not apply" >&2

stuck=$(psql_q "$broken_db" "select max(version_id) from goose_db_version")
[ "$stuck" = "$OLD_MIGRATIONS" ] ||
  fail "expected the schema to stay at $OLD_MIGRATIONS after the failure, found $stuck"
still_rows=$(psql_q "$broken_db" "select count(*) from ledger_entry")
still_cents=$(psql_q "$broken_db" "select coalesce(sum(cost_cents),0) from ledger_entry")
[ "$still_rows" = "$broken_rows" ] && [ "$still_cents" = "$broken_cents" ] ||
  fail "data changed under a failed migration: $broken_rows/$broken_cents -> $still_rows/$still_cents"
echo "schema still at $stuck, $still_rows ledger row(s) and ${still_cents}c untouched" >&2

for db in "$UPGRADE_DB" "$BROKEN_DB"; do
  psql_q "$PGDATABASE" "drop database if exists $db" >/dev/null
done

say "plane upgrade $OLD_REV -> this checkout: data intact, plane serving, failure fails closed"
