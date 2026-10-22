#!/usr/bin/env bash
# External exact-version CI qualification, never an installed CLI installer.
set -euo pipefail
umask 077
exec 3>&1 4>&2
work=
created=false
phase=arguments
mode=${1:-qualify}
span=
# Closed timing markers: fixed span names, numeric UTC epoch seconds and
# integer status only; never commands, paths, environment or payloads.
timing() {
  case "$1:$2" in
    bootstrap:start | qualify:start | suite-evaluation:start | suite-helpers:start | cleanup:start) span=$1 ;;
    bootstrap:end | qualify:end | suite-evaluation:end | suite-helpers:end | cleanup:end) span= ;;
    *) return 0 ;;
  esac
  printf 'kagent timing: span=%s boundary=%s utc_epoch=%s%s\n' "$1" "$2" "${EPOCHREALTIME/[!0-9]/.}" \
    "${3:+ status=$(($3))}" >&4
}
cleanup() {
  result=$?
  trap - EXIT
  # A span still open here failed; end it with the primary status.
  if [ -n "$span" ]; then timing "$span" end "$result"; fi
  if [ "$result" -ne 0 ]; then
    printf 'kagent bootstrap failed: phase=%s exitstatus=%s; runtime MCP/native A2A NOT qualified\n' "$phase" "$result" >&4
    if [ "$phase" = kind-create ]; then
      printf 'failed kind create is ambiguous; unowned cluster left for inspection\n' >&4
    fi
  fi
  cleanup_status=0
  if [ "$phase" != arguments ]; then timing cleanup start; fi
  if [ "$created" = true ]; then
    phase=cluster-cleanup
    if kind delete cluster --name kmx-kagent-lift > "$work/cleanup.log" 2>&1; then
      :
    else
      cleanup_status=$?
      printf 'kagent bootstrap failed: phase=cluster-cleanup exitstatus=%s\n' "$cleanup_status" >&4
      if [ "$result" -eq 0 ]; then result=$cleanup_status; fi
    fi
  fi
  if [ -n "$work" ]; then
    phase='work-cleanup'
    if rm -rf -- "$work" >/dev/null 2>&1; then
      :
    else
      cleanup_status=$?
      printf 'kagent bootstrap failed: phase=work-cleanup exitstatus=%s\n' "$cleanup_status" >&4
      if [ "$result" -eq 0 ]; then result=$cleanup_status; fi
    fi
  fi
  if [ "$span" = cleanup ]; then timing cleanup end "$cleanup_status"; fi
  if [ "$result" -eq 0 ]; then
    if [ "$mode" = bootstrap ]; then
      printf 'external bootstrap and deterministic fixture characterized; runtime MCP/native A2A NOT qualified\n' >&3
    else
      printf 'one independent upstream Go native A2A MCP execution qualified\n' >&3
      printf 'tool-free native full eval/evaluated push/source-free digest Lift qualified separately; no MCP tool qualification from suite phase\n' >&3
      printf 'native_a2a_counts=tool:1 model:2 terminal:1\n' >&3
      printf 'private suite kagent prepare/place/observe qualified; no additional execution calls\n' >&3
    fi
  fi
  exit "$result"
}
trap cleanup EXIT
if [ "$#" -gt 1 ] || { [ "$mode" != bootstrap ] && [ "$mode" != qualify ]; }; then
  printf 'usage: kagent-lift.sh [qualify|bootstrap]\n' >&4
  exit 2
fi
timing bootstrap start
phase=root
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd) 2>/dev/null
phase='work-create'
work=$(mktemp -d "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift.XXXXXX") 2>/dev/null
# All external output, including download/extract/version errors, is private.
# Only our closed phase/status messages use the original output descriptors.
phase=private-log
exec >"$work/raw.log" 2>&1
phase='work-layout'
mkdir -p "$work/bin" "$work/charts/crds" "$work/charts/runtime" "$work/kube-cache" \
  "$work/helm-config/registry" "$work/helm-cache/repository" "$work/helm-data/plugins"
export KUBECONFIG="$work/kubeconfig" KUBECACHEDIR="$work/kube-cache"
export KMX_HOME="$work/kmx-home" KMX_TOOLCHAIN=off
export HELM_CONFIG_HOME="$work/helm-config" HELM_CACHE_HOME="$work/helm-cache" HELM_DATA_HOME="$work/helm-data"
export HELM_REGISTRY_CONFIG="$work/helm-config/registry/config.json"
export HELM_REPOSITORY_CONFIG="$work/helm-config/repositories.yaml"
export HELM_REPOSITORY_CACHE="$work/helm-cache/repository" HELM_PLUGINS="$work/helm-data/plugins"
# Connection/auth overrides can defeat even an explicit kube-context.
unset "${!HELM_KUBE@}"
phase=registry-config
# An empty GHCR entry is keyless but suppresses Helm's native-helper detection.
# Scope its Docker credential fallback to Helm, preserving kind/Docker access.
printf '%s\n' '{"auths":{"ghcr.io":{}}}' > "$HELM_REGISTRY_CONFIG"
helm() {
  DOCKER_CONFIG="$work/helm-config/registry" command helm "$@"
}
# Never overwrite global tools or use the caller's kubeconfig or Helm auth/cache.
phase=platform
uname -s > "$work/os"
uname -m > "$work/arch"
test "$(<"$work/os")" = Linux
test "$(<"$work/arch")" = x86_64
phase=kind-download
curl -fsSL https://kind.sigs.k8s.io/dl/v0.33.0/kind-linux-amd64 -o "$work/bin/kind"
phase=kind-checksum
printf '%s %s\n' 'aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d' "$work/bin/kind" | sha256sum --check --status
phase=helm-download
curl -fsSL https://get.helm.sh/helm-v3.20.0-linux-amd64.tar.gz -o "$work/helm.tar.gz"
phase=helm-checksum
printf '%s %s\n' 'dbb4c8fc8e19d159d1a63dda8db655f9ffa4aac1b9a6b188b34a40957119b286' "$work/helm.tar.gz" | sha256sum --check --status
phase=helm-extract
tar -xzf "$work/helm.tar.gz" -C "$work"
phase=helm-copy
cp "$work/linux-amd64/helm" "$work/bin/helm"
phase=kubectl-download
curl -fsSL https://dl.k8s.io/release/v1.35.0/bin/linux/amd64/kubectl -o "$work/bin/kubectl"
phase=kubectl-checksum
printf '%s %s\n' 'a2e984a18a0c063279d692533031c1eff93a262afcc0afdc517375432d060989' "$work/bin/kubectl" | sha256sum --check --status
phase='tool-permissions'
chmod 0755 "$work/bin/kind" "$work/bin/helm" "$work/bin/kubectl"
export PATH="$work/bin:$PATH"
phase=kind-version
kind version -q > "$work/kind-version"
test "$(<"$work/kind-version")" = 0.33.0
phase=helm-version
helm version --template '{{.Version}}' > "$work/helm-version"
test "$(<"$work/helm-version")" = v3.20.0
phase=cluster-absent
python3 "$root/scripts/ci/kagent-lift.py" cluster-absent
phase=kind-create
# A failed create is ambiguous: never infer ownership from a partial cluster.
kind create cluster --name kmx-kagent-lift --image kindest/node:v1.35.0@sha256:4613778f3cfcd10e615029370f5786704559103cf27bef934597ba562b269661 --wait 180s
created=true
phase=node
python3 "$root/scripts/ci/kagent-lift.py" node
phase=chart-crds-pull
helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds@sha256:76582530f0b58b48575d19936e19d790ca2cea8e0421775be65bd4c99b2d8da0 --untar --untardir "$work/charts/crds"
phase=chart-runtime-pull
helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent@sha256:2f355400c760116e830cd7e86e7046477d480fb7dc309131832c51d65dfe9063 --untar --untardir "$work/charts/runtime"
phase=values
python3 "$root/scripts/ci/kagent-lift.py" values "$work/values.yaml"
phase=chart-render
helm template kagent "$work/charts/runtime/kagent" --namespace kagent -f "$work/values.yaml" > "$work/runtime.yaml"
phase=chart
python3 "$root/scripts/ci/kagent-lift.py" chart "$work/charts" "$work/runtime.yaml"
phase=install-crds
helm install kagent-crds "$work/charts/crds/kagent-crds" --namespace kagent --create-namespace --kube-context kind-kmx-kagent-lift --set kmcp.enabled=false --wait --timeout 5m
phase=install-runtime
helm install kagent "$work/charts/runtime/kagent" --namespace kagent --kube-context kind-kmx-kagent-lift -f "$work/values.yaml" --wait --timeout 10m
phase=controller-ready
kubectl --context kind-kmx-kagent-lift -n kagent rollout status deployment/kagent-controller --timeout=300s
phase=postgres-ready
kubectl --context kind-kmx-kagent-lift -n kagent rollout status deployment/kagent-postgresql --timeout=300s
phase=provision
python3 "$root/scripts/ci/kagent-lift.py" provision "$work"
phase=bootstrap
python3 "$root/scripts/ci/kagent-lift.py" bootstrap
timing bootstrap end 0
if [ "$mode" = bootstrap ]; then exit 0; fi
# Install traps only after external bootstrap; record swallowed tool attempts.
phase=installer-traps
mkdir "$work/fail-bin"
for tool in helm kagent; do
  printf '#!/bin/sh\ntouch "%s/installer-attempt"\nexit 97\n' "$work" > "$work/fail-bin/$tool"
  chmod 0755 "$work/fail-bin/$tool"
done
ln -s "$work/bin/kubectl" "$work/fail-bin/kubectl"
phase=qualify
timing qualify start
KMX_QUALIFICATION_DIAGNOSTIC_FD=4 PATH="$work/fail-bin:$PATH" python3 "$root/scripts/ci/kagent-lift.py" qualify "$root/bin/kmx" "$work"
timing qualify end 0
phase=suite-evaluation
timing suite-evaluation start
PATH="$work/fail-bin:$PATH" python3 "$root/scripts/ci/suite-kagent.py" "$root/bin/kmx" "$work"
timing suite-evaluation end 0
# Qualify runtime-specific private helpers without taking over release lift.
# The existing fixture, cluster and private logs remain the only infrastructure.
phase=suite-helpers
timing suite-helpers start
(cd "$root" && KMX_SUITE_KAGENT_LIVE=1 KMX_LIVE_KIND_CONTEXT=kind-kmx-kagent-lift \
  PATH="$work/fail-bin:$PATH" go test ./internal/kmx/app -run '^TestLiveSuiteKagentPlacement$' -count=1 -timeout=8m)
timing suite-helpers end 0
phase=installer-audit
test ! -e "$work/installer-attempt"
phase=artifact
mkdir -p "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift"
cp "$work/qualification.json" "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift/qualification.json"
cp "$work/suite-qualification.json" "${RUNNER_TEMP:-/tmp}/kmx-kagent-lift/suite-qualification.json"
