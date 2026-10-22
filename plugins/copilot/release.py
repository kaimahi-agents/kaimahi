#!/usr/bin/env python3
"""Package already signature-verified release engines; never download or execute them."""
from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

import package

TARGETS = ("linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")
PREFIX = "kmx-agent-builder/"
FILES = {"plugin.json", "mcp.json", "selected-root.json", "engine.json",
         "skills/kmx-agent-builder/SKILL.md", "bin/kmx", "SETUP.md"}
SETUP = """# Configure before installation

Verify this archive against the trusted release digest before extraction/execution.
This bundle is unconfigured: it cannot start its MCP server until you select a root.
In mcp.json replace __SELECTED_ROOT__ with your existing absolute repository path.
Set the identical path in selected-root.json. Use valid JSON (escape Windows backslashes).
Never select the filesystem root or a path containing control characters,
${PLUGIN_ROOT} or ${PLUGIN_DATA}. Review both files before installing.
These two local configuration edits are not covered by the original archive digest.
The bundled engine and skill must remain unchanged; do not edit installed cache files.
Run copilot plugin install ./kmx-agent-builder, then copilot plugin list.
Confirm the exact selected root in chat before allowing any kmx tool calls.
Changing root requires explicit reconfiguration, reinstall and server restart.
An update must be verified and configured again, never silently rebound to cwd.
"""


def read_inventory(text: str) -> dict[str, str]:
    expected = {"kmx-" + target.replace("/", "-") for target in TARGETS}
    entries = {}
    for line in text.splitlines():
        parts = line.split()
        if (len(parts) != 2 or not re.fullmatch(r"[a-f0-9]{64}", parts[0])
                or parts[1] not in expected or parts[1] in entries):
            raise ValueError("invalid engine checksum inventory")
        entries[parts[1]] = parts[0]
    if set(entries) != expected:
        raise ValueError("incomplete engine checksum inventory")
    return entries


def validate_buildinfo(info: dict, target: str, revision: str) -> None:
    settings = {entry["Key"]: entry["Value"] for entry in info.get("Settings", [])}
    os_name, arch = target.split("/")
    if (info.get("Path") != "github.com/kaimahi-agents/kaimahi/cmd/kmx"
            or settings.get("GOOS") != os_name or settings.get("GOARCH") != arch
            or settings.get("vcs.revision") != revision
            or settings.get("vcs.modified") != "false"):
        raise ValueError("engine metadata does not match platform, clean source and revision")
    # Go omits ldflags under -trimpath. Release version stamping is checked by
    # the release job against its native engine, not invented from missing data.


def verify_archive(path: Path, digest: str, target: str, version: str, revision: str) -> None:
    files = {}
    with tarfile.open(path, "r:gz") as source:
        for member in source:
            name = member.name.removeprefix(PREFIX)
            mode = 0o755 if name == "bin/kmx" else 0o644
            maximum = package.MAX_BINARY if name == "bin/kmx" else 64 << 10
            if (not member.name.startswith(PREFIX) or name not in FILES or name in files
                    or not member.isfile() or member.mode != mode
                    or member.uid != 0 or member.gid != 0 or member.mtime != 0
                    or not 0 < member.size <= maximum):
                raise ValueError("unsafe or unexpected archive entry")
            files[name] = source.extractfile(member).read()
    if set(files) != FILES or hashlib.sha256(files["bin/kmx"]).hexdigest() != digest:
        raise ValueError("archive inventory or engine digest mismatch")
    expected_engine = {"schemaVersion": 1, "sha256": digest, "platform": target,
                       "version": version, "revision": revision, "previewSchemaVersion": 1,
                       "purpose": "release-bundle; configure root before installation"}
    server = json.loads(files["mcp.json"])["mcpServers"]["kmx-agent-builder"]
    if (json.loads(files["engine.json"]) != expected_engine
            or json.loads(files["plugin.json"])["version"] != version.removeprefix("v")
            or server != {"type": "stdio", "command": "./bin/kmx",
                          "args": ["mcp", "--root", "__SELECTED_ROOT__"]}
            or json.loads(files["selected-root.json"]) != {"root": "__SELECTED_ROOT__"}
            or files["skills/kmx-agent-builder/SKILL.md"] != (package.HERE / "skills/kmx-agent-builder/SKILL.md").read_bytes()
            or files["SETUP.md"] != SETUP.encode()):
        raise ValueError("archive contract metadata mismatch")


def archive(*, binary: Path, sha256: str, target: str, version: str,
            revision: str, output: Path) -> Path:
    if target not in TARGETS or not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise ValueError("invalid release platform or revision")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("invalid release version")
    with tempfile.TemporaryDirectory(prefix="plugin stage ", dir=output.parent) as stage:
        root = Path(stage) / "kmx-agent-builder"
        package.build(binary=binary, sha256=sha256, target=target, root=None,
                      output=root, version=version[1:])
        package._write_json(root / "engine.json", {
            "schemaVersion": 1, "sha256": sha256, "platform": target,
            "version": version, "revision": revision, "previewSchemaVersion": 1,
            "purpose": "release-bundle; configure root before installation"})
        (root / "SETUP.md").write_bytes(SETUP.encode("utf-8"))
        # Exclusive publication: failed new outputs are ours, existing files are not.
        sink = output.open("xb")
        try:
            with sink:
                with gzip.GzipFile(fileobj=sink, mode="wb", filename="", mtime=0) as zipped:
                    with tarfile.open(fileobj=zipped, mode="w") as tar:
                        for name in sorted(FILES):
                            data = (root / name).read_bytes()
                            entry = tarfile.TarInfo(PREFIX + name)
                            entry.size = len(data)
                            entry.mode = 0o755 if name == "bin/kmx" else 0o644
                            tar.addfile(entry, io.BytesIO(data))
            verify_archive(output, sha256, target, version, revision)
        except Exception:
            output.unlink()
            raise
    return output


def build_all(assets: Path, version: str, revision: str) -> None:
    """Caller must first verify checksums.txt's Sigstore signature and identity."""
    inventory = read_inventory((assets / "checksums.txt").read_text(encoding="utf-8"))
    checked = []
    for target in TARGETS:
        name = "kmx-" + target.replace("/", "-")
        binary = assets / name
        if binary.is_symlink() or not binary.is_file() or not 0 < binary.stat().st_size <= package.MAX_BINARY:
            raise ValueError("engine must be a bounded regular file")
        result = subprocess.run(["go", "version", "-m", "-json", str(binary)],
                                check=True, capture_output=True, timeout=30)
        if len(result.stdout) > 2 << 20:
            raise ValueError("engine metadata exceeds limit")
        info = json.loads(result.stdout)
        validate_buildinfo(info, target, revision)
        checked.append((binary, inventory[name], target))
    rows = []
    for binary, digest, target in checked:
        name = "kmx-agent-builder-" + target.replace("/", "-") + ".tar.gz"
        result = archive(binary=binary, sha256=digest, target=target, version=version,
                         revision=revision, output=assets / name)
        rows.append(hashlib.sha256(result.read_bytes()).hexdigest() + "  " + name + "\n")
    with (assets / "plugin-checksums.txt").open("x", encoding="utf-8") as manifest:
        manifest.write("".join(rows))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--assets", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    args = parser.parse_args()
    build_all(args.assets, args.version, args.revision)


if __name__ == "__main__":
    main()
