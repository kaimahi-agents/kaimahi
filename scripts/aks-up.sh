#!/usr/bin/env bash
# Provision the Azure side of the managed-cluster path: a resource
# group, a PRIVATE Azure Container Registry, and an AKS cluster with pull
# rights on that registry.
#
# Everything is parameterised. No subscription, tenant, resource-group,
# registry or cluster identifier belongs in this repo — the repo is
# public, and a committed name is a standing invitation to squat it or to
# fingerprint the owner. You supply them:
#
#   AKS_RESOURCE_GROUP   required   resource group to CREATE (must not exist)
#   ACR_NAME             required   globally-unique registry name (5-50 alnum)
#   AKS_CLUSTER          optional   cluster + kube-context name (default kaimahi)
#   AKS_LOCATION         optional   default westus3
#   AKS_NODE_SIZE        optional   default Standard_B4ms
#   AKS_NODE_COUNT       optional   default 1
#   AKS_NETWORK_POLICY   optional   cilium (default) | azure | calico — see below
#
# NetworkPolicy enforcement is NOT a given on AKS. `az aks create` with no
# `--network-policy` builds a cluster whose CNI ignores NetworkPolicy
# objects entirely: the plane's policies (k8s/plane/network-policy.yaml)
# would be present and inert, which reads as protection and is worse than
# none. So this script ALWAYS provisions a policy
# engine, and refuses a value that would not enforce:
#
#   cilium  (default) Azure CNI Overlay powered by Cilium. Microsoft's
#           recommendation for new clusters, eBPF dataplane, and the
#           engine the other two are being retired in favour of (Azure
#           NPM on Linux: end of support 2028-09-30; kubenet: 2028-03-31).
#           Verified enforcing the plane's whole matrix on 2026-09-01.
#   azure   Azure Network Policy Manager (iptables). Retiring; accepted
#           for clusters that need it, not recommended.
#   calico  Azure-managed Calico. Accepted; not exercised here.
#
# All three ride Azure CNI Overlay (--network-plugin azure
# --network-plugin-mode overlay): kubenet is retiring and Azure NPM never
# supported it. An EXISTING cluster is not migrated: if the cluster is
# already there on a different engine (or none) this script refuses
# rather than reimaging every node pool behind your back — the cluster is
# ephemeral, so the honest fix is aks-down and a fresh create.
#
# Cost shape (see docs/aks.md): control plane on the Free tier is
# $0; the node and a Standard load balancer are the running cost; ACR
# Basic is a small daily charge. This is an EPHEMERAL cluster — create it,
# prove the thing, run scripts/aks-down.sh.
#
# Safety: this script CREATES a resource group and refuses to adopt one it
# did not create. Everything it makes is tagged, and scripts/aks-down.sh
# deletes only what carries that tag — so a mistyped group name can never
# turn teardown into someone else's outage.
set -euo pipefail
umask 077

RG="${AKS_RESOURCE_GROUP:-}"
ACR="${ACR_NAME:-}"
CLUSTER="${AKS_CLUSTER:-kaimahi}"
LOCATION="${AKS_LOCATION:-westus3}"
NODE_SIZE="${AKS_NODE_SIZE:-Standard_B4ms}"
NODE_COUNT="${AKS_NODE_COUNT:-1}"
# `-` not `:-`: an EXPLICITLY empty AKS_NETWORK_POLICY must reach the
# refusal below with its message, not be silently swapped for the default.
NETWORK_POLICY="${AKS_NETWORK_POLICY-cilium}"

# The tag that makes teardown safe. Must match scripts/aks-down.sh.
OWNER_TAG_KEY=kaimahi-ephemeral
OWNER_TAG_VALUE=p5b

usage() {
  echo "usage: AKS_RESOURCE_GROUP=<new-rg> ACR_NAME=<unique-name> $0" >&2
  echo "  optional: AKS_CLUSTER AKS_LOCATION AKS_NODE_SIZE AKS_NODE_COUNT" >&2
  echo "            AKS_NETWORK_POLICY=cilium|azure|calico (default cilium)" >&2
}

[ -n "$RG" ] || { echo "aks-up: AKS_RESOURCE_GROUP is required" >&2; usage; exit 1; }
[ -n "$ACR" ] || { echo "aks-up: ACR_NAME is required" >&2; usage; exit 1; }

# ACR names are globally unique, alphanumeric only, 5-50 chars. Check
# locally so a bad name fails in a second rather than after the group and
# cluster already exist.
case "$ACR" in
  *[!a-zA-Z0-9]*) echo "aks-up: ACR_NAME must be alphanumeric only" >&2; exit 1 ;;
esac
if [ "${#ACR}" -lt 5 ] || [ "${#ACR}" -gt 50 ]; then
  echo "aks-up: ACR_NAME must be 5-50 characters" >&2
  exit 1
fi

# A policy ENGINE is mandatory. "none" and "" are the AKS default and are
# exactly the case this script exists to prevent, so they are refused
# rather than accepted as "the operator knows best": a cluster without an
# engine would deploy the plane's policies and enforce nothing.
case "$NETWORK_POLICY" in
  cilium | azure | calico) ;;
  *)
    echo "aks-up: AKS_NETWORK_POLICY='$NETWORK_POLICY' is not a policy engine." >&2
    echo "  Accepted: cilium (default), azure, calico. Without an engine AKS" >&2
    echo "  ignores NetworkPolicy and the plane's boundary is inert." >&2
    exit 1 ;;
esac
# The flags each engine needs (Azure docs, use-network-policies, 2026-08).
# Cilium is "Azure CNI Overlay powered by Cilium": the dataplane flag is
# what installs it; the policy flag is what turns enforcement on.
network_flags=(--network-plugin azure --network-plugin-mode overlay
  --network-policy "$NETWORK_POLICY")
[ "$NETWORK_POLICY" = cilium ] && network_flags+=(--network-dataplane cilium)

command -v az >/dev/null 2>&1 || { echo "aks-up: the az CLI is not installed" >&2; exit 1; }
az account show >/dev/null 2>&1 || {
  echo "aks-up: not logged in — run: az login" >&2; exit 1; }

# `az group exists` prints true/false on stdout, but on failure (expired
# token, throttled ARM call) it prints nothing there and errors on stderr.
# Piping it straight into `grep -qx true` cannot tell "does not exist" from
# "could not ask" — and getting that backwards here is the worst bug in the
# repo: the caller would fall through to `az group create`, which is
# idempotent-by-UPDATE and would stamp our ephemeral tag onto a
# pre-existing group. That tag is the only thing standing between someone
# else's resource group and `aks-down.sh`.
#
# So: accept only a well-formed positive (standing guidance). Anything that
# is not literally "true" or "false" is an error, not a "no".
group_exists() { # <rg> -> prints true|false; nonzero if the answer is unusable
  local out
  out=$(az group exists --name "$1" 2>/dev/null) || return 1
  case "$out" in
    true | false) printf '%s' "$out" ;;
    *) return 1 ;;
  esac
}

# --- resource group -------------------------------------------------------
# Refuse to touch a pre-existing group. Adopting one would mean teardown
# later deletes resources this script never created.
if ! rg_state=$(group_exists "$RG"); then
  echo "aks-up: cannot determine whether resource group '$RG' exists." >&2
  echo "  Refusing to continue: guessing 'no' here would create-and-TAG a" >&2
  echo "  group that may already belong to someone else. Check 'az account" >&2
  echo "  show' / your credentials and re-run." >&2
  exit 1
fi

if [ "$rg_state" = true ]; then
  existing=$(az group show --name "$RG" \
    --query "tags.\"$OWNER_TAG_KEY\"" -o tsv 2>/dev/null || true)
  if [ "$existing" != "$OWNER_TAG_VALUE" ]; then
    echo "aks-up: resource group '$RG' already exists and is not tagged" >&2
    echo "  $OWNER_TAG_KEY=$OWNER_TAG_VALUE. Refusing to build inside a group" >&2
    echo "  this script did not create — pick a fresh AKS_RESOURCE_GROUP." >&2
    exit 1
  fi
  echo "aks-up: reusing the ephemeral group '$RG' (tagged, created by this script)" >&2
else
  echo "aks-up: creating resource group '$RG' in $LOCATION" >&2
  az group create --name "$RG" --location "$LOCATION" \
    --tags "$OWNER_TAG_KEY=$OWNER_TAG_VALUE" \
    --output none
fi

# --- registry -------------------------------------------------------------
# PRIVATE by design: --admin-enabled is left off, so the only way in
# is Entra auth. Publishing a public image would be an outward-facing
# artifact and a soft claim on a provisional project name.
if az acr show --name "$ACR" --resource-group "$RG" >/dev/null 2>&1; then
  echo "aks-up: registry '$ACR' already present" >&2
else
  echo "aks-up: creating private ACR '$ACR' (Basic, admin user disabled)" >&2
  az acr create --name "$ACR" --resource-group "$RG" --location "$LOCATION" \
    --sku Basic --output none
fi

# --- cluster --------------------------------------------------------------
# What the control plane reports as its policy engine. Only a well-formed
# answer counts (standing guidance): an errored query must never read as
# "no engine" — or as "the right one".
cluster_policy() { # -> prints the engine ('' when none); nonzero if unusable
  local out
  out=$(az aks show --name "$CLUSTER" --resource-group "$RG" \
    --query 'networkProfile.networkPolicy' -o tsv 2>/dev/null) || return 1
  case "$out" in
    '' | none | cilium | azure | calico) printf '%s' "$out" ;;
    *) return 1 ;;
  esac
}

# Existence is asked the same way as group_exists: a NotFound is "no", a
# success is "yes", and anything else (throttled, expired token, ARM
# hiccup) is an ERROR — not a "no". Falling through to `az aks create`
# on a misread would PUT the full spec, network flags included, onto a
# cluster that exists: the very migration the refusal below promises
# never to perform.
cluster_exists() { # -> prints true|false; nonzero if the answer is unusable
  local err
  if err=$(az aks show --name "$CLUSTER" --resource-group "$RG" --output none 2>&1); then
    printf 'true'
  elif printf '%s' "$err" | grep -q 'NotFound'; then
    printf 'false'
  else
    return 1
  fi
}
if ! cluster_state=$(cluster_exists); then
  echo "aks-up: cannot determine whether cluster '$CLUSTER' exists — refusing to" >&2
  echo "  guess: a create aimed at an existing cluster would rewrite its network" >&2
  echo "  profile. Check 'az account show' and re-run (re-running is safe)." >&2
  exit 1
fi

if [ "$cluster_state" = true ]; then
  echo "aks-up: cluster '$CLUSTER' already present" >&2
  # NOT migrated. `az aks update --network-policy` exists for azure/calico
  # but reimages every node pool at once, and moving to cilium is a
  # dataplane upgrade with its own prerequisites. Neither belongs behind a
  # script whose contract is "create". Refuse, and say what the cluster
  # actually has, so a cluster created before this script demanded an
  # engine cannot be mistaken for an enforcing one just because this
  # script now asks for one.
  if ! have=$(cluster_policy); then
    echo "aks-up: cannot read the existing cluster's network policy engine —" >&2
    echo "  refusing to assume it enforces. Check: az aks show ... --query networkProfile" >&2
    exit 1
  fi
  if [ "$have" != "$NETWORK_POLICY" ]; then
    echo "aks-up: existing cluster '$CLUSTER' has network policy engine" >&2
    echo "  '${have:-none}', not '$NETWORK_POLICY'. Existing clusters are NOT migrated." >&2
    if [ -z "$have" ] || [ "$have" = none ]; then
      echo "  With no engine, AKS ignores NetworkPolicy: the plane's boundary would" >&2
      echo "  be present and inert. Tear it down (make aks-down) and re-create." >&2
    else
      echo "  Re-run with AKS_NETWORK_POLICY=$have to keep it, or re-create it." >&2
    fi
    exit 1
  fi
else
  echo "aks-up: creating AKS '$CLUSTER' ($NODE_COUNT x $NODE_SIZE, Free tier," >&2
  echo "  NetworkPolicy engine: $NETWORK_POLICY)" >&2
  echo "  this takes a few minutes..." >&2
  # --tier free: no control-plane charge and no SLA, which is right for an
  #   ephemeral demo cluster.
  # --attach-acr: grants the kubelet identity AcrPull on the registry, so
  #   the proxy image is pulled with no imagePullSecret anywhere.
  # --node-osdisk-size 32: the smallest managed OS disk that fits; nothing
  #   here is stored on the node.
  az aks create \
    --name "$CLUSTER" --resource-group "$RG" --location "$LOCATION" \
    --node-count "$NODE_COUNT" --node-vm-size "$NODE_SIZE" \
    --node-osdisk-size 32 \
    --tier free \
    --generate-ssh-keys \
    --attach-acr "$ACR" \
    "${network_flags[@]}" \
    --output none
fi

# The claim this script exists for, read back from the control plane rather
# than inferred from a create that returned 0. Enforcement itself is a
# CNI property the API server cannot vouch for; that proof is
# `TARGET=aks make netpol-verify`, which the next-steps text below insists
# on. This check catches the cheaper failure: the flag not taking.
if ! have=$(cluster_policy); then
  echo "aks-up: the cluster's network policy engine could not be read." >&2
  echo "  Not claiming an enforcing cluster. Re-run once 'az aks show' works." >&2
  exit 1
fi
if [ "$have" != "$NETWORK_POLICY" ]; then
  echo "aks-up: the cluster does not report network policy engine '$NETWORK_POLICY'" >&2
  echo "  (reports '${have:-none}'). Not claiming an enforcing cluster." >&2
  exit 1
fi
echo "aks-up: network policy engine '$NETWORK_POLICY' confirmed by the control plane" >&2

# Whether the cluster was just created or already existed, the kubelet
# identity must actually hold AcrPull on the registry — without it the
# proxy pod fails with ImagePullBackOff and the cause is two layers away
# from the symptom.
#
# VERIFY, then repair. `az aks update --attach-acr` is idempotent but runs
# a full cluster update (~2 min) even when nothing needs doing, so a blind
# re-run taxes every invocation. Reading the role assignment is seconds,
# and it checks the thing that actually matters rather than assuming a
# command that returned 0 produced the effect (standing guidance: accept
# only a well-formed positive).
echo "aks-up: checking AcrPull for the cluster's kubelet identity" >&2
acr_id=$(az acr show --name "$ACR" --resource-group "$RG" --query id -o tsv)
kubelet_oid=$(az aks show --name "$CLUSTER" --resource-group "$RG" \
  --query identityProfile.kubeletidentity.objectId -o tsv)
if [ -z "$acr_id" ] || [ -z "$kubelet_oid" ]; then
  echo "aks-up: could not resolve the registry or kubelet identity — refusing to guess" >&2
  exit 1
fi

# Same rule as group_exists: a query that ERRORED must never read as an
# answer. Returning "" on failure would make `[ "$out" = 0 ]` false and
# `[ "$out" != 0 ]` true at the same time — skipping the repair, breaking
# the wait loop on its first pass, and sailing through the final check to
# print "AcrPull confirmed" with no evidence whatsoever.
#
# Asked of ARM directly (`az rest`), not via `az role assignment list`:
# that command also calls Microsoft Graph to decorate principals, and a
# tenant's conditional-access token-protection policy can refuse the
# CLI a Graph token (AADSTS530084 — observed 2026-09-01) while ARM calls
# keep working. The assignment is an ARM fact; asking Graph about it made
# a working cluster fail this gate. The AcrPull role definition is
# resolved at runtime rather than pinned as a GUID (this repo refuses
# committed GUIDs, and the built-in role's id is one). Note the ARM
# naming: a role definition's `roleName` is the display name ("AcrPull")
# and its `name` IS the GUID — the last segment of every assignment's
# `roleDefinitionId`, which is what ends_with() below matches against.
acrpull_role=$(az role definition list --name AcrPull --query '[0].name' -o tsv 2>/dev/null || true)
if [ -z "$acrpull_role" ]; then
  echo "aks-up: could not resolve the AcrPull role definition — refusing to guess." >&2
  echo "  Re-running is safe: everything created so far is reused, not rebuilt." >&2
  exit 1
fi
acrpull_count() { # -> prints a count; nonzero if the answer is unusable
  local out
  out=$(az rest --method get \
    --url "${acr_id}/providers/Microsoft.Authorization/roleAssignments" \
    --url-parameters "api-version=2022-04-01" "\$filter=principalId eq '$kubelet_oid'" \
    --query "length(value[?ends_with(properties.roleDefinitionId, '$acrpull_role')])" \
    -o tsv 2>/dev/null) || return 1
  case "$out" in
    '' | *[!0-9]*) return 1 ;;
    *) printf '%s' "$out" ;;
  esac
}

# Repair when the role is genuinely absent — and also when the answer is
# unreadable, since attaching is idempotent and the alternative is
# proceeding on an unknown.
if ! count=$(acrpull_count) || [ "$count" -eq 0 ]; then
  echo "aks-up: AcrPull not confirmed — attaching the registry (a few minutes)" >&2
  az aks update --name "$CLUSTER" --resource-group "$RG" \
    --attach-acr "$ACR" --output none
fi

# Fail closed on the claim itself, and give role propagation a moment: the
# assignment is created asynchronously, so a create that raced would
# otherwise surface much later as an unexplained ImagePullBackOff. Only a
# well-formed count >= 1 counts as confirmation.
confirmed=no
for _ in $(seq 1 12); do
  if count=$(acrpull_count) && [ "$count" -ge 1 ]; then
    confirmed=yes
    break
  fi
  sleep 5
done
if [ "$confirmed" != yes ]; then
  echo "aks-up: could not confirm AcrPull for the kubelet identity on '$ACR'." >&2
  echo "  Either the assignment is missing, or the role query failed (reading" >&2
  echo "  role assignments needs Microsoft.Authorization/roleAssignments/read" >&2
  echo "  on the registry)." >&2
  echo "  Not claiming success: the proxy image would fail to pull. Re-run" >&2
  echo "  once you can read role assignments, or grant AcrPull manually." >&2
  exit 1
fi
echo "aks-up: AcrPull confirmed" >&2

# --- kubeconfig -----------------------------------------------------------
# The context is named after the cluster. Note this is NOT a kind context,
# so every mutating make target will demand confirmation (scripts/kube-guard.sh).
echo "aks-up: writing kubeconfig context '$CLUSTER'" >&2
az aks get-credentials --name "$CLUSTER" --resource-group "$RG" \
  --overwrite-existing --output none

kubectl --context "$CLUSTER" get nodes

cat >&2 <<EOF

aks-up: ready.

  context:   $CLUSTER   (NOT a kind context — targets will ask to confirm)
  registry:  $ACR.azurecr.io
  netpol:    $NETWORK_POLICY engine (present ≠ enforced: prove it with
             TARGET=aks make netpol-verify once the plane is deployed)
  teardown:  AKS_RESOURCE_GROUP=$RG KAIMAHI_CONFIRM=$RG make aks-down
             ^ do not skip this. The confirmation names the RESOURCE
               GROUP, not the cluster — see docs/aks.md, "Tear it down".

Next (see docs/aks.md):
  export TARGET=aks AKS_CLUSTER=$CLUSTER ACR_NAME=$ACR
  export KAIMAHI_CONFIRM=$CLUSTER
  make kagent plane-copilot-secret plane govern agent tools-agent govern-tools
  make netpol-verify        # the boundary, ENFORCED — not merely present

  (plane-copilot-secret comes BEFORE plane: the proxy mounts that Secret
   optionally, so a pod started without it fails closed for minutes. Or
   just run 'make up', which is these steps in this order.)
EOF
