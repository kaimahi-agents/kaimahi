#!/usr/bin/env bash
set +x
set -Eeuo pipefail
umask 077

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
kmx="${KMX_BIN:-$repo_root/bin/kmx}"
artifacts="${ARTIFACT_DIR:?set ARTIFACT_DIR for receipt and verify report}"
image='ghcr.io/kaito-project/aikit/qwen3.5:2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4'
work="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/kmx-eval-loop.XXXXXX")"
container="${work##*/}-aikit"
model_port="${AIKIT_HOST_PORT:-18089}"
sessions_port="${SESSIONS_PORT:-18088}"
sessions="127.0.0.1:$sessions_port"
daemon_pid=''
start=$SECONDS

cleanup() {
  local status=$?
  trap - EXIT
  if (( status != 0 )); then
    printf 'live eval loop failed after %ss\n' "$((SECONDS - start))" >&2
    printf '%s\n' '=== AIKit failure logs (public toy bundle) ===' >&2
    docker logs --tail 100 "$container" >&2 2>&1 || true
    if [[ -f "$work/daemon.log" ]]; then
      printf '%s\n' '=== agentsessions failure logs ===' >&2
      tail -n 100 "$work/daemon.log" >&2
    fi
  fi
  if [[ -n "$daemon_pid" ]]; then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

for cmd in docker curl jq go python3; do
  command -v "$cmd" >/dev/null || { printf 'missing command: %s\n' "$cmd" >&2; exit 1; }
done
test -x "$kmx"
cd "$repo_root"
# Build exactly the same revision as kmx's local replay harness, not @latest.
module_dir="$(go list -m -f '{{.Dir}}' github.com/aramase/agentsessions)"
(cd "$module_dir" && GOWORK=off go build -o "$work/agentsessionsd" ./cmd/agentsessionsd)
cp -R internal/kmx/app/testdata/live-eval-loop "$work/bundle"

cpus="$(docker info --format '{{.NCPU}}')"
[[ "$cpus" =~ ^[1-9][0-9]*$ ]] || { echo 'invalid Docker CPU count' >&2; exit 1; }
if (( cpus > 4 )); then cpus=4; fi
printf 'Starting pinned AIKit on CPU (%s CPUs)\n' "$cpus"
docker run -d --name "$container" --cpus "$cpus" \
  -e LOCALAI_FORCE_META_BACKEND_CAPABILITY=cpu \
  --mount "type=bind,src=$repo_root/scripts/ci/eval-loop-model.yaml,dst=/config.yaml,readonly" \
  -p "127.0.0.1:$model_port:8080" "$image" --config-file=/config.yaml >/dev/null
ready=false
deadline=$((SECONDS + 180))
while (( SECONDS < deadline )); do
  [[ "$(docker inspect -f '{{.State.Running}}' "$container")" == true ]] || exit 1
  if curl -fsS --connect-timeout 2 --max-time 5 "http://127.0.0.1:$model_port/readyz" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 2
done
[[ "$ready" == true ]] || { echo 'AIKit readiness deadline exceeded' >&2; exit 1; }
# Readiness alone does not prove inference. Do one bounded completion warmup,
# keeping its public prompt in a file so successful Actions logs stay quiet.
curl -fsS --connect-timeout 5 --max-time 180 \
  -H 'Content-Type: application/json' --data-binary @scripts/ci/eval-loop-warmup.json \
  "http://127.0.0.1:$model_port/v1/chat/completions" >"$work/warmup.json"
jq -e '.model == "qwen-3.5-2b" and (.created | type == "number") and
  (.choices[0].message.content | type == "string" and length > 0)' "$work/warmup.json" >/dev/null

# No inherited hosted-provider credentials belong in this secret-free fixture.
MODEL_API_KEY='' OPENAI_API_KEY='' "$work/agentsessionsd" \
  -addr "$sessions" -journal "$work/evaluation.db" \
  -model qwen-3.5-2b -model-base-url "http://127.0.0.1:$model_port/v1" \
  >"$work/daemon.log" 2>&1 &
daemon_pid=$!
ready=false
deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do
  kill -0 "$daemon_pid" || exit 1
  if python3 - "$sessions_port" <<'PY' 2>/dev/null
import socket, sys
with socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=1):
    pass
PY
  then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'sessions readiness deadline exceeded' >&2; exit 1; }

python3 scripts/ci/eval-loop.py evaluate --kmx "$kmx" --bundle "$work/bundle" \
  --sessions "$sessions" --artifacts "$artifacts"
# Keep the daemon and journal alive, but remove live inference before replay.
docker stop "$container" >/dev/null
python3 scripts/ci/eval-loop.py verify --kmx "$kmx" --bundle "$work/bundle" \
  --sessions "$sessions" --artifacts "$artifacts"
printf 'Live eval loop passed in %ss\n' "$((SECONDS - start))"
