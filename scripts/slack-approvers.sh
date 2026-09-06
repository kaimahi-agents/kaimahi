#!/usr/bin/env bash
# Store WHO may approve or deny an approval request from Slack:
# the Slack user ids the plane accepts `@kaimahi approve <id>` / `deny
# <id>` from, as the `approvers` key of the plane-side Secret
# kaimahi-slack-approvers, projected into the proxy pod next to the
# channel file (/etc/kaimahi/slack/approvers) and read per command.
#
# Channel membership is NOT authority: being in the room lets a
# person ask the agent a question; only this list lets them decide. An
# empty or missing list means nobody can — the plane fails a command
# closed (503) rather than open.
#
# A Slack user id is a workspace identifier. It is not a secret, but it
# does not belong in a public repo, in argv (shell history, `ps`), or in
# YAML on disk — so this script takes the ids on stdin only, like every
# other workspace value here, and the manifest exists only inside the
# apply pipe.
#
# Ids: U… (or W… on Enterprise Grid), uppercase alphanumerics. Find
# yours in Slack: profile → ⋯ → Copy member ID. A display name is
# refused: names change and can be spoofed; ids are what Slack signs.
#
# Usage: bash scripts/slack-approvers.sh   (then paste ids, Enter, Ctrl-D)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE="${SLACK_SECRET_NAMESPACE:-kaimahi}"
SECRET="${SLACK_APPROVERS_SECRET:-kaimahi-slack-approvers}"

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

echo "Paste the Slack USER ids who may approve from Slack (comma- or newline-separated), press Enter, then Ctrl-D:" >&2
# The trailing newline is added explicitly: a paste (or a pipe) whose
# last id has no newline would otherwise be skipped by the `read` loop
# below — stored, but never validated or counted.
{ tr ', \r' '\n\n\n' < /dev/stdin; printf '\n'; } | sed '/^$/d' > "$workdir/ids"
test -s "$workdir/ids" || { echo 'no ids read on stdin — refusing to store an empty list' >&2; exit 1; }

# Every line must be an id. One bad entry refuses the whole list: the
# plane would refuse it too (a half-garbled list is not a list of
# approvers), and better here than at the first command.
n=0
while IFS= read -r id || [ -n "$id" ]; do
  if ! [[ "$id" =~ ^[UW][A-Z0-9]{1,63}$ ]]; then
    case "$id" in
      ([CG]*) echo "'$id' looks like a CHANNEL id; this list is people (U…), not rooms." >&2 ;;
      (@*|*[a-z]*) echo "'$id' is not a Slack user id (want U…, uppercase; profile → ⋯ → Copy member ID). Names are refused." >&2 ;;
      (*) echo "'$id' is not a Slack user id (want U… or W…, uppercase alphanumerics)" >&2 ;;
    esac
    exit 1
  fi
  n=$((n + 1))
done < "$workdir/ids"
[ "$n" -gt 0 ] || { echo 'no ids validated — refusing to store an empty list' >&2; exit 1; }

$KUBECTL get namespace "$NAMESPACE" >/dev/null 2>&1 || $KUBECTL create namespace "$NAMESPACE"
$KUBECTL -n "$NAMESPACE" create secret generic "$SECRET" \
  --from-file=approvers="$workdir/ids" \
  --dry-run=client -o yaml | $KUBECTL -n "$NAMESPACE" apply -f - >/dev/null
echo "Secret $NAMESPACE/$SECRET stored: $n approver(s). The proxy reads it per command" >&2
echo "from /etc/kaimahi/slack/approvers; a first-time projection can take kubelet up to a minute." >&2
