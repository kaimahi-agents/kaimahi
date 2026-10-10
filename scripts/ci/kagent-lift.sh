#!/usr/bin/env bash
# External exact-version CI precondition, never a KMX installer.
set -euo pipefail
umask 077
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
mode=${1:-qualify}
case "$mode" in qualify|bootstrap) ;; *) echo 'usage: kagent-lift.sh [qualify|bootstrap]' >&2; exit 2 ;; esac
work=$(mktemp -d "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift.XXXXXX")
mkdir -p "$work/bin" "$work/charts/crds" "$work/charts/runtime"
export KUBECONFIG="$work/kubeconfig"
export KMX_HOME="$work/kmx-home" KMX_TOOLCHAIN=off
created=false
cleanup() {
  result=$?
  trap - EXIT
  if [ "$result" -ne 0 ]; then
    echo 'kagent lift qualification failed; no live-success claim (raw logs remain private)' >&2
    if [ "$created" = true ]; then
      python3 "$root/scripts/ci/kagent-lift.py" diagnostics || true
    fi
  fi
  if [ "$created" = true ]; then
    kind delete cluster --name kmx-kagent-lift > "$work/cleanup.log" 2>&1 || { echo 'dedicated cluster cleanup failed' >&2; result=1; }
  fi
  rm -rf "$work"
  exit "$result"
}
trap cleanup EXIT
# Never overwrite global tools. The CI proof uses these verified local binaries.
test "$(uname -s)" = Linux && test "$(uname -m)" = x86_64
curl -fsSL https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-amd64 -o "$work/bin/kind"
echo 'aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d' "$work/bin/kind" | sha256sum --check --status
curl -fsSL https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz -o "$work/helm.tar.gz"
echo 'dbb4c8fc8e19d159d1a63dda8db655f9ffa4aac1b9a6b188b34a40957119b286' "$work/helm.tar.gz" | sha256sum --check --status
tar -xzf "$work/helm.tar.gz" -C "$work"
cp "$work/linux-amd64/helm" "$work/bin/helm"
curl -fsSL https://dl.k8s.io/release/v1.35.0/bin/linux/amd64/kubectl -o "$work/bin/kubectl"
echo 'a2e984a18a0c063279d692533031c1eff93a262afcc0afdc517375432d060989' "$work/bin/kubectl" | sha256sum --check --status
chmod 0755 "$work/bin/kind" "$work/bin/helm" "$work/bin/kubectl"
export PATH="$work/bin:$PATH"
test "$(kind version -q)" = 0.33.0
test "$(helm version --template '{{.Version}}')" = v3.20.0
python3 "$root/scripts/ci/kagent-lift.py" cluster-absent
# A failed create is ambiguous: leave it for inspection, never infer ownership.
kind create cluster --name kmx-kagent-lift --image kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661 --wait 180s > "$work/kind.log" 2>&1
created=true
python3 "$root/scripts/ci/kagent-lift.py" node
helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds@sha256:76582530f0b58b48575d19936e19d790ca2cea8e0421775be65bd4c99b2d8da0 --untar --untardir "$work/charts/crds" > "$work/chart-crds.log" 2>&1
helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent@sha256:2f355400c760116e830cd7e86e7046477d480fb7dc309131832c51d65dfe9063 --untar --untardir "$work/charts/runtime" > "$work/chart-runtime.log" 2>&1
python3 "$root/scripts/ci/kagent-lift.py" values "$work/values.yaml"
helm template kagent "$work/charts/runtime/kagent" --namespace kagent -f "$work/values.yaml" > "$work/runtime.yaml"
python3 "$root/scripts/ci/kagent-lift.py" chart "$work/charts" "$work/runtime.yaml"
helm install kagent-crds "$work/charts/crds/kagent-crds" --namespace kagent --create-namespace --kube-context kind-kmx-kagent-lift --set kmcp.enabled=false --wait --timeout 5m > "$work/install-crds.log" 2>&1
helm install kagent "$work/charts/runtime/kagent" --namespace kagent --kube-context kind-kmx-kagent-lift -f "$work/values.yaml" --wait --timeout 10m > "$work/install-runtime.log" 2>&1
kubectl --context kind-kmx-kagent-lift -n kagent rollout status deployment/kagent-controller --timeout=300s > "$work/controller.log" 2>&1
kubectl --context kind-kmx-kagent-lift -n kagent rollout status deployment/kagent-postgresql --timeout=300s > "$work/postgres.log" 2>&1
python3 "$root/scripts/ci/kagent-lift.py" provision "$work"
python3 "$root/scripts/ci/kagent-lift.py" bootstrap
if [ "$mode" = bootstrap ]; then
  echo 'external bootstrap and deterministic fixture characterized; lift/native A2A NOT qualified'
  exit 0
fi
# These stubs are installed only AFTER the external install. KMX must neither
# fetch nor invoke installer tools. Record any attempt even if KMX swallows it.
mkdir "$work/fail-bin"
for tool in helm kagent; do
  printf '#!/bin/sh\ntouch "%s/installer-attempt"\nexit 97\n' "$work" > "$work/fail-bin/$tool"
  chmod 0755 "$work/fail-bin/$tool"
done
ln -s "$work/bin/kubectl" "$work/fail-bin/kubectl"
PATH="$work/fail-bin:$PATH" python3 "$root/scripts/ci/kagent-lift.py" qualify "$root/bin/kmx" "$work"
test ! -e "$work/installer-attempt"
mkdir -p "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift"
cp "$work/qualification.json" "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift/qualification.json"
echo 'Go lift/repeat/status/conflict and one native A2A MCP execution qualified'
