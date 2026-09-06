#!/usr/bin/env bash
# Wait for a REAL person to approve one request — by typing in Slack, or
# from the operator's chair.
#
# WHY THIS EXISTS. scripts/ap-demo.sh and scripts/ap-injection.sh already
# had two ways to approve: the admin bearer, and — with SLACK_USER set —
# scripts/slack-mention-probe.sh, which SYNTHESISES a correctly signed
# app_mention as that user id. The synthetic one is right on kind and in
# CI, where Slack cannot reach the cluster and the id is invented
# (U0CIAPPROVER). It is exactly wrong against a real workspace: forging a
# signed event that claims a named colleague approved a payment is not a
# demonstration of a human approving a payment, it is a demonstration of
# how to fake one.
#
# So on a cluster Slack can actually reach, the scenarios hand the
# decision to this script instead. NOTHING HERE APPROVES ANYTHING. It
# prints the line for a person to type, waits for the plane to record
# THEIR decision — arriving over the internet through the public edge,
# and parsed as an app-mention command — and fails
# closed if the decision never comes, comes from somebody else, or is a
# denial.
#
# It was generalised from the accounts-payable scenario it was written for
# (it was scripts/ap-await-approval.sh) so the release driver can reuse
# the checks rather than reimplement them: the credential is a parameter,
# and a user id of "-" means "whoever is entitled to decide" — for a
# release cut from a laptop with no Slack workspace attached, where the
# approval arrives through `kmx approve`. The call-binding check and the
# new-grant check are unchanged in that mode and are what still make the
# wait meaningful; only the "and it was THAT person" check is dropped,
# and dropping it is stated in the output rather than left implicit.
#
# Usage:  await-approval.sh <request id> <slack user id | -> [uses]
#   env: KUBECTL             kubectl invocation incl. --context
#        CRED                the credential whose grants are checked
#        CRED_AP             accepted as CRED (the accounts-payable name)
#        HUMAN_TIMEOUT       seconds to wait (default 900)
#        HUMAN_POLL          seconds between polls (default 5)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
CRED_AP="${CRED:-${CRED_AP:-ap-agent}}"
TIMEOUT="${HUMAN_TIMEOUT:-${AP_HUMAN_TIMEOUT:-900}}"
POLL="${HUMAN_POLL:-${AP_HUMAN_POLL:-5}}"
export KUBECTL

id="${1:?usage: await-approval.sh <request id> <slack user id | -> [uses]}"
user="${2:?usage: await-approval.sh <request id> <slack user id | -> [uses]}"
uses="${3:-1}"

# "-" means any entitled approver: the plane still decides WHO may decide
# (the approver file for Slack, the admin bearer for the CLI); this
# script simply does not additionally require a named one.
any_approver=no
if [ "$user" = "-" ]; then
  any_approver=yes
else
  case "$user" in
    (*[!A-Z0-9]*|'') echo "invalid Slack user id '$user' (want uppercase alphanumerics, or - for any approver)" >&2; exit 2 ;;
  esac
fi
case "$TIMEOUT$POLL" in
  (*[!0-9]*|'') echo "HUMAN_TIMEOUT and HUMAN_POLL must be whole seconds" >&2; exit 2 ;;
esac

here="$(cd "$(dirname "$0")" && pwd)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

admin() { bash "$here/plane-admin.sh" "$@"; }

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
want_call=$(grep -F "$id" "$work/pending.out" | grep -o "$subject: .*" | head -1)
if [ -z "$want_call" ]; then
  cat "$work/pending.out" >&2
  echo "await-approval: request $id states no call — refusing to wait on a" >&2
  echo "  request whose approval could not afterwards be tied to one." >&2
  exit 1
fi

# The grants that already exist for this subject, BEFORE the wait. The
# check at the end requires a grant that is not in this set: "a grant for
# this tool decided by this person exists" is not the same claim as "this
# wait produced one", and on a credential that has approved the same tool
# before, the weaker claim is satisfied by history.
admin grants "$CRED_AP" > "$work/grants-before.out"
awk -v cred="$CRED_AP" -v subj="$subject" \
  '$2==cred && $3=="tool" && $4==subj {print $1}' "$work/grants-before.out" \
  | sort > "$work/grant-ids-before"

printf '\n\033[1m   WAITING FOR A HUMAN.\033[0m This is the request, as the plane states it:\n\n' >&2
grep -F "$id" "$work/pending.out" >&2 || true
if [ "$any_approver" = yes ]; then
  printf '\n   An approver decides it, either in the Slack channel this cluster posts to:\n\n' >&2
  printf '       @kaimahi approve %s uses=%s ttl=10m\n\n' "${id%%-*}" "$uses" >&2
  printf '   or from the operator'"'"'s chair:\n\n' >&2
  printf '       kmx approve %s --uses %s --ttl 10m\n\n' "$id" "$uses" >&2
  # This script is holding a port-forward on ADMIN_PORT while it waits,
  # so an operator command on the same port meets "address already in
  # use". Name a free one for them rather than let them discover it.
  printf '   (if that reports the admin port is in use, add ADMIN_PORT=%s)\n\n' \
    "$(( ${ADMIN_PORT:-19091} + 100 ))" >&2
  printf '   No named approver was required, so this wait does NOT check who decided —\n' >&2
  printf '   only that THIS call was approved and that the grant is new.\n\n' >&2
else
  printf '\n   In the Slack channel this cluster posts to, %s types:\n\n' "$user" >&2
  printf '       @kaimahi approve %s uses=%s ttl=10m\n\n' "${id%%-*}" "$uses" >&2
fi
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
  echo "  Not claiming an approval. Check that the Slack app's Request URL points" >&2
  echo "  at this cluster's edge, and that $user is in the approver file" >&2
  echo "  (make slack-approvers)." >&2
  exit 1
fi

# Decided is not approved; approved is not approved BY THAT PERSON; and
# approved by that person is not approved FOR THIS CALL. All three are
# read from the plane's own records rather than inferred from the request
# having left the pending list — a denial empties it too.
#
# The last of the three is the one that matters most here, because two
# requests for the same tool can be pending at once (a grant binds to one
# call, not to its verb, and that is the normal case in these
# scenarios). Without it, a
# human who approved the OTHER request and denied this one would satisfy
# every other check.
admin approval-audit "$CRED_AP" > "$work/audit.out"
who="slack:$user"
[ "$any_approver" = yes ] && who=""
if ! awk -v cred="$CRED_AP" -v subj="$subject" -v who="$who" -v want="$want_call" \
  '$2==cred && $3=="tool" && $4==subj && $5=="approved" && (who=="" || $6==who) \
   && substr($0, length($0) - length(want) + 1) == want {found=1} END{exit !found}' \
  "$work/audit.out"; then
  cat "$work/audit.out" >&2
  echo "await-approval: no 'approved' row${who:+ by $who} for this exact call:" >&2
  echo "    $want_call" >&2
  echo "  The request was decided, but not approved by that person for that call —" >&2
  echo "  treated as a failure rather than continuing on an approval of something" >&2
  echo "  else." >&2
  exit 1
fi

# The grant this wait produced: NEW (not in the snapshot above), LIVE, and
# decided by that person. Requiring all three is what makes the caller's
# next call — which spends this grant — the one the human actually
# authorised, rather than any grant that happens to be lying around.
admin grants "$CRED_AP" > "$work/grants.out"
awk -v cred="$CRED_AP" -v subj="$subject" -v who="$who" \
  '$2==cred && $3=="tool" && $4==subj && $5=="yes" && (who=="" || $10==who) {print $1}' \
  "$work/grants.out" | sort > "$work/grant-ids-after"
if [ -z "$(comm -13 "$work/grant-ids-before" "$work/grant-ids-after")" ]; then
  cat "$work/grants.out" >&2
  echo "await-approval: no NEW live grant for $subject${who:+ decided by $who}." >&2
  echo "  A grant that already existed before this wait does not show that the" >&2
  echo "  decision just made is the one about to be spent. Not continuing." >&2
  exit 1
fi

printf '   \033[1m%s approved%s\033[0m — recorded by the plane, with a grant.\n' \
  "$subject" "${who:+ by $who}" >&2
