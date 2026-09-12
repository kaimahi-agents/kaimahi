#!/usr/bin/env bash
# Hosted MODEL proof without a real public endpoint or credential.
# The documentation-range address is routed unchanged over kind's network,
# so NetworkPolicy evaluates public-looking TCP 443, while the hardened
# dialer keeps its real private-address refusals. A throwaway CA proves TLS.
# up | load-refusal | rebind ADDR | down
set -euo pipefail
umask 077
KUBECTL="${KUBECTL:-kubectl}"
KIND_CLUSTER="${KIND_CLUSTER:-kaimahi-p1}"
NODE="${KIND_CLUSTER}-control-plane"
WORKDIR="${WORKDIR:-$PWD/.synthetic-model}"
ADDR=203.0.113.10
HOST=model-echo.kaimahi-ci.test
REBIND_HOST=model-echo-rebind.kaimahi-ci.test
CONTAINER=kaimahi-ci-model-echo
here=$(cd "$(dirname "$0")/../.." && pwd)
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="kaimahi, kube-system" KUBE_CTX="$probe_ctx" \
  bash "$here/scripts/kube-guard.sh" "$(basename "$0") ${1:-}"

corefile_hosts() {
  local rebind_addr=$1
  mkdir -p "$WORKDIR"
  $KUBECTL -n kube-system get configmap coredns -o json > "$WORKDIR/coredns.json"
  REBIND="$rebind_addr" python3 - "$WORKDIR/coredns.json" <<'PY' > "$WORKDIR/coredns-patched.json"
import json, os, re, sys
cm = json.load(open(sys.argv[1]))
cf = cm['data']['Corefile']
block = ('    hosts {\n        203.0.113.10 model-echo.kaimahi-ci.test\n'
         '        %s model-echo-rebind.kaimahi-ci.test\n        fallthrough\n    }\n' % os.environ['REBIND'])
if os.environ['REBIND'] == 'remove':
    block = ''
cf, n = re.subn(r'    hosts \{\n(?:        .*\n)*?        fallthrough\n    \}\n', block, cf, count=1)
if n == 0 and block:
    cf, n = re.subn(r'(    ready\n)', r'\1' + block, cf, count=1)
assert n == 1 or not block, 'could not place the hosts block in the Corefile'
cm['data']['Corefile'] = cf
for k in ('resourceVersion', 'uid', 'creationTimestamp', 'managedFields'):
    cm['metadata'].pop(k, None)
json.dump(cm, sys.stdout)
PY
  $KUBECTL apply -f "$WORKDIR/coredns-patched.json" >/dev/null
  # Delete, rather than surge: the CI node has no spare CoreDNS request.
  $KUBECTL -n kube-system delete pod -l k8s-app=kube-dns --wait=true >/dev/null
  $KUBECTL -n kube-system rollout status deploy/coredns --timeout=180s >/dev/null
}
roll_proxy() {
  $KUBECTL -n kaimahi rollout restart deploy/kaimahi-proxy >/dev/null
  $KUBECTL -n kaimahi rollout status deploy/kaimahi-proxy --timeout=300s
}
apply_table() {
  $KUBECTL -n kaimahi create configmap kaimahi-upstreams --from-file="upstreams.json=$1" \
    --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
}
case "${1:-}" in
  up)
    mkdir -p "$WORKDIR" && cd "$WORKDIR"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
      -keyout ca.key -out ca.crt -days 2 -subj '/CN=kaimahi-ci-throwaway-ca' 2>/dev/null
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
      -keyout server.key -out server.csr -subj "/CN=$HOST" 2>/dev/null
    printf 'subjectAltName=DNS:%s,DNS:%s\n' "$HOST" "$REBIND_HOST" > san.ext
    openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
      -out server.crt -days 2 -extfile san.ext 2>/dev/null
    chmod 644 ca.crt server.crt server.key
    cp "$here/scripts/ci/plain-model-server.py" .
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker run -d --name "$CONTAINER" --network kind --cap-add NET_ADMIN \
      -v "$WORKDIR:/fixture:ro" -e PORT=443 \
      -e TLS_CERT=/fixture/server.crt -e TLS_KEY=/fixture/server.key python:3.12-alpine \
      sh -c "ip addr add $ADDR/32 dev eth0 && exec python3 /fixture/plain-model-server.py" >/dev/null
    serving=
    for _ in $(seq 1 30); do
      docker logs "$CONTAINER" > server.log 2>&1
      if grep -q 'serving https' server.log; then serving=1; break; fi
      sleep 1
    done
    [ -n "$serving" ] || { cat server.log >&2; exit 1; }
    sidecar_ip=$(docker inspect "$CONTAINER" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
    printf '%s\n' "$sidecar_ip" > sidecar.ip
    docker exec "$NODE" ip route replace "$ADDR/32" via "$sidecar_ip"
    docker exec "$NODE" bash -c "exec 3<>/dev/tcp/$ADDR/443"
    corefile_hosts "$ADDR"
    $KUBECTL -n kaimahi create configmap kaimahi-upstream-ca --from-file=model-echo.crt=ca.crt \
      --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
    $KUBECTL -n kaimahi get configmap kaimahi-upstreams -o jsonpath='{.data.upstreams\.json}' > committed.json
    python3 - committed.json <<'PY' > patched.json
import json, sys
c = json.load(open(sys.argv[1]))
for name, host, path in [('model-echo', 'model-echo', 'v1/responses'),
                         ('model-echo-rebind', 'model-echo-rebind', 'v1/responses'),
                         ('model-echo-redirect', 'model-echo', 'redirect')]:
    c['upstreams'][name] = {
        'base_url': 'https://' + host + '.kaimahi-ci.test', 'path': path,
        'protocol': 'responses', 'classification': 'free', 'internet': True,
        'ca_file': '/etc/kaimahi/upstream-ca/model-echo.crt',
    }
json.dump(c, sys.stdout, indent=2)
PY
    apply_table patched.json
    roll_proxy
    ;;
  rebind)
    corefile_hosts "${2:?usage: synthetic-model.sh rebind ADDR}"
    ;;
  load-refusal)
    cd "$WORKDIR"
    python3 - patched.json <<'PY' > refused.json
import json, sys
c = json.load(open(sys.argv[1]))
c['upstreams']['inside'] = {'base_url': 'https://kaimahi-postgres.kaimahi',
    'path': 'v1/responses', 'classification': 'free', 'internet': True}
json.dump(c, sys.stdout)
PY
    apply_table refused.json
    $KUBECTL -n kaimahi rollout restart deploy/kaimahi-proxy >/dev/null
    found=
    for _ in $(seq 1 60); do
      # Capture before grep: a matching grep -q must not SIGPIPE kubectl.
      $KUBECTL -n kaimahi logs -l app=kaimahi-proxy --tail=40 > refusal.log 2>/dev/null || true
      if grep -q 'hosted upstream configuration refused.*refused at config load.*kaimahi-postgres.kaimahi resolves to' refusal.log; then found=1; break; fi
      sleep 2
    done
    ready=$($KUBECTL -n kaimahi get deploy kaimahi-proxy -o jsonpath='{.status.readyReplicas}')
    # Restore before asserting; no failure may wedge the next probe.
    apply_table patched.json
    roll_proxy
    [ -n "$found" ] || { cat refusal.log >&2; echo 'hosted private address not refused at load' >&2; exit 1; }
    [ "$ready" = 2 ] || { echo "refused rollout lost a serving replica: $ready" >&2; exit 1; }
    ;;
  down)
    $KUBECTL apply -f "$here/k8s/plane/upstreams.yaml" >/dev/null
    $KUBECTL -n kaimahi delete configmap kaimahi-upstream-ca --ignore-not-found >/dev/null
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker exec "$NODE" ip route del "$ADDR/32" 2>/dev/null || true
    corefile_hosts remove
    roll_proxy
    ;;
  *) echo 'usage: synthetic-model.sh up | load-refusal | rebind ADDR | down' >&2; exit 2 ;;
esac
