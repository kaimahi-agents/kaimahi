#!/usr/bin/env python3
"""Check source-plugin versions and derive PATH variant identities from WSL sources.

Run from the repository root. Checks never write; --write explicitly regenerates
three identity-bearing files and optionally stamps the paired release version.
The mocked-SDK Node contracts remain the semantic provider-registration check.
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
SKILL_ENTRY = b"/kmx-agent-builder:"
PATH_SKILL_ENTRY = b"/kmx-agent-builder-path:"
SKILL_NAMES = {
    "skills/kmx/SKILL.md": b"kmx",
    "skills/kmx-agent-builder/SKILL.md": b"kmx-agent-builder",
}
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


def skill_body(relative: str, canonical: bytes) -> bytes:
    normalized = canonical.replace(b"\r\n", b"\n")
    frontmatter = re.match(rb"\A---\n(.*?)\n---\n", normalized, re.DOTALL)
    if frontmatter is None:
        raise ValueError(f"{relative}: canonical skill frontmatter is invalid")
    fields = frontmatter.group(1).split(b"\n")
    if (len(fields) != 2 or fields[0] != b"name: " + SKILL_NAMES[relative]
            or not fields[1].startswith(b"description: ") or not fields[1][len(b"description: "):].strip()):
        raise ValueError(f"{relative}: canonical skill name is invalid")
    return re.sub(rb"<!--.*?-->", b"", normalized[frontmatter.end():], flags=re.DOTALL)


def markdown_prose(source: bytes) -> bytes:
    lines, output, fence = source.splitlines(keepends=True), [], None
    for line in lines:
        if fence is None and (line.startswith(b"    ") or line.startswith(b"\t")):
            output.append(b"\n")
            continue
        marker = re.match(rb"^[ \t]{0,3}(`{3,}|~{3,})", line)
        if fence is None and marker:
            fence = (marker.group(1)[:1], len(marker.group(1)))
            output.append(b"\n")
            continue
        if fence is not None:
            closing = re.match(rb"^[ \t]{0,3}([`~]+)[ \t]*(?:\r?\n)?$", line)
            if closing and closing.group(1)[:1] == fence[0] and len(closing.group(1)) >= fence[1]:
                fence = None
            output.append(b"\n")
            continue
        output.append(line)
    return b"".join(output)


def validate_canonical(relative: str, canonical: bytes) -> None:
    if PATH_PROVIDER in canonical:
        raise ValueError(f"{relative}: canonical provider identity is missing or mixed")
    if PATH_SKILL_ENTRY in canonical:
        raise ValueError(f"{relative}: canonical skill entry identity is missing or mixed")
    if relative == "skills/kmx/SKILL.md":
        body = skill_body(relative, canonical)
        prose = markdown_prose(body)
        blocks = re.findall(rb"(?ms)^```json[ \t]*\n(.*?)^```[ \t]*$", body)
        try:
            canvas = json.loads(blocks[0]) if len(blocks) == 1 else None
        except (UnicodeDecodeError, json.JSONDecodeError):
            canvas = None
        if canvas is None or canvas.get("extensionId") != PROVIDER.decode():
            raise ValueError(f"{relative}: canonical provider identity is missing or mixed")
        entry = b"`Use /kmx-agent-builder:kmx open FULL_PATH, or ask: Open the kmx agent for FULL_PATH.`"
        lines = [line.strip() for line in prose.splitlines()]
        if lines.count(entry) != 1 or prose.count(SKILL_ENTRY) != 1:
            raise ValueError(f"{relative}: canonical skill entry identity is missing or mixed")
        return
    if relative == "skills/kmx-agent-builder/SKILL.md":
        body = markdown_prose(skill_body(relative, canonical))
        provider = b"Use provider `" + PROVIDER + b"` and canvas `kmx-agent-view`."
        lines = [line.strip() for line in body.splitlines()]
        if lines.count(provider) != 1 or body.count(PROVIDER) != 1 or SKILL_ENTRY in body:
            raise ValueError(f"{relative}: canonical provider identity is missing or mixed")
        return
    if b"`" in canonical:
        raise ValueError(f"{relative}: canonical provider registration uses unsupported JavaScript syntax")
    active = re.sub(rb"/\*.*?\*/", b"", canonical, flags=re.DOTALL)
    active = re.sub(rb"(?m)^[ \t]*//[^\r\n]*(?:\r?\n|$)", b"", active)
    lines = [line.strip() for line in active.splitlines()]
    handlers = (
        b'handler: createOpenHandler({ getSession: () => session, extensionId: "' + PROVIDER + b'" }),',
        b'handler: createOpenCommand({ getSession: () => session, extensionId: "' + PROVIDER + b'" }),',
    )
    import_line = b'import { OPEN_TOOL_NAME, OPEN_TOOL_PARAMETERS, createOpenHandler, createOpenCommand, readOpenView } from "./open.mjs";'
    provider_ids = re.findall(rb"plugin:[A-Za-z0-9._-]+:agent-view", active)
    identifiers = (active.count(b"createOpenHandler"), active.count(b"createOpenCommand"))
    if (provider_ids != [PROVIDER, PROVIDER] or identifiers != (2, 2)
            or lines.count(import_line) != 1 or any(lines.count(handler) != 1 for handler in handlers)):
        raise ValueError(f"{relative}: canonical provider identity is missing or mixed")


def derived(root: Path) -> dict:
    files = {}
    for relative in DERIVED:
        canonical = (root / "plugins/copilot-wsl" / relative).read_bytes()
        validate_canonical(relative, canonical)
        content = canonical.replace(PROVIDER, PATH_PROVIDER)
        if relative.startswith("skills/"):
            content = content.replace(SKILL_ENTRY, PATH_SKILL_ENTRY)
        files[root / "plugins/copilot" / relative] = content
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
