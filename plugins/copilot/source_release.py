#!/usr/bin/env python3
"""Check source-plugin versions and derive PATH provider references from WSL sources.

Run from the repository root. Checks never write; --write explicitly regenerates
three identity-bearing files and optionally stamps the paired release version.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

DERIVED = ("skills/kmx/SKILL.md", "skills/kmx-agent-builder/SKILL.md",
           "com.github.copilot/extensions/agent-view/extension.mjs")
IDENTITIES = {"copilot-wsl": "kmx-agent-builder", "copilot": "kmx-agent-builder-path"}
WORDING = "plugins/copilot-wsl/com.github.copilot/extensions/agent-view/failure-wording"
PROVIDER = b"plugin:kmx-agent-builder:agent-view"
PATH_PROVIDER = b"plugin:kmx-agent-builder-path:agent-view"
NUMBER = r"(?:0|[1-9][0-9]*)"
PRERELEASE = r"(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
SEMVER = re.compile(rf"{NUMBER}\.{NUMBER}\.{NUMBER}(?:-{PRERELEASE}(?:\.{PRERELEASE})*)?")


def release_version(version: str) -> str:
    if not version.startswith("v") or not SEMVER.fullmatch(version[1:]):
        raise ValueError("expected a semantic vX.Y.Z[-prerelease] release version")
    return version[1:]


def manifests(root: Path) -> dict:
    values = {}
    for directory, name in IDENTITIES.items():
        path = root / "plugins" / directory / "plugin.json"
        manifest = json.loads(path.read_text(encoding="utf-8"))
        if manifest.get("name") != name:
            raise ValueError(f"{directory}: unexpected source plugin identity")
        values[path] = manifest
    return values


def versions(values: dict, version: str | None) -> None:
    actual = [value.get("version") for value in values.values()]
    if any(not isinstance(v, str) or not SEMVER.fullmatch(v) or v == "0.1.0-demo" for v in actual):
        raise ValueError("source plugins need non-demo semantic release versions")
    if len(set(actual)) != 1:
        raise ValueError("source plugin versions differ")
    if version is not None and actual[0] != release_version(version):
        raise ValueError(f"source plugin versions must match {version} exactly (including RC suffix)")


def derived(root: Path) -> dict:
    files = {}
    for relative in DERIVED:
        canonical = (root / "plugins/copilot-wsl" / relative).read_bytes()
        if PROVIDER not in canonical:
            raise ValueError(f"{relative}: canonical provider reference missing")
        files[root / "plugins/copilot" / relative] = canonical.replace(PROVIDER, PATH_PROVIDER)
    return files


def wording(root: Path) -> bytes:
    """Browser-safe ESM from the canonical JSON; avoids JSON import attributes."""
    templates = json.loads((root / f"{WORDING}.json").read_text(encoding="utf-8"))
    return ("// Generated from failure-wording.json by source_release.py --write; do not edit.\n"
            f"const TEMPLATES = {json.dumps(templates, indent=2)};\n"
            "export function failureWording(type, value) {\n"
            "  if (!Object.hasOwn(TEMPLATES, type)) return;\n"
            "  return TEMPLATES[type].replace(\"{value}\", () => value);\n"
            "}\n").encode()


def check(root: Path, version: str | None = None) -> None:
    versions(manifests(root), version)
    for path, expected in derived(root).items():
        if path.is_symlink() or not path.is_file() or path.read_bytes() != expected:
            raise ValueError(f"{path.relative_to(root)}: regenerate with source_release.py --write")
    module = root / f"{WORDING}.mjs"
    if module.is_symlink() or not module.is_file() or module.read_text(encoding="utf-8") != wording(root).decode("utf-8"):
        raise ValueError(f"{module.relative_to(root)}: regenerate with source_release.py --write")


def sync(root: Path, version: str | None = None) -> None:
    values = manifests(root)
    if version is not None:
        stamped = release_version(version)
        for value in values.values():
            value["version"] = stamped
    versions(values, version)
    files = derived(root)
    # Replace links, not their canonical targets. All other shared assets remain linked.
    for path, content in files.items():
        if path.is_symlink():
            path.unlink()
        path.write_bytes(content)
    (root / f"{WORDING}.mjs").write_bytes(wording(root))
    if version is not None:
        for path, value in values.items():
            path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    check(root, version)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="regenerate PATH copies; optionally stamp versions")
    parser.add_argument("--version", help="paired v-prefixed release, including its exact RC suffix")
    args = parser.parse_args()
    try:
        (sync if args.write else check)(Path.cwd(), args.version)
    except (ValueError, OSError) as exc:
        print(f"source plugin release check refused: {exc}", file=sys.stderr)
        return 1
    print("source plugin versions and PATH provider copies verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
