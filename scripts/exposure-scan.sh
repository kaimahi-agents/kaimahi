#!/usr/bin/env bash
# Prove what the cluster exposes to the internet — and that it is exactly
# the inbound edge on 443 and nothing else.
#
# Two views, both required:
#   1. Cluster-side: the ONLY Service of type LoadBalancer in ANY namespace
#      is kaimahi/kaimahi-inbound-edge. (A stray LoadBalancer elsewhere is
#      a second public address whether or not anyone scanned it.)
#   2. Internet-side: every public IP in the cluster's node resource group
#      is enumerated through the Azure API — not just the one we know
#      about — and every TCP port (1-65535) of each is connect-scanned
#      from THIS machine. The edge's IP must answer on {443} exactly;
#      every other public IP (the cluster's outbound SNAT address, which
#      AKS creates for egress) must answer on nothing.
#
# Fail closed: a scan that finds NOTHING open anywhere is a broken scan
# (a firewall between here and Azure, a dead tool), not a secure cluster
# — the edge's 443 is the positive control. A Service without an IP, an
# IP the Azure API does not list, or an unreadable list all fail. Two
# more ways a scan could report clean while proving nothing, both
# refused: a scanning host whose own egress allows only 443 would see
# every other port "closed" everywhere (so a known-open NON-443 port on
# the internet must answer first — the egress control), and a host that
# runs out of sockets mid-sweep would too (so a resource error aborts
# the scan instead of counting as closed).
#
# The IPs are Azure identifiers; they are MASKED in the output by default
# so a pasted transcript carries none (REVEAL_IPS=1 shows them locally).
#
# Env:  KUBECTL, AKS_RESOURCE_GROUP (required), AKS_CLUSTER (default kaimahi)
#       SCAN_TIMEOUT (per-port connect timeout, seconds, default 2)
#       SCAN_WORKERS (default 512)
#       EGRESS_CONTROL (host:port that must answer on a non-443 port from
#                       this machine; default 1.1.1.1:80 — the same
#                       control target scripts/netpol-probe.sh uses)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
RG="${AKS_RESOURCE_GROUP:-}"
CLUSTER="${AKS_CLUSTER:-kaimahi}"
NAMESPACE=kaimahi
[ -n "$RG" ] || { echo "exposure-scan: AKS_RESOURCE_GROUP is required" >&2; exit 1; }

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

# --- 1. cluster-side ---------------------------------------------------------
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
$KUBECTL get svc -A -o json > "$workdir/svc.json"
python3 - "$workdir/svc.json" <<'EOF'
import json, sys
svcs = json.load(open(sys.argv[1]))["items"]
lbs = sorted(f'{s["metadata"]["namespace"]}/{s["metadata"]["name"]}'
             for s in svcs if s["spec"].get("type") == "LoadBalancer")
want = ["kaimahi/kaimahi-inbound-edge"]
if lbs != want:
    sys.exit(f"exposure-scan: LoadBalancer Services are {lbs}, expected exactly {want}")
edge = next(s for s in svcs if s["metadata"]["namespace"] == "kaimahi" and s["metadata"]["name"] == "kaimahi-inbound-edge")
ports = sorted((p.get("protocol", "TCP"), p["port"]) for p in edge["spec"]["ports"])
if ports != [("TCP", 443)]:
    sys.exit(f"exposure-scan: edge Service ports are {ports}, expected [('TCP', 443)]")
ing = edge.get("status", {}).get("loadBalancer", {}).get("ingress") or []
ips = [i.get("ip") for i in ing if i.get("ip")]
if len(ips) != 1:
    sys.exit(f"exposure-scan: edge Service has {len(ips)} public IP(s), expected 1 (not provisioned yet?)")
print("cluster-side: one LoadBalancer Service (kaimahi/kaimahi-inbound-edge), TCP 443 only", file=sys.stderr)
open(sys.argv[1] + ".edge-ip", "w").write(ips[0])
EOF
edge_ip=$(cat "$workdir/svc.json.edge-ip")

# --- 2. internet-side --------------------------------------------------------
node_rg=$(az aks show --name "$CLUSTER" --resource-group "$RG" --query nodeResourceGroup -o tsv)
[ -n "$node_rg" ] || { echo "exposure-scan: cannot resolve the node resource group of $CLUSTER" >&2; exit 1; }
az network public-ip list --resource-group "$node_rg" --query '[].ipAddress' -o tsv > "$workdir/ips"
[ -s "$workdir/ips" ] || { echo "exposure-scan: Azure lists no public IPs in the node resource group — refusing to report clean" >&2; exit 1; }
grep -Fqx "$edge_ip" "$workdir/ips" || {
  echo "exposure-scan: the edge Service's IP is not among the node resource group's public IPs" >&2; exit 1; }

REVEAL_IPS="${REVEAL_IPS:-0}" EDGE_IP="$edge_ip" SCAN_TIMEOUT="${SCAN_TIMEOUT:-2}" SCAN_WORKERS="${SCAN_WORKERS:-512}" \
EGRESS_CONTROL="${EGRESS_CONTROL:-1.1.1.1:80}" \
python3 - "$workdir/ips" <<'EOF'
import errno, os, socket, sys
from concurrent.futures import ThreadPoolExecutor

ips = [l.strip() for l in open(sys.argv[1]) if l.strip()]
edge = os.environ["EDGE_IP"]
timeout = float(os.environ["SCAN_TIMEOUT"])
workers = int(os.environ["SCAN_WORKERS"])
reveal = os.environ.get("REVEAL_IPS") == "1"
ctl_host, ctl_port = os.environ["EGRESS_CONTROL"].rsplit(":", 1)

def label(ip):
    if reveal:
        return ip
    return "<edge-public-ip>" if ip == edge else f"<public-ip-{ips.index(ip) + 1}>"

# Errors that mean THIS HOST could not attempt the connection — out of
# file descriptors, ephemeral ports or buffers, or a policy refusal —
# not that the target declined it. Counting them as "closed" would let
# an exhausted scanner report a clean cluster.
LOCAL_FAILURE = {errno.EMFILE, errno.ENFILE, errno.EAGAIN, errno.ENOBUFS, errno.ENOMEM,
                 errno.EPERM, errno.EACCES, errno.EADDRNOTAVAIL}

class ScanBroken(Exception):
    pass

def probe(ip, port):
    try:
        with socket.create_connection((ip, port), timeout=timeout):
            return port
    except socket.timeout:
        return None
    except OSError as e:
        if e.errno in LOCAL_FAILURE:
            raise ScanBroken(f"{label(ip)} port {port}: {e}") from e
        return None  # ECONNREFUSED / EHOSTUNREACH: the target's answer

# Egress control: this machine must reach a known-open port that is not
# 443, or a "nothing but 443 is open" verdict would say more about the
# scanner's network than the cluster's.
if int(ctl_port) == 443 or probe(ctl_host, int(ctl_port)) is None:
    sys.exit(f"exposure-scan: egress control {ctl_host}:{ctl_port} (a non-443 port) is not reachable from here — "
             "this host cannot tell a closed port from a blocked one; refusing to scan")

failed = False
for ip in ips:
    expect = {443} if ip == edge else set()
    print(f"scanning {label(ip)} tcp/1-65535 ...", file=sys.stderr, flush=True)
    try:
        with ThreadPoolExecutor(max_workers=workers) as ex:
            open_ports = sorted(p for p in ex.map(lambda p: probe(ip, p), range(1, 65536)) if p)
    except ScanBroken as e:
        sys.exit(f"exposure-scan: the scanner itself failed mid-sweep ({e}); lower SCAN_WORKERS and re-run — no verdict")
    ok = set(open_ports) == expect
    failed |= not ok
    print(f"{label(ip)}: open {open_ports or 'none'} — expected {sorted(expect) or 'none'} — {'OK' if ok else 'FAIL'}")
if failed:
    sys.exit("exposure-scan: the internet-facing surface is NOT exactly the edge on 443")
print("exposure-scan: exactly one port on one public IP (the inbound edge, 443); every other public IP answers nothing")
EOF
