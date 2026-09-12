#!/usr/bin/env bash
# Wait for an operator to approve one request with `kmx approve`.
# Used by scripts/ap-demo.sh and scripts/ap-injection.sh with AP_HUMAN=1.
# NOTHING HERE APPROVES ANYTHING: a decision must appear in the plane's
# audit for this exact call, with a new live grant. A missing request, a
# denial, an approval for another call or a pre-existing grant fails closed.
# The admin bearer authorizes the decision; it does not identify a person.
#
# Usage:  await-approval.sh <request id> [uses]
#   env: KUBECTL             kubectl invocation incl. --context
#        CRED                the credential whose grants are checked
#        CRED_AP             accepted as CRED (the accounts-payable name)
#        HUMAN_TIMEOUT       seconds to wait (default 900)
#        HUMAN_POLL          seconds between polls (default 5)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
here="$(cd "$(dirname "$0")" && pwd)"
KMX="${KMX:-$here/../bin/kmx}"
CRED_AP="${CRED:-${CRED_AP:-ap-agent}}"
TIMEOUT="${HUMAN_TIMEOUT:-${AP_HUMAN_TIMEOUT:-900}}"
POLL="${HUMAN_POLL:-${AP_HUMAN_POLL:-5}}"
export KUBECTL KMX KUBE_CTX

id="${1:?usage: await-approval.sh <request id> [uses]}"
uses="${2:-1}"
if ! [[ "$uses" =~ ^[1-9][0-9]*$ ]]; then
  echo "uses must be a positive integer" >&2
  exit 2
fi
case "$TIMEOUT$POLL" in
  (*[!0-9]*|'') echo "HUMAN_TIMEOUT and HUMAN_POLL must be whole seconds" >&2; exit 2 ;;
esac

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

admin() { "$KMX" "$@"; }

# What is pending, read BEFORE the wait: the subject (the tool) is what
# the checks below match on, and once the request is decided it is no
# longer in this list to be read from.
admin approvals > "$work/pending.out"
subject=$(awk -v id="$id" '$1==id {print $5; exit}' "$work/pending.out")
if [ -z "$subject" ]; then
  cat "$work/pending.out" >&2
  echo "await-approval: request $id is not pending — nothing to wait for" >&2
  exit 1
fi

# The CALL this request is for, not just the tool. `make approvals` does
# not print the argument digest, but its last column is the arg_summary —
# the rendering of exactly the policy-relevant fields the digest is taken
# over — so it identifies the call just as precisely. "<tool>: " appears
# only at the head of that summary; the subject column carries the bare
# tool name with no colon.
want_call=$(awk -v id="$id" -v prefix="$subject: " \
  '$1==id {start=index($0, prefix); if (start) print substr($0, start); exit}' "$work/pending.out")
if [ -z "$want_call" ]; then
  cat "$work/pending.out" >&2
  echo "await-approval: request $id states no call — refusing to wait on a" >&2
  echo "  request whose approval could not afterwards be tied to one." >&2
  exit 1
fi

# The grants that already exist for this subject, BEFORE the wait. The
# check at the end requires a grant that is not in this set: "a grant for
# this tool exists" is not the same claim as "this
# wait produced one", and on a credential that has approved the same tool
# before, the weaker claim is satisfied by history.
admin grants "$CRED_AP" > "$work/grants-before.out"
awk -v cred="$CRED_AP" -v subj="$subject" \
  '$2==cred && $3=="tool" && $4==subj {print $1}' "$work/grants-before.out" \
  | sort > "$work/grant-ids-before"

printf '\n\033[1m   WAITING FOR A HUMAN.\033[0m This is the request, as the plane states it:\n\n' >&2
grep -F "$id" "$work/pending.out" >&2 || true
printf '\n   From the operator'"'"'s chair:\n\n' >&2
printf '       kmx approve %s --uses %s --ttl 10m\n\n' "$id" "$uses" >&2
printf '   (if that reports the admin port is in use, add ADMIN_PORT=%s)\n\n' \
  "$(( ${ADMIN_PORT:-19091} + 100 ))" >&2
printf '   The admin bearer does not identify a person. This wait checks\n' >&2
printf '   that THIS call was approved and that the grant is new.\n\n' >&2
printf '   Nothing here can do that for them. This script watches the plane for\n' >&2
printf '   THEIR decision and gives up after %ss.\n\n' "$TIMEOUT" >&2

deadline=$(( $(date +%s) + TIMEOUT ))
decided=no
while [ "$(date +%s)" -lt "$deadline" ]; do
  # A read that FAILED is not an answer. Only a successful listing may
  # decide anything: treating a transient admin-read error as "the
  # request is gone" would report an approval nobody gave.
  if admin approvals > "$work/pending.new" 2>/dev/null; then
    mv "$work/pending.new" "$work/pending.out"
    if ! awk -v id="$id" '$1==id {found=1} END{exit !found}' "$work/pending.out"; then
      decided=yes
      break
    fi
  fi
  sleep "$POLL"
done

if [ "$decided" != yes ]; then
  echo "await-approval: request $id was still pending after ${TIMEOUT}s." >&2
  echo "  Not claiming an approval. Check kmx approvals on this cluster." >&2
  exit 1
fi

# Decided is not approved, and approved is not approved FOR THIS CALL.
# Read the plane's records rather than inferring approval from the
# request having left the pending list — a denial empties it too.
#
# Two requests for the same tool can be pending at once. Without call
# binding, approving the OTHER request and denying this one could satisfy
# every other check.
admin audit approval "$CRED_AP" > "$work/audit.out"
if ! awk -v cred="$CRED_AP" -v subj="$subject" -v want="$want_call" \
  '$2==cred && $3=="tool" && $4==subj && $5=="approved" && $6=="admin" \
   && substr($0, length($0) - length(want) + 1) == want {found=1} END{exit !found}' \
  "$work/audit.out"; then
  cat "$work/audit.out" >&2
  echo "await-approval: no 'approved' row by admin for this exact call:" >&2
  echo "    $want_call" >&2
  echo "  The request was decided, but not approved by admin for that call —" >&2
  echo "  treated as a failure rather than continuing on an approval of something" >&2
  echo "  else." >&2
  exit 1
fi

# The grant this wait produced: NEW (not in the snapshot above), LIVE, and
# decided by the admin bearer. Requiring all three is what makes the caller's
# next call — which spends this grant — the one the human actually
# authorised, rather than any grant that happens to be lying around.
admin grants "$CRED_AP" > "$work/grants.out"
awk -v cred="$CRED_AP" -v subj="$subject" \
  '$2==cred && $3=="tool" && $4==subj && $5=="yes" && $10=="admin" {print $1}' \
  "$work/grants.out" | sort > "$work/grant-ids-after"
if [ -z "$(comm -13 "$work/grant-ids-before" "$work/grant-ids-after")" ]; then
  cat "$work/grants.out" >&2
  echo "await-approval: no NEW live grant for $subject decided by admin." >&2
  echo "  A grant that already existed before this wait does not show that the" >&2
  echo "  decision just made is the one about to be spent. Not continuing." >&2
  exit 1
fi

printf '   \033[1m%s approved by admin\033[0m — recorded by the plane, with a grant.\n' \
  "$subject" >&2
