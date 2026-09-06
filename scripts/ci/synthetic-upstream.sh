#!/usr/bin/env bash
# CI's SYNTHETIC hosted upstream (docs/hosted-upstreams.md): a tiny
# https MCP echo server, reachable from the kind cluster at a
# PUBLIC-LOOKING address the hardened dialer accepts, so the hosted path
# is proven end to end with no GitHub token anywhere.
#
# How the address works. 203.0.113.10 is a documentation-range address:
# not private (the dialer's refusal list is NOT relaxed for it — a
# documentation range is neither private nor routable, which is the
# point) and not routed anywhere on the internet. The echo server runs in
# a sidecar container on kind's docker network holding that address as a
# secondary IP, and the kind node gets a host route to it. Pod traffic
# therefore leaves the node with the destination UNCHANGED — no DNAT —
# so kube-network-policies evaluates the policy on 203.0.113.10:443,
# exactly as it would for a real public host: blocked without the
# 443-to-public allowance, allowed with it (measured on kind
# 2026-09-02). The cluster resolves two names to it through a CoreDNS
# hosts entry (`rebind` repoints the second one, for the DNS-rebinding
# proof), and the gateway trusts the server's throwaway certificate only
# through the upstream's ca_file.
#
#   up          generate a throwaway CA + certificate, start the sidecar,
#               route the node, add the CoreDNS entries, create the CA
#               ConfigMap, patch the committed upstream table with the
#               three synthetic entries, and roll the proxy
#   rebind ADDR repoint mcp-echo-rebind.kaimahi-ci.test to ADDR (the
#               sidecar's private docker address, say) and flush CoreDNS
#   load-refusal prove a private-resolving hosted entry is refused at boot
#   down        restore the committed table, remove everything
#
# Env: KUBECTL (with --context), KIND_CLUSTER (default kaimahi-p1),
#      WORKDIR (where the certificate lives across invocations)
set -euo pipefail
umask 077

KUBECTL="${KUBECTL:-kubectl}"
KIND_CLUSTER="${KIND_CLUSTER:-kaimahi-p1}"
NODE="${KIND_CLUSTER}-control-plane"
WORKDIR="${WORKDIR:-$PWD/.synthetic-upstream}"
ADDR=203.0.113.10
HOST=mcp-echo.kaimahi-ci.test
REBIND_HOST=mcp-echo-rebind.kaimahi-ci.test
CONTAINER=kaimahi-ci-mcp-echo
here=$(cd "$(dirname "$0")/../.." && pwd)

# Context safety: the table patch and the CoreDNS edit mutate a cluster.
# shellcheck disable=SC2086
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="kaimahi, kube-system" KUBE_CTX="$probe_ctx" \
  bash "$here/scripts/kube-guard.sh" "$(basename "$0") ${1:-}"

corefile_hosts() { # addr-for-rebind|remove -> rewrite the CoreDNS Corefile with (or without) our hosts block
  local rebind_addr=$1
  mkdir -p "$WORKDIR"
  $KUBECTL -n kube-system get configmap coredns -o json > "$WORKDIR/coredns.json"
  REBIND="$rebind_addr" python3 - "$WORKDIR/coredns.json" <<'PY' > "$WORKDIR/coredns-patched.json"
import json, os, re, sys
cm = json.load(open(sys.argv[1]))
cf = cm["data"]["Corefile"]
block = ("    hosts {\n        203.0.113.10 mcp-echo.kaimahi-ci.test\n        %s mcp-echo-rebind.kaimahi-ci.test\n        fallthrough\n    }\n"
         % os.environ["REBIND"])
if os.environ["REBIND"] == "remove":
    block = ""
# Replace an earlier block of ours, else insert right after `ready`.
cf, n = re.subn(r"    hosts \{\n(?:        .*\n)*?        fallthrough\n    \}\n", block, cf, count=1)
if n == 0 and block:
    cf, n = re.subn(r"(    ready\n)", r"\1" + block.replace("\\", "\\\\"), cf, count=1)
assert n == 1 or not block, "could not place the hosts block in the Corefile"
cm["data"]["Corefile"] = cf
for k in ("resourceVersion", "uid", "creationTimestamp", "managedFields"):
    cm["metadata"].pop(k, None)
json.dump(cm, sys.stdout)
PY
  $KUBECTL apply -f "$WORKDIR/coredns-patched.json" >/dev/null
  # Flush: delete the CoreDNS pods rather than roll the Deployment — a
  # rolling update needs a surge pod's CPU request, which the full CI
  # node does not have; a delete frees the request first. `cache 30`
  # would otherwise serve the old answer for up to 30 s.
  $KUBECTL -n kube-system delete pod -l k8s-app=kube-dns --wait=true >/dev/null
  $KUBECTL -n kube-system rollout status deploy/coredns --timeout=180s >/dev/null
}

roll_proxy() {
  $KUBECTL -n kaimahi rollout restart deploy/kaimahi-proxy >/dev/null
  $KUBECTL -n kaimahi rollout status deploy/kaimahi-proxy --timeout=300s
}

case "${1:-}" in
  up)
    mkdir -p "$WORKDIR" && cd "$WORKDIR"
    # A throwaway CA that lives for one job; nothing here is committed.
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
      -keyout ca.key -out ca.crt -days 2 -subj '/CN=kaimahi-ci-throwaway-ca' 2>/dev/null
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
      -keyout server.key -out server.csr -subj "/CN=$HOST" 2>/dev/null
    printf 'subjectAltName=DNS:%s,DNS:%s\n' "$HOST" "$REBIND_HOST" > san.ext
    openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
      -out server.crt -days 2 -extfile san.ext 2>/dev/null
    chmod 644 ca.crt server.crt server.key # the sidecar runs as another uid
    cp "$here/scripts/ci/mcp-echo-server.py" .
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker run -d --name "$CONTAINER" --network kind --cap-add NET_ADMIN \
      -v "$WORKDIR:/echo:ro" python:3.12-alpine \
      sh -c "ip addr add $ADDR/32 dev eth0 && exec python3 /echo/mcp-echo-server.py --cert /echo/server.crt --key /echo/server.key" >/dev/null
    sleep 3
    docker logs "$CONTAINER" 2>&1 | grep -q 'serving https' || { docker logs "$CONTAINER"; exit 1; }
    sidecar_ip=$(docker inspect "$CONTAINER" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
    echo "$sidecar_ip" > sidecar.ip
    docker exec "$NODE" ip route replace "$ADDR/32" via "$sidecar_ip"
    # The node itself reaches it (a positive control for the route).
    docker exec "$NODE" bash -c "exec 3<>/dev/tcp/$ADDR/443" || { echo "node cannot reach $ADDR:443" >&2; exit 1; }
    echo "synthetic upstream: sidecar $sidecar_ip holds $ADDR; node routed" >&2

    corefile_hosts "$ADDR"
    $KUBECTL -n kaimahi create configmap kaimahi-upstream-ca --from-file=mcp-echo.crt=ca.crt \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
    # Three entries on top of the committed table: the stand-in, the
    # same server under a name that will be rebound, and a path that
    # redirects. All three are `internet: true` and trust the throwaway
    # CA only through ca_file. The stand-in also declares its
    # policy-relevant fields and a standing constraint, so CI can prove
    # argument policy end to end keylessly: `pay_invoice` declares its
    # policy-relevant fields, and hello-github may call it without asking
    # anyone while the amount is at or under $10,000 and the payee is the
    # one named. Anything else is denied and files an approval request
    # welded to that exact call.
    $KUBECTL -n kaimahi get configmap kaimahi-upstreams -o jsonpath='{.data.upstreams\.json}' > committed.json
    python3 - committed.json <<'PY' > patched.json
import json, sys
c = json.load(open(sys.argv[1]))
ca = "/etc/kaimahi/upstream-ca/mcp-echo.crt"

# The release tools are declared by COPYING the committed
# declaration rather than restating it. A tool name means one thing
# across the whole table (the plane refuses two upstreams that declare
# one tool differently), so restating them here would either duplicate
# the shipped policy or refuse to load - and a copy is the only version
# that cannot drift away from what ships.
release_tools = {}
for up in c["tool_upstreams"].values():
    for tool, decl in (up.get("tools") or {}).items():
        if tool in ("list_pull_requests", "create_branch", "actions_run_trigger"):
            release_tools[tool] = decl
missing = {"list_pull_requests", "create_branch", "actions_run_trigger"} - set(release_tools)
if missing:
    sys.exit("the committed table declares no policy fields for %s" % ", ".join(sorted(missing)))

c["tool_upstreams"]["mcp-echo"] = {
    "url": "https://mcp-echo.kaimahi-ci.test/mcp", "internet": True, "ca_file": ca,
    "tools": dict(release_tools,
                  pay_invoice={"policy_fields": ["invoice_id", "amount_cents", "payee_id"]}),
}
# The same stand-in, narrowed AT THE SERVER by a committed header -
# the outer of the two rings. The gateway will admit an allowlisted call
# here and the server will still refuse it, because the tool is not
# enabled on it. That is a guarantee an allowlist cannot make, and it is
# how the real GitHub and Azure DevOps seams exclude their destructive
# tools.
c["tool_upstreams"]["mcp-echo-narrowed"] = {
    "url": "https://mcp-echo.kaimahi-ci.test/mcp", "internet": True, "ca_file": ca,
    "extra_headers": {"X-MCP-Tools": "echo"},
}
# ADD to whatever the committed table already carries, never replace it:
# that block holds the accounts-payable agent's real constraint, and a patch
# that dropped it would quietly test a different policy than the one that
# ships.
c.setdefault("standing_constraints", {})["hello-github"] = {
    "pay_invoice": [
        {"field": "amount_cents", "op": "lte", "value": 1000000},
        {"field": "payee_id", "op": "in", "values": ["MER-4471"]},
    ],
    # What `make release-bind` applies against a real repository -
    # a READ tool bound to ONE of them. A read naming any other is
    # denied and files a request, which is the one-repository claim made
    # at the plane rather than only at the token.
    "list_pull_requests": [
        {"field": "owner", "op": "eq", "value": "kaimahi-ci"},
        {"field": "repo", "op": "eq", "value": "the-one-repo"},
    ],
}
c["tool_upstreams"]["mcp-echo-rebind"] = {"url": "https://mcp-echo-rebind.kaimahi-ci.test/mcp", "internet": True, "ca_file": ca}
c["tool_upstreams"]["mcp-echo-redirect"] = {"url": "https://mcp-echo.kaimahi-ci.test/redirect", "internet": True, "ca_file": ca}
json.dump(c, sys.stdout, indent=2)
PY
    $KUBECTL -n kaimahi create configmap kaimahi-upstreams --from-file=upstreams.json=patched.json \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
    roll_proxy
    ;;
  rebind)
    addr="${2:?usage: synthetic-upstream.sh rebind ADDR}"
    corefile_hosts "$addr"
    echo "synthetic upstream: $REBIND_HOST now resolves to $addr" >&2
    ;;
  load-refusal)
    # An upstream marked internet whose host resolves PRIVATE (the
    # plane's own Postgres Service — in-cluster-shaped, so the pure shape
    # check lets it through; only the boot-time vet can know what the
    # name resolves to) must refuse the config LOUDLY at load: the new
    # pod crash-loops saying so and the old replicas keep serving
    # (maxUnavailable 0). Then the table is put back.
    cd "$WORKDIR"
    python3 - patched.json <<'PY' > refused.json
import json, sys
c = json.load(open(sys.argv[1]))
c["tool_upstreams"]["inside"] = {"url": "https://kaimahi-postgres.kaimahi/mcp", "internet": True}
json.dump(c, sys.stdout, indent=2)
PY
    $KUBECTL -n kaimahi create configmap kaimahi-upstreams --from-file=upstreams.json=refused.json \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
    $KUBECTL -n kaimahi rollout restart deploy/kaimahi-proxy >/dev/null
    found=""
    for _ in $(seq 1 60); do
      if $KUBECTL -n kaimahi logs -l app=kaimahi-proxy --tail=20 2>/dev/null \
          | grep -q 'hosted upstream configuration refused.*refused at config load.*kaimahi-postgres.kaimahi resolves to'; then
        found=1; break
      fi
      sleep 2
    done
    ready=$($KUBECTL -n kaimahi get deploy kaimahi-proxy -o jsonpath='{.status.readyReplicas}')
    # Restore first, so a failed assertion below never leaves the cluster
    # wedged for the steps after this one.
    $KUBECTL -n kaimahi create configmap kaimahi-upstreams --from-file=upstreams.json=patched.json \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
    roll_proxy
    [ -n "$found" ] || { echo "the private-resolving hosted upstream was NOT refused at config load" >&2; exit 1; }
    [ "$ready" = 2 ] || { echo "the refused rollout took a serving replica down (ready=$ready)" >&2; exit 1; }
    echo "load refusal: the new pod refused the table loudly; both old replicas kept serving" >&2
    ;;
  down)
    # Restore first and unconditionally: the committed table must come
    # back whether or not this run's workdir still exists.
    $KUBECTL apply -f "$here/k8s/plane/upstreams.yaml" >/dev/null
    $KUBECTL -n kaimahi delete configmap kaimahi-upstream-ca --ignore-not-found >/dev/null
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker exec "$NODE" ip route del "$ADDR/32" 2>/dev/null || true
    corefile_hosts remove
    roll_proxy
    ;;
  *)
    echo "usage: synthetic-upstream.sh up | load-refusal | rebind ADDR | down" >&2
    exit 2
    ;;
esac
