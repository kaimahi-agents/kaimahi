#!/usr/bin/env bash
# Capture a GitHub token stdin-only and store it as the plane-side Secret
# the GitHub hosted MCP upstream reads (kaimahi-github-pat, key:
# token) — after PROVING, fail-closed, that it is the kind of token this
# path accepts and that it can read the one repository named.
#
# Which token: a FINE-GRAINED personal access token, scoped to ONE
# repository, with read-only permissions (Issues: Read, Pull requests:
# Read; Metadata comes along). Not the Copilot device-flow token: GitHub's
# hosted MCP server does accept it (verified 2026-09-02), but the plane
# only ever holds the exchanged Copilot token, which expires in ~30
# minutes, and the cached OAuth token behind it is scoped read:user — it
# reads public repositories only. A classic PAT (ghp_) or an OAuth token
# (gho_) is refused too: neither can be scoped to one repository, and
# `repo` is read-write. Least privilege is the point of custody.
#
# Custody rules (docs/COORDINATION.md security guidance):
#   - Token bytes travel only through pipes and 0600 files — never argv,
#     env listings, YAML, or logs. curl reads the auth header from a file.
#   - No -L on any keyed call.
#   - Every step checks a well-formed positive; a failed check stores
#     nothing.
#
# Usage: GITHUB_REPO=owner/name bash scripts/github-secret.sh   (token on stdin)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${GITHUB_SECRET_NAMESPACE:-kaimahi}"
SECRET_NAME="${GITHUB_SECRET_NAME:-kaimahi-github-pat}"
repo="${GITHUB_REPO:-}"

if [ -z "$repo" ]; then
  echo 'usage: make github-secret GITHUB_REPO=owner/name   (fine-grained read-only token on stdin)' >&2
  exit 2
fi
# Anchored: the value is interpolated into a URL and must be exactly
# owner/name.
if ! [[ "$repo" =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$ ]]; then
  echo "invalid GITHUB_REPO '$repo' (want owner/name)" >&2
  exit 2
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

echo 'Paste the fine-grained GitHub token (github_pat_...), press Enter, then Ctrl-D:' >&2
tr -d '\r\n' < /dev/stdin > "$workdir/token"
test -s "$workdir/token" || { echo 'no token read on stdin' >&2; exit 1; }
if ! grep -qE '^github_pat_[A-Za-z0-9_]{20,}$' "$workdir/token"; then
  echo 'REFUSING: that is not a fine-grained token (expected a github_pat_ prefix).' >&2
  echo 'A classic PAT or an OAuth token cannot be scoped to one read-only repository.' >&2
  exit 1
fi
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

# 1. The token can read THE repository, and is what it claims to be. A
# classic token announces its scopes in X-OAuth-Scopes; a fine-grained
# one does not. Both facts are asserted from the same response.
status=$(curl -sS -X GET -H @"$workdir/auth-header" \
  -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
  -D "$workdir/resp-headers" -o "$workdir/resp" -w '%{http_code}' \
  "https://api.github.com/repos/$repo")
[ "$status" = 200 ] || {
  echo "GET /repos/$repo answered HTTP $status — the token cannot read that repository" >&2
  echo '(a fine-grained token must list the repository under "Repository access")' >&2
  exit 1; }
if tr -d '\r' < "$workdir/resp-headers" | grep -qiE '^x-oauth-scopes: *[^ ]'; then
  echo 'REFUSING: the token carries OAuth scopes (a classic token); use a fine-grained one.' >&2
  exit 1
fi
python3 - "$workdir/resp" "$repo" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
want = sys.argv[2].lower()
if not isinstance(d, dict) or str(d.get("full_name", "")).lower() != want:
    sys.exit("the repository answer does not name %s — refusing to store" % sys.argv[2])
print("token vetted: it reads %s" % sys.argv[2], file=sys.stderr)
EOF

# 2. Store. --from-file keeps the value out of argv; the manifest exists
# only inside the apply pipe, never on disk. Create-or-update in one
# apply so an existing Secret stays intact if this fails partway.
$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || $KUBECTL create namespace "$NAMESPACE"
$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET_NAME" \
  --from-file=token="$workdir/token" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
echo "Secret $NAMESPACE/$SECRET_NAME stored." >&2

cat >&2 <<'NOTE'

The gateway injects this token on calls to the github upstream from plane
custody; the agent never holds it. What was PROVEN here: the token is
fine-grained and can read that one repository. What was NOT (GitHub does
not expose a fine-grained token's permissions): that you chose read-only
permissions — that part is yours. The gateway allowlist (make govern-github)
never names a write tool regardless; a write is the action a human approves.
Delete it with: make github-revoke
NOTE
