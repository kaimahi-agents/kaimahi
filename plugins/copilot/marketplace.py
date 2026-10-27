#!/usr/bin/env python3
"""Check kmx's catalog against frozen source manifests; --release also checks publication.

Run from the repository root with full Git history/tags. Never writes, fetches,
installs a plugin or updates the user's binary. Catalog versions may lag main.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

from source_release import release_version

CATALOG = ".github/plugin/marketplace.json"
REPOSITORY = "kaimahi-agents/kaimahi"
VARIANTS = {"kmx-agent-builder": "plugins/copilot-wsl", "kmx-agent-builder-path": "plugins/copilot"}
BINARIES = {"kmx-" + platform for platform in ("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64")}


def git(root: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=root, check=True, capture_output=True,
                          text=True, encoding="utf-8", timeout=15).stdout.strip()


def release_metadata(info: dict, tag: str) -> None:
    if info.get("tag_name") != tag or info.get("draft") is not False:
        raise ValueError("catalog needs a published release with the exact tag")
    assets = info.get("assets")
    if not isinstance(assets, list) or not (BINARIES | {"checksums.txt"}) <= {
            asset.get("name") for asset in assets if isinstance(asset, dict)}:
        raise ValueError("published release assets must include four binaries and checksums.txt")


def check(root: Path, released: bool = False) -> list[dict]:
    catalog = json.loads((root / CATALOG).read_text(encoding="utf-8"))
    if catalog.get("name") != "kmx-plugins" or catalog.get("owner", {}).get("name") != "kaimahi-agents":
        raise ValueError("unexpected marketplace identity or owner")
    entries = catalog.get("plugins", [])
    if len(entries) != 2 or {entry["name"] for entry in entries} != set(VARIANTS):
        raise ValueError("catalog needs exactly the two variant identities")
    for entry in entries:
        source = entry["source"]
        if not isinstance(source, dict) or source.get("source") != "github":
            raise ValueError("catalog requires a GitHub source, not a moving relative path")
        if source.get("repo") != REPOSITORY:
            raise ValueError("unexpected source repository")
        if source.get("path") != VARIANTS[entry["name"]]:
            raise ValueError("unexpected source path for variant")
        if not re.fullmatch(r"[0-9a-f]{40}", source.get("sha", "")):
            raise ValueError("source must pin a full commit SHA")
        release_version("v" + entry["version"])
    pins = {entry["source"]["sha"] for entry in entries}
    if len(pins) != 1:
        raise ValueError("both variants must pin the same release commit")
    sha = pins.pop()
    try:
        if git(root, "cat-file", "-t", sha) != "commit":
            raise ValueError("catalog SHA must name a commit object")
        for entry in entries:
            path = entry["source"]["path"] + "/plugin.json"
            manifest = json.loads(git(root, "show", f"{sha}:{path}"))
            if manifest.get("name") != entry["name"] or manifest.get("version") != entry["version"]:
                raise ValueError("catalog identity/version differs from pinned manifest")
    except subprocess.CalledProcessError as exc:
        raise ValueError("cannot read pinned commit object/manifests; fetch full history") from exc
    if len({entry["version"] for entry in entries}) != 1:
        raise ValueError("both variants need the same release version")
    if released:
        tag = "v" + entries[0]["version"]
        try:
            commit = git(root, "rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}")
        except subprocess.CalledProcessError as exc:
            raise ValueError(f"release tag {tag} is unavailable; fetch the published tag before opening a PR") from exc
        if commit != sha:
            raise ValueError("catalog SHA differs from resolved release tag commit")
        info = subprocess.run(["gh", "api", f"repos/{REPOSITORY}/releases/tags/{tag}"], cwd=root,
                              check=True, capture_output=True, text=True, encoding="utf-8", timeout=30)
        release_metadata(json.loads(info.stdout), tag)
    return entries


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", action="store_true", help="also require exact tag and published binary assets")
    args = parser.parse_args()
    try:
        check(Path.cwd(), args.release)
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as exc:
        print(f"marketplace check refused: {exc}", file=sys.stderr)
        return 1
    print("marketplace pins and frozen source versions verified" + ("; release published" if args.release else ""))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
