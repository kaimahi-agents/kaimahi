#!/usr/bin/env bash
# Tear down everything scripts/aks-up.sh created: the AKS cluster, the
# private ACR, and the resource group holding them — plus the kubeconfig
# entries, so a dead context cannot be targeted later.
#
# This is the most dangerous command in the repo, so it is the most
# suspicious one. `az group delete` is recursive and irreversible, and a
# subscription typically holds many groups that are emphatically not
# ours. Two independent gates therefore stand in front of it:
#
#   1. TAG PROOF. The group must carry the tag scripts/aks-up.sh sets.
#      A group we did not create cannot be deleted by this script at all,
#      no matter what is typed or confirmed. This is the gate that makes a
#      typo'd group name harmless instead of catastrophic.
#   2. EXPLICIT CONFIRMATION naming the group (KAIMAHI_CONFIRM, or an
#      interactive answer). Fail closed: no TTY and no KAIMAHI_CONFIRM
#      means no deletion.
#
#   AKS_RESOURCE_GROUP   required   the group to delete
#   AKS_CLUSTER          optional   kube-context to remove (default kaimahi)
set -euo pipefail

RG="${AKS_RESOURCE_GROUP:-}"
CLUSTER="${AKS_CLUSTER:-kaimahi}"

# Must match scripts/aks-up.sh.
OWNER_TAG_KEY=kaimahi-ephemeral
OWNER_TAG_VALUE=p5b

[ -n "$RG" ] || {
  echo "aks-down: AKS_RESOURCE_GROUP is required" >&2
  echo "usage: AKS_RESOURCE_GROUP=<rg> [AKS_CLUSTER=<name>] $0" >&2
  exit 1; }

command -v az >/dev/null 2>&1 || { echo "aks-down: the az CLI is not installed" >&2; exit 1; }
az account show >/dev/null 2>&1 || {
  echo "aks-down: not logged in — run: az login" >&2; exit 1; }

# `az group exists` prints true/false on stdout and nothing there when the
# call itself fails. Piping into `grep -qx true` collapses "does not exist"
# and "could not ask" into the same answer — and in THIS script that
# mistake reports a teardown that never happened: exit 0, "nothing to
# delete", kubeconfig entries removed (taking away the easiest way to
# notice), and a cluster quietly still billing. Accept only a well-formed
# positive.
group_exists() { # <rg> -> prints true|false; nonzero if the answer is unusable
  local out
  out=$(az group exists --name "$1" 2>/dev/null) || return 1
  case "$out" in
    true | false) printf '%s' "$out" ;;
    *) return 1 ;;
  esac
}

if ! rg_state=$(group_exists "$RG"); then
  echo "aks-down: cannot determine whether resource group '$RG' exists." >&2
  echo "  NOT reporting a teardown that may not have happened. Check your" >&2
  echo "  credentials ('az account show') and re-run — if the cluster is" >&2
  echo "  still there it is still billing." >&2
  exit 1
fi

if [ "$rg_state" = false ]; then
  echo "aks-down: resource group '$RG' does not exist — nothing to delete." >&2
  # Still clean up any stale kubeconfig entries for the cluster name.
  kubectl config delete-context "$CLUSTER" >/dev/null 2>&1 || true
  kubectl config delete-cluster "$CLUSTER" >/dev/null 2>&1 || true
  kubectl config delete-user "clusterUser_${RG}_${CLUSTER}" >/dev/null 2>&1 || true
  exit 0
fi

# --- gate 1: tag proof ----------------------------------------------------
tag=$(az group show --name "$RG" --query "tags.\"$OWNER_TAG_KEY\"" -o tsv 2>/dev/null || true)
if [ "$tag" != "$OWNER_TAG_VALUE" ]; then
  echo "aks-down: REFUSING to delete resource group '$RG'." >&2
  echo "  It does not carry $OWNER_TAG_KEY=$OWNER_TAG_VALUE, so this script did" >&2
  echo "  not create it. Nothing was deleted." >&2
  exit 1
fi

echo "----------------------------------------------------------------" >&2
echo "  about to DELETE resource group: $RG" >&2
echo "  tagged:   $OWNER_TAG_KEY=$OWNER_TAG_VALUE (created by scripts/aks-up.sh)" >&2
echo "  contains:" >&2
az resource list --resource-group "$RG" --query "[].{type:type,name:name}" -o tsv 2>/dev/null |
  sed 's/^/    /' >&2 || true
echo "  This is irreversible." >&2
echo "----------------------------------------------------------------" >&2

# --- gate 2: explicit confirmation ---------------------------------------
if [ -n "${KAIMAHI_CONFIRM:-}" ]; then
  if [ "$KAIMAHI_CONFIRM" != "$RG" ]; then
    echo "aks-down: KAIMAHI_CONFIRM does not name this resource group — refusing." >&2
    echo "  to proceed:  KAIMAHI_CONFIRM=$RG make aks-down" >&2
    # The likeliest cause, called out by name: the runbook has you export
    # KAIMAHI_CONFIRM=<cluster> once for the session, which is what the
    # context guard wants. Deleting a whole resource group is a bigger act
    # than applying to a context, so it takes its own confirmation naming
    # the GROUP — a session-wide "yes" to one cluster is not consent to
    # destroy everything around it.
    if [ "$KAIMAHI_CONFIRM" = "$CLUSTER" ]; then
      echo "  (KAIMAHI_CONFIRM currently names the CLUSTER '$CLUSTER' — probably" >&2
      echo "   the session-wide export. Teardown needs the RESOURCE GROUP.)" >&2
    fi
    exit 1
  fi
  echo "aks-down: confirmed via KAIMAHI_CONFIRM." >&2
elif [ -t 0 ]; then
  printf 'Type the resource group name to delete it (anything else aborts): ' >&2
  IFS= read -r answer || answer=""
  if [ "$answer" != "$RG" ]; then
    echo "aks-down: not confirmed — nothing was deleted." >&2
    exit 1
  fi
else
  echo "aks-down: no TTY and no KAIMAHI_CONFIRM — refusing to delete unattended." >&2
  echo "  to proceed:  KAIMAHI_CONFIRM=$RG make aks-down" >&2
  exit 1
fi

# Deliberately NOT --no-wait: teardown is only reportable once it is done,
# and "I asked Azure to delete it" is not the same claim as "it is gone".
echo "aks-down: deleting '$RG' (waiting for completion — this takes a few minutes)" >&2
az group delete --name "$RG" --yes --output none

# Fail closed on the claim itself: confirm the group is actually gone
# before saying so — and an unreadable answer is not a confirmation, for
# the same reason as above.
if ! rg_state=$(group_exists "$RG"); then
  echo "aks-down: delete returned, but the group's state could not be" >&2
  echo "  re-checked — not claiming it is gone. Verify with:" >&2
  echo "    az group exists --name $RG" >&2
  exit 1
fi
if [ "$rg_state" = true ]; then
  echo "aks-down: resource group '$RG' still exists after delete returned." >&2
  exit 1
fi

# Kubeconfig hygiene: a context pointing at a deleted cluster is a live
# foot-gun for the next `make` invocation.
kubectl config delete-context "$CLUSTER" >/dev/null 2>&1 || true
kubectl config delete-cluster "$CLUSTER" >/dev/null 2>&1 || true
kubectl config delete-user "clusterUser_${RG}_${CLUSTER}" >/dev/null 2>&1 || true
# `az aks get-credentials` made this cluster the current context, and
# delete-context leaves that pointer dangling. Unset it so
# a bare kubectl says "no current context" rather than naming a cluster
# that no longer exists — and never re-point it at something else.
if [ "$(kubectl config current-context 2>/dev/null || true)" = "$CLUSTER" ]; then
  kubectl config unset current-context >/dev/null 2>&1 || true
fi

echo "aks-down: resource group '$RG' deleted; kubeconfig entries removed." >&2
