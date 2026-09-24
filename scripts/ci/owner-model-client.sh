#!/usr/bin/env bash
# The OWNER-MANAGED workload the governance evidence is produced against.
#
# Nothing here belongs to Kaimahi. This is a Deployment somebody else
# runs: its own namespace, its own ConfigMap, its own image, and a model
# endpoint configured through its environment the way a chart would
# configure one. It is created BEFORE the plane knows about it and it is
# never patched by kmx — `kmx migrate` writes a patch to a file and the
# owner applies it, which is the boundary the shard exists to prove.
#
# It starts UNGOVERNED on purpose: `OPENAI_BASE_URL` points straight at
# Ollama, from a ConfigMap the application owns. That is both the honest
# starting state and the positive control for the migration — `config`
# below reports that URL before the owner applies the patch and the seam's
# URL afterwards, so "the patch took effect" is read off the workload
# rather than assumed from a command's exit code.
#
#   up                create the namespace, ConfigMaps, Deployment and
#                     Service, and wait for it to be Ready
#   down              delete the namespace and everything in it
#   snapshot          print the parts of the Deployment a migration must
#                     not change: uid, generation and the whole spec
#   config            print the wiring the application would use, as JSON
#   ask <prompt>      one real model turn from inside the pod, as JSON
#
# `config` and `ask` run through `kubectl exec`, so they read the
# container's OWN environment and the CA the patch mounted into it —
# not a copy of either reconstructed on the runner.
#
# Env: KUBECTL (with --context), OWNER_NS (default owner-app), OWNER_NAME
#      (default owner-ci), OWNER_IMAGE (default python:3.12-alpine),
#      OWNER_PRE_MIGRATION_MODEL (default qwen2.5:3b — the model name the
#      application uses BEFORE the migration)
#
# Every knob is OWNER_-prefixed rather than NS/NAME/IMAGE/MODEL: a bare
# `NAME` is already exported in many shells, and it silently selected a
# Deployment nobody meant when this was written. `MODEL` is also the name
# replica-kill-probe.sh reads, and the two are not the same model.
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NS="${OWNER_NS:-owner-app}"
NAME="${OWNER_NAME:-owner-ci}"
IMAGE="${OWNER_IMAGE:-python:3.12-alpine}"
MODEL="${OWNER_PRE_MIGRATION_MODEL:-qwen2.5:3b}"
here=$(cd "$(dirname "$0")/../.." && pwd)

# Context safety: this creates a namespace and a workload.
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NS" KUBE_CTX="$probe_ctx" \
  bash "$here/scripts/kube-guard.sh" "$(basename "$0") ${1:-}"

up() {
  $KUBECTL create namespace "$NS" --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  $KUBECTL -n "$NS" create configmap "$NAME-src" \
    --from-file=owner-model-client.py="$here/scripts/ci/owner-model-client.py" \
    --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  # The application's OWN configuration, in the application's own
  # ConfigMap: the two variables a migration reads off the live workload
  # before it decides anything. kmx never edits this object.
  $KUBECTL -n "$NS" create configmap "$NAME-config" \
    --from-literal=OPENAI_BASE_URL=http://ollama.ollama.svc.cluster.local:11434/v1 \
    --from-literal=OPENAI_CHAT_MODEL="$MODEL" \
    --from-literal=PORT=9000 \
    --dry-run=client -o yaml | $KUBECTL apply -f - >/dev/null
  $KUBECTL apply -f - <<YAML >/dev/null
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $NAME
  namespace: $NS
spec:
  replicas: 1
  selector:
    matchLabels:
      app: $NAME
  template:
    metadata:
      labels:
        app: $NAME
    spec:
      containers:
        - name: app
          image: $IMAGE
          command: ["python3", "/src/owner-model-client.py", "serve"]
          envFrom:
            - configMapRef:
                name: $NAME-config
          ports:
            - containerPort: 9000
          readinessProbe:
            httpGet:
              path: /healthz
              port: 9000
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
            name: $NAME-src
---
apiVersion: v1
kind: Service
metadata:
  name: $NAME
  namespace: $NS
spec:
  selector:
    app: $NAME
  ports:
    - port: 80
      targetPort: 9000
      protocol: TCP
YAML
  $KUBECTL -n "$NS" rollout status "deploy/$NAME" --timeout=300s
  # A well-formed negative: the application is up and is NOT governed.
  # Starting from a workload that already pointed at the seam would make
  # every later assertion about the patch unfalsifiable.
  config | python3 -c '
import json, sys
wiring = json.load(sys.stdin)
assert wiring["base_url"] == "http://ollama.ollama.svc.cluster.local:11434/v1", wiring
assert wiring["credential"] == "absent", wiring
assert wiring["ca_file"] is None, wiring
print("owner-ci is Ready and ungoverned: " + wiring["base_url"] + ", no credential, no mounted authority")
'
}

down() { $KUBECTL delete namespace "$NS" --ignore-not-found --wait=true; }

# uid, generation and the entire spec. A migration that changed ANY of
# them before the owner applied the patch would have mutated somebody
# else's workload, which is the thing `kmx migrate` promises not to do.
snapshot() {
  $KUBECTL -n "$NS" get deployment "$NAME" -o json | python3 -c '
import json, sys
d = json.load(sys.stdin)
print(json.dumps({"uid": d["metadata"]["uid"], "generation": d["metadata"]["generation"],
                  "spec": d["spec"]}, sort_keys=True, indent=2))
'
}

config() { $KUBECTL -n "$NS" exec "deploy/$NAME" -- python3 /src/owner-model-client.py config; }

ask() {
  [ "$#" -ge 1 ] || { echo "usage: $(basename "$0") ask <prompt>" >&2; exit 2; }
  $KUBECTL -n "$NS" exec "deploy/$NAME" -- python3 /src/owner-model-client.py ask "$@"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  snapshot) snapshot ;;
  config) config ;;
  ask) shift; ask "$@" ;;
  *) echo "usage: $(basename "$0") up|down|snapshot|config|ask <prompt>" >&2; exit 2 ;;
esac
