#!/usr/bin/env python3
"""Opt-in installed-Copilot check in a new private profile; no chat or sign-in."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import threading

from package import NATIVE_FILES


def read_file(path: Path, maximum: int = 512 << 20) -> bytes:
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= maximum:
        raise ValueError("package files must be bounded regular files")
    return path.read_bytes()


def validate_package(plugin: Path, digest: str) -> Path:
    config = json.loads(read_file(plugin / "mcp.json", 65536))
    selected = json.loads(read_file(plugin / "selected-root.json", 65536))["root"]
    root = Path(selected)
    if (not root.is_absolute() or not root.is_dir() or root == Path(root.anchor)
            or str(root.resolve()) != selected or any(ord(c) < 32 for c in selected)
            or any(token in selected for token in ("${PLUGIN_ROOT}", "${PLUGIN_DATA}"))):
        raise ValueError("configure a safe absolute existing root before installing")
    if config["mcpServers"] != {"kmx-agent-builder": {
            "type": "stdio", "command": "./bin/kmx", "args": ["mcp", "--root", selected]}}:
        raise ValueError("selected root and MCP root/configuration differ")
    if hashlib.sha256(read_file(plugin / "bin/kmx")).hexdigest() != digest:
        raise ValueError("engine digest mismatch")
    return root


def discovery(binary: Path, root: Path, env: dict) -> None:
    process = subprocess.Popen([str(binary), "mcp", "--root", str(root)], stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                               encoding="utf-8", env=env)
    timer = threading.Timer(20, process.kill)
    timer.start()
    try:
        def request(number, method, params):
            process.stdin.write(json.dumps({"jsonrpc": "2.0", "id": number,
                                           "method": method, "params": params}) + "\n")
            process.stdin.flush()
            reply = json.loads(process.stdout.readline())
            if reply.get("id") != number or "error" in reply:
                raise ValueError("MCP discovery failed")
            return reply["result"]
        result = request(1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                         "clientInfo": {"name": "plugin-install-check", "version": "1"}})
        if result["serverInfo"]["name"] != "kmx":
            raise ValueError("unexpected MCP engine")
        process.stdin.write('{"jsonrpc":"2.0","method":"notifications/initialized"}\n')
        process.stdin.flush()
        tools = request(2, "tools/list", {})["tools"]
        if len(tools) != 4 or {t["name"] for t in tools} != {"create", "edit", "run", "test"}:
            raise ValueError("unexpected MCP tool inventory")
        process.stdin.close()
        process.wait(timeout=5)
        if process.returncode or process.stdout.read() or process.stderr.read():
            raise ValueError("MCP stdout/shutdown contract failed")
    finally:
        timer.cancel()
        if process.poll() is None:
            process.kill()
        process.wait(timeout=5)
        for stream in (process.stdin, process.stdout, process.stderr):
            stream.close()


def check(copilot: str, plugin: Path, digest: str, home: Path) -> None:
    root = validate_package(plugin, digest)
    home.mkdir(mode=0o700)  # Refuse existing profiles, including the operator's.
    home = home.resolve()
    env = {**os.environ, "COPILOT_HOME": str(home.resolve()), "COPILOT_AUTO_UPDATE": "false",
           "KMX_HOME": str(home / "kmx-state")}
    def cli(*args):
        return subprocess.check_output([copilot, "--no-auto-update", *args], env=env,
                                       cwd=root, text=True, encoding="utf-8", timeout=60)
    installed = False
    try:
        if cli("--version").splitlines()[0] != "GitHub Copilot CLI 1.0.86-2.":
            raise ValueError("use qualified Copilot CLI 1.0.86-2; requalify other hosts first")
        if json.loads(cli("plugin", "list", "--json")):
            raise ValueError("check profile is not empty; refusing installation")
        for _ in range(2):  # A real uninstall/reinstall, not a simulated cache copy.
            cli("plugin", "install", str(plugin.resolve()))
            installed = True
            plugins = json.loads(cli("plugin", "list", "--json"))
            if not any(p["name"] == "kmx-agent-builder" and p["enabled"] for p in plugins):
                raise ValueError("plugin discovery failed")
            skills = json.loads(cli("skill", "list", "--json"))
            skill = next(s for s in skills if s["name"] == "kmx-agent-builder" and s["source"] == "plugin")
            cache = Path(skill["path"]).parent.parent.resolve()
            if home.resolve() not in cache.parents or validate_package(cache, digest) != root:
                raise ValueError("unexpected installed cache/root")
            names = ["plugin.json", "mcp.json", "selected-root.json", "engine.json", "bin/kmx",
                     "skills/kmx-agent-builder/SKILL.md", *NATIVE_FILES]
            if (plugin / "SETUP.md").exists():
                names.append("SETUP.md")
            for name in names:
                if read_file(cache / name) != read_file(plugin / name):
                    raise ValueError("installed cache differs from configured package")
            if not stat.S_IMODE((cache / "bin/kmx").stat().st_mode) & 0o111:
                raise ValueError("installed engine is not executable")
            discovery(cache / "bin/kmx", root, env)
            cli("plugin", "uninstall", "kmx-agent-builder")
            installed = False
        print("PASS: installed plugin/skill, four real tools, pinned cache bytes, uninstall/reinstall")
    finally:
        if installed:
            cli("plugin", "uninstall", "kmx-agent-builder")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--copilot", default="copilot")
    parser.add_argument("--plugin", required=True, type=Path)
    parser.add_argument("--engine-sha256", required=True)
    parser.add_argument("--home", required=True, type=Path, help="new private check profile; must not exist")
    args = parser.parse_args()
    check(args.copilot, args.plugin, args.engine_sha256, args.home)
