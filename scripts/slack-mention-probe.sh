#!/usr/bin/env bash
# Deliver ONE synthetic app_mention to the slack-events hook exactly as
# Slack's Events API would: an event_callback envelope, signed v0
# with the hook's signing secret from the kaimahi-inbound-signing Secret,
# in the channel the hook is bound to (read from the same Secret key the
# plane reads), from the given user. CI's stand-in for a person typing
# `@kaimahi approve <id>` in the channel on a cluster Slack cannot reach;
# on a live cluster the real thing is typing it.
#
# Fail closed like inbound-probe.sh: passes ONLY when the HTTP status
# equals $EXPECT (default 200 — a handled command or an ignored event;
# a question is 202). For 200 the body must be the plane's well-formed
# answer, and when WANT is set it must be a command whose outcome
# contains it ("approved request", "denied request", "already
# approved") — a 200 is a HANDLED command, which includes "invalid" and
# "no such request"; WANT is what says the decision went the way the
# caller meant.
#
# The user id given here is whatever the caller chooses; on a kind
# cluster it is a synthetic one (CI uses U0CIAPPROVER). Never paste a
# real workspace's ids into a committed file.
#
# Usage: slack-mention-probe.sh <slack user id> <text>
#   env: EXPECT=200|202|403|503 (default 200)
#        WANT=<substring>     require the command outcome to contain it
#        SLACK_EVENT_ID=<id>  reuse an event id (a replay)
#        INBOUND_PORT=<local port> (default 18085)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
AGENT_NAMESPACE=kagent
HOOK="${HOOK:-slack-events}"
INBOUND_PORT="${INBOUND_PORT:-18085}"
SIGNING_SECRET="${INBOUND_SIGNING_SECRET:-kaimahi-inbound-signing}"
BOT_SECRET="${SLACK_BOT_SECRET:-kaimahi-slack-bot}"
EXPECT="${EXPECT:-200}"
WANT="${WANT:-}"

user="${1:?usage: slack-mention-probe.sh <slack user id> <text>}"
text="${2:?usage: slack-mention-probe.sh <slack user id> <text>}"
case "$user" in
  (*[!A-Z0-9]*|'') echo "invalid Slack user id '$user' (want uppercase alphanumerics)" >&2; exit 2 ;;
esac
case "$EXPECT" in
  (*[!0-9]*|'') echo "invalid EXPECT '$EXPECT' (want an HTTP status)" >&2; exit 2 ;;
esac
event_id="${SLACK_EVENT_ID:-Ev$(od -An -N5 -tx1 /dev/urandom | tr -d ' \n' | tr 'a-f' 'A-F')}"

# Context safety: see scripts/inbound-probe.sh — the context is
# derived from $KUBECTL, never from an ambient KUBE_CTX.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, $AGENT_NAMESPACE" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0") $user"

workdir=$(mktemp -d)
pf_pid=""
cleanup() {
  [ -n "$pf_pid" ] && kill "$pf_pid" 2>/dev/null || true
  rm -rf "$workdir"
}
trap cleanup EXIT

# The channel: the hook is bound to whatever the bot Secret's channel key
# says, so read it from there rather than have the caller name one.
$KUBECTL -n "$NAMESPACE" get secret "$BOT_SECRET" -o jsonpath='{.data.SLACK_MCP_ADD_MESSAGE_TOOL}' | base64 -d > "$workdir/channel"
test -s "$workdir/channel" || { echo "no channel in $BOT_SECRET (run make slack-secret, or in CI create the key)" >&2; exit 1; }
$KUBECTL -n "$NAMESPACE" get secret "$SIGNING_SECRET" -o jsonpath="{.data.$HOOK}" | base64 -d > "$workdir/key"
test -s "$workdir/key" || { echo "no signing key for hook '$HOOK' in $SIGNING_SECRET (run make inbound-secret HOOK=$HOOK)" >&2; exit 1; }

ts=$(date +%s)
# The envelope, built by python so the text is escaped, then signed over
# the exact bytes sent. The mention token names a synthetic bot user;
# the plane strips every <@…> token before parsing.
python3 - "$workdir/channel" "$user" "$text" "$event_id" "$ts" "$workdir/key" "$workdir/body" "$workdir/headers" <<'EOF'
import hashlib, hmac, json, sys
channel = open(sys.argv[1]).read().strip()
user, text, event_id, ts = sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5]
key = open(sys.argv[6], "rb").read().strip()
body = json.dumps({"type": "event_callback", "event_id": event_id, "team_id": "T0PROBE",
    "event": {"type": "app_mention", "user": user, "text": "<@U0KAIMAHIBOT> " + text,
              "channel": channel, "ts": ts + ".000100"}}).encode()
open(sys.argv[7], "wb").write(body)
sig = hmac.new(key, b"v0:" + ts.encode() + b":" + body, hashlib.sha256).hexdigest()
with open(sys.argv[8], "w") as f:
    f.write(f"X-Slack-Request-Timestamp: {ts}\nX-Slack-Signature: v0={sig}\n")
EOF

# --address pins IPv4 explicitly (see plane-admin.sh for why).
$KUBECTL -n "$NAMESPACE" port-forward --address 127.0.0.1 \
  svc/kaimahi-inbound "$INBOUND_PORT:8082" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 150); do
  curl -fsS -o /dev/null "http://127.0.0.1:$INBOUND_PORT/healthz" 2>/dev/null && break
  sleep 0.2
done
curl -fsS -o /dev/null "http://127.0.0.1:$INBOUND_PORT/healthz" \
  || { echo "inbound port-forward failed" >&2; exit 1; }

status=$(curl -sS -X POST -H @"$workdir/headers" -H 'Content-Type: application/json' \
  --data-binary @"$workdir/body" -o "$workdir/resp" -w '%{http_code}' \
  "http://127.0.0.1:$INBOUND_PORT/hook/$HOOK")

if [ "$status" != "$EXPECT" ]; then
  echo "mention by $user (event $event_id): expected HTTP $EXPECT, got HTTP $status:" >&2
  cat "$workdir/resp" >&2; echo >&2
  exit 1
fi
case "$status" in
  200)
    python3 - "$workdir/resp" "$user" "$event_id" "$WANT" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
assert d.get("status") in ("command", "ignored"), f"malformed answer: {d}"
if d["status"] == "command":
    print(f"COMMAND by {sys.argv[2]} (event {sys.argv[3]}) handled: {d.get('outcome')}")
    if sys.argv[4] and sys.argv[4] not in (d.get("outcome") or ""):
        sys.exit(f"outcome does not contain WANT={sys.argv[4]!r}")
else:
    if sys.argv[4]:
        sys.exit(f"the mention was ignored, not handled as a command (WANT={sys.argv[4]!r})")
    print(f"IGNORED (event {sys.argv[3]}): {d.get('reason')}")
EOF
    ;;
  202)
    echo "ADMITTED as a question (event $event_id): $(tr -d '\n' < "$workdir/resp")" ;;
  *)
    echo "REFUSED as expected (HTTP $status, user=$user, event $event_id): $(tr -d '\n' < "$workdir/resp")" ;;
esac
