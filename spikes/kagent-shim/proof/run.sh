#!/usr/bin/env bash
# SPIKE manual proof, NOT CI. Needs kind, docker, kubectl, helm, Go, Python+PyYAML.
# Run from spikes/kagent-shim: bash proof/run.sh /absolute/evidence-directory
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $# == 1 && $1 == /* ]] || { echo 'usage: bash proof/run.sh /absolute/evidence-directory' >&2; exit 2; }
evidence=$1
mkdir -p "$evidence"
cluster=kagent-shim-spike
context=kind-kagent-shim-spike
if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  echo "refusing to touch existing cluster $cluster" >&2
  exit 1
fi
k() { kubectl --context "$context" "$@"; }
forward=
created=false
cleanup() {
  [[ -z $forward ]] || kill "$forward" 2>/dev/null || true
  if $created; then kind delete cluster --name "$cluster"; fi
}
trap cleanup EXIT
# The ownership flag also covers a partially failed create.
created=true
kind create cluster --name "$cluster" --image kindest/node:v1.32.2 --wait 90s
curl -fsSL https://raw.githubusercontent.com/orka-agents/orka/b07d42c0b9e52fe511b434827a342b4720f5d422/deploy/orka.yaml -o "$evidence/orka.yaml"
printf '%s  %s\n' 33bdd38bc4aff5d9ef0c32cd5a6c2810a186d2c3ab482b5fdc0f0673a5d734cd "$evidence/orka.yaml" | sha256sum --check
k create namespace orka-system
# Runtime-generated wrapper material travels stdin only. It is never converter input.
head -c 32 /dev/urandom | k -n orka-system create secret generic harness-wrapper-auth --from-file=token=/dev/stdin
k apply --server-side -f "$evidence/orka.yaml"
k -n orka-system rollout status deployment/orka-controller-manager --timeout=180s
helm --kube-context "$context" install shim-tools oci://ghcr.io/kagent-dev/tools/helm/kagent-tools \
  --version 0.2.1 -n orka-system --set fullnameOverride=shim-tools \
  --set 'tools.enabledTools[0]=k8s' --set 'tools.args[0]=--read-only' \
  --set rbac.readOnly=true --set 'rbac.namespaces[0]=orka-system' --wait --timeout 180s
k apply -f proof/model.yaml
# The local model ignores auth. This is not a usable external credential.
printf 'local-keyless-placeholder' | k -n orka-system create secret generic shim-model-credentials --from-file=token=/dev/stdin
k -n orka-system rollout status deployment/shim-model --timeout=180s
k -n orka-system exec deployment/shim-model -- ollama pull qwen2.5:3b

go run ./cmd/convert proof/source.yaml proof/tools.json > "$evidence/converted.yaml"
k get crd agents.core.orka.ai providers.core.orka.ai tools.core.orka.ai tasks.core.orka.ai -o json > "$evidence/installed-crds.json"
python3 proof/check.py "$evidence/installed-crds.json" "$evidence/converted.yaml" proof/task.yaml
k apply --dry-run=server --validate=strict -f "$evidence/converted.yaml"
CGO_ENABLED=0 go build -o adapter ./cmd/adapter
docker build -t kagent-shim-spike:dev .
kind load docker-image --name "$cluster" kagent-shim-spike:dev
k apply -f "$evidence/converted.yaml"
k apply -f proof/adapter.yaml
k -n orka-system rollout status deployment/kagent-shim-adapter --timeout=60s
k -n orka-system wait --for=jsonpath='{.status.ready}'=true providers.core.orka.ai/shim-model --timeout=60s
k -n orka-system wait --for=jsonpath='{.status.ready}'=true agents.core.orka.ai/shim-reader --timeout=60s
# No prompt or model ever sees this value except by the MCP tool result.
nonce="mcp-executed-$(python3 -c 'import secrets; print(secrets.token_hex(12))')"
k -n orka-system create configmap shim-proof-target --from-literal="proof=$nonce"
printf '%s' "$nonce" > "$evidence/expected.txt"
k apply --dry-run=server --validate=strict -f proof/task.yaml
k apply -f proof/task.yaml
k -n orka-system wait --for=jsonpath='{.status.phase}'=Succeeded tasks.core.orka.ai/shim-proof-success --timeout=220s
k -n orka-system port-forward svc/orka-api 18080:8080 > "$evidence/port-forward.log" 2>&1 &
forward=$!
for _ in {1..30}; do
  if grep -q 'Forwarding from' "$evidence/port-forward.log"; then break; fi
  kill -0 "$forward"
  sleep 1
done
for part in result events; do
  # Ephemeral API authentication stays in pipe/process memory, never an artifact.
  k -n orka-system create token orka-ai-worker --duration=10m | \
    python3 -c 'import sys; print("header = \"Authorization: Bearer " + sys.stdin.read().strip() + "\"")' | \
    curl -fsS --config - "http://127.0.0.1:18080/api/v1/tasks/shim-proof-success/$part?namespace=orka-system" > "$evidence/$part.json"
done
k -n orka-system logs deployment/shim-tools > "$evidence/mcp-server.log"
k -n orka-system get pods -o json > "$evidence/pods.json"
python3 - "$evidence" <<'PY'
import json
from pathlib import Path
import sys
p = Path(sys.argv[1])
result = json.loads((p / 'result.json').read_text())['result']
assert result == (p / 'expected.txt').read_text(), result
events = json.loads((p / 'events.json').read_text())['events']
assert any(e['type'] == 'ToolCallCompleted' and e['toolName'] == 'shim-4c93a72e18eb8dd4c3f84fc9b46250a9' for e in events)
assert any(e['type'] == 'ModelRequestStarted' and e['content']['toolCount'] == 5 for e in events)
log = (p / 'mcp-server.log').read_text()
assert 'command execution successful' in log and 'get configmap shim-proof-target -n orka-system -o json' in log
print('PASS: Task called the real MCP Kubernetes tool and returned fresh value:', result)
PY
