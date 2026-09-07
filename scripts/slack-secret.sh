#!/usr/bin/env bash
# Capture the Slack bot token stdin-only and store the plane-side Secrets
# the Slack MCP server needs — after PROVING, fail-closed, that the
# named channel is one it is safe to post into.
#
# Two Secrets, deliberately split so no pod holds more than its job needs:
#   kaimahi-slack-bot      SLACK_MCP_XOXB_TOKEN + SLACK_MCP_ADD_MESSAGE_TOOL
#                          -> the whole Secret to the MCP server pod
#                             (kagent renders secretRefs as envFrom); the
#                             CHANNEL key alone is projected by name into
#                             the proxy too, so the posting restriction and
#                             the inbound hook's channel allowlist have one
#                             source of truth. The workspace token
#                             never reaches the proxy or any agent.
#   kaimahi-slack-mcp-key  SLACK_MCP_API_KEY
#                          -> the MCP server pod AND the proxy (which
#                             injects it upstream from plane custody).
#
# Custody rules (docs/COORDINATION.md security guidance):
#   - Token bytes travel only through pipes and 0600 files — never argv,
#     env listings, YAML, or logs. curl reads the auth header from a file.
#   - No -L on any keyed call.
#   - Every step checks a well-formed positive; a failed check stores
#     nothing.
#
# OUTWARD-FACING GUARD: posting to Slack sends messages real
# people read, so this script REFUSES any channel it cannot prove is
# private and that the bot has actually been invited to. `conversations.info`
# must answer ok with is_private=true. That check needs the bot scope
# `groups:read`; without it the script stops rather than guessing.
#
# Usage: SLACK_CHANNEL=C0XXXXXXXXX bash scripts/slack-secret.sh
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${SLACK_SECRET_NAMESPACE:-kaimahi}"
BOT_SECRET="${SLACK_BOT_SECRET:-kaimahi-slack-bot}"
KEY_SECRET="${SLACK_MCP_KEY_SECRET:-kaimahi-slack-mcp-key}"
channel="${SLACK_CHANNEL:-}"

if [ -z "$channel" ]; then
  echo 'usage: make slack-secret SLACK_CHANNEL=C0XXXXXXXXX' >&2
  echo 'The channel must be a PRIVATE test channel you have designated and invited the bot to.' >&2
  exit 2
fi
# Anchored and exact: a `case` glob like C[A-Z0-9]* would accept
# "CA&pretty=1", which is then interpolated into the conversations.info
# query AND written verbatim into the server's channel restriction —
# turning one of the three posting guards into a non-channel-ID.
# G... is Slack's legacy private-channel (private group) prefix; modern
# private channels are C.... Accepting both costs nothing: this is only a
# shape check, and conversations.info's is_private remains the authority
# on whether the channel is actually private.
if ! [[ "$channel" =~ ^[CG][A-Z0-9]{7,}$ ]]; then
  echo "invalid channel id '$channel' (want a Slack channel ID like C0XXXXXXXXX or G0XXXXXXXXX, not a #name)" >&2
  exit 2
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

echo 'Paste the Slack BOT token (xoxb-...), press Enter, then Ctrl-D:' >&2
tr -d '\r\n' < /dev/stdin > "$workdir/token"
test -s "$workdir/token" || { echo 'no token read on stdin' >&2; exit 1; }
grep -q '^xoxb-' "$workdir/token" || {
  echo 'that is not a bot token (expected an xoxb- prefix).' >&2
  echo 'A BOT token is deliberate: a user token would act as a person.' >&2
  exit 1
}
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

api() { # method -> body in $workdir/resp, status in $status
  status=$(curl -sS -X GET -H @"$workdir/auth-header" \
    -o "$workdir/resp" -w '%{http_code}' "https://slack.com/api/$1")
}

# 1. The token is live and is a bot.
api auth.test
[ "$status" = 200 ] || { echo "auth.test failed (HTTP $status)" >&2; exit 1; }
python3 - "$workdir/resp" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
if not d.get("ok"):
    sys.exit(f"Slack rejected the token: {d.get('error')}")
if not d.get("bot_id"):
    sys.exit("that token is not a bot token (auth.test returned no bot_id)")
print(f"authenticated: workspace {d.get('team')!r} as bot {d.get('user')!r}", file=sys.stderr)
EOF

# 2. The channel is PRIVATE and the bot is in it. Fail closed: anything
# other than a well-formed ok/is_private=true stops the script.
api "conversations.info?channel=$channel"
[ "$status" = 200 ] || { echo "conversations.info failed (HTTP $status)" >&2; exit 1; }
python3 - "$workdir/resp" "$channel" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
chan = sys.argv[2]
if not d.get("ok"):
    err = d.get("error")
    if err == "missing_scope":
        sys.exit("the bot lacks the 'groups:read' scope, so this script cannot PROVE the\n"
                 "channel is private. Add groups:read to the bot, reinstall the app, and\n"
                 "re-run. Kaimahi will not store a posting credential it cannot vet.")
    if err == "channel_not_found":
        sys.exit(f"{chan} is not visible to this bot — invite it to the channel\n"
                 "(/invite @your-bot) and re-run. A bot must be a member to post to a\n"
                 "private channel.")
    sys.exit(f"conversations.info refused: {err}")
c = d.get("channel") or {}
if not c.get("is_private"):
    sys.exit(f"REFUSING: {chan} (#{c.get('name')}) is not a private channel.\n"
             "This demo posts only to a private test channel: a demo must not\n"
             "put messages in front of people who did not agree to be an audience.")
# A well-formed positive only (standing guidance): an ABSENT is_member
# must not pass as membership. `is False` would let a response shape that
# omits the field through, and the first approved post would then fail at
# Slack with not_in_channel after a human had already burned a grant.
if c.get("is_member") is not True:
    sys.exit(f"cannot confirm the bot is a member of #{c.get('name')} "
             f"(is_member={c.get('is_member')!r}) — invite it (/invite @your-bot) and re-run.")
print(f"channel vetted: #{c.get('name')} is private, bot is a member", file=sys.stderr)
EOF

# 3. The MCP server's own bearer credential. Generated here, straight
# into a 0600 file; the proxy injects it, the agent never sees it.
od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$workdir/mcp-api-key"
test -s "$workdir/mcp-api-key" || { echo 'entropy read failed' >&2; exit 1; }

# 4. Store. --from-file keeps every value out of argv; the manifest exists
# only inside the apply pipe, never on disk.
printf '%s' "$channel" > "$workdir/channel"
$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || $KUBECTL create namespace "$NAMESPACE"
$KUBECTL -n "$NAMESPACE" create secret generic "$BOT_SECRET" \
  --from-file=SLACK_MCP_XOXB_TOKEN="$workdir/token" \
  --from-file=SLACK_MCP_ADD_MESSAGE_TOOL="$workdir/channel" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
echo "Secret $NAMESPACE/$BOT_SECRET stored (token + channel restriction)." >&2

# The gateway key is generated once and kept: rotating it under a running
# server would break the injected calls until both sides roll.
if $KUBECTL -n "$NAMESPACE" get secret "$KEY_SECRET" >/dev/null 2>&1; then
  echo "Secret $NAMESPACE/$KEY_SECRET exists; keeping it." >&2
else
  $KUBECTL -n "$NAMESPACE" create secret generic "$KEY_SECRET" \
    --from-file=SLACK_MCP_API_KEY="$workdir/mcp-api-key" >/dev/null
  echo "Secret $NAMESPACE/$KEY_SECRET created." >&2
fi

cat >&2 <<'NOTE'

Posting is restricted at the SERVER to that one channel (SLACK_MCP_ADD_MESSAGE_TOOL)
and is NOT in the gateway allowlist — it is the action a human approves.
Next: make slack-mcp && make govern-slack
NOTE
