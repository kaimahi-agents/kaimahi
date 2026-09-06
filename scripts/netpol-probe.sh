#!/usr/bin/env bash
# Prove the plane's network boundary is ENFORCED, not merely present.
#
# A NetworkPolicy the CNI ignores looks identical to one it enforces:
# the objects exist, kubectl shows them, nothing is blocked. So this
# script does not check that policies exist. It asserts NEGATIVES —
# connections that must time out — against a CONTROL that must succeed,
# so a "blocked" result can only mean policy (not a dead target, a
# runner without internet, or a typo'd address).
#
# The matrix (see k8s/plane/network-policy.yaml for the rules):
#
#   who                               dns  ollama  postgres  net:443  net:80
#   control (default ns, unpoliced)    ok    ok     BLOCKED    ok       ok
#   proxy-shaped (kaimahi)             ok    ok       ok     BLOCKED  BLOCKED
#   slack-shaped (kaimahi)             ok  BLOCKED  BLOCKED    ok     BLOCKED
#   unlabeled (kaimahi)             BLOCKED BLOCKED  BLOCKED  BLOCKED  BLOCKED
#   kaimahi-postgres (the real pod) BLOCKED BLOCKED    -     BLOCKED  BLOCKED
#
# The control reaching everything except postgres proves the targets
# are live AND that postgres's ingress rule holds from outside the
# namespace. The unlabeled pod reaching nothing proves default-deny is
# enforced at all — if the CNI ignored policy, that row would read
# "ok" across and this script fails with a message saying exactly that.
# The shaped pods carry the same labels the real proxy and Slack MCP
# pods carry, so they are evaluated by the same rules; the real proxy
# is distroless (nothing to exec), and CI deploys no Slack pod (no
# token, no CPU). The real Postgres pod IS exec'd: it is the negative on
# a live workload, not a stand-in. Every probe pod is BestEffort (no
# requests) so it schedules on the full CI node, runs as non-root, and
# is deleted on exit.
#
# With Copilot enabled (`make plane-copilot-secret` applies
# k8s/egress-copilot.yaml) the proxy row legitimately reaches net:443;
# set COPILOT_EGRESS=1 to expect that instead of failing on it.
#
# Env:
#   KUBECTL          kubectl invocation incl. --context
#   PROBE_IMAGE      default busybox:1.36
#   INTERNET_TARGET  a public IP answering on 443 and 80 (default 1.1.1.1)
#   COPILOT_EGRESS   1 if k8s/egress-copilot.yaml is applied
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"
NAMESPACE=kaimahi
PROBE_IMAGE="${PROBE_IMAGE:-busybox:1.36}"
INTERNET_TARGET="${INTERNET_TARGET:-1.1.1.1}"
COPILOT_EGRESS="${COPILOT_EGRESS:-0}"
SETTLE_SECONDS="${SETTLE_SECONDS:-5}"

# Context safety: run directly, so nothing has resolved a context
# for us — derive it from $KUBECTL, never from an inherited KUBE_CTX
# (see scripts/tool-denial-probe.sh for why).
# shellcheck disable=SC2086 # KUBECTL deliberately carries --context args
probe_ctx=$($KUBECTL config view --minify -o jsonpath='{.contexts[0].name}')
KUBE_NS="$NAMESPACE, default" KUBE_CTX="$probe_ctx" \
  bash "$(dirname "$0")/kube-guard.sh" "$(basename "$0")"

suffix=$(od -An -N3 -tx1 /dev/urandom | tr -d ' ')
pods=()
cleanup() {
  for p in "${pods[@]:-}"; do
    if [ -n "$p" ]; then
      $KUBECTL delete pod --ignore-not-found --wait=false "${p#*/}" -n "${p%%/*}" >/dev/null 2>&1 || true
    fi
  done
}
trap cleanup EXIT

# Under errexit a bare `var=$(cmd)` exits on cmd's failure before any
# message, so capture the outcome explicitly to say what to do about it.
if ! pg_ip=$($KUBECTL -n "$NAMESPACE" get svc kaimahi-postgres -o jsonpath='{.spec.clusterIP}' 2>&1) || [ -z "$pg_ip" ]; then
  echo "kaimahi-postgres Service not found — deploy the plane first (make plane): $pg_ip" >&2
  exit 1
fi
# Only a genuine NotFound may drop the ollama column (a Copilot-only
# managed cluster has no ollama). Any other failure — RBAC, a
# transient API error, a wrong context — must not quietly turn into
# "nothing to check" and shrink every row's assertions.
ollama_ip=""
if out=$($KUBECTL -n ollama get svc ollama -o jsonpath='{.spec.clusterIP}' 2>&1); then
  ollama_ip=$out
elif ! printf '%s' "$out" | grep -q 'NotFound'; then
  echo "cannot look up the ollama Service (refusing to skip its column): $out" >&2
  exit 1
fi
if [ -z "$ollama_ip" ]; then
  echo "note: no ollama Service on this cluster — the ollama column is SKIPPED, not asserted" >&2
fi

# The check program every probe runs. Prints one RESULT line per target
# and always exits 0, so a pod's phase never hides its findings.
# Targets are IPs on purpose: name resolution is its own column.
#
# The settle sleep is a MEASURED number, not a guess: on kind
# (kube-network-policies) a brand-new pod's first packets leave before
# the enforcer's pod informer has seen it, so its first ~1-2s of egress
# are unpoliced (three runs, 2026-09-01: reachable at t+0s, blocked
# from t+1s or t+2s onward). Without the settle, this probe's first two
# checks in the unlabeled pod read "reachable" and the run failed —
# correctly, since that IS a gap; docs/egress.md records it. 5s is the
# measured window with margin for a slower CI node. It affects only
# pods that dial out in their first moments; the real workloads here
# are long-lived and the exec'd Postgres pod needs no settle at all.
checks="
sleep $SETTLE_SECONDS
r() { n=\$1; shift; if \"\$@\" >/dev/null 2>&1; then echo \"RESULT \$n reachable\"; else echo \"RESULT \$n blocked\"; fi; }
r dns timeout 6 nslookup kubernetes.default.svc.cluster.local
${ollama_ip:+r ollama nc -z -w 3 $ollama_ip 11434}
r postgres nc -z -w 3 $pg_ip 5432
r net443 nc -z -w 5 $INTERNET_TARGET 443
r net80 nc -z -w 5 $INTERNET_TARGET 80
"

# start_probe <ns> <name> <labels-json> — create a throwaway pod that runs
# the checks and exits; its log is the result. Creating every probe pod at
# once and collecting them afterwards is what keeps this step near the
# duration of ONE probe rather than the sum of five: each pod's ~25s of
# deliberate timeouts is wall-clock nobody has to spend twice. The pods are
# independent — different pods, different labels, read-only checks against
# unrelated targets — and the RESULTS are still evaluated in the order below,
# with the same early exits, so a run reads exactly as it always did.
start_probe() {
  local ns=$1 name=$2 labels=$3
  pods+=("$ns/$name")
  $KUBECTL -n "$ns" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $name
  namespace: $ns
  labels: $labels
spec:
  restartPolicy: Never
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
    - name: probe
      image: $PROBE_IMAGE
      command: ["sh", "-c", $(printf '%s' "$checks" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')]
      securityContext:
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
EOF
}

# collect_probe <ns> <name> — bounded wait for the pod to finish (image pull
# + up to ~25s of timeouts), then its log. Phase Failed would mean the shell
# itself died — the checks never exit nonzero — so require Succeeded
# specifically.
collect_probe() {
  local ns=$1 name=$2 phase=
  for _ in $(seq 1 180); do
    phase=$($KUBECTL -n "$ns" get pod "$name" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in Succeeded) break ;; Failed) break ;; esac
    sleep 1
  done
  if [ "$phase" != Succeeded ]; then
    echo "probe pod $ns/$name did not complete (phase '$phase'):" >&2
    $KUBECTL -n "$ns" describe pod "$name" 2>&1 | tail -15 >&2
    return 1
  fi
  $KUBECTL -n "$ns" logs "$name"
}

# in_background <who> <command...> — run one probe's collection into
# $work/<who>, recording its exit status in $work/<who>.status so a failure
# inside a background subshell cannot pass as an empty result file.
in_background() {
  local who=$1
  shift
  (
    if "$@" > "$work/$who" 2>"$work/$who.err"; then
      echo 0 > "$work/$who.status"
    else
      echo 1 > "$work/$who.status"
    fi
  ) &
}

# await_probes <who...> — wait for every background collection and fail
# closed on any that did not finish cleanly, printing what it said. An
# absent status file is a failure too: a killed subshell must never read as
# an empty, uncontested result.
await_probes() {
  wait
  local who status
  for who in "$@"; do
    status=$(cat "$work/$who.status" 2>/dev/null || echo missing)
    cat "$work/$who.err" >&2 2>/dev/null || true
    if [ "$status" != 0 ]; then
      echo "netpol-probe: the $who probe did not produce results (status '$status')" >&2
      exit 1
    fi
  done
}

# exec_probe <ns> <deploy> <loopback-port> — the same checks inside a
# REAL pod, prefixed with a POSITIVE the pod must pass: a connect to its
# own listener over loopback, which no NetworkPolicy governs. Without
# it, an image with no `nc` (exit 127) would print "blocked" down the
# whole row and the run would report the boundary enforced without the
# pod ever having dialed anything — the exact fail-open shape the
# board's probe rule forbids. The throwaway pods do not need this: the
# control pod runs the same image and must read "reachable" first.
exec_probe() {
  $KUBECTL -n "$1" exec "deploy/$2" -- sh -c "
r() { n=\$1; shift; if \"\$@\" >/dev/null 2>&1; then echo \"RESULT \$n reachable\"; else echo \"RESULT \$n blocked\"; fi; }
r loopback nc -z -w 3 127.0.0.1 $3
$checks"
}

failures=0
# expect <who> <results-file> <target>=<reachable|blocked|skip> ...
expect() {
  local who=$1 file=$2; shift 2
  local spec target want got
  for spec in "$@"; do
    target=${spec%%=*}; want=${spec#*=}
    [ "$want" = skip ] && continue
    got=$(awk -v t="$target" '$1=="RESULT" && $2==t {print $3}' "$file")
    if [ -z "$got" ]; then
      # Only the ollama column may be absent, and only when there is no
      # ollama Service; anything else missing means the checks did not
      # run, which must not pass as "blocked".
      if [ "$target" = ollama ] && [ -z "$ollama_ip" ]; then continue; fi
      echo "  $who: $target — NO RESULT (probe did not run)"; failures=$((failures + 1)); continue
    fi
    if [ "$got" = "$want" ]; then
      echo "  $who: $target $got (expected)"
    else
      echo "  $who: $target $got — EXPECTED $want"; failures=$((failures + 1))
    fi
  done
}

work=$(mktemp -d)
trap 'cleanup; rm -rf "$work"' EXIT

echo "== starting every probe at once (results are asserted in order below)"
start_probe default "netpol-control-$suffix" '{}'
start_probe "$NAMESPACE" "netpol-unlabeled-$suffix" '{}'
start_probe "$NAMESPACE" "netpol-proxy-$suffix" '{"app": "kaimahi-proxy"}'
start_probe "$NAMESPACE" "netpol-slack-$suffix" '{"app.kubernetes.io/name": "kaimahi-slack-mcp"}'
in_background control collect_probe default "netpol-control-$suffix"
in_background unlabeled collect_probe "$NAMESPACE" "netpol-unlabeled-$suffix"
in_background proxy collect_probe "$NAMESPACE" "netpol-proxy-$suffix"
in_background slack collect_probe "$NAMESPACE" "netpol-slack-$suffix"
# The real Postgres pod is exec'd at the same time — it is already running,
# so it has nothing to wait for but its own checks.
in_background postgres exec_probe "$NAMESPACE" kaimahi-postgres 5432
await_probes control unlabeled proxy slack postgres

echo "== control: unpoliced pod in namespace default"
expect control "$work/control" dns=reachable ollama=reachable postgres=blocked net443=reachable net80=reachable
if grep -q 'blocked' <(awk '$2!="postgres"' "$work/control"); then
  echo "  the control cannot reach a target — nothing below can be attributed to policy" >&2
  echo "  (no internet from this cluster? ollama not running?)" >&2
  exit 1
fi

echo "== unlabeled pod in namespace $NAMESPACE (default-deny, no allowance)"
if ! grep -q blocked "$work/unlabeled"; then
  echo "  NetworkPolicy is NOT ENFORCED on this cluster: a pod with no allowance reached everything." >&2
  echo "  The policies exist and protect nothing. See docs/egress.md (CNI enforcement)." >&2
  exit 1
fi
expect unlabeled "$work/unlabeled" dns=blocked ollama=blocked postgres=blocked net443=blocked net80=blocked

echo "== proxy-shaped pod (labels of kaimahi-proxy)"
if [ "$COPILOT_EGRESS" = 1 ]; then proxy_net=reachable; else proxy_net=blocked; fi
expect proxy "$work/proxy" dns=reachable ollama=reachable postgres=reachable net443=$proxy_net net80=blocked

echo "== slack-shaped pod (labels of the Slack MCP server)"
expect slack "$work/slack" dns=reachable ollama=blocked postgres=blocked net443=reachable net80=blocked

echo "== kaimahi-postgres (exec into the real pod)"
# loopback is the row's positive (see exec_probe). The postgres column
# is the pod dialing its own Service IP; not a boundary statement either
# way, so it is not asserted.
expect postgres "$work/postgres" loopback=reachable dns=blocked ollama=blocked postgres=skip net443=blocked net80=blocked

if [ "$failures" -ne 0 ]; then
  echo "netpol-probe: $failures expectation(s) failed" >&2
  exit 1
fi
echo "netpol-probe: boundary enforced as written"
