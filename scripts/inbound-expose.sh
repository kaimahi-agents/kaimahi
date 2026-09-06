#!/usr/bin/env bash
# Put the inbound bridge on the internet — the public edge
# (k8s/inbound-edge.yaml): a Caddy pod terminating TLS with a Let's
# Encrypt certificate obtained by TLS-ALPN-01 on port 443, behind an
# Azure load balancer whose public IP carries a DNS label.
#
# The two Azure identifiers involved are supplied here, never committed:
#
#   KAIMAHI_DNS_LABEL   required   the label you choose; must be unique in
#                                  the region ([a-z][a-z0-9-]{2,62})
#   AKS_LOCATION        optional   the cluster's region; read from the cluster
#                                  (az aks show) when unset, never defaulted —
#                                  the FQDN is <label>.<location>.cloudapp.azure.com
#                                  and a wrong region is a name that never resolves
#   AKS_RESOURCE_GROUP / AKS_CLUSTER   used only to read the region
#
# Fail closed, in order: refuse a malformed label; refuse to apply a
# manifest with a render token left unfilled; and report success ONLY
# when the public name answers over TLS with a certificate curl trusts —
# a LoadBalancer with an IP is not an exposure that works, and "Caddy is
# Running" is not a certificate.
#
# Prints the Request URL to paste into the Slack app. That URL names live
# infrastructure: redact it from anything you commit or paste publicly
# (scripts/check-no-azure-ids.sh refuses it in the tree).
#
# Usage: KUBECTL="kubectl --context <ctx>" KAIMAHI_DNS_LABEL=<label> bash scripts/inbound-expose.sh
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
LABEL="${KAIMAHI_DNS_LABEL:-}"
LOCATION="${AKS_LOCATION:-}"
RG="${AKS_RESOURCE_GROUP:-}"
CLUSTER="${AKS_CLUSTER:-kaimahi}"
NAMESPACE=kaimahi
# How long to wait for the LB, the DNS label and the certificate together.
TIMEOUT_SECONDS="${EXPOSE_TIMEOUT_SECONDS:-600}"

here=$(cd "$(dirname "$0")/.." && pwd)
manifest="$here/k8s/inbound-edge.yaml"

case "$LABEL" in
  ([a-z][a-z0-9-][a-z0-9-][a-z0-9-]*) ;;
  (*) echo "inbound-expose: KAIMAHI_DNS_LABEL is required: [a-z][a-z0-9-]{2,62}, e.g. kaimahi-demo-4c1f" >&2; exit 1 ;;
esac
case "$LABEL" in
  (*[!a-z0-9-]*|*-) echo "inbound-expose: invalid KAIMAHI_DNS_LABEL '$LABEL'" >&2; exit 1 ;;
esac
[ "${#LABEL}" -le 63 ] || { echo "inbound-expose: KAIMAHI_DNS_LABEL longer than 63 characters" >&2; exit 1; }
if [ -z "$LOCATION" ]; then
  [ -n "$RG" ] || { echo "inbound-expose: set AKS_LOCATION, or AKS_RESOURCE_GROUP so the region can be read from the cluster" >&2; exit 1; }
  LOCATION=$(az aks show --name "$CLUSTER" --resource-group "$RG" --query location -o tsv 2>/dev/null || true)
  [ -n "$LOCATION" ] || { echo "inbound-expose: could not read the region of $CLUSTER in $RG (az aks show)" >&2; exit 1; }
fi
case "$LOCATION" in
  (*[!a-z0-9]*|'') echo "inbound-expose: invalid AKS_LOCATION '$LOCATION'" >&2; exit 1 ;;
esac
FQDN="$LABEL.$LOCATION.cloudapp.azure.com"

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

# Render the two tokens. Then prove the render is complete: a manifest
# with a token still in it would apply cleanly and expose a Service
# annotated with the literal string "${KAIMAHI_DNS_LABEL}".
sed -e "s|\${KAIMAHI_DNS_LABEL}|$LABEL|g" -e "s|\${KAIMAHI_PUBLIC_FQDN}|$FQDN|g" \
  "$manifest" > "$workdir/edge.yaml"
if grep -q '\${KAIMAHI_' "$workdir/edge.yaml"; then
  echo "inbound-expose: unrendered token left in the manifest — refusing to apply:" >&2
  grep -n '\${KAIMAHI_' "$workdir/edge.yaml" >&2
  exit 1
fi
grep -q "azure-dns-label-name: \"$LABEL\"" "$workdir/edge.yaml" || {
  echo "inbound-expose: render did not place the DNS label on the Service" >&2; exit 1; }

# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
$KUBECTL apply -f "$workdir/edge.yaml"
# shellcheck disable=SC2086
$KUBECTL -n "$NAMESPACE" rollout status deploy/kaimahi-inbound-edge --timeout=300s

echo "inbound-expose: waiting for the public IP, the DNS label and the certificate ($FQDN)..." >&2
deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
ip=""
while :; do
  # shellcheck disable=SC2086
  ip=$($KUBECTL -n "$NAMESPACE" get svc kaimahi-inbound-edge \
        -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)
  if [ -n "$ip" ]; then
    # A well-formed positive only: HTTPS to the public NAME, curl
    # verifying the chain against its trust store, answered by Caddy's
    # 404 for a path that is not the hook. Anything else (connection
    # refused while the LB programs, a TLS error while ACME runs, a 5xx)
    # keeps waiting. --resolve pins the name to THIS Service's IP: a
    # stale or hijacked record for a reused label cannot pass as ours.
    code=$(curl -sS -o /dev/null -m 10 -w '%{http_code}' --resolve "$FQDN:443:$ip" "https://$FQDN/" 2>/dev/null || true)
    if [ "$code" = 404 ]; then
      break
    fi
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "inbound-expose: $FQDN did not come up with a trusted certificate within ${TIMEOUT_SECONDS}s" >&2
    echo "  public IP: ${ip:-<none yet>}" >&2
    echo "  inspect: $KUBECTL -n $NAMESPACE logs deploy/kaimahi-inbound-edge" >&2
    echo "  (a label already taken in this region shows up as a Service that never gets an IP)" >&2
    exit 1
  fi
  sleep 10
done

# The hook path itself must reach the bridge: an unsigned POST must come
# back as the PLANE's answer — 401 (secret in place, signature missing)
# or 503 (hook not yet armed: no signing secret / credential) — the
# proof that the edge forwards to the bridge and the bridge still
# refuses at the door. Caddy's own 404, a 502, or anything else fails.
post=$(curl -sS -o /dev/null -m 10 -w '%{http_code}' --resolve "$FQDN:443:$ip" -X POST --data '{}' "https://$FQDN/hook/slack-events" || true)
case "$post" in
  (401) armed="unsigned POST refused 401 by the bridge (hook armed)" ;;
  (503) armed="unsigned POST refused 503 by the bridge (hook NOT yet armed: run make inbound-credential CRED_INBOUND=inbound-slack and make inbound-secret HOOK=slack-events)" ;;
  (*) echo "inbound-expose: an unsigned POST to the hook answered HTTP '$post', expected the bridge's 401 or 503" >&2; exit 1 ;;
esac

echo "inbound-expose: up. Certificate trusted; $armed." >&2
echo "  public IP:   $ip" >&2
echo "  Request URL: https://$FQDN/hook/slack-events   <- paste into the Slack app's Event Subscriptions" >&2
echo "  Both are Azure identifiers: redact them from anything committed or pasted." >&2
