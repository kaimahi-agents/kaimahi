#!/usr/bin/env bash
# Deploy the PLAIN, non-Kaimahi model endpoint the model-seam onboarding
# path is proven against (docs/govern-your-agent.md, docs/spend.md).
#
# It is the model seam's counterpart to `plain-upstream.sh`, and it exists
# for the same reason: nothing about this endpoint is Kaimahi's. It has no
# upstream-table entry, no NetworkPolicy and no ModelConfig committed
# anywhere in this repo, and it speaks the OpenAI **Responses API** —
# which the committed model upstreams do not, and which the bundled Ollama
# image has no route for at all. A proof driven against `ollama` would
# prove only that chat-completions works, which is the thing that already
# did.
#
# The Service publishes 8000 while the container listens on 9000, on
# purpose and for the same reason as the tool fixture's mismatch: it is
# the mistake a hand-written NetworkPolicy makes, and the one
# `kmx models add` cannot make because it reads the Service's resolved
# targetPort rather than its published port.
#
#   up      create the namespace, the ConfigMap holding the server, the
#           Deployment and the Service; wait for it to answer
#   down    delete the namespace and everything in it
#
# Env: KUBECTL (with --context), NS (default acme-model), IMAGE (default
#      python:3.12-alpine)
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NS="${NS:-acme-model}"
IMAGE="${IMAGE:-python:3.12-alpine}"
here=$(cd "$(dirname "$0")/../.." && pwd)

# Context safety: this creates a namespace and a workload.
# shellcheck disable=SC2086
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NS" KUBE_CTX="$probe_ctx" \
  bash "$here/scripts/kube-guard.sh" "$(basename "$0") ${1:-}"

up() {
  $KUBECTL create namespace "$NS" --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  $KUBECTL -n "$NS" create configmap acme-model-src \
    --from-file=server.py="$here/scripts/ci/plain-model-server.py" \
    --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  $KUBECTL apply -f - <<YAML >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: acme-model
  namespace: $NS
spec:
  replicas: 1
  selector:
    matchLabels:
      app: acme-model
  template:
    metadata:
      labels:
        app: acme-model
    spec:
      containers:
        - name: server
          image: $IMAGE
          command: ["python3", "/src/server.py"]
          env:
            - name: PORT
              value: "9000"
          ports:
            - containerPort: 9000
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
            name: acme-model-src
---
apiVersion: v1
kind: Service
metadata:
  name: acme-model
  namespace: $NS
spec:
  selector:
    app: acme-model
  ports:
    # Published 8000, container 9000 — deliberately different.
    - port: 8000
      targetPort: 9000
      protocol: TCP
YAML
  $KUBECTL -n "$NS" rollout status deploy/acme-model --timeout=300s
  # A well-formed positive: the endpoint answers its own protocol with a
  # usage envelope. A pod that is Running but not listening, or listening
  # and reporting no usage, would otherwise pass — and "reports no usage"
  # is precisely the case the plane now refuses, so the fixture must not
  # be allowed to drift into it silently.
  for _ in $(seq 1 60); do
    if $KUBECTL -n "$NS" exec deploy/acme-model -- \
        python3 -c 'import urllib.request,json,sys
r=urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:9000/v1/responses",
  data=json.dumps({"model":"fixture","input":"one two three"}).encode(),
  headers={"Content-Type":"application/json"}),timeout=5)
u=json.loads(r.read().decode())["usage"]
sys.exit(0 if u["input_tokens"] == 3 else 1)' >/dev/null 2>&1; then
      echo "acme-model is serving the Responses API on 9000 (Service 8000)"
      return 0
    fi
    sleep 1
  done
  echo "acme-model never answered /v1/responses with a usage envelope" >&2
  exit 1
}

down() { $KUBECTL delete namespace "$NS" --ignore-not-found --wait=true; }

case "${1:-}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $(basename "$0") up|down" >&2; exit 2 ;;
esac
