#!/usr/bin/env bash
# Capture an Azure DevOps access token stdin-only and store it as the
# plane-side Secret the `ado` tool upstream reads (kaimahi-ado-token, key:
# token) — after PROVING, fail-closed, that it is a token for the right
# resource and that the hosted MCP server actually accepts it.
#
# NOT A PAT, which is what this script first expected. Microsoft's hosted Azure
# DevOps MCP server answers an unauthenticated request with
#     WWW-Authenticate: Bearer resource_metadata="https://mcp.dev.azure.com/.well-known/oauth-protected-resource/"
# and that document declares
#     {"authorization_servers": ["https://login.microsoftonline.com/organizations/v2.0"],
#      "bearer_methods_supported": ["header"],
#      "scopes_supported": ["https://mcp.dev.azure.com/.default"]}
# — Microsoft Entra ID only. A PAT is not accepted. The LOCAL server does
# take a PAT, but it speaks stdio only (v2.9.0, src/index.ts), and the
# gateway relays streamable HTTP, so it is not reachable from here without
# a shim this project declined to write.
#
# What that buys and costs: bearer_methods_supported ["header"] is the
# shape the gateway already injects, so the ADO seam is configuration and
# not code. The cost is a token that lives about an hour. The release
# driver refreshes it automatically; this script is for capturing one by
# hand, and it tells you when it dies rather than letting you find out
# mid-release.
#
# Getting one (credential capture is a human's job here):
#
#     az account get-access-token --scope https://mcp.dev.azure.com/.default \
#        --query accessToken -o tsv
#
# Pipe that in. If your tenant has not consented the Azure CLI to the
# Azure DevOps MCP application, that command fails and you need a custom
# Entra app registration (Microsoft documents one for Cursor and Claude
# Code); this script will tell you plainly rather than storing something
# that cannot work.
#
# Custody rules (docs/COORDINATION.md security guidance): token bytes
# travel only through pipes and 0600 files — never argv, env listings,
# YAML, or logs. curl reads the auth header from a file. No -L on a keyed
# call. Every step checks a well-formed positive; a failed check stores
# nothing.
#
# The organization name is an Azure identifier and never enters this
# repository (scripts/check-no-azure-ids.sh): it is passed in, used to
# build one URL, and not written down.
#
# Usage: ADO_ORG=<organization> bash scripts/ado-secret.sh   (token on stdin)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${ADO_SECRET_NAMESPACE:-kaimahi}"
SECRET_NAME="${ADO_SECRET_NAME:-kaimahi-ado-token}"
org="${ADO_ORG:-}"

if [ -z "$org" ]; then
  echo 'usage: make ado-secret ADO_ORG=<organization>   (Entra access token on stdin)' >&2
  exit 2
fi
# Anchored: interpolated into a URL, and an Azure DevOps organization name
# is a restricted string.
if ! [[ "$org" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,62}$ ]]; then
  echo "invalid ADO_ORG '$org' (want an Azure DevOps organization name)" >&2
  exit 2
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

echo 'Paste the Entra access token (eyJ...), press Enter, then Ctrl-D:' >&2
tr -d '\r\n' < /dev/stdin > "$workdir/token"
test -s "$workdir/token" || { echo 'no token read on stdin' >&2; exit 1; }
if ! grep -qE '^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$' "$workdir/token"; then
  echo 'REFUSING: that is not a JWT access token.' >&2
  echo 'The hosted Azure DevOps MCP server takes an Entra access token, not a PAT.' >&2
  exit 1
fi

# 1. The right resource, and a deadline. Read from the token's own claims
# — no network, no secret printed: only the audience and the expiry leave
# this step, and the audience is a public identifier.
python3 - "$workdir/token" <<'EOF' > "$workdir/claims"
import base64, json, sys, time
tok = open(sys.argv[1]).read().strip()
part = tok.split(".")[1]
part += "=" * (-len(part) % 4)
try:
    c = json.loads(base64.urlsafe_b64decode(part))
except Exception:
    sys.exit("REFUSING: the token's payload is not readable JSON")

aud = str(c.get("aud", ""))
# Reported, not enforced, and the reason matters because a check here
# refused a correct token.
#
# Entra writes `aud` two ways depending on the target app's token
# version: the resource URI for v2.0, the app's client-id GUID for v1.0.
# Asking for --scope https://mcp.dev.azure.com/.default can legitimately
# give either, and accepting only the first refused a token minted by the
# exact command this script recommends. The GUID can't just be added to
# an accept list either — an application id is an Azure identifier and
# this repo won't carry one.
#
# No need to guess anyway: step 2 asks the server, which is the only
# authority on whether it accepts this token. The audience is printed so
# an operator can see what they got.
print("audience: %s" % aud, file=sys.stderr)

exp = c.get("exp")
if not isinstance(exp, int):
    sys.exit("REFUSING: the token carries no expiry claim")
left = exp - int(time.time())
if left <= 0:
    sys.exit("REFUSING: this token expired %d minutes ago" % (-left // 60))
print("%d" % exp)
print("%d" % (left // 60))
EOF
token_exp=$(sed -n 1p "$workdir/claims")
minutes_left=$(sed -n 2p "$workdir/claims")
echo "token vetted: an Azure DevOps access token, ${minutes_left} minutes left" >&2

# 2. The server accepts it. A well-formed POSITIVE: an MCP `initialize`
# that comes back 200 with a JSON-RPC result. A 401 means the token is
# not accepted (wrong resource, or the tenant has not consented); a 404
# means the organization name is wrong. Anything else is refused too —
# a WAF's HTML 200 must not read as success, so the body is parsed.
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"
status=$(curl -sS -X POST -H @"$workdir/auth-header" \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  --max-time 60 -o "$workdir/resp" -w '%{http_code}' \
  "https://mcp.dev.azure.com/$org" \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"kaimahi-ado-secret","version":"1"}}}')
case "$status" in
  200) ;;
  401) echo 'REFUSING: the server answered 401 — it does not accept this token.' >&2
       echo 'The organization must be backed by a Microsoft Entra tenant (standalone' >&2
       echo 'Microsoft-account organizations are not supported by the remote server),' >&2
       echo 'and your tenant must have consented the client that minted this token.' >&2
       exit 1 ;;
  404) echo "REFUSING: the server answered 404 — no organization '$org'." >&2; exit 1 ;;
  *)   echo "REFUSING: the server answered HTTP $status to initialize." >&2
       head -c 300 "$workdir/resp" >&2; echo >&2; exit 1 ;;
esac
python3 - "$workdir/resp" <<'EOF'
import json, sys
raw = open(sys.argv[1], encoding="utf-8", errors="replace").read()
# The endpoint may answer as SSE; take the last data: frame if so.
frames = [l[5:].strip() for l in raw.splitlines() if l.startswith("data:")]
body = frames[-1] if frames else raw.strip()
try:
    d = json.loads(body)
except Exception:
    sys.exit("REFUSING: initialize did not return JSON — refusing to store")
if "error" in d or "result" not in d:
    sys.exit("REFUSING: initialize returned %r — refusing to store" % (d.get("error") or d))
print("server vetted: initialize succeeded", file=sys.stderr)
EOF

# 3. Store. --from-file keeps the value out of argv; the manifest exists
# only inside the apply pipe, never on disk.
$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || $KUBECTL create namespace "$NAMESPACE"
$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET_NAME" \
  --from-file=token="$workdir/token" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
echo "Secret $NAMESPACE/$SECRET_NAME stored." >&2

cat >&2 <<NOTE

The gateway injects this token on calls to the ado upstream from plane
custody; the agent never holds it. It dies in about ${minutes_left} minutes
(epoch ${token_exp}) — re-run this before a release session rather than
during one. A call made after it dies fails closed with the server's own
401, audited, and nothing is half-done.

Delete it with: make release-revoke
NOTE
