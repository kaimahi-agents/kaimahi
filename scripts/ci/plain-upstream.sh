#!/usr/bin/env bash
# Deploy the PLAIN, non-Kaimahi MCP server the generic onboarding path is
# proven against (docs/govern-your-agent.md).
#
# Nothing about this server is Kaimahi's: it lives in its own namespace,
# speaks plain http, offers its own two tools, and has no NetworkPolicy,
# no upstream-table entry and no RemoteMCPServer committed anywhere in
# this repo. That is the point — a proof driven against one of the six tool
# upstreams this repo commits would prove only that those six work.
#
# The Service publishes 8090 while the container listens on 9090, on
# purpose: it is the mistake a human writing the NetworkPolicy by hand
# makes, and the one `kmx tools add` cannot make because it reads the
# Service's resolved targetPort instead of its published port.
#
#   up      create the namespace, the ConfigMap holding the server, the
#           Deployment and the Service; wait for it to answer
#   client  a second workload in the same namespace that CALLS the seam.
#           It is separate from the server on purpose: the scaffolded
#           policy pair gives the server zero egress, so the server can
#           reach nothing — including the gateway — and a client hosted
#           inside it would be measuring that policy rather than the
#           seam. This one is unpoliced, which is what an adopter's own
#           application looks like.
#   calls   print the calls the SERVER actually served (not what the
#           plane decided — that is `kmx audit tool`)
#   down    delete the namespace and everything in it
#
# Env: KUBECTL (with --context), NS (default acme), IMAGE (default
#      python:3.12-alpine)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NS="${NS:-acme}"
IMAGE="${IMAGE:-python:3.12-alpine}"
here=$(cd "$(dirname "$0")/../.." && pwd)

# Context safety: this creates a namespace and a workload.
# shellcheck disable=SC2086
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NS" KUBE_CTX="$probe_ctx" \
  bash "$here/scripts/kube-guard.sh" "$(basename "$0") ${1:-}"

up() {
  $KUBECTL create namespace "$NS" --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  $KUBECTL -n "$NS" create configmap acme-warehouse-src \
    --from-file=server.py="$here/scripts/ci/plain-mcp-server.py" \
    --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  $KUBECTL apply -f - <<YAML >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: acme-warehouse
  namespace: $NS
spec:
  replicas: 1
  selector:
    matchLabels:
      app: acme-warehouse
  template:
    metadata:
      labels:
        app: acme-warehouse
    spec:
      containers:
        - name: server
          image: $IMAGE
          command: ["python3", "/src/server.py", "--port", "9090"]
          ports:
            - containerPort: 9090
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 128Mi
          volumeMounts:
            - name: src
              mountPath: /src
              readOnly: true
      volumes:
        - name: src
          configMap:
            name: acme-warehouse-src
---
apiVersion: v1
kind: Service
metadata:
  name: acme-warehouse
  namespace: $NS
spec:
  selector:
    app: acme-warehouse
  ports:
    # Published 8090, container 9090 — deliberately different.
    - port: 8090
      targetPort: 9090
      protocol: TCP
YAML
  $KUBECTL -n "$NS" rollout status deploy/acme-warehouse --timeout=300s
  # A well-formed positive: the server answers its own protocol. A pod
  # that is Running but not listening would otherwise pass.
  for _ in $(seq 1 60); do
    if $KUBECTL -n "$NS" exec deploy/acme-warehouse -- \
        python3 -c 'import urllib.request,json,sys
r=urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:9090/mcp",
  data=json.dumps({"jsonrpc":"2.0","id":1,"method":"tools/list"}).encode(),
  headers={"Content-Type":"application/json"}),timeout=5)
sys.exit(0 if "stock_adjust" in r.read().decode() else 1)' >/dev/null 2>&1; then
      echo "acme-warehouse is serving MCP on 9090 (Service 8090)"
      return 0
    fi
    sleep 1
  done
  echo "acme-warehouse never answered tools/list" >&2
  exit 1
}

client() {
  $KUBECTL apply -f - <<YAML >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: acme-client
  namespace: $NS
spec:
  replicas: 1
  selector:
    matchLabels:
      app: acme-client
  template:
    metadata:
      labels:
        app: acme-client
    spec:
      containers:
        - name: client
          image: $IMAGE
          # It does nothing on its own. What it is for is being exec'd
          # into: an MCP client's pod, with the network position one has.
          command: ["sleep", "infinity"]
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 128Mi
YAML
  $KUBECTL -n "$NS" rollout status deploy/acme-client --timeout=300s
}

calls() {
  $KUBECTL -n "$NS" exec deploy/acme-warehouse -- \
    python3 -c 'import urllib.request;print(urllib.request.urlopen("http://127.0.0.1:9090/calls",timeout=5).read().decode())'
}

down() { $KUBECTL delete namespace "$NS" --ignore-not-found --wait=true; }

case "${1:-}" in
  up) up ;;
  client) client ;;
  calls) calls ;;
  down) down ;;
  *) echo "usage: $(basename "$0") up|client|calls|down" >&2; exit 2 ;;
esac
