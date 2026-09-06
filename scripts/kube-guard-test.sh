#!/usr/bin/env bash
# Hermetic tests for scripts/kube-guard.sh — the context-safety net.
#
# No cluster and no network: the script builds its own throwaway
# kubeconfig, so CI can assert the guard's decisions in seconds. That
# matters because the guard is pure policy — every branch of it is a
# decision about whether to touch someone's cluster, and the expensive
# e2e job can only ever exercise the one branch CI itself runs on
# (local kind).
#
# Run:  bash scripts/kube-guard-test.sh
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
guard="$here/kube-guard.sh"
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

# Three contexts in the kubeconfig, chosen to separate NAME from ADDRESS:
#   kind-real    kind-named  + loopback   -> local, no confirmation
#   kind-sneaky  kind-named  + remote     -> remote (a name proves nothing)
#   aks-remote   other name  + remote     -> remote
# A fourth name, kind-not-created-yet, is tested precisely by being ABSENT
# from this file — that is `make up` on an empty machine.
cat > "$workdir/kubeconfig" <<'YAML'
apiVersion: v1
kind: Config
clusters:
  - name: c-local
    cluster: {server: 'https://127.0.0.1:36453'}
  - name: c-remote
    cluster: {server: 'https://example.invalid:443'}
contexts:
  - name: kind-real
    context: {cluster: c-local, user: u}
  - name: kind-sneaky
    context: {cluster: c-remote, user: u}
  - name: aks-remote
    context: {cluster: c-remote, user: u}
users:
  - name: u
    user: {}
current-context: kind-real
YAML
export KUBECONFIG="$workdir/kubeconfig"

fails=0
# run <expect-rc> <label> [env assignments...] -- runs the guard with no
# stdin (</dev/null), because "no TTY" is the CI/scripted case and the
# guard must never hang waiting for an answer nobody can give.
run() {
  local want=$1 label=$2
  shift 2
  local rc=0
  # `env -u` the guard's own inputs before setting the ones this case
  # wants. Without it the tests are not actually hermetic: the runbook
  # tells operators to `export KAIMAHI_CONFIRM=$AKS_CLUSTER` for a
  # session, and that is precisely the variable the refusal cases assume
  # is absent — so running these tests mid-AKS-session could turn a
  # "refused" case green. CI never had it set, which is exactly why this
  # would have gone unnoticed.
  env -u KAIMAHI_CONFIRM -u KUBE_CTX -u KUBE_NS \
    "$@" bash "$guard" "$label" </dev/null >"$workdir/out" 2>&1 || rc=$?
  if [ "$rc" != "$want" ]; then
    fails=$((fails + 1))
    echo "FAIL [$label]: exit $rc, want $want"
    sed 's/^/    | /' "$workdir/out"
  else
    echo "ok   [$label] exit $rc"
  fi
}

# Allowed: a genuinely local kind cluster.
run 0 "local kind proceeds" KUBE_CTX=kind-real

# Allowed: kind context that does not exist yet — this is `make up` on an
# empty machine (and every CI run), which must not need a confirmation.
run 0 "absent kind- context is 'about to be created'" KUBE_CTX=kind-not-created-yet

# Refused: a typo'd non-kind context. Before KUBE_CTX was overridable this
# was a harmless "context not found"; the guard must keep it harmless.
run 1 "absent non-kind context is a typo" KUBE_CTX=prod-oops

# Refused: kind-NAMED but remotely addressed. The whole point of checking
# the API server and not just the name.
run 1 "kind-named remote still needs confirmation" KUBE_CTX=kind-sneaky

# Refused: remote, no TTY, no pre-confirmation -> fail closed, never hang.
run 1 "remote without confirmation is refused" KUBE_CTX=aks-remote

# Refused: confirmation that names a DIFFERENT context must not carry
# over. Confirming one cluster is not consent for another.
run 1 "confirmation for another context is refused" \
  KUBE_CTX=aks-remote KAIMAHI_CONFIRM=kind-real

# Allowed: explicit, exact confirmation.
run 0 "exact confirmation admits a remote context" \
  KUBE_CTX=aks-remote KAIMAHI_CONFIRM=aks-remote

# Refused: no context named at all.
run 1 "empty KUBE_CTX is refused" KUBE_CTX=

# The banner is the other half of the contract: even when it proceeds
# without asking, the guard must say where the action is going.
env -u KAIMAHI_CONFIRM -u KUBE_NS KUBE_CTX=kind-real \
  bash "$guard" "banner check" </dev/null >/dev/null 2>"$workdir/banner"
banner_fails=0
# namespace(s) is part of the contract, not decoration: the guard's whole
# claim is that it says WHERE the action lands.
for needle in 'kind-real' '127.0.0.1' 'about to:' 'namespace(s):'; do
  if ! grep -q "$needle" "$workdir/banner"; then
    banner_fails=$((banner_fails + 1))
    echo "FAIL [banner]: missing '$needle'"
  fi
done
fails=$((fails + banner_fails))
# `[ ... ] && echo` is a top-level AND list: under `set -e` a false test
# would end the script right here, so the failure summary below would
# never print. Exit status stayed correct, but the reason vanished.
if [ "$banner_fails" -eq 0 ]; then
  echo "ok   [banner] prints context, server and namespaces"
fi

if [ "$fails" -ne 0 ]; then
  echo "kube-guard: $fails check(s) failed" >&2
  exit 1
fi
echo "kube-guard: all checks passed"
