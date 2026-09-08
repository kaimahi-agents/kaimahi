#!/usr/bin/env bash
# CLI glue for the Kaimahi proxy's admin plane (issue governed
# credentials, set budgets, read the ledger). The admin port is not on
# any Service: this script port-forwards to the pod, so cluster
# credentials gate every operation before the admin bearer token does.
#
# Custody rules (docs/COORDINATION.md security guidance):
#   - The admin token and any issued governed token travel only through
#     pipes and 0600 files (curl reads the auth header from a file;
#     kubectl reads the secret value with --from-file, and the issued
#     token's dry-run manifest exists only inside the apply pipe) —
#     never argv, env listings, on-disk YAML, or logs.
#   - Fail closed: every step checks a well-formed positive; no -L on
#     any authenticated call.
#
# Usage:
#   plane-admin.sh issue <name>            mint a governed credential and
#                                          store it as the agent-side
#                                          Secret $GOVERNED_SECRET
#                                          (default kaimahi-governed-token;
#                                          the tools credential uses
#                                          GOVERNED_SECRET=kaimahi-tools-token;
#                                          GOVERNED_SECRET=- DISCARDS the
#                                          token — signed inbound hooks
#                                          need the identity, never a
#                                          bearer;
#                                          SECRET_NAMESPACE overrides the
#                                          kagent default)
#                                          (an optional third argument, or
#                                          TTL=, sets the credential's
#                                          lifetime — the plane defaults one;
#                                          there is no "never")
#   plane-admin.sh credentials             list credentials and when each expires
#   plane-admin.sh renew <name> [ttl]      extend a credential's expiry; the
#                                          token does not move, so no Secret
#                                          has to be rewritten
#   plane-admin.sh budget <name> <cents|-> <tokens|->   set caps (- = none)
#   plane-admin.sh ledger [name]           show ledger (+ month totals)
#   plane-admin.sh tool-allow <name> <tool,tool|->      replace tool allowlist
#                                          (- = empty: nothing callable)
#   plane-admin.sh tool-allowlist <name>   show the tool allowlist
#   plane-admin.sh tool-audit [name]       show the tool-call audit trail
#   plane-admin.sh approvals               list pending approval requests
#   plane-admin.sh approve <id> <ttl|-> <uses|-> <amount|->
#                                          approve with bounds (>=1 of
#                                          ttl/uses required; ttl takes
#                                          s/m/h/d suffixes; amount only
#                                          for budget requests)
#   plane-admin.sh deny <id>               deny a pending request
#   plane-admin.sh request <name> <tool|budget|inbound> <subject> [args-json]
#                                          file a request explicitly
#                                          (inbound subject: hook name;
#                                          args-json, tool requests only,
#                                          names the CALL to pre-approve —
#                                          omitted means the argument-less
#                                          call, never "any call")
#   plane-admin.sh grants [name]           list grants (with liveness)
#   plane-admin.sh approval-audit [name]   show the approvals audit trail
#   plane-admin.sh inbound-audit [hook]    show the inbound event trail
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
SECRET_NAMESPACE="${SECRET_NAMESPACE:-$AGENT_NAMESPACE}"
GOVERNED_SECRET="${GOVERNED_SECRET:-kaimahi-governed-token}"
ADMIN_PORT="${ADMIN_PORT:-19091}"

workdir=$(mktemp -d)
pf_pid=""
cleanup() {
  [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT

# Admin auth header into a 0600 file; curl reads it with -H @file so the
# token never appears in argv or the environment.
$KUBECTL -n "$NAMESPACE" get secret kaimahi-admin -o jsonpath='{.data.token}' \
  | base64 -d > "$workdir/token"
test -s "$workdir/token" || { echo "kaimahi-admin secret missing/empty (run make plane)" >&2; exit 1; }
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

# --address pins IPv4 explicitly: if the port is already taken, kubectl
# must fail (and this script with it) rather than bind only the v6 side
# while curl talks to whatever squats on 127.0.0.1.
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  deploy/kaimahi-proxy "$ADMIN_PORT:9091" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS -o /dev/null "http://127.0.0.1:$ADMIN_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS -o /dev/null "http://127.0.0.1:$ADMIN_PORT/healthz" \
  || { echo "admin port-forward failed" >&2; exit 1; }

admin_curl() { # method path [json-body-file] -> body on stdout, status in $status
  local method=$1 path=$2 body="${3:-}"
  local args=(-sS -X "$method" -H @"$workdir/auth-header" \
    -o "$workdir/resp" -w '%{http_code}' "http://127.0.0.1:$ADMIN_PORT$path")
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data @"$body")
  status=$(curl "${args[@]}")
}

json_get() { # file key -> stdout (empty if missing)
  python3 -c 'import json,sys
d = json.load(open(sys.argv[1]))
v = d.get(sys.argv[2], "")
sys.stdout.write(v if isinstance(v, str) else "")' "$1" "$2"
}

# Credential names and caps are interpolated into JSON/query strings;
# validate their shape here (the server validates again).
check_name() {
  case "$1" in
    (*[!a-z0-9-]*|'') echo "invalid credential name '$1' (want [a-z0-9-]+)" >&2; exit 2 ;;
  esac
}
check_cap() {
  case "$1" in
    (null) ;;
    (*[!0-9]*|'') echo "invalid cap '$1' (want a non-negative integer or -)" >&2; exit 2 ;;
  esac
}
# ttl_seconds parses a duration the way `approve` always has (90, 90s,
# 5m, 2h, 1d) and leaves the answer in $secs. Factored out because a
# credential's lifetime and a grant's are typed the same way, and two
# copies of a parser are two parsers to disagree.
ttl_seconds() {
  case "$1" in
    (*[!0-9smhd]*|'') echo "invalid TTL '$1' (want e.g. 90, 90s, 5m, 2h, 1d)" >&2; exit 2 ;;
  esac
  local n unit
  n="${1%[smhd]}"; unit="${1#"$n"}"
  case "$n" in
    (*[!0-9]*|'') echo "invalid TTL '$1' (want e.g. 90, 90s, 5m, 2h, 1d)" >&2; exit 2 ;;
  esac
  case "$unit" in
    (s|'') secs=$n ;;
    (m) secs=$((n * 60)) ;;
    (h) secs=$((n * 3600)) ;;
    (d) secs=$((n * 86400)) ;;
  esac
}
check_uuid() {
  if ! [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; then
    echo "invalid request id '$1' (want a UUID from 'make approvals')" >&2
    exit 2
  fi
}

cmd="${1:-}"
case "$cmd" in
  issue)
    name="${2:?usage: plane-admin.sh issue <name> [ttl]}"
    check_name "$name"
    ttl="${3:-${TTL:--}}"
    if [ "$ttl" = - ]; then
      printf '{"name": "%s"}\n' "$name" > "$workdir/req"
    else
      ttl_seconds "$ttl"
      printf '{"name": "%s", "ttl_seconds": %s}\n' "$name" "$secs" > "$workdir/req"
    fi
    admin_curl POST /admin/credentials "$workdir/req"
    if [ "$status" = 409 ] && [ "$GOVERNED_SECRET" = - ]; then
      echo "Credential '$name' already issued; keeping it (no token is stored for it)." >&2
      exit 0
    fi
    if [ "$status" = 409 ]; then
      bound=$($KUBECTL -n "$SECRET_NAMESPACE" get secret "$GOVERNED_SECRET" \
        -o jsonpath='{.metadata.annotations.kaimahi\.dev/credential}' 2>/dev/null || true)
      if [ "$bound" = "$name" ]; then
        echo "Credential '$name' already issued and $GOVERNED_SECRET is bound to it; keeping both." >&2
        exit 0
      fi
      if [ -n "$bound" ]; then
        echo "Secret $GOVERNED_SECRET holds the token for credential '$bound', not '$name' — refusing." >&2
        exit 1
      fi
      echo "Credential '$name' exists in the plane but Secret $GOVERNED_SECRET is missing (or unlabeled)." >&2
      echo "The token is shown exactly once at issue time and cannot be recovered;" >&2
      echo "delete the row (kubectl -n $NAMESPACE exec deploy/kaimahi-postgres -- \\" >&2
      echo "  psql -U kaimahi -c \"DELETE FROM credential WHERE name='$name'\") and re-run." >&2
      exit 1
    fi
    [ "$status" = 201 ] || { echo "issue failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    if [ "$GOVERNED_SECRET" = - ]; then
      # For a signed inbound hook the credential is an IDENTITY (grants,
      # audit); the caller proves it with the hook's signing secret, so
      # the bearer token is discarded here — it exists in $workdir only until
      # the trap removes it, and nowhere else, ever.
      echo "Credential '$name' issued as an identity only; its bearer token was discarded, not stored." >&2
      exit 0
    fi
    json_get "$workdir/resp" token > "$workdir/governed-token"
    test -s "$workdir/governed-token" || { echo "no token in response" >&2; exit 1; }
    $KUBECTL -n "$SECRET_NAMESPACE" create secret generic "$GOVERNED_SECRET" \
      --from-file=api-key="$workdir/governed-token" \
      --dry-run=client -o yaml | $KUBECTL -n "$SECRET_NAMESPACE" apply -f -
    # Bind the Secret to its credential so a later issue of a DIFFERENT
    # name can detect the mismatch instead of silently reusing this token.
    $KUBECTL -n "$SECRET_NAMESPACE" annotate --overwrite secret "$GOVERNED_SECRET" \
      "kaimahi.dev/credential=$name" >/dev/null
    echo "Governed credential '$name' issued; Secret $SECRET_NAMESPACE/$GOVERNED_SECRET created." >&2
    echo "The plane stores only its hash — the real upstream keys stay with the proxy." >&2
    expires=$(json_get "$workdir/resp" expires_at || true)
    # Said at issue time, not only when it bites: an operator who never
    # learns the deadline meets it as an outage.
    [ -z "$expires" ] || echo "It expires $expires — 'make credentials' shows every deadline, 'make credential-renew NAME=$name' extends this one." >&2
    ;;
  renew)
    name="${2:?usage: plane-admin.sh renew <name> [ttl]}"
    check_name "$name"
    ttl="${3:-${TTL:--}}"
    if [ "$ttl" = - ]; then
      printf '{}\n' > "$workdir/req"
    else
      ttl_seconds "$ttl"
      printf '{"ttl_seconds": %s}\n' "$secs" > "$workdir/req"
    fi
    admin_curl POST "/admin/credentials/$name/renew" "$workdir/req"
    [ "$status" = 200 ] || { echo "renew failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    # No token is minted, sent or read here: renewal moves a date, which
    # is why it needs no custody handling at all.
    echo "Credential '$name' now expires $(json_get "$workdir/resp" expires_at)." >&2
    ;;
  credentials)
    admin_curl GET /admin/credentials
    [ "$status" = 200 ] || { echo "credentials read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("credentials") or []
if not rows:
    print("no credentials")
fmt = "%-16s %-10s %-12s %-22s %-9s %s"
if rows:
    print(fmt % ("credential", "cap cents", "cap tokens", "expires (UTC)", "state", "created (UTC)"))
for c in rows:
    # "no expiry" is the LEGACY class: issued before credentials expired,
    # still valid, and a class that can only shrink. Never a blank, which
    # would read as a bug.
    if c.get("expires_at") is None:
        state = "no expiry"
    elif c.get("expired"):
        state = "EXPIRED"
    elif c.get("expiring_soon"):
        state = "EXPIRING"
    else:
        state = "ok"
    print(fmt % (c["credential"],
                 c["cap_cents"] if c.get("cap_cents") is not None else "-",
                 c["cap_tokens"] if c.get("cap_tokens") is not None else "-",
                 (c.get("expires_at") or "-")[:19], state, c["created_at"][:19]))
EOF
    ;;
  budget)
    name="${2:?usage: plane-admin.sh budget <name> <cents|-> <tokens|->}"
    cents="${3:?cents cap (or - for none)}"
    tokens="${4:?token cap (or - for none)}"
    check_name "$name"
    [ "$cents" = - ] && cents=null
    [ "$tokens" = - ] && tokens=null
    check_cap "$cents"; check_cap "$tokens"
    printf '{"credential": "%s", "cap_cents": %s, "cap_tokens": %s}\n' \
      "$name" "$cents" "$tokens" > "$workdir/req"
    admin_curl PUT /admin/budgets "$workdir/req"
    [ "$status" = 204 ] || { echo "budget set failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    echo "Budget for '$name': cap_cents=$cents cap_tokens=$tokens (monthly, UTC)." >&2
    ;;
  ledger)
    name="${2:-}"
    [ -z "$name" ] || check_name "$name"
    admin_curl GET "/admin/ledger?credential=$name&limit=50"
    [ "$status" = 200 ] || { echo "ledger read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("entries") or []
# The trailing column is "acted for": WHO the call was made for. Last on
# purpose — every existing grep and doc example anchors on the columns
# before it. 'none' means there is no person, 'unknown' means the plane
# cannot say, 'legacy' means the row predates attribution: three
# different answers that stay different.
#
# The two columns before it say WHO CALLED. "caller (claimed)" is the
# client's own word for itself and is not checked by anything; "from
# (observed)" is the address the plane saw at its own socket. They went
# in here, immediately before "acted for", because every existing grep
# names a column ahead of them.
# cell keeps a value to ONE printable line. The plane bounds what it
# writes into an audit column, but this prints rows it did not write
# today — an older plane's, a restored dump's. A newline in a cell
# renders as a second line, which reads as a governed row nobody wrote.
def cell(v):
    s = "" if v is None else str(v)
    return "".join(" " if c in "\n\r\t" else c for c in s if c.isprintable() or c in "\n\r\t")
# clip shortens a cell and SAYS it shortened it — a silently shortened
# address still looks like a whole one, and every caller in the same
# prefix would render identically.
def clip(s, n):
    return s if len(s) <= n else s[:n - 1] + "…"
fmt = "%-19s %-12s %-9s %-16s %6s %6s %6s %-8s %-6s %-28s %-16s %s"
print(fmt % ("created (UTC)", "credential", "upstream", "model", "in", "out", "cents", "source", "status", "caller (claimed)", "from (observed)", "acted for"))
for e in rows:
    print(fmt % (cell(e["created_at"])[:19], cell(e["credential"]), cell(e["upstream"]), cell(e["model"])[:16],
                 e["input_tokens"], e["output_tokens"], e["cost_cents"], cell(e["cost_source"]), e["status"],
                 clip(cell(e.get("caller_claim") or "unrecorded"), 28), clip(cell(e.get("caller_addr") or "unrecorded"), 16),
                 cell(e.get("acted_for") or "unknown")))
if "month_cents" in d:
    print(f'-- month to date: {d["month_cents"]} cents, {d["month_tokens"]} tokens')
EOF
    ;;
  tool-allow)
    name="${2:?usage: plane-admin.sh tool-allow <name> <tool,tool|->}"
    tools="${3:?tool list (comma-separated, or - for empty = nothing callable)}"
    check_name "$name"
    json_tools=""
    if [ "$tools" != - ]; then
      IFS=, read -ra parts <<< "$tools"
      for t in "${parts[@]}"; do
        case "$t" in
          (*[!A-Za-z0-9._-]*|'') echo "invalid tool name '$t' (want [A-Za-z0-9._-]+)" >&2; exit 2 ;;
        esac
        json_tools="$json_tools${json_tools:+, }\"$t\""
      done
    fi
    printf '{"credential": "%s", "tools": [%s]}\n' "$name" "$json_tools" > "$workdir/req"
    admin_curl PUT /admin/tool-allowlist "$workdir/req"
    [ "$status" = 204 ] || { echo "tool-allow failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    echo "Tool allowlist for '$name': [${json_tools:-}] (enforced on tools/call, projected on tools/list)." >&2
    echo "kagent re-discovers the projection on its next RemoteMCPServer reconcile; enforcement is immediate." >&2
    ;;
  tool-allowlist)
    name="${2:?usage: plane-admin.sh tool-allowlist <name>}"
    check_name "$name"
    admin_curl GET "/admin/tool-allowlist?credential=$name"
    [ "$status" = 200 ] || { echo "tool-allowlist read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 -c 'import json,sys
d = json.load(open(sys.argv[1]))
print(f'"'"'{d["credential"]}: {", ".join(d["tools"]) or "(empty — nothing callable)"}'"'"')' "$workdir/resp"
    ;;
  tool-audit)
    name="${2:-}"
    [ -z "$name" ] || check_name "$name"
    admin_curl GET "/admin/tool-audit?credential=$name&limit=50"
    [ "$status" = 200 ] || { echo "tool-audit read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("entries") or []
# cell keeps a value to ONE printable line — see the note on the ledger
# view above; the same rule, for the same reason.
def cell(v):
    s = "" if v is None else str(v)
    return "".join(" " if c in "\n\r\t" else c for c in s if c.isprintable() or c in "\n\r\t")
# clip shortens a cell and SAYS it shortened it — a silently shortened
# address still looks like a whole one, and every caller in the same
# prefix would render identically.
def clip(s, n):
    return s if len(s) <= n else s[:n - 1] + "…"
fmt = "%-19s %-12s %-12s %-12s %-24s %-8s %6s %-44s %-44s %-28s %-16s %s"
print(fmt % ("created (UTC)", "credential", "upstream", "method", "tool", "decision", "status", "detail", "call", "caller (claimed)", "from (observed)", "acted for"))
for e in rows:
    # arg_digest identifies the call; arg_summary says what it was. Both
    # are on the denial and on the admitted call, so an approved call and
    # the call that ran are provably the same one.
    call = cell(e.get("arg_summary") or "")
    if e.get("arg_digest"):
        call = (call + " ") if call else ""
        call += "[" + cell(e["arg_digest"])[:12] + "]"
    # "caller (claimed)" is the client's own word for itself — self-reported,
    # unverified, and bounded by the plane at the write. "from (observed)" is
    # the peer address the plane saw. A row from before these were recorded
    # has neither, and says so rather than reading as an empty answer.
    print(fmt % (cell(e["created_at"])[:19], cell(e["credential"]), cell(e["upstream"]), cell(e["method"]),
                 cell(e["tool"]), cell(e["decision"]), e["status"], cell(e["detail"]), call or "-",
                 clip(cell(e.get("caller_claim") or "unrecorded"), 28), clip(cell(e.get("caller_addr") or "unrecorded"), 16),
                 cell(e.get("acted_for") or "unknown")))
EOF
    ;;
  approvals)
    admin_curl GET /admin/approvals
    [ "$status" = 200 ] || { echo "approvals read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("pending") or []
if not rows:
    print("no pending approval requests")
# cell keeps a value to ONE printable line. Repeated per block because
# each of these renderings is a self-contained python3 invocation; the
# rule is the same one the ledger and tool-audit views apply, and the
# subject here is a caller-supplied tool name.
def cell(v):
    s = "" if v is None else str(v)
    return "".join(" " if c in "\n\r\t" else c for c in s if c.isprintable() or c in "\n\r\t")
fmt = "%-36s %-19s %-12s %-8s %-18s %-34s %s"
if rows:
    print(fmt % ("id", "created (UTC)", "credential", "kind", "subject", "detail", "call"))
for r in rows:
    # The call is what a human is actually approving: an approver
    # who cannot see the transaction is the whole problem restated.
    print(fmt % (cell(r["id"]), cell(r["created_at"])[:19], cell(r["credential"]), cell(r["kind"]),
                 cell(r["subject"]), cell(r["detail"]), cell(r.get("arg_summary") or "-")))
EOF
    ;;
  approve)
    id="${2:?usage: plane-admin.sh approve <id> <ttl|-> <uses|-> <amount|->}"
    ttl="${3:--}"; uses="${4:--}"; amount="${5:--}"
    check_uuid "$id"
    body='{'
    if [ "$ttl" != - ]; then
      ttl_seconds "$ttl"
      body="$body\"ttl_seconds\": $secs, "
    fi
    if [ "$uses" != - ]; then
      check_cap "$uses"
      body="$body\"max_uses\": $uses, "
    fi
    if [ "$amount" != - ]; then
      check_cap "$amount"
      body="$body\"amount\": $amount, "
    fi
    printf '%s}\n' "${body%, }" > "$workdir/req"
    admin_curl POST "/admin/approvals/$id/approve" "$workdir/req"
    [ "$status" = 201 ] || { echo "approve failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 -c 'import json,sys
g = json.load(open(sys.argv[1]))
bounds = []
if g.get("expires_at"): bounds.append("expires " + g["expires_at"])
if g.get("max_uses") is not None: bounds.append(f'"'"'{g["max_uses"]} use(s)'"'"')
if g.get("amount") is not None: bounds.append(f'"'"'amount {g["amount"]}'"'"')
print(f'"'"'Granted: {g["credential"]} {g["kind"]}/{g["subject"]} — {", ".join(bounds)} (grant {g["id"]})'"'"')' "$workdir/resp"
    ;;
  deny)
    id="${2:?usage: plane-admin.sh deny <id>}"
    check_uuid "$id"
    admin_curl POST "/admin/approvals/$id/deny"
    [ "$status" = 204 ] || { echo "deny failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    echo "Request $id denied." >&2
    ;;
  request)
    name="${2:?usage: plane-admin.sh request <name> <tool|budget|inbound> <subject> [args-json]}"
    kind="${3:?kind (tool|budget|inbound)}"
    subject="${4:?subject (tool name, tokens|cents, or hook name)}"
    args="${5:-}"
    check_name "$name"
    case "$kind" in (tool|budget|inbound) ;; (*) echo "kind must be tool, budget or inbound" >&2; exit 2 ;; esac
    case "$subject" in
      (*[!A-Za-z0-9._-]*|'') echo "invalid subject '$subject'" >&2; exit 2 ;;
    esac
    # A tool request names the CALL it is about. The arguments are
    # embedded as JSON (validated here so a typo fails before the admin
    # port sees it); the plane computes the digest with the gateway's own
    # code, so this request and the agent's retry are the same call.
    if [ -n "$args" ]; then
      [ "$kind" = tool ] || { echo "args-json is meaningful only on tool requests" >&2; exit 2; }
      python3 -c 'import json,sys
d = json.loads(sys.argv[1])
assert isinstance(d, dict), "tool arguments must be a JSON object"' "$args" \
        || { echo "invalid args-json (want a JSON object)" >&2; exit 2; }
      python3 -c 'import json,sys
print(json.dumps({"credential": sys.argv[1], "kind": sys.argv[2], "subject": sys.argv[3],
                  "arguments": json.loads(sys.argv[4])}))' "$name" "$kind" "$subject" "$args" > "$workdir/req"
    else
      printf '{"credential": "%s", "kind": "%s", "subject": "%s"}\n' "$name" "$kind" "$subject" > "$workdir/req"
    fi
    admin_curl POST /admin/requests "$workdir/req"
    [ "$status" = 201 ] || { echo "request failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    if grep -q '"deduped":true' "$workdir/resp"; then
      echo "Already pending — deduped (run 'make approvals')." >&2
    else
      echo "Approval request filed (run 'make approvals')." >&2
    fi
    ;;
  grants)
    name="${2:-}"
    [ -z "$name" ] || check_name "$name"
    admin_curl GET "/admin/grants?credential=$name&limit=50"
    [ "$status" = 200 ] || { echo "grants read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("grants") or []
if not rows:
    print("no grants")
# The last column is the CREDENTIAL's deadline, not the grant's: a grant
# that outlives the credential it was given on is a promise the plane
# cannot keep, so the two are read side by side.
fmt = "%-36s %-12s %-8s %-18s %-6s %-22s %-9s %-8s %-19s %-18s %-20s %s"
if rows:
    print(fmt % ("id", "credential", "kind", "subject", "live", "expires (UTC)", "uses", "amount", "created (UTC)", "decided by", "binds", "cred expires (UTC)"))
for g in rows:
    uses = str(g["uses"]) + ("/" + str(g["max_uses"]) if g.get("max_uses") is not None else "")
    # A tool grant admits ONE call: "binds" is that call's digest.
    # "verb-level" marks the closed legacy class — a grant minted before
    # argument binding, which admits any arguments; none can be created.
    if g["kind"] != "tool":
        binds = "-"
    elif g.get("arg_digest"):
        binds = "call " + g["arg_digest"][:12]
    else:
        binds = "verb-level (legacy)"
    print(fmt % (g["id"], g["credential"], g["kind"], g["subject"],
                 "yes" if g["live"] else "no",
                 (g.get("expires_at") or "-")[:19], uses,
                 g.get("amount") if g.get("amount") is not None else "-",
                 g["created_at"][:19], g.get("decided_by") or "-", binds,
                 (g.get("credential_expires_at") or "-")[:19]))
EOF
    ;;
  approval-audit)
    name="${2:-}"
    [ -z "$name" ] || check_name "$name"
    admin_curl GET "/admin/approval-audit?credential=$name&limit=50"
    [ "$status" = 200 ] || { echo "approval-audit read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("entries") or []
# cell keeps a value to ONE printable line — see the note on the
# approvals view; the subject here is a caller-supplied tool name too.
def cell(v):
    s = "" if v is None else str(v)
    return "".join(" " if c in "\n\r\t" else c for c in s if c.isprintable() or c in "\n\r\t")
fmt = "%-19s %-12s %-8s %-18s %-10s %-18s %-40s %s"
print(fmt % ("created (UTC)", "credential", "kind", "subject", "action", "decided by", "bounds", "call"))
for e in rows:
    print(fmt % (cell(e["created_at"])[:19], cell(e["credential"]), cell(e["kind"]), cell(e["subject"]),
                 cell(e["action"]), cell(e.get("decided_by") or "-"), cell(e["bounds"]),
                 cell(e.get("arg_summary") or "-")))
EOF
    ;;
  inbound-audit)
    hook="${2:-}"
    [ -z "$hook" ] || check_name "$hook"
    admin_curl GET "/admin/inbound-audit?hook=$hook&limit=50"
    [ "$status" = 200 ] || { echo "inbound-audit read failed (HTTP $status):" >&2; cat "$workdir/resp" >&2; exit 1; }
    python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
rows = d.get("entries") or []
# cell keeps a value to ONE printable line — see the note on the
# approvals view. The delivery id and the detail come off a webhook.
def cell(v):
    s = "" if v is None else str(v)
    return "".join(" " if c in "\n\r\t" else c for c in s if c.isprintable() or c in "\n\r\t")
fmt = "%-19s %-12s %-14s %-20s %-9s %6s %6s %6s %-40s %s"
print(fmt % ("created (UTC)", "hook", "credential", "delivery", "decision", "status", "in", "out", "detail", "acted for"))
for e in rows:
    print(fmt % (cell(e["created_at"])[:19], cell(e["hook"]), cell(e["credential"]), cell(e["delivery_id"])[:20],
                 cell(e["decision"]), e["status"], e["input_tokens"], e["output_tokens"], cell(e["detail"]),
                 cell(e.get("acted_for") or "unknown")))
EOF
    ;;
  *)
    echo "usage: plane-admin.sh issue|renew|credentials|budget|ledger|tool-allow|tool-allowlist|tool-audit|approvals|approve|deny|request|grants|approval-audit|inbound-audit ..." >&2
    exit 2
    ;;
esac
