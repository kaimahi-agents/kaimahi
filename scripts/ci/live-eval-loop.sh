#!/usr/bin/env bash
# Linux runner: caller bundle stays in its checkout; only sanitized gate output is public.
set +x +v
set -Eeuo pipefail
# Embedded validators must retain their assertions under inherited CI settings.
unset PYTHONOPTIMIZE
umask 077
start=$SECONDS
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work=''
artifacts=''
daemon_pid=''
child_pid=''
container=''
install_mode=''
phase='configuration'

output() {
  [[ -n "${GITHUB_OUTPUT:-}" ]] || return 0
  [[ "$2" != *$'\n'* && "$2" != *$'\r'* ]] || return 1
  printf '%s=%s\n' "$1" "$2" >>"$GITHUB_OUTPUT"
}

stop_group() {
  local pid="$1" deadline=$((SECONDS + 5))
  [[ -n "$pid" ]] || return 0
  kill -TERM -- "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
  while kill -0 -- "-$pid" 2>/dev/null && (( SECONDS < deadline )); do sleep 0.1; done
  kill -KILL -- "-$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  ! kill -0 -- "-$pid" 2>/dev/null
}

cleanup() {
  local status=$? cleanup_failed=false
  trap - EXIT INT TERM
  set +e
  stop_group "$child_pid" || cleanup_failed=true
  stop_group "$daemon_pid" || cleanup_failed=true
  if [[ -n "$container" ]]; then
    docker rm -f "$container" >/dev/null 2>&1 || cleanup_failed=true
  fi
  # Generated evidence survives failure, unlike private command logs and journal.
  if [[ -n "$artifacts" ]]; then
    python3 - "$artifacts" "${GITHUB_OUTPUT:-}" <<'PY' >/dev/null 2>&1
from pathlib import Path
import sys
root, dest = Path(sys.argv[1]), sys.argv[2]
if dest:
    with open(dest, "a") as out:
        for name, pattern in (("receipt", "eval-*.json"), ("verify-report", "verify-*.json")):
            paths = [p for p in root.glob(pattern) if p.is_file() and not p.is_symlink()]
            if len(paths) == 1 and not any(c in str(paths[0]) for c in "\r\n"):
                out.write(f"{name}={paths[0]}\n")
PY
  fi
  if [[ -n "$work" ]]; then
    rm -rf -- "$work" >/dev/null 2>&1 || cleanup_failed=true
  fi
  if [[ "$cleanup_failed" == true ]]; then
    printf '%s\n' 'error: live evaluation runtime cleanup failed' >&2
    if (( status == 0 )); then status=1; phase='cleanup'; fi
  fi
  output elapsed-seconds "$((SECONDS - start))"
  [[ -z "$install_mode" ]] || output daemon-install "$install_mode"
  if (( status != 0 )); then
    printf 'error: live evaluation runner failed during %s (%ss)\n' "$phase" "$((SECONDS - start))" >&2
  else
    printf 'Live eval loop passed in %ss\n' "$((SECONDS - start))"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

[[ "$(uname -s)" == Linux ]] || exit 1
for cmd in python3 setsid; do command -v "$cmd" >/dev/null 2>&1 || exit 1; done
# mkdir is exclusive, never reuse or remove the requested artifact directory.
artifacts="$(python3 - <<'PY' 2>/dev/null
import os
from pathlib import Path
path = Path(os.environ["ARTIFACT_DIR"]).resolve()
assert os.environ["ARTIFACT_DIR"] and not any(c in str(path) for c in "\r\n")
path.mkdir(mode=0o700)
print(path)
PY
)"
output artifact-dir "$artifacts"
work="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/kmx-eval-loop.XXXXXX" 2>/dev/null)"
export KMX_HOME="$work/.kmx" NO_COLOR=1
mkdir -m 700 "$KMX_HOME" >/dev/null 2>&1

export MODEL_MODE="${MODEL_MODE:-aikit}"
export MODEL_NAME="${MODEL_NAME:-}"
export MODEL_BASE_URL="${MODEL_BASE_URL:-}"
key_env="${MODEL_KEY_ENV:-}"
model_key=''
if [[ -n "$key_env" ]]; then
  [[ "$key_env" =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]] || exit 1
  # Do not allow a nominated credential to overwrite runner control variables.
  case "$key_env" in
    MODEL_API_KEY|OPENAI_API_KEY) ;;
    PATH|HOME|IFS|ENV|BASH*|SHELL*|KMX_BIN|KMX_HOME|MODEL_MODE|MODEL_NAME|MODEL_BASE_URL|MODEL_KEY_ENV|AIKIT_*|SESSIONS_*|ARTIFACT_DIR|BUNDLE_DIR|GITHUB_*|RUNNER_*|NO_COLOR|VERIFY|*_TIMEOUT) exit 1 ;;
  esac
  model_key="${!key_env:-}"
  [[ -n "$model_key" ]] || exit 1
  unset "$key_env"
fi
export MODEL_API_KEY='' OPENAI_API_KEY=''
export MODEL_KEY_ENV="$key_env"
verify="${VERIFY:-true}"
[[ "$verify" == true || "$verify" == false ]] || exit 1
sessions_port="${SESSIONS_PORT:-18088}"
model_port="${AIKIT_HOST_PORT:-18089}"
sessions="127.0.0.1:$sessions_port"
bundle="${BUNDLE_DIR:-$repo_root/internal/kmx/app/testdata/live-eval-loop}"
bundle="$(cd "$bundle" 2>/dev/null && pwd)"
kmx="${KMX_BIN:-}"
if [[ -n "$kmx" ]]; then
  [[ "$kmx" == /* ]] || kmx="$PWD/$kmx"
  [[ -x "$kmx" && -f "$kmx" ]] || exit 1
fi
archive_url="${SESSIONS_ARCHIVE_URL:-}"
archive_sha="${SESSIONS_ARCHIVE_SHA256:-}"
image="${AIKIT_IMAGE:-ghcr.io/kaito-project/aikit/qwen3.5:2b@sha256:d838c5eebf533b5b73f67cc7f4984921f87d64d28e4a52d482740b667f46b8b4}"
config="${AIKIT_CONFIG:-$repo_root/scripts/ci/eval-loop-model.yaml}"
case "$MODEL_MODE" in
  endpoint) [[ -n "$MODEL_NAME" && -n "$MODEL_BASE_URL" ]] || exit 1 ;;
  aikit)
    [[ -z "$key_env" ]] || exit 1
    [[ "$image" =~ ^[^[:space:]]+@sha256:[a-f0-9]{64}$ ]] || exit 1
    [[ -f "$config" && ! -L "$config" && ( "$config" == *.yaml || "$config" == *.yml ) ]] || exit 1
    config="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$config" 2>/dev/null)"
    export MODEL_NAME="${MODEL_NAME:-qwen-3.5-2b}"
    export MODEL_BASE_URL="http://127.0.0.1:$model_port/v1"
    ;;
  *) exit 1 ;;
esac
# Parse URLs rather than doing DNS lookups: only literal loopback can use keyless HTTP.
export SESSIONS_ARCHIVE_URL="$archive_url"
python3 - "$sessions_port" "$model_port" "$archive_sha" "$key_env" 3<<<"$model_key" <<'PY' >/dev/null 2>&1
import ipaddress, os, re, sys
from urllib.parse import unquote, urlsplit
with os.fdopen(3) as key_input:
    selected_key = key_input.read().removesuffix("\n")
for value in sys.argv[1:3]:
    assert value.isdecimal() and 0 < int(value) < 65536

def url(value, release=False):
    assert value and not any(c.isspace() or ord(c) < 32 for c in value)
    assert not selected_key or selected_key not in unquote(value)
    p = urlsplit(value)
    assert p.hostname and p.username is None and p.password is None
    assert not p.query and not p.fragment and "@" not in unquote(p.netloc)
    assert p.port is None or 0 < p.port < 65536
    if p.scheme != "https":
        assert not release and p.scheme == "http" and not sys.argv[4]
        assert ipaddress.ip_address(p.hostname).is_loopback
url(os.environ["MODEL_BASE_URL"])
archive, digest = os.environ["SESSIONS_ARCHIVE_URL"], sys.argv[3]
assert bool(archive) == bool(digest)
if archive:
    url(archive, release=True)
    assert re.fullmatch(r"[a-fA-F0-9]{64}", digest)
PY

# Every potentially noisy tool runs in an owned process group with private output.
quiet() {
  setsid "$@" <&0 >"$work/command.log" 2>"$work/command.stderr" &
  child_pid=$!
  local status=0
  wait "$child_pid" || status=$?
  if (( status == 0 )); then child_pid=''; fi
  return "$status"
}

gate() {
  (
    # The reporter alone also needs the original nominated variable for redaction.
    if [[ -n "$key_env" ]]; then
      export "${key_env?}=$model_key"
    fi
    exec setsid python3 "$repo_root/scripts/ci/eval-loop.py" "$@" \
      --kmx "$kmx" --bundle "$bundle" --sessions "$sessions" --artifacts "$artifacts"
  ) &
  child_pid=$!
  wait "$child_pid"
  child_pid=''
}

cd "$repo_root"
build_kmx=false
if [[ -z "$kmx" ]]; then
  build_kmx=true
  kmx="$work/kmx"
fi
phase='prepare'
gate prepare
if [[ "$build_kmx" == true || -z "$archive_url" ]]; then command -v go >/dev/null 2>&1 || exit 1; fi
phase='build'
if [[ "$build_kmx" == true ]]; then
  GOWORK=off quiet go build -o "$kmx" ./cmd/kmx
fi
if [[ -n "$archive_url" ]]; then
  phase='daemon archive'
  command -v curl >/dev/null 2>&1 || exit 1
  # Restrict both initial and redirect protocols; URL carries no credentials.
  quiet curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 180 \
    -o "$work/daemon.tar.gz" "$archive_url"
  # Verify the entire archive before opening it. Never extract archive paths/links.
  quiet python3 - "$work/daemon.tar.gz" "$archive_sha" "$work/agentsessionsd" <<'PY'
import hashlib
from pathlib import Path, PurePosixPath
import sys, tarfile
archive, digest, target = sys.argv[1:]
assert hashlib.sha256(Path(archive).read_bytes()).hexdigest() == digest.lower()
with tarfile.open(archive, "r:gz") as tar:
    members = tar.getmembers()
    for member in members:
        path = PurePosixPath(member.name)
        assert not path.is_absolute() and ".." not in path.parts
        assert member.isdir() or member.isreg()
    candidates = [m for m in members if PurePosixPath(m.name).name == "agentsessionsd" and m.isreg()]
    assert len(candidates) == 1
    member = candidates[0]
    assert 0 < member.size <= 256 * 1024 * 1024
    with tar.extractfile(member) as src, open(target, "xb") as dest:
        import shutil
        shutil.copyfileobj(src, dest)
Path(target).chmod(0o700)
PY
  install_mode='release-sha256'
else
  # The published v0.1.2 lacks chat/system_prompt; use KMX's replay revision.
  GOWORK=off quiet go list -m -f '{{.Dir}}' github.com/aramase/agentsessions
  IFS= read -r module_dir <"$work/command.log"
  [[ -d "$module_dir" ]] || exit 1
  cd "$module_dir"
  GOWORK=off quiet go build -o "$work/agentsessionsd" ./cmd/agentsessionsd
  cd "$repo_root"
  install_mode='source-fallback'
fi
output daemon-install "$install_mode"
printf 'agentsessions install: %s\n' "$install_mode"

if [[ "$MODEL_MODE" == aikit ]]; then
  phase='AIKit startup'
  for cmd in docker curl; do command -v "$cmd" >/dev/null 2>&1 || exit 1; done
  quiet docker info --format '{{.NCPU}}'
  IFS= read -r cpus <"$work/command.log"
  [[ "$cpus" =~ ^[1-9][0-9]*$ ]] || exit 1
  if (( cpus > 4 )); then cpus=4; fi
  container="${work##*/}-aikit"
  quiet docker run -d --name "$container" --cpus "$cpus" \
    -e LOCALAI_FORCE_META_BACKEND_CAPABILITY=cpu \
    --mount "type=bind,src=$config,dst=/config.yaml,readonly" \
    -p "127.0.0.1:$model_port:8080" "$image" --config-file=/config.yaml
  ready=false
  deadline=$((SECONDS + 180))
  while (( SECONDS < deadline )); do
    quiet docker inspect -f '{{.State.Running}}' "$container"
    IFS= read -r running <"$work/command.log"
    [[ "$running" == true ]] || exit 1
    if quiet curl -fsS --connect-timeout 2 --max-time 5 "http://127.0.0.1:$model_port/readyz"; then
      ready=true
      break
    fi
    sleep 2
  done
  [[ "$ready" == true ]] || exit 1
  quiet python3 - "$repo_root/scripts/ci/eval-loop-warmup.json" "$work/warmup-request.json" <<'PY'
import json, os, sys
with open(sys.argv[1]) as src:
    request = json.load(src)
request["model"] = os.environ["MODEL_NAME"]
with open(sys.argv[2], "w") as dest:
    json.dump(request, dest)
PY
  quiet curl -fsS --connect-timeout 5 --max-time 180 \
    -H 'Content-Type: application/json' --data-binary "@$work/warmup-request.json" \
    -o "$work/warmup.json" "$MODEL_BASE_URL/chat/completions"
  quiet python3 - "$work/warmup.json" <<'PY'
import json, os, sys
with open(sys.argv[1]) as src:
    result = json.load(src)
assert result["model"] == os.environ["MODEL_NAME"]
assert type(result["created"]) in (int, float)
assert isinstance(result["choices"][0]["message"]["content"], str) and result["choices"][0]["message"]["content"]
PY
fi

phase='sessions startup'
MODEL_API_KEY="$model_key" OPENAI_API_KEY='' setsid "$work/agentsessionsd" \
  -addr "$sessions" -journal "$work/evaluation.db" \
  -model "$MODEL_NAME" -model-base-url "$MODEL_BASE_URL" \
  >"$work/daemon.log" 2>&1 &
daemon_pid=$!
ready=false
deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do
  kill -0 "$daemon_pid" 2>/dev/null || exit 1
  if quiet python3 - "$sessions_port" <<'PY'
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
[[ "$ready" == true ]] || exit 1
kill -0 "$daemon_pid" 2>/dev/null || exit 1
phase='evaluation'
gate evaluate --case-timeout "${CASE_TIMEOUT:-2m}" --command-timeout "${COMMAND_TIMEOUT:-600}"
if [[ "$verify" == true ]]; then
  phase='verification'
  if [[ "$MODEL_MODE" == aikit ]]; then quiet docker stop "$container"; fi
  gate verify --verify-timeout "${VERIFY_TIMEOUT:-5m}" --command-timeout "${COMMAND_TIMEOUT:-600}"
fi
