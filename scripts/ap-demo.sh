#!/usr/bin/env bash
# The accounts-payable exception scenario, end to end (docs/ap-demo.md).
#
# Three outcomes in one run, on the same credential, with nothing
# reconfigured between them:
#
#   1. a ROUTINE invoice pays itself under the standing constraint, with
#      no human in the loop, audited like any allowed call;
#   2. the EXCEPTION invoice's correct payment is above the bound, so it
#      is denied, files a request carrying the amount and the payee, and
#      proceeds only under an approval welded to that exact call;
#   3. the dispute and the vendor notice are outside the allowlist too, so
#      each is denied on its first attempt, files its OWN request, and
#      needs its OWN approval and its OWN grant. One approval never covers
#      the sequence: a grant is welded to one call.
#
# The ERP is fixtures. The denials, the approvals, the grants and the
# audit are the real plane.
#
# The agent's investigation is run first when the agent is deployed, and
# it is INFORMATIONAL — printed, never asserted on. Every assertion below
# is made against the gateway's own decisions with fixed arguments, so a
# model that phrases things differently, or reaches the wrong number, can
# change what this prints and cannot change what it proves. (A model that
# does attempt a consequential call simply files the same request first;
# the filing dedupes on the call.)
#
# Usage:  make ap-demo [SLACK_USER=U0EXAMPLE]
#   SLACK_USER      the Slack user id that approves. CI uses the invented
#                   U0CIAPPROVER and the mention is SYNTHESISED, correctly
#                   signed, by scripts/slack-mention-probe.sh.
#   AP_HUMAN=1      with SLACK_USER, do not synthesise anything: print the
#                   line and WAIT for that person to type it in Slack
#                   (scripts/await-approval.sh). This is the only
#                   honest setting against a real workspace — a signed
#                   event forged in a named colleague's name would prove
#                   nothing about a human approving a payment.
#   AP_AGENT_TURN=0 skip the agent's investigation entirely.
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
CRED_AP="${CRED_AP:-ap-agent}"
SLACK_USER="${SLACK_USER:-}"
AP_HUMAN="${AP_HUMAN:-0}"
AP_AGENT_TURN="${AP_AGENT_TURN:-1}"
# The chat command, handed down by the Makefile so the agent turn lands
# on the SAME cluster as everything else — a bare `make chat` here would
# use the default KIND_CLUSTER whatever the caller asked for. Word
# splitting is deliberate.
AP_CHAT="${AP_CHAT:-make chat AGENT=ap-agent}"
export KUBECTL

here="$(cd "$(dirname "$0")" && pwd)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The whole scenario runs on one credential and one upstream.
export GOVERNED_SECRET=kaimahi-ap-token UPSTREAM=erp

# The corpus's numbers, in whole cents, named once. They are the demo:
# 3255000 + 945000 + 600000 = 4800000, and the reader can check it.
ROUTINE_INVOICE=INV-88121; ROUTINE_PAYEE=HAR-2088;  ROUTINE_CENTS=412000
EXC_INVOICE=INV-88134;     EXC_PAYEE=MER-4471;      EXC_PAY_CENTS=3255000
EXC_HELD_CENTS=945000;     EXC_FEE_CENTS=600000;    EXC_TOTAL_CENTS=4800000
EXC_VENDOR=MER-4471

step() { printf '\n\033[1m== %s\033[0m\n' "$*" >&2; }
note() { printf '   %s\n' "$*" >&2; }
fail() { printf '\nap-demo: %s\n' "$*" >&2; exit 1; }

# The agent's turn is one large A2A task object. Print the reply and the
# tool calls it made — the story — rather than the whole JSON, which is
# what `make chat` shows and is unreadable in a demo.
show_turn() {
  python3 - "$1" <<'EOF' || tail -20 "$1"
import json, re, sys
raw = open(sys.argv[1]).read()
m = re.search(r"^\{.*\}$", raw, re.M | re.S)
if not m:
    sys.exit("no task object in the agent's output")
d = json.loads(m.group(0))
for msg in d.get("history", []):
    for p in msg.get("parts", []):
        if (p.get("metadata") or {}).get("kagent_type") == "function_call":
            print("   tool call: %s %s" % (p["data"]["name"], json.dumps(p["data"].get("args", {}))))
print("   state: %s" % d.get("status", {}).get("state"))
for a in d.get("artifacts", []):
    for p in a.get("parts", []):
        if p.get("text"):
            print("\n" + p["text"].strip() + "\n")
EOF
}

call()  { bash "$here/tool-call-probe.sh" "$1" "$2"; }
deny()  { bash "$here/tool-denial-probe.sh" "$1" "$2"; }
admin() { bash "$here/plane-admin.sh" "$@"; }
audit() { admin tool-audit "$CRED_AP"; }

# request_id <tool> <summary substring> -> the pending request for THAT
# call. Selected by its summary, never by position: several requests for
# the SAME tool can be pending at once, because a grant binds to one call's
# arguments and not to its verb.
#
# The substring must name EVERY policy-relevant field of the call, not
# just the memorable ones. A grant is welded to the exact call, so
# a selector that is less specific than the digest can pick a DIFFERENT
# pending request that happens to share the fields it does name — and
# then a human approves one call while the script makes another. That is
# not hypothetical: on the first live AKS run the agent's own turn had
# filed payment_schedule for INV-88140 at the same amount and payee as
# the scripted INV-88134 call, and the approval landed on the wrong one.
# CI never saw it because CI runs with AP_AGENT_TURN=0.
request_id() {
  admin approvals > "$work/approvals.out"
  awk -v cred="$CRED_AP" -v tool="$1" -v want="$2" \
    '$3==cred && $4=="tool" && $5==tool && index($0, want) {print $1; exit}' "$work/approvals.out"
}

# approve <id> [uses] — three ways in, all minting the same call-bound
# grant, differing only in who decides and how the decision arrives:
#
#   AP_HUMAN=1 + SLACK_USER   a real person types it in a real Slack; this
#                             script only waits and verifies.
#   SLACK_USER                a SYNTHETIC, correctly signed app_mention as
#                             that id (kind and CI, where Slack cannot
#                             reach the cluster).
#   neither                   the admin bearer.
approve() {
  local id=$1 uses=${2:-1}
  if [ -n "$SLACK_USER" ] && [ "$AP_HUMAN" = 1 ]; then
    CRED_AP="$CRED_AP" bash "$here/await-approval.sh" "$id" "$SLACK_USER" "$uses"
  elif [ -n "$SLACK_USER" ]; then
    WANT="approved request $id" bash "$here/slack-mention-probe.sh" \
      "$SLACK_USER" "approve ${id%%-*} uses=$uses ttl=10m"
  else
    admin approve "$id" 10m "$uses" -
  fi
}

# --- 0. the arithmetic, stated before anything runs ----------------------
step "The exception, in whole cents (k8s/erp-fixtures.json)"
cat >&2 <<TXT
   $EXC_INVOICE from Meridian Industrial Supply ($EXC_VENDOR) bills \$48,000.00:
     400 valve assemblies x \$105.00                   \$42,000.00
     "expedited handling"                               \$6,000.00
   PO-2291 ordered 400 at \$105.00 and authorizes NO fee.
   RCV-2291-A shows 310 received, 90 backordered.
   The contract requires prior WRITTEN authorization for any fee.
   Policy: pay the received quantity, hold the rest, dispute the fee,
   and anything over \$10,000.00 needs a human.
     pay      310 x \$105.00 = \$32,550.00   ($EXC_PAY_CENTS cents)
     hold      90 x \$105.00 =  \$9,450.00   ($EXC_HELD_CENTS cents)
     dispute                  =  \$6,000.00   ($EXC_FEE_CENTS cents)
                               ------------
                                \$48,000.00   ($EXC_TOTAL_CENTS cents)
TXT

# --- 1. the investigation (informational) --------------------------------
if [ "$AP_AGENT_TURN" = 1 ] && $KUBECTL -n kagent get agent ap-agent >/dev/null 2>&1; then
  step "The agent investigates $EXC_INVOICE (informational — nothing below asserts on it)"
  # shellcheck disable=SC2086 # AP_CHAT is a command line, not a word
  if ! $AP_CHAT TASK="Investigate invoice $EXC_INVOICE and resolve it." > "$work/chat.out" 2>&1; then
    note "the agent's turn did not complete; the scenario continues without it"
  fi
  show_turn "$work/chat.out" >&2 || true
  note "Whatever it concluded, it cannot act on it without what follows."
else
  step "No agent turn (AP_AGENT_TURN=$AP_AGENT_TURN, or ap-agent is not deployed) — the scenario below does not need one"
fi

# --- 2. routine: the standing constraint does the work, silently ---------
step "A routine invoice: $ROUTINE_INVOICE, \$4,120.00, complete delivery, no fees"
before=$(admin approvals | grep -c "payment_schedule" || true)
call payment_schedule \
  "{\"invoice_id\": \"$ROUTINE_INVOICE\", \"amount_cents\": $ROUTINE_CENTS, \"payee_id\": \"$ROUTINE_PAYEE\"}"
audit > "$work/audit-routine.out"
grep -E "$CRED_AP +erp +tools/call +payment_schedule +allowed +200 +within standing constraint" \
  "$work/audit-routine.out" \
  || { cat "$work/audit-routine.out" >&2; fail "the routine payment was not admitted by the standing constraint"; }
after=$(admin approvals | grep -c "payment_schedule" || true)
[ "$before" = "$after" ] || fail "the routine payment asked a human — the constraint is not doing its job"
note "Paid. No approval request, no grant, no human. Audited with the constraint named."

# --- 3. the exception: above the bound, so a human decides ---------------
step "The exception: paying \$32,550.00 on $EXC_INVOICE is above the \$10,000.00 bound"
deny payment_schedule \
  "{\"invoice_id\": \"$EXC_INVOICE\", \"amount_cents\": $EXC_PAY_CENTS, \"payee_id\": \"$EXC_PAYEE\"}"
pay_id=$(request_id payment_schedule "invoice_id $EXC_INVOICE, amount_cents $EXC_PAY_CENTS, payee_id $EXC_PAYEE")
[ -n "$pay_id" ] || { cat "$work/approvals.out" >&2; fail "no request was filed for the \$32,550.00 payment"; }
note "Request $pay_id — and what a human is asked is the TRANSACTION:"
grep -F "$pay_id" "$work/approvals.out" >&2

step "The approver decides that call"
approve "$pay_id"
admin grants > "$work/grants-pay.out"
grep -E "$CRED_AP +tool +payment_schedule +yes .*call [0-9a-f]{12}" "$work/grants-pay.out" \
  || { cat "$work/grants-pay.out" >&2; fail "the grant is not welded to a call"; }

step "The approved payment proceeds — and only it"
call payment_schedule \
  "{\"invoice_id\": \"$EXC_INVOICE\", \"amount_cents\": $EXC_PAY_CENTS, \"payee_id\": \"$EXC_PAYEE\"}"
audit > "$work/audit-pay.out"
grep -E "$CRED_AP +erp +tools/call +payment_schedule +allowed +200 +granted .*amount_cents $EXC_PAY_CENTS" \
  "$work/audit-pay.out" \
  || { cat "$work/audit-pay.out" >&2; fail "the approved payment was not admitted under its grant"; }
note "The denial row and this row carry the same digest: the call a human"
note "approved is provably the call that ran."

# --- 4. the dispute needs its own approval ------------------------------
step "The \$6,000.00 fee: dispute_open is on no allowlist, so it is denied too"
deny dispute_open "{\"invoice_id\": \"$EXC_INVOICE\", \"amount_cents\": $EXC_FEE_CENTS, \"reason\": \"expedite fee not authorized on PO-2291\"}"
dis_id=$(request_id dispute_open "invoice_id $EXC_INVOICE, amount_cents $EXC_FEE_CENTS")
[ -n "$dis_id" ] || { cat "$work/approvals.out" >&2; fail "no request was filed for the dispute"; }
[ "$dis_id" != "$pay_id" ] || fail "the dispute reused the payment's request"
note "Its OWN request $dis_id — the payment's approval does not cover it."
approve "$dis_id"
call dispute_open "{\"invoice_id\": \"$EXC_INVOICE\", \"amount_cents\": $EXC_FEE_CENTS, \"reason\": \"expedite fee not authorized on PO-2291\"}"

# --- 5. and so does the vendor notice ------------------------------------
step "Telling the vendor: vendor_notify is denied, filed and approved on its own"
deny vendor_notify "{\"vendor_id\": \"$EXC_VENDOR\", \"message\": \"Paying 32550.00 against $EXC_INVOICE for 310 units received. 9450.00 held pending delivery of 90 backordered units. 6000.00 expedite fee disputed: not authorized on PO-2291.\"}"
ven_id=$(request_id vendor_notify "vendor_id $EXC_VENDOR")
[ -n "$ven_id" ] || { cat "$work/approvals.out" >&2; fail "no request was filed for the vendor notice"; }
[ "$ven_id" != "$dis_id" ] || fail "the vendor notice reused the dispute's request"
approve "$ven_id"
call vendor_notify "{\"vendor_id\": \"$EXC_VENDOR\", \"message\": \"Paying 32550.00 against $EXC_INVOICE for 310 units received. 9450.00 held pending delivery of 90 backordered units. 6000.00 expedite fee disputed: not authorized on PO-2291.\"}"

# --- 6. the receipts -----------------------------------------------------
step "Three denials, three approvals, three grants — the whole chain"
admin grants > "$work/grants.out"
cat "$work/grants.out" >&2
n=$(awk -v cred="$CRED_AP" '$2==cred && $3=="tool" {print}' "$work/grants.out" | wc -l)
[ "$n" -ge 3 ] || fail "expected a grant per approved call, found $n"

step "The approvals audit — who decided what, and which transaction"
admin approval-audit "$CRED_AP" >&2

step "The tool audit — every decision this credential got"
audit >&2

printf '\n\033[1map-demo: the routine invoice paid itself; the exception needed a named human;\n' >&2
printf 'each consequential call needed its own approval. Nothing was reconfigured.\033[0m\n' >&2
