#!/usr/bin/env bash
# Mint a short-lived GitHub Copilot API token and store it in-cluster as the
# github-copilot-token Secret (key: api-key), for the github-copilot preset.
#
# Flow: GitHub device login (once, token cached 0600 under ~/.config/kaimahi/)
#   -> exchange at GitHub's Copilot token endpoint
#   -> kubectl create secret from a 0600 temp file.
#
# The gh CLI cannot do this: its OAuth token is not Copilot-entitled and the
# exchange returns 403 (verified 2026-08-31), so this script runs GitHub's
# device flow with the VS Code OAuth client ID — the Copilot-entitled client
# that Copilot tooling authenticates as.
#
# Custody rules (docs/COORDINATION.md security guidance):
#   - Token bytes only ever travel through pipes and 0600 files — never
#     argv, env listings, YAML, or logs. Even the device_code goes via
#     --data @file.
#   - Fail closed: every step checks its output; an empty or failed
#     exchange never reaches kubectl.
#   - No curl -L anywhere: keyed calls must not follow redirects.
set -euo pipefail
umask 077

CLIENT_ID="01ab8ac9400c4e429b23" # GitHub's VS Code OAuth app (Copilot-entitled)
TOKEN_FILE="${KAIMAHI_COPILOT_TOKEN_FILE:-$HOME/.config/kaimahi/copilot-oauth-token}"
KUBECTL="${KUBECTL:-kubectl}"
# Defaults store the token for the ungoverned direct-to-Copilot preset;
# The native governed path is `kmx models credential copilot`; this retained
# script serves only the direct github-copilot preset.
NAMESPACE="${COPILOT_SECRET_NAMESPACE:-kagent}"
SECRET_NAME="${COPILOT_SECRET_NAME:-github-copilot-token}"

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

json_field() { # file field -> stdout (empty if missing)
  python3 -c 'import json,sys
d = json.load(open(sys.argv[1]))
v = d.get(sys.argv[2], "")
sys.stdout.write(v if isinstance(v, str) else "")' "$1" "$2"
}

device_login() {
  echo "No Copilot OAuth token at $TOKEN_FILE — starting GitHub device login." >&2
  curl -fsS -X POST -H 'Accept: application/json' \
    -d "client_id=$CLIENT_ID" -d 'scope=read:user' \
    https://github.com/login/device/code > "$workdir/device.json"
  user_code=$(json_field "$workdir/device.json" user_code)
  verification_uri=$(json_field "$workdir/device.json" verification_uri)
  interval=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("interval", 5))' "$workdir/device.json")
  if [ -z "$user_code" ] || [ -z "$verification_uri" ]; then
    echo "device-code request returned no user_code" >&2
    exit 1
  fi

  echo >&2
  echo "  Open:  $verification_uri" >&2
  echo "  Code:  $user_code" >&2
  echo >&2
  echo "Waiting for approval..." >&2

  {
    printf 'client_id=%s&grant_type=urn:ietf:params:oauth:grant-type:device_code&device_code=' "$CLIENT_ID"
    json_field "$workdir/device.json" device_code
  } > "$workdir/poll.data"

  for _ in $(seq 1 120); do
    sleep "$interval"
    curl -fsS -X POST -H 'Accept: application/json' \
      --data @"$workdir/poll.data" \
      https://github.com/login/oauth/access_token > "$workdir/access.json" || continue
    if [ -n "$(json_field "$workdir/access.json" access_token)" ]; then
      mkdir -p "$(dirname "$TOKEN_FILE")"
      json_field "$workdir/access.json" access_token > "$TOKEN_FILE"
      echo "Login OK — token cached at $TOKEN_FILE" >&2
      return 0
    fi
    case "$(json_field "$workdir/access.json" error)" in
      authorization_pending) ;;
      slow_down) sleep "$interval" ;;
      *) echo "device login failed: $(json_field "$workdir/access.json" error)" >&2; exit 1 ;;
    esac
  done
  echo "device login timed out" >&2
  exit 1
}

[ -s "$TOKEN_FILE" ] || device_login

# Exchange the cached OAuth token for a short-lived Copilot API token.
{ printf 'Authorization: token '; tr -d '\n' < "$TOKEN_FILE"; } > "$workdir/hdr"
if ! curl -fsS -H @"$workdir/hdr" -H 'Accept: application/json' \
    https://api.github.com/copilot_internal/v2/token > "$workdir/exchange.json"; then
  echo "Copilot token exchange failed. If your login is stale or lacks a" >&2
  echo "Copilot subscription, remove $TOKEN_FILE and re-run to log in again." >&2
  exit 1
fi
json_field "$workdir/exchange.json" token > "$workdir/copilot-token"
[ -s "$workdir/copilot-token" ] || {
  echo "exchange response contained no token — refusing to store a Secret" >&2
  exit 1; }

# Allow this to run BEFORE the plane is deployed, so the proxy pod
# mounts the credential at start rather than waiting on kubelet to project
# a Secret that appeared later (see docs/aks.md — that lag is a
# real first-run failure on a fresh cluster). Same create-if-missing
# pattern as scripts/slack-secret.sh.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || \
  $KUBECTL create namespace "$NAMESPACE"

# Create-or-update in one apply so the existing Secret stays intact if this
# fails partway (no delete-then-create gap). The manifest carrying the token
# exists only inside the pipe — never on disk, argv, or in logs.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET_NAME" \
  --from-file=api-key="$workdir/copilot-token" \
  --dry-run=client -o yaml \
  | $KUBECTL -n "$NAMESPACE" apply -f -
echo "Secret $SECRET_NAME refreshed. Note: the Copilot token expires;" >&2
echo "re-run this (then 'make use PRESET=github-copilot') when auth fails." >&2
