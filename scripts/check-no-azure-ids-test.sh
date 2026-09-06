#!/usr/bin/env bash
# Prove scripts/check-no-azure-ids.sh still catches every identifier
# class it claims to — and still lets the placeholder forms through. A
# scanner is a gate that fails OPEN when a pattern quietly stops matching,
# so CI runs this BEFORE the tree scan and trusts the scanner's verdict
# only after it passes.
#
# Each case is one file under a temp dir, scanned by path (the scanner's
# explicit-paths mode), so the repo's own contents never affect it.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
scanner="$here/check-no-azure-ids.sh"
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

fail=0
expect() { # expect <refused|clean> <label> <content> [<finding class the refusal must name>]
  local want=$1 label=$2 content=$3 class=${4:-} dir rc
  dir=$(mktemp -d "$workdir/case.XXXXXX")
  printf '%s\n' "$content" > "$dir/file.txt"
  set +e
  bash "$scanner" "$dir" > "$dir/out" 2>&1
  rc=$?
  set -e
  # A refusal counts only when the scanner names the class this case
  # exists to catch: exit 1 for another reason (an empty file list, a
  # traceback) must not pass as "still catches it".
  case "$want:$rc" in
    (refused:1) if grep -q "$class" "$dir/out"; then echo "ok   $want  $label"; else
                  echo "FAIL want=$want (class '$class') got=rc1 without that class  $label"; sed 's/^/     /' "$dir/out"; fail=1; fi ;;
    (clean:0) echo "ok   $want  $label" ;;
    (*) echo "FAIL want=$want got=rc$rc  $label"; sed 's/^/     /' "$dir/out"; fail=1 ;;
  esac
}

# --- must be refused ---------------------------------------------------------
# Fixtures are ASSEMBLED here rather than written out, so this file does
# not itself carry an identifier-shaped literal for the tree scan to find.
guid=$(printf '3f2a9c1e-7b4d-4e8a-9c21-%s' 0d5e6f7a8b9c)
aks=$(printf 'kaimahi-demo-abc123.hcp.westus3.%s' azmk8s.io)
acr=$(printf 'kaimahidemo.%s' azurecr.io)
edge=$(printf 'kaimahi-demo-4c1f.westus3.cloudapp.%s' azure.com)
ip1=$(printf '20.150.%s' 32.11)
ip2=$(printf '52.160.%s' 1.2)
# The two workspace kinds observability adds. Assembled like the rest so this
# file carries no identifier-shaped literal of its own.
amw=$(printf '/subscriptions/S/resourceGroups/RG/providers/%s.%s/accounts/%s' Microsoft Monitor kaimahi-metrics-a1b2c3d4)
law=$(printf '/subscriptions/S/resourceGroups/RG/providers/%s.%s/workspaces/%s' Microsoft OperationalInsights kaimahi-logs-a1b2c3d4)
bare_amw=$(printf '/providers/%s.%s/accounts/%s' Microsoft Monitor kaimahi-metrics-a1b2c3d4)
promep=$(printf 'kaimahi-metrics-a1b2c3d4-abcd.westus3.prometheus.monitor.%s' azure.com)
dce=$(printf 'kaimahi-dce-a1b2c3d4-xyz9.westus3.ingest.monitor.%s' azure.com)
expect refused "subscription/tenant GUID"     "sub $guid"                                        "GUID"
expect refused "AKS API server FQDN"          "https://$aks:443"                                 "AKS cluster FQDN"
expect refused "literal ACR login server"     "image: $acr/kaimahi-proxy:p8"                     "literal ACR login server"
expect refused "literal cloudapp DNS label"   "Request URL: https://$edge/hook/slack-events"     "literal public DNS label"
expect refused "bare cloudapp FQDN"           "$edge"                                            "literal public DNS label"
expect refused "public IPv4 address"          "public IP: $ip1"                                  "public IPv4 address"
expect refused "public IPv4 in a URL"         "curl https://$ip2/"                               "public IPv4 address"
expect refused "public IPv4 ending a sentence" "the edge answered on $ip1."                       "public IPv4 address"
expect refused "Azure Monitor workspace id"   "amw: $amw"                                        "Azure Monitor workspace resource id"
expect refused "Log Analytics workspace id"   "law: $law"                                        "Log Analytics workspace resource id"
expect refused "Managed Prometheus endpoint"  "query: https://$promep/api/v1/query"              "Azure Monitor endpoint"
expect refused "data collection endpoint"     "$dce"                                             "Azure Monitor endpoint"
# A workspace id carries a subscription GUID too, so a real one trips two
# rules at once. This case proves the workspace rule fires on its own, on the
# relative id shape that carries no GUID at all.
expect refused "workspace id with no GUID"    "$bare_amw"                                        "Azure Monitor workspace resource id"

# --- must pass -----------------------------------------------------------------
expect clean "variable ACR reference"         'image: $(ACR_NAME).azurecr.io/kaimahi-proxy:p8'
expect clean "placeholder ACR reference"      "image: <your-registry>.azurecr.io/kaimahi-proxy"
expect clean "variable cloudapp FQDN"         'https://$LABEL.$LOCATION.cloudapp.azure.com/hook/slack-events'
expect clean "placeholder cloudapp FQDN"      "https://<label>.<region>.cloudapp.azure.com/hook/slack-events"
expect clean "make-variable cloudapp FQDN"    '$(KAIMAHI_DNS_LABEL).westus3.cloudapp.azure.com'
expect clean "render-token cloudapp FQDN"     '${KAIMAHI_DNS_LABEL}.${AKS_LOCATION}.cloudapp.azure.com'
expect clean "private/loopback/link-local"    "10.0.0.0/8 172.16.0.0/12 192.168.0.16 169.254.169.254 127.0.0.1 100.64.0.1"
expect clean "unspecified/broadcast/multicast" "0.0.0.0/0 255.255.255.255 224.0.0.1"
expect clean "documentation ranges"           "192.0.2.10 198.51.100.7 203.0.113.99"
expect clean "well-known resolver (probe)"    "INTERNET_TARGET=1.1.1.1"
expect clean "version-shaped number"          "kubectl v1.35.7.0 and go1.26.2.1"
expect clean "synthetic GUID fixture"         "00000000-0000-0000-0000-000000000099"
expect clean "variable workspace id"          '/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Monitor/accounts/$AMW_NAME'
expect clean "placeholder workspace id"       "/providers/Microsoft.OperationalInsights/workspaces/<your-workspace>"
expect clean "variable Prometheus endpoint"   'https://${AMW}.${REGION}.prometheus.monitor.azure.com/api/v1/query'
expect clean "placeholder monitor endpoint"   "https://<workspace>.<region>.prometheus.monitor.azure.com"
# The workbook template names ARM TYPES, which look like provider paths but
# carry no name segment and identify nothing.
expect clean "ARM resource type, not an id"   'resourceId("microsoft.insights/workbooks", x) type: microsoft.monitor/accounts'
expect clean "ARM template contentVersion"    '"contentVersion": "1.0.0.0",'

if [ "$fail" -ne 0 ]; then
  echo "check-no-azure-ids-test: the scanner no longer behaves as documented" >&2
  exit 1
fi
echo "check-no-azure-ids-test: scanner catches every class and admits every placeholder form"
