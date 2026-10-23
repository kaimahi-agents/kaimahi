#!/bin/sh
# Install a complete Copilot try-it bundle, never the source-only plugin.
# Trust: HTTPS + the selected GitHub release's checksum, NOT independent
# signature verification. No cosign, sudo, model install or OS-policy changes.
set -eu
umask 077
repo=kaimahi-agents/kaimahi
version=latest
dry_run=no
no_open=no
host=native
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || exit 2; version=$2; shift 2 ;;
    --dry-run) dry_run=yes; shift ;;
    --no-open) no_open=yes; shift ;;
    --windows) host=windows; shift ;;
    -h|--help)
      printf '%s\n' 'Usage: sh try-it.sh [--version vX.Y.Z[-rc.N]] [--windows] [--dry-run] [--no-open]' \
        'Mac: installed Copilot app + copilot CLI. WSL: add --windows; requires Windows copilot.exe and github.exe on PATH.' \
        'Dry run downloads, checks and configures a fresh bundle/sample; no sign-in, install, model calls or app launch.' \
        'The script asks you to confirm the folder. It never overwrites a sample.'
      exit 0 ;;
    *) printf 'Unknown option: %s\n' "$1" >&2; exit 2 ;;
  esac
done
say() { printf '%s\n' "$*"; }
die() { say "Setup stopped: $*" >&2; exit 1; }
ask() { printf '%s' "$1"; IFS= read -r reply || die 'No answer received. Run the saved script in a terminal.'; }
valid_path() {
  # Windows argv and JSON are kept literal. Refuse uncommon ambiguous names,
  # rather than guessing shell/host escaping rules or widening the binding.
  case "$1" in *\"*|*\\*) return 1;; esac
  # Host placeholders must remain literal.
  # shellcheck disable=SC2016
  case "$1" in *'${PLUGIN_ROOT}'*|*'${PLUGIN_DATA}'*) return 1;; esac
  control_free=$(printf '%s.' "$1" | LC_ALL=C tr -d '[:cntrl:]')
  [ "$control_free" = "$1." ]
}
case "$version" in latest) ;; *) printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || die 'Invalid release tag.';; esac
case "$(uname -s)" in Darwin) os=darwin;; Linux) os=linux;; *) die 'Use macOS or WSL, not native Windows.';; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64;; x86_64|amd64) arch=amd64;; *) die 'Unsupported CPU.';; esac
if [ "$host" = windows ]; then
  if [ "$os" != linux ] || [ -z "${WSL_DISTRO_NAME:-}" ]; then die '--windows must run inside the intended WSL distro.'; fi
  distro=$WSL_DISTRO_NAME
  valid_path "$distro" || die 'Unsupported WSL distro name.'
  for tool in wsl.exe wslpath cmd.exe copilot.exe github.exe; do command -v "$tool" >/dev/null || die "Put $tool on your WSL PATH first."; done
  copilot=copilot.exe
  launcher=github.exe
else
  copilot=copilot
  launcher=github
fi
for tool in curl tar awk; do command -v "$tool" >/dev/null || die "Missing $tool."; done
if [ "$os" = darwin ]; then
  digest() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  command -v python3 >/dev/null || die 'WSL/Linux needs Python 3 for JSON (Ubuntu includes it).'
  digest() { sha256sum "$1" | awk '{print $1}'; }
fi
# JSON parsing uses only tools shipped with macOS / Ubuntu. No downloaded parser.
json_get() {
  if [ "$os" = darwin ]; then
    /usr/bin/osascript -l JavaScript - "$1" "$2" <<'JXA'
function run(args) {
  ObjC.import('Foundation');
  const text = $.NSString.stringWithContentsOfFileEncodingError(args[0], $.NSUTF8StringEncoding, null);
  const value = JSON.parse(ObjC.unwrap(text));
  if (args[1] === 'models') return value.map(x => x.model).join('\n');
  if (!(args[1] in value)) throw new Error('Missing field');
  return String(value[args[1]]);
}
JXA
  else
    python3 - "$1" "$2" <<'PY'
import json, sys
with open(sys.argv[1], encoding='utf-8') as source:
    value = json.load(source)
print('\n'.join(x['model'] for x in value) if sys.argv[2] == 'models' else value[sys.argv[2]])
PY
  fi
}
state=$HOME/.local/share/kmx-try-it
valid_path "$state" || die 'Home path contains unsupported characters.'
[ ! -L "$state" ] || die 'Setup storage is a symlink; choose a normal home directory.'
mkdir -p "$state"
run_dir=$(mktemp -d "$state/run.XXXXXX")
log=$run_dir/setup.log
phase=start
finish() {
  result=$?
  printf 'phase=%s exit=%s\n' "$phase" "$result" >> "$log"
  if [ "$result" -ne 0 ]; then printf 'Send this setup log to Tom: %s\n' "$log" >&2; fi
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
say "Setup log: $log"
printf 'os=%s arch=%s host=%s\n' "$os" "$arch" "$host" >> "$log"
phase=release
if [ "$version" = latest ]; then
  effective=$(curl -fsSLI --proto '=https' --proto-redir '=https' --max-time 30 -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest" 2>/dev/null) || die 'Cannot resolve latest release. Retry or use --version.'
  version=${effective##*/}
  printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' || die 'Invalid latest release response.'
fi
printf 'version=%s\n' "$version" >> "$log"
base=https://github.com/$repo/releases/download/$version
asset=kmx-agent-builder-$os-$arch.tar.gz
fetch() { curl -fsSL --proto '=https' --proto-redir '=https' --max-time 180 --max-filesize 268435456 "$base/$1" -o "$run_dir/$1" 2>/dev/null; }
say "Downloading kmx $version for $os/$arch (HTTPS + checksum verification)."
fetch plugin-checksums.txt || die "Release $version has no reachable try-it checksums. v0.8.1 has no plugin bundle; use the new try-it release tag with --version."
want=$(awk -v name="$asset" '$2 == name {count++; value=$1} END {if (count == 1 && length(value) == 64 && value !~ /[^0-9a-f]/) print value; else exit 1}' "$run_dir/plugin-checksums.txt") || die 'Missing, duplicate or invalid archive checksum.'
fetch "$asset" || die 'Cannot download this platform bundle. Check the release tag and connection.'
[ "$(digest "$run_dir/$asset")" = "$want" ] || die 'Archive checksum mismatch; nothing extracted or executed.'
printf 'archive_sha256=%s\n' "$want" >> "$log"
phase=archive
# Exact regular-file inventory prevents traversal, duplicates and link extraction.
cat > "$run_dir/expected" <<'FILES'
kmx-agent-builder/SETUP.md
kmx-agent-builder/bin/kmx
kmx-agent-builder/com.github.copilot/extensions/agent-view/app.js
kmx-agent-builder/com.github.copilot/extensions/agent-view/authoring.mjs
kmx-agent-builder/com.github.copilot/extensions/agent-view/extension.mjs
kmx-agent-builder/com.github.copilot/extensions/agent-view/index.html
kmx-agent-builder/com.github.copilot/extensions/agent-view/open.mjs
kmx-agent-builder/com.github.copilot/extensions/agent-view/style.css
kmx-agent-builder/com.github.copilot/extensions/agent-view/view.mjs
kmx-agent-builder/engine.json
kmx-agent-builder/mcp.json
kmx-agent-builder/plugin.json
kmx-agent-builder/samples/release-writer.agent.md
kmx-agent-builder/selected-root.json
kmx-agent-builder/skills/kmx-agent-builder/SKILL.md
kmx-agent-builder/skills/kmx/SKILL.md
kmx-agent-builder/try-it.json
FILES
tar -tzf "$run_dir/$asset" > "$run_dir/inventory" 2>/dev/null || die 'Invalid archive.'
LC_ALL=C sort "$run_dir/inventory" > "$run_dir/sorted"
cmp -s "$run_dir/expected" "$run_dir/sorted" || die 'This release has no compatible try-it bundle (schema 1). Use the new release tag.'
tar -tvzf "$run_dir/$asset" 2>/dev/null | awk 'substr($0,1,1) != "-" {bad=1} END {exit bad}' || die 'Archive contains links or special entries.'
tar -xzf "$run_dir/$asset" -C "$run_dir" --no-same-owner 2>/dev/null || die 'Could not extract bundle.'
plugin=$run_dir/kmx-agent-builder
engine=$plugin/bin/kmx
[ "$(json_get "$plugin/try-it.json" schemaVersion)" = 1 ] || die 'Unsupported try-it schema.'
[ "$(json_get "$plugin/engine.json" schemaVersion)" = 1 ] || die 'Unsupported engine schema.'
[ "$(json_get "$plugin/engine.json" version)" = "$version" ] || die 'Bundle version mismatch.'
[ "$(json_get "$plugin/engine.json" platform)" = "$os/$arch" ] || die 'Bundle platform mismatch.'
[ "$(digest "$engine")" = "$(json_get "$plugin/engine.json" sha256)" ] || die 'Bundled engine checksum mismatch.'
if [ "$os" = darwin ]; then
  attributes=$(/usr/bin/xattr "$engine") || die 'Cannot inspect macOS attributes.'
  if printf '%s\n' "$attributes" | grep -qx com.apple.quarantine; then die 'macOS quarantined the engine. Obtain normal OS approval; this script never clears quarantine.'; fi
fi
chmod 700 "$engine"
phase=root
ask "Folder for the sample [$HOME/kmx-try-it-sample]: "
root=${reply:-$HOME/kmx-try-it-sample}
case "$root" in /*) ;; *) die 'Enter an absolute folder path.';; esac
valid_path "$root" || die 'Folder cannot contain quotes, backslashes, control characters or plugin placeholders.'
if [ -d "$root" ]; then
  root=$(CDPATH='' cd -- "$root" && pwd -P)
else
  if [ -e "$root" ] || [ -L "$root" ]; then die 'Folder path is occupied.'; fi
  parent=$(dirname "$root")
  parent=$(CDPATH='' cd -- "$parent" && pwd -P) || die 'The parent folder must already exist.'
  root=$parent/$(basename "$root")
fi
if [ "$root" = / ] || ! valid_path "$root"; then die 'Refusing this folder as the operation root.'; fi
case "$run_dir/" in "$root/"*) die 'Plugin storage must be outside the operation root.';; esac
say "kmx will be allowed to create, edit, run and test agent files only in: $root"
[ "$host" != windows ] || say "Windows Copilot will use WSL distro: $distro"
ask 'Use exactly this folder and binding? [y/N] '
case "$reply" in y|Y|yes) ;; *) die 'Folder not confirmed; no workspace created.';; esac
sample=$root/release-writer.agent.md
if [ -e "$sample" ] || [ -L "$sample" ]; then die 'Sample already exists and was not changed. For an interrupted install, use the configured directory printed by the previous run, or choose a fresh folder.'; fi
phase=model
model=claude-haiku-5.5
engine_cli() {
  if [ "$host" = windows ]; then wsl.exe -d "$distro" -e "$engine" "$@"; else "$engine" "$@"; fi
}
if [ "$dry_run" != yes ]; then
  command -v "$copilot" >/dev/null || die 'Install the Copilot CLI and put it on PATH first.'
  say 'Run/Test sends instructions, inputs, answers and judge rubrics to Copilot. Nothing is posted.'
  if ! engine_cli model list --provider copilot -o json < /dev/null > "$run_dir/models.json" 2>/dev/null; then
    ask 'kmx needs Copilot authorization. Start kmx model login now? [y/N] '
    case "$reply" in y|Y|yes) engine_cli model login || die 'Sign-in failed.';; *) die 'Sign-in declined. No plugin installed.';; esac
    engine_cli model list --provider copilot -o json < /dev/null > "$run_dir/models.json" 2>/dev/null || die 'Copilot credential check failed in the MCP launch environment. On WSL, make gh available to wsl.exe or use a working session keychain; never put tokens in plugin files.'
  fi
  json_get "$run_dir/models.json" models > "$run_dir/model-ids" || die 'Cannot read Copilot model catalog.'
  say 'Available Copilot models:'
  cat "$run_dir/model-ids"
  ask "Exact model for both agent and judge [$model]: "
  model=${reply:-$model}
  LC_ALL=C grep -Fx -- "$model" "$run_dir/model-ids" >/dev/null || die 'That exact model is unavailable. Rerun and choose a listed model; no fallback was used.'
fi
printf '%s\n' "$model" | grep -Eq '^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,255}$' || die 'Invalid exact model ID.'
if [ "$dry_run" != yes ]; then
  phase=install-preflight
  "$copilot" plugin list --json < /dev/null > "$run_dir/plugins.json" 2>/dev/null || die 'Cannot list Copilot plugins.'
  if grep -q 'kmx-agent-builder' "$run_dir/plugins.json"; then
    ask 'kmx is already installed. Replace its binding with this confirmed folder? [y/N] '
    case "$reply" in y|Y|yes) ;; *) die 'Existing plugin left unchanged.';; esac
  fi
fi
phase=configure
mkdir -p "$root"
# Noclobber also refuses a file that appears after the initial check.
(set -C; awk -v model="$model" '{gsub(/claude-haiku-5[.]5/, model); print}' "$plugin/samples/release-writer.agent.md" > "$sample") || die 'Could not create sample exclusively.'
printf '{"root":"%s"}\n' "$root" > "$plugin/selected-root.json"
if [ "$host" = windows ]; then
  printf '{"mcpServers":{"kmx-agent-builder":{"type":"stdio","command":"wsl.exe","args":["-d","%s","-e","%s","mcp","--root","%s","--provider","copilot"]}}}\n' "$distro" "$engine" "$root" > "$plugin/mcp.json"
  # Host cache lives on Windows; the checked ELF remains at its private WSL path.
  win_home=$(cmd.exe /d /c 'echo %USERPROFILE%' < /dev/null 2>/dev/null | tr -d '\r\n') || die 'Cannot locate Windows user profile.'
  win_home=$(wslpath -u "$win_home") || die 'Cannot map Windows profile.'
  valid_path "$win_home" || die 'Unsupported Windows profile path.'
  mkdir -p "$win_home/.kmx-try-it"
  host_stage=$(mktemp -d "$win_home/.kmx-try-it/run.XXXXXX")
  say "Windows staging directory (keep while installed): $host_stage"
  cp -R "$plugin" "$host_stage/kmx-agent-builder"
  install_path=$(wslpath -w "$host_stage/kmx-agent-builder")
  open_path=$(wslpath -w "$root")
else
  printf '{"mcpServers":{"kmx-agent-builder":{"type":"stdio","command":"./bin/kmx","args":["mcp","--root","%s","--provider","copilot"]}}}\n' "$root" > "$plugin/mcp.json"
  install_path=$plugin
  open_path=$root
fi
say "Sample: $sample"
say "Verified engine (kept for this installation): $engine"
say "To retry or refresh installation, pass this configured directory as one argument to $copilot plugin install: $install_path"
if [ "$dry_run" = yes ]; then say 'Dry run complete: no sign-in, plugin install, model calls or app launch.'; phase=dry-run-complete; exit 0; fi
phase=install
# Do not copy host output (which may include private configuration) into the log.
"$copilot" plugin install "$install_path" > /dev/null 2>&1 || die 'Copilot refused installation. Inspect copilot plugin list; no trust policy was bypassed.'
printf 'plugin_install=ok\n' >> "$log"
say 'Installed. Start a NEW Copilot session so it loads the confirmed binding.'
say 'Confirm the same folder in chat, then ask: Open release-writer.agent.md in the kmx agent view.'
say 'Use chat to Test examples, edit the instruction, and Run an input. The current canvas is read-only.'
phase=open
if [ "$no_open" != yes ]; then
  if command -v "$launcher" >/dev/null; then
    "$launcher" open "$open_path" > /dev/null 2>&1 || die 'Installed, but app opening failed. Open the confirmed folder in Copilot manually.'
  else
    say "App launcher not on PATH. Open $root in the Copilot app manually."
  fi
fi
phase=complete
say "Done. If something goes wrong, send Tom: $log"
