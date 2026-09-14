#!/usr/bin/env bash
# Presenter orchestration only: kmx owns installation, authoring and migration.
set -euo pipefail
umask 077

usage() {
  printf 'Usage: %s {prepare|record|verify|teardown} /absolute/run-directory\n' "$0"
  printf 'Each prepare requires a fresh directory and a dedicated kind cluster.\n'
  printf 'KIND_CLUSTER=kmx-hello-governed[-suffix]; DEMO_RECORD_PROFILE=presenter|docs\n'
  printf 'Recordings retain real elapsed time; setup and teardown are separately timed.\n'
}
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
case "${1:-}" in
  -h|--help) usage; exit 0 ;;
  prepare|record|verify|teardown|_terminal|_beats|_watch) ;;
  *) usage >&2; exit 64 ;;
esac
[[ $# == 2 && $2 == /* ]] || { usage >&2; exit 64; }
ACTION=$1
RUN_DIR=$2
export KIND_CLUSTER=${KIND_CLUSTER:-kmx-hello-governed}
[[ $KIND_CLUSTER =~ ^kmx-hello-governed(-[a-z0-9]+)*$ ]] || fail 'KIND_CLUSTER must be kmx-hello-governed or a suffixed demo name'
export DEMO_RECORD_PROFILE=${DEMO_RECORD_PROFILE:-presenter}
case $DEMO_RECORD_PROFILE in
  presenter) PAUSE=2 ;;
  docs) PAUSE=1 ;;
  *) fail 'DEMO_RECORD_PROFILE must be presenter or docs; compressed recordings are not evidence' ;;
esac
if [[ $ACTION == prepare ]]; then
  [[ ! -e $RUN_DIR ]] || fail "run directory already exists: $RUN_DIR"
else
  [[ -f $RUN_DIR/cluster && -f $RUN_DIR/kubeconfig ]] || fail 'run directory was not prepared by this demo'
  [[ $(<"$RUN_DIR/cluster") == "$KIND_CLUSTER" ]] || fail 'prepared cluster differs from KIND_CLUSTER'
fi
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SCRIPT=$ROOT/scripts/demo-hello-to-governed.sh
export KUBECONFIG=$RUN_DIR/kubeconfig
export KUBE_CTX=kind-$KIND_CLUSTER
export CONTAINER_ENGINE=docker MODEL=qwen2.5:3b
export PATH=$RUN_DIR/bin:$PATH
CTX=$KUBE_CTX
SOCKET=$RUN_DIR/tmux.sock
kmx() { "$RUN_DIR/bin/kmx" --context "$CTX" "$@"; }
kube() { kubectl --context "$CTX" "$@"; }
mux() { tmux -S "$SOCKET" -f /dev/null "$@"; }
run() { printf '\n$ '; printf '%q ' "$@"; printf '\n'; "$@"; sleep "$PAUSE"; }
now() { date +%s; }

# A real wall-clock measurement, including command output and presenter pauses.
beat() {
  local number=$1 sentence=$2 started elapsed
  shift 2
  started=$(now)
  printf '\n=== %s ===\n%s\n' "$number" "$sentence"
  "$@"
  elapsed=$(( $(now) - started ))
  printf 'Beat %s: %ss\n' "$number" "$elapsed" | tee -a "$RUN_DIR/timings.txt"
  if (( elapsed > 90 )); then
    printf 'This cold beat exceeded 90 seconds; the full wait is retained.\n'
  fi
}

prepare() {
  local tool started
  for tool in go docker kind kubectl helm python3 curl gh tmux asciinema; do
    command -v "$tool" >/dev/null || fail "missing prerequisite: $tool"
  done
  python3 -c 'import yaml'
  # Do not adopt or delete a cluster another invocation/lane already owns.
  local clusters
  clusters=$(kind get clusters)
  ! grep -Fxq "$KIND_CLUSTER" <<<"$clusters" || fail "cluster already exists: $KIND_CLUSTER"
  mkdir -p "$RUN_DIR/bin"
  printf '%s\n' "$KIND_CLUSTER" > "$RUN_DIR/cluster"
  started=$(now)
  exec > >(tee "$RUN_DIR/setup.log") 2>&1
  (cd "$ROOT" && go build -o "$RUN_DIR/bin/kmx" ./cmd/kmx)
  kmx up --step cluster
  kmx up --step ollama
  kmx up --step model
  kmx plane --source "$ROOT"
  kube create namespace orka-system
  # Provision prerequisite Secrets before the camera starts. Values only use stdin.
  python3 - <<'PY' | kubectl --context "$CTX" create -f -
import json, secrets
for name, data in [
    ("harness-wrapper-auth", {"token": secrets.token_hex(32)}),
    ("local-provider-key", {"api-key": "not-used-by-this-endpoint"}),
]:
    print(json.dumps({"apiVersion": "v1", "kind": "Secret", "metadata": {
        "name": name, "namespace": "orka-system"}, "stringData": data}))
    print("---")
PY
  kube -n orka-system create serviceaccount orka-result-reader
  # A Role can precede its CRD; kubectl's --resource shortcut requires discovery.
  printf '%s\n' '{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"orka-result-reader","namespace":"orka-system"},"rules":[{"apiGroups":["core.orka.ai"],"resources":["tasks"],"verbs":["get"]}]}' \
    | kubectl --context "$CTX" create -f -
  kube -n orka-system create rolebinding orka-result-reader --role=orka-result-reader \
    --serviceaccount=orka-system:orka-result-reader
  prepare_app
  gh pr view 564 --repo orka-agents/orka --json state > "$RUN_DIR/a2a-pr.json"
  printf 'Preparation: %ss (kind, model download, plane, existing app, prerequisite Secrets)\n' \
    "$(( $(now) - started ))" | tee "$RUN_DIR/setup-time.txt"
  git -C "$ROOT" rev-parse HEAD > "$RUN_DIR/revision.txt"
  sha256sum "$SCRIPT" > "$RUN_DIR/script.sha256"
  touch "$RUN_DIR/prepared"
}

prepare_app() {
  local revision=bd2a0354e38e7fe92a7b24e6a29ba637e745e91e
  local archive=$RUN_DIR/sundae.tar.gz source=$RUN_DIR/sundae-funday-$revision
  curl --fail --silent --show-error --location --max-time 120 \
    "https://codeload.github.com/pauldotyu/sundae-funday/tar.gz/$revision" -o "$archive"
  printf '0762981a550b293bd7b590daa835b4bfe87b58b7dd2ad9842e288860ac4043e4  %s\n' "$archive" | sha256sum -c -
  tar -xzf "$archive" -C "$RUN_DIR"
  # Build the existing application's unchanged Dockerfile and frozen lockfile.
  docker build -t sundae-funday:hello-demo-bd2a035 "$source"
  kind load docker-image sundae-funday:hello-demo-bd2a035 --name "$KIND_CLUSTER"
  kube create namespace demo
  python3 - <<'PY' | kubectl --context "$CTX" create -f -
import json
print(json.dumps({"apiVersion": "v1", "kind": "Secret", "metadata": {
    "name": "app-secrets", "namespace": "demo"}, "stringData": {
    "OPENAI_API_KEY": "not-used-by-this-endpoint"}}))
PY
  helm --kube-context "$CTX" install sundae "$source/deploy/helm/sundae-funday" \
    --namespace demo --wait --timeout 5m \
    --set image.registry= --set image.repository=sundae-funday \
    --set image.tag=hello-demo-bd2a035 --set image.pullPolicy=Never \
    --set secret.create=false --set secret.existingSecret=app-secrets \
    --set config.OPENAI_BASE_URL=http://ollama.ollama.svc.cluster.local:11434/v1 \
    --set config.OPENAI_CHAT_MODEL=qwen2.5:3b \
    --set-string config.ENABLE_INSTRUMENTATION=false \
    --set components.concierge.service.type=ClusterIP
}

hello() {
  run kmx agent create hello --namespace orka-system --provider-type openai \
    --model qwen2.5:3b --secret local-provider-key \
    --base-url http://ollama.ollama.svc.cluster.local:11434/v1 \
    --task 'Reply with exactly: Hello world.' --result-service-account orka-result-reader \
    --out "$RUN_DIR/hello.yaml"
}

show() {
  printf '\nThe generated Provider, Agent and Task YAML is the reviewable artifact.\n'
  # Omit only commentary and the value-free Secret skeleton; never read Secret data.
  python3 - "$RUN_DIR/hello.yaml" <<'PY'
import sys, yaml
with open(sys.argv[1]) as f:
    docs = [d for d in yaml.safe_load_all(f) if d and d["kind"] != "Secret"]
print(yaml.safe_dump_all(docs, sort_keys=False))
PY
  sleep "$PAUSE"
  run kmx agent show hello --namespace orka-system
}

snapshot() {
  kube -n demo get deployment concierge -o json | python3 -c '
import json,sys
x=json.load(sys.stdin)
print(json.dumps({"uid": x["metadata"]["uid"], "generation": x["metadata"]["generation"], "spec": x["spec"]}, sort_keys=True))'
}

wait_for_text() {
  local text=$1 file=$2 deadline=$((SECONDS + 30))
  until grep -q "$text" "$file" 2>/dev/null; do
    (( SECONDS < deadline )) || fail "timed out waiting for $text in $file"
    sleep 0.2
  done
}

migrate() {
  snapshot > "$RUN_DIR/before-migrate.json"
  # Reserve the second pane now, but start its forward after migrate rolls the proxy.
  local command
  printf -v command 'bash %q _watch %q' "$SCRIPT" "$RUN_DIR"
  mux split-window -v -l 12 "$command"
  mux select-pane -t demo:0.0
  run kmx migrate concierge --namespace demo --model local/qwen2.5:3b \
    --out "$RUN_DIR/concierge.yaml"
  snapshot > "$RUN_DIR/after-migrate.json"
  cmp "$RUN_DIR/before-migrate.json" "$RUN_DIR/after-migrate.json"
  printf 'kmx left the Deployment unchanged; its owner now applies the generated patch.\n'
  touch "$RUN_DIR/watch-ready"
  wait_for_text 'watching concierge' "$RUN_DIR/watch.log"
  run kubectl --context "$CTX" -n demo patch deployment concierge \
    --patch-file "$RUN_DIR/concierge-patch.yaml"
  run kubectl --context "$CTX" -n demo rollout status deployment/concierge --timeout=180s
  snapshot > "$RUN_DIR/after-owner-patch.json"
  python3 - "$RUN_DIR" <<'PY'
import json, pathlib, sys
p=pathlib.Path(sys.argv[1])
a=json.loads((p/"before-migrate.json").read_text())
b=json.loads((p/"after-owner-patch.json").read_text())
assert a["uid"] == b["uid"]
assert a["spec"]["template"]["spec"]["containers"][0]["image"] == b["spec"]["template"]["spec"]["containers"][0]["image"]
PY
  kube -n demo port-forward --address 127.0.0.1 svc/concierge 18301:80 > "$RUN_DIR/app-forward.log" 2>&1 &
  APP_PID=$!
  wait_for_text 'Forwarding from' "$RUN_DIR/app-forward.log"
  printf '\nThe existing concierge application now sends this fresh greeting through the model seam.\n'
  run curl --fail --silent --show-error --max-time 180 \
    http://127.0.0.1:18301/api/chat -H 'Content-Type: application/json' \
    --data '{"session_id":"hello-governed","message":"Hello! Please greet me in one short sentence."}' \
    -o "$RUN_DIR/app-answer.json"
  python3 - "$RUN_DIR/app-answer.json" <<'PY'
import json, sys
x=json.load(open(sys.argv[1]))
assert isinstance(x.get("reply"), str) and x["reply"].strip(), "application returned no answer"
print(x["reply"])
PY
  wait_for_text 'model.*concierge.*200' "$RUN_DIR/watch.log"
  kmx ledger concierge > "$RUN_DIR/ledger.txt"
  verify
  printf 'The application image is unchanged after its owner applied the routing patch.\n'
  sleep "$PAUSE"
}

# Also runnable after teardown: verify the saved model evidence, not cluster readiness.
verify() {
  python3 - "$RUN_DIR" <<'PY'
import json, pathlib, sys
p=pathlib.Path(sys.argv[1])
answer=json.loads((p/"app-answer.json").read_text())
assert isinstance(answer.get("reply"), str) and answer["reply"].strip(), "application returned no answer"
rows=[line.split() for line in (p/"ledger.txt").read_text().splitlines()]
rows=[r for r in rows if len(r)>8 and r[1]=="concierge" and r[2]=="orka"]
assert rows and all(r[8]=="200" for r in rows), "missing successful model rows or unexpected model failure"
assert all(r[3]=="local/qwen2.5:3b" for r in rows), "unexpected model"
assert sum(int(r[4])+int(r[5]) for r in rows)>0, "no actual model tokens"
print("The ledger records successful local-model traffic for concierge with nonzero token usage.")
PY
}

watch() {
  printf 'The lower pane will show new model ledger rows after migration prepares the credential.\n'
  local deadline=$((SECONDS + 180))
  until [[ -f $RUN_DIR/watch-ready ]]; do
    (( SECONDS < deadline )) || fail 'migration did not prepare the watch credential within 180s'
    sleep 0.2
  done
  printf '\n$ kmx --context %s watch concierge --interval 1s --for 4m\n' "$CTX"
  ADMIN_PORT=19093 kmx watch concierge --interval 1s --for 4m 2>&1 | tee "$RUN_DIR/watch.log"
}

teardown() {
  local started
  started=$(now)
  kmx down
  local clusters
  clusters=$(kind get clusters)
  ! grep -Fxq "$KIND_CLUSTER" <<<"$clusters" || fail 'dedicated cluster still exists'
  printf 'Teardown: %ss; dedicated cluster %s deleted.\n' "$(( $(now) - started ))" "$KIND_CLUSTER" | tee "$RUN_DIR/teardown.txt"
}

finish_terminal() {
  local status=$?
  trap - EXIT
  [[ -z ${APP_PID:-} ]] || kill "$APP_PID" 2>/dev/null || true
  printf '%s\n' "$status" > "$RUN_DIR/exit-status"
  if (( status != 0 )); then
    printf '\nDemo failed (exit %s); no success claimed. Recording retained.\n' "$status"
  fi
  sleep 3
  mux kill-session -t demo || true
}

beats() {
  trap finish_terminal EXIT
  local started
  started=$(now)
  printf 'Hello world to governed model traffic: one kmx journey, local Ollama qwen2.5:3b, no paid model endpoint.\n'
  printf 'Kind, Ollama, the plane and the existing app were prepared before recording; prerequisite Secrets were piped in off-screen.\n'
  printf '%s\n' "$(<"$RUN_DIR/setup-time.txt")"
  printf 'No Orka installation or model answer was pre-run; all waits below are real time.\n'
  beat 1 'This command installs the pinned Orka platform on the dedicated kind cluster.' run kmx orka install
  beat 2 'This command creates a Provider, hello Agent and Task, then retrieves a real local-model answer.' hello
  beat 3 'This is the agent-as-code artifact and its live Provider-to-Secret dependency chain.' show
  beat 4 'kmx prepares the governed route; the application owner applies the configuration change.' migrate
  local state
  state=$(gh pr view 564 --repo orka-agents/orka --json state --jq .state)
  printf '%s\n' "$state" > "$RUN_DIR/a2a-state.txt"
  if [[ $state == MERGED ]]; then
    printf 'Next: deploy and verify the external A2A adapter against this installed Orka version; this run does not demonstrate it.\n'
  else
    printf 'Next: external A2A discovery and an answer from hello after orka-agents/orka PR #564 merges (currently %s).\n' "$state"
  fi
  printf 'Demo total: %ss\n' "$(( $(now) - started ))" | tee -a "$RUN_DIR/timings.txt"
  mux send-keys -t demo:0.1 C-c
  sleep 1
  teardown
}

record() {
  [[ -f $RUN_DIR/prepared ]] || fail 'setup has not finished; directory is not prepared'
  [[ ! -e $RUN_DIR/demo.cast ]] || fail 'recording already exists; use a new fresh run, never overwrite evidence'
  command -v agg >/dev/null || fail 'install asciinema agg on PATH to render the recording'
  local command
  printf -v command 'bash %q _terminal %q' "$SCRIPT" "$RUN_DIR"
  # No stdin capture, no idle limit, no speedup. Preserve long cold waits.
  asciinema rec --cols 144 --rows 54 --env TERM --title 'Hello world to governed model traffic' \
    --command "$command" "$RUN_DIR/demo.cast"
  [[ -f $RUN_DIR/exit-status ]] || fail 'recording ended without an exit status'
  [[ $(<"$RUN_DIR/exit-status") == 0 ]] || fail "demo failed; retained $RUN_DIR/demo.cast; run teardown after diagnosis"
  agg --speed 1 --idle-time-limit 999999 --theme github-dark --font-size 16 \
    "$RUN_DIR/demo.cast" "$RUN_DIR/demo.gif"
  printf 'Recording: %s/demo.cast\nRendered: %s/demo.gif\n' "$RUN_DIR" "$RUN_DIR"
}

case $ACTION in
  prepare) prepare ;;
  record) record ;;
  teardown) teardown ;;
  verify) verify ;;
  _watch) watch ;;
  _beats) beats ;;
  _terminal)
    printf -v command 'bash %q _beats %q' "$SCRIPT" "$RUN_DIR"
    mux new-session -s demo "$command"
    ;;
esac
