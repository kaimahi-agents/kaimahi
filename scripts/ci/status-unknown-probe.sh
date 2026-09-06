#!/usr/bin/env bash
# Prove the cannot-tell branch of `kmx status` on a real cluster.
#
# A reader that cannot LIST the tool seams or the Secret names must produce
# a stated `unknown` carrying kubectl's own reason, and must publish no
# counts at all — never "0 governed", which is the false zero this whole
# change exists to prevent. The half that CAN be read stays counted.
#
# The reader is a ServiceAccount minted on the cluster and a token that
# lives for the run: no repo secret, because CI in this public,
# fork-exposed repo holds no credential of any kind.
set -euo pipefail

ctx="${KUBE_CTX:-kind-${KIND_CLUSTER:-kaimahi-p1}}"
kmx="${KMX:-bin/kmx}"
work="$(mktemp -d)"
cleanup() {
  rm -rf "$work"
  kubectl --context "$ctx" delete clusterrolebinding kmx-narrow-reader --ignore-not-found >/dev/null
  kubectl --context "$ctx" delete clusterrole kmx-narrow-reader --ignore-not-found >/dev/null
  kubectl --context "$ctx" delete sa kmx-narrow -n default --ignore-not-found >/dev/null
}
trap cleanup EXIT

kubectl --context "$ctx" create sa kmx-narrow -n default
# Everything status reads EXCEPT remotemcpservers and secrets.
kubectl --context "$ctx" create clusterrole kmx-narrow-reader --verb=get,list --resource=pods,namespaces,deployments,agents.kagent.dev,modelconfigs.kagent.dev
kubectl --context "$ctx" create clusterrolebinding kmx-narrow-reader --clusterrole=kmx-narrow-reader --serviceaccount=default:kmx-narrow

server="$(kubectl --context "$ctx" config view -o "jsonpath={.clusters[?(@.name=='$ctx')].cluster.server}")"
ca="$(kubectl --context "$ctx" config view --raw -o "jsonpath={.clusters[?(@.name=='$ctx')].cluster.certificate-authority-data}")"
token="$(kubectl --context "$ctx" create token kmx-narrow -n default --duration=1h)"

umask 077
cat > "$work/kubeconfig" <<EOF
apiVersion: v1
kind: Config
clusters: [{name: $ctx, cluster: {server: $server, certificate-authority-data: $ca}}]
users: [{name: kmx-narrow, user: {token: $token}}]
contexts: [{name: $ctx, context: {cluster: $ctx, user: kmx-narrow}}]
current-context: $ctx
EOF

KUBECONFIG="$work/kubeconfig" "$kmx" status | tee "$work/status.out"
grep -E 'tool seams: +unknown — .*remotemcpservers' "$work/status.out"
grep -E 'credentials: +unknown — .*secrets' "$work/status.out"

KUBECONFIG="$work/kubeconfig" "$kmx" status -o json > "$work/status.json"
python3 - "$work/status.json" <<'PY'
import json, sys

governance = json.load(open(sys.argv[1]))["governance"]
for name in ("toolSeams", "credentials"):
    population = governance[name]
    assert population["state"] == "unknown", (name, population)
    assert population["reason"], (name, population)
    for absent in ("governed", "direct", "total", "required", "present", "missing"):
        assert absent not in population, (name, absent, population)
# The half that COULD be read is still counted: one unreadable population
# must not take the readable ones down with it.
assert governance["modelSeams"]["state"] == "counted", governance["modelSeams"]
assert governance["plane"]["state"] == "installed", governance["plane"]
print("cannot-tell branch proven:", json.dumps(governance))
PY
