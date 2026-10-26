#!/usr/bin/env python3
"""Offline, root-bound development package builder; not release verification."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
TARGETS = {"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"}
MAX_BINARY = 512 << 20
NATIVE_DIRECTORY = "com.github.copilot/extensions/agent-view"
NATIVE_FILES = tuple(f"{NATIVE_DIRECTORY}/{name}" for name in
                     ("extension.mjs", "view.mjs", "open.mjs", "index.html", "app.js", "style.css", "authoring.mjs", "editor.mjs",
                      "draft-controller.mjs", "action-controller.mjs", "observation-controller.mjs", "host-relay.mjs", "tool-catalog.mjs", "tools-view.mjs", "test-tools.mjs",
                      "dom.mjs", "results.mjs", "check-controls.mjs", "instruction-text.mjs",
                      "run-views.mjs", "view-resource.mjs"))
SKILL_FILES = ("skills/kmx-agent-builder/SKILL.md", "skills/kmx/SKILL.md")
SAMPLE_FILES = ("samples/release-writer.agent.md",)


def asset_source(name: str) -> Path:
    """Shared skills/runtime live in the installable WSL tree; samples stay local."""
    root = HERE.with_name("copilot-wsl") if name in (*SKILL_FILES, *NATIVE_FILES) else HERE
    return root / name


def _quarantine(path: Path) -> None:
    if sys.platform != "darwin":
        return
    try:
        # Python's os.getxattr is Linux-only. The macOS system tool lists names
        # without reading attribute values or modifying OS execution policy.
        inspected = subprocess.run(["/usr/bin/xattr", str(path.resolve())],
                                   capture_output=True, timeout=5, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ValueError("cannot inspect macOS quarantine; no package created") from exc
    if inspected.returncode != 0:
        raise ValueError("macOS attribute inspection failed; no package created")
    if b"com.apple.quarantine" in inspected.stdout.splitlines():
        raise ValueError("source has macOS quarantine; obtain OS approval, never remove it automatically")


def _write_json(path: Path, value: dict) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    path.chmod(0o600)


def squad_arguments(program: Path | None, release: str | None) -> list[str]:
    """A local startup declaration, not a dependency install or authentication."""
    if program is None and release is None:
        return []
    if program is None or release != "1.1.0" or not program.is_absolute():
        raise ValueError("native Squad requires an absolute qualified program and release 1.1.0")
    program = program.resolve(strict=True)
    if (not program.is_file() or program.name not in ("squad", "squad.exe")
            or any(ord(c) < 32 for c in str(program))
            or any(token in str(program) for token in ("${PLUGIN_ROOT}", "${PLUGIN_DATA}"))):
        raise ValueError("native Squad requires a separately qualified standalone launcher")
    return ["--squad-program", str(program), "--squad-release", release]


def build(*, binary: Path, sha256: str, target: str, root: Path | None,
          output: Path, version: str, squad_program: Path | None = None,
          squad_release: str | None = None) -> Path:
    """Copy exactly the caller-pinned bytes without running the supplied engine.

    The SHA256 is caller input, not independent publisher authentication. The
    caller chooses the root before install; this is developer tooling only.
    """
    native = squad_arguments(squad_program, squad_release)
    if native and root is None:
        raise ValueError("native Squad requires a configured operation root")
    if not re.fullmatch(r"[0-9a-f]{64}", sha256):
        raise ValueError("SHA256 must be 64 lowercase hex digits")
    if target not in TARGETS:
        raise ValueError("unsupported platform")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?", version):
        raise ValueError("invalid plugin version")
    try:
        root = root.resolve(strict=True) if root is not None else None
        parent = output.parent.resolve(strict=True)
    except OSError as exc:
        raise ValueError("root must be an existing directory; output parent must exist") from exc
    if root is not None:
        if not root.is_dir():
            raise ValueError("root must be an existing directory")
        if root == Path(root.anchor):
            raise ValueError("refusing filesystem root as operation root")
        if any(ord(c) < 32 for c in str(root)):
            raise ValueError("root cannot contain control characters")
        # Hosts expand these exact tokens in args; a literal root must stay literal.
        if any(token in str(root) for token in ("${PLUGIN_ROOT}", "${PLUGIN_DATA}")):
            raise ValueError("root cannot contain plugin placeholders")
    output = parent / output.name
    if os.path.lexists(output):
        raise ValueError("output already exists; refusing replacement")
    if output == root or root in output.parents:
        raise ValueError("output must be outside the selected operation root")
    try:
        info = binary.lstat()
    except OSError as exc:
        raise ValueError("engine must be a readable regular non-symlink file") from exc
    if not stat.S_ISREG(info.st_mode) or binary.is_symlink():
        raise ValueError("engine must be a regular non-symlink file")
    if not 0 < info.st_size <= MAX_BINARY:
        raise ValueError("engine must be nonempty and at most 512 MiB")
    _quarantine(binary)
    manifest = json.loads((HERE / "plugin.json").read_text(encoding="utf-8"))
    config = json.loads((HERE / "native-mcp.json").read_text(encoding="utf-8"))
    server = config["mcpServers"]["kmx-agent-builder"]
    if server != {"type": "stdio", "command": "./bin/kmx",
                  "args": ["mcp", "--root", "__SELECTED_ROOT__"]}:
        raise ValueError("unexpected MCP template; review the startup contract")
    # Native bundles use canonical WSL assets, not the PATH source provider.
    manifest["name"] = "kmx-agent-builder"
    manifest["version"] = version
    name = "kmx.exe" if target.startswith("windows/") else "kmx"
    server["command"] = "./bin/" + name
    selected_root = str(root) if root is not None else "__SELECTED_ROOT__"
    server["args"][-1] = selected_root
    server["args"].extend(native)
    # Hash and stage the same bytes, avoiding a second read of mutable input.
    with tempfile.TemporaryFile() as staged:
        digest = hashlib.sha256()
        size = 0
        flags = os.O_RDONLY | getattr(os, "O_BINARY", 0)
        flags |= getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0)
        with os.fdopen(os.open(binary, flags), "rb") as source:
            opened = os.fstat(source.fileno())
            if not stat.S_ISREG(opened.st_mode) or not os.path.samestat(info, opened):
                raise ValueError("engine changed or is not a regular file; no package created")
            while chunk := source.read(1 << 20):
                size += len(chunk)
                if size > MAX_BINARY:
                    raise ValueError("engine exceeds 512 MiB")
                digest.update(chunk)
                staged.write(chunk)
        if size == 0 or digest.hexdigest() != sha256:
            raise ValueError("engine SHA256 mismatch; no package created")
        staged.seek(0)
        try:
            output.mkdir(mode=0o700)
        except FileExistsError as exc:
            raise ValueError("output already exists; refusing replacement") from exc
        try:
            (output / "bin").mkdir(mode=0o700)
            destination = output / "bin" / name
            with destination.open("xb") as sink:
                shutil.copyfileobj(staged, sink)
            destination.chmod(0o700)
            _write_json(output / "plugin.json", manifest)
            _write_json(output / "mcp.json", config)
            _write_json(output / "selected-root.json", {"root": selected_root})
            _write_json(output / "engine.json", {
                "schemaVersion": 1, "sha256": sha256, "platform": target,
                "purpose": "development-fixture; not a signed release",
            })
            for name in (*SKILL_FILES, *NATIVE_FILES, *SAMPLE_FILES):
                source = asset_source(name)
                maximum = 256 << 10 if name in SAMPLE_FILES else 65536
                if source.is_symlink() or not source.is_file() or not 0 < source.stat().st_size <= maximum:
                    raise ValueError("bundled assets must be bounded regular files")
                destination = output / name
                destination.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
                shutil.copyfile(source, destination)
                destination.chmod(0o600)
        except Exception:
            shutil.rmtree(output)
            raise
    return output


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--sha256", required=True, help="caller-approved engine digest, not a signature")
    parser.add_argument("--platform", required=True, choices=sorted(TARGETS))
    parser.add_argument("--root", required=True, type=Path,
                        help="explicit operation directory bound before fixture installation")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--squad-program", type=Path, help="optional separately qualified standalone Squad launcher; never bundled or installed")
    parser.add_argument("--squad-release", help="qualified native release 1.1.0; requires --squad-program")
    options = parser.parse_args()
    try:
        result = build(binary=options.binary, sha256=options.sha256, target=options.platform,
                       root=options.root, output=options.output, version=options.version,
                       squad_program=options.squad_program, squad_release=options.squad_release)
    except (ValueError, OSError) as exc:
        print("plugin package refused: " + str(exc), file=sys.stderr)
        return 1
    print("Development plugin created: " + str(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
