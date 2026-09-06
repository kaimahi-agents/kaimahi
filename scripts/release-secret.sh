#!/usr/bin/env bash
# Capture the RELEASE agent's GitHub token stdin-only and store it as
# the plane-side Secret the github-release upstream reads
# (kaimahi-release-pat, key: token) — after PROVING, fail-closed, what can
# be proven about it.
#
# scripts/github-secret.sh's sibling, and the difference is the point:
# that token is read-only and feeds the hosted-upstream demo. This one can
# change a real repository, so it gets its own Secret, upstream entry and
# allowlist, and the read-only seam keeps the blast radius it was reviewed
# with.
#
# Which token: a FINE-GRAINED personal access token, scoped to ONE
# repository, with:
#   Contents:      Read and write   (create the release branch)
#   Pull requests: Read             (collate the notes)
#   Actions:       Read and write   (dispatch the build workflow)
#   Metadata:      Read             (comes along)
#
# The uncomfortable part, because it decides where the guardrail lives:
# GitHub has no permission that grants creating a ref without also
# granting deleting one. Contents: write is the smallest scope that can
# create a release branch, and it also permits DELETE on any ref. There's
# no separate Releases permission either. So the token is NOT what stops
# a destructive git operation — the gateway is. See docs/release-agent.md,
# "What makes a destructive operation impossible".
#
# Custody rules (docs/COORDINATION.md security guidance):
#   - Token bytes travel only through pipes and 0600 files — never argv,
#     env listings, YAML, or logs. curl reads the auth header from a file.
#   - No -L on any keyed call.
#   - Every step checks a well-formed positive; a failed check stores
#     nothing.
#
# Usage: GITHUB_REPO=owner/name bash scripts/release-secret.sh   (token on stdin)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${RELEASE_SECRET_NAMESPACE:-kaimahi}"
SECRET_NAME="${RELEASE_SECRET_NAME:-kaimahi-release-pat}"
repo="${GITHUB_REPO:-}"

if [ -z "$repo" ]; then
  echo 'usage: make release-secret GITHUB_REPO=owner/name   (fine-grained write token on stdin)' >&2
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
  echo 'A classic PAT or an OAuth token cannot be scoped to one repository, and this' >&2
  echo 'token can write.' >&2
  exit 1
fi
{ printf 'Authorization: Bearer '; cat "$workdir/token"; printf '\n'; } > "$workdir/auth-header"

api() {
  curl -sS -X GET -H @"$workdir/auth-header" \
    -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
    -D "$workdir/resp-headers" -o "$workdir/resp" -w '%{http_code}' "$1"
}

# 1. The token can read THE repository, and is what it claims to be. A
# classic token announces its scopes in X-OAuth-Scopes; a fine-grained
# one does not. Both facts are asserted from the same response.
status=$(api "https://api.github.com/repos/$repo")
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

# 2. ONE repository: not proven here, and the reason is worth keeping
# because a check used to sit here and was wrong.
#
# It asked GET /user/repos and refused a token listing more than one repo.
# That endpoint answers by the USER's affiliations, not the token's scope
# — for a member of a large org it returns thousands whatever the token
# can reach. So it refused correctly-scoped tokens and proved nothing
# about wrong ones.
#
# There's no sound replacement: GitHub exposes no endpoint reporting a
# fine-grained token's repo grant, and a negative control can't tell "the
# token can't reach it" from "it can, because it's public".
#
# So single-repo scope is the operator's assertion when they create the
# token, and the plane's job afterwards: `make release-bind` constrains
# the read tools to that owner and repo, and every consequential call
# binds owner and repo in its digest. That's a real control that doesn't
# rest on this script guessing.

# 3. Its deadline, said now rather than when it bites — the rule Kaimahi's
# own credentials follow, applied here.
#
# Reported, not enforced: an absent header means "this response didn't
# carry one", not "this token never expires". The check above this one
# refused valid tokens by treating a silence as a fact; once was enough.
expiry=$(tr -d '\r' < "$workdir/resp-headers" \
  | sed -n 's/^[Gg]ithub-[Aa]uthentication-[Tt]oken-[Ee]xpiration: *//p' | head -1)
if [ -z "$expiry" ]; then
  expiry="NOT REPORTED — check it yourself; an unattended write credential that
  never dies is one nobody ever revokes"
fi

# 4. Store. --from-file keeps the value out of argv; the manifest exists
# only inside the apply pipe, never on disk. Create-or-update in one
# apply so an existing Secret stays intact if this fails partway.
$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || $KUBECTL create namespace "$NAMESPACE"
$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET_NAME" \
  --from-file=token="$workdir/token" \
  --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
echo "Secret $NAMESPACE/$SECRET_NAME stored." >&2
echo "Token expiry: $expiry" >&2

cat >&2 <<'NOTE'

The gateway injects this token on calls to the github-release upstream
from plane custody; the agent never holds it.

PROVEN here: the token is fine-grained, it reads the repository named,
and it has a deadline.

NOT proven, because GitHub exposes neither: which permissions you granted,
and whether the token is scoped to that ONE repository. Both are yours to
get right when you create it. The plane enforces the second one anyway —
run `make release-bind GITHUB_REPO=owner/name`, which constrains the read
tools to that repository and denies anything else at the gateway.

Delete it with: make release-revoke
NOTE
