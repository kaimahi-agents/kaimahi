#!/usr/bin/env python3
"""Check kmx-owned portable fixture bytes/modes offline, optionally against suite."""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import stat
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1] / "testdata" / "agentsuite"
METADATA = {"README.md", "provenance.json"}


def check(root: pathlib.Path, upstream: pathlib.Path | None = None) -> list[str]:
    try:
        files = json.loads((root / "provenance.json").read_text())["files"]
    except (OSError, ValueError, KeyError) as exc:
        return [f"cannot read fixture provenance: {exc}"]
    actual = {p.relative_to(root).as_posix() for p in root.rglob("*")
              if p.is_file() or p.is_symlink()}
    problems = []
    if actual - METADATA != set(files):
        problems.append(f"fixture inventory differs: {sorted((actual - METADATA) ^ set(files))}")
    for name, expected in files.items():
        for directory in [root] + ([upstream / "testdata"] if upstream else []):
            path = directory / name
            try:
                mode = "100755" if path.stat().st_mode & stat.S_IXUSR else "100644"
                if path.is_symlink() or mode != expected["mode"]:
                    problems.append(f"{path}: file mode differs")
                if hashlib.sha256(path.read_bytes()).hexdigest() != expected["sha256"]:
                    problems.append(f"{path}: SHA256 differs")
            except OSError as exc:
                problems.append(f"{path}: {exc}")
    return problems


class ParityTests(unittest.TestCase):
    """Real byte, inventory and executable-mode drift must not pass silently."""

    def test_parity_and_drift(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp) / "owned"
            root.mkdir()
            fixture = root / "input.json"
            fixture.write_bytes(b"{}\n")
            fixture.chmod(0o644)
            manifest = {"files": {"input.json": {
                "sha256": "ca3d163bab055381827226140568f3bef7eaac187cebd76878e0b63e9e442356",
                "mode": "100644",
            }}}
            provenance = root / "provenance.json"
            provenance.write_text(json.dumps(manifest))
            self.assertEqual(check(root), [])
            fixture.write_bytes(b"[]\n")
            self.assertTrue(check(root), "changed bytes must fail")
            fixture.write_bytes(b"{}\n")
            fixture.chmod(0o755)
            self.assertTrue(check(root), "changed executable mode must fail")
            fixture.chmod(0o644)
            extra = root / "extra.json"
            extra.write_bytes(b"{}\n")
            self.assertTrue(check(root), "unlisted files must fail")
            extra.unlink()
            extra.symlink_to(root, target_is_directory=True)
            self.assertTrue(check(root), "unlisted directory symlinks must fail")
            extra.unlink()
            extra.symlink_to(root / "missing.json")
            self.assertTrue(check(root), "unlisted dangling symlinks must fail")
            extra.unlink()
            fixture.unlink()
            self.assertTrue(check(root), "missing files must fail")
            fixture.write_bytes(b"{}\n")
            fixture.chmod(0o644)
            upstream = pathlib.Path(tmp) / "upstream"
            (upstream / "testdata").mkdir(parents=True)
            peer = upstream / "testdata" / "input.json"
            peer.write_bytes(b"{}\n")
            peer.chmod(0o644)
            self.assertEqual(check(root, upstream), [])
            peer.write_bytes(b"[]\n")
            self.assertTrue(check(root, upstream), "upstream drift must fail")
            peer.unlink()
            self.assertTrue(check(root, upstream), "missing upstream files must fail")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--selftest", action="store_true")
    parser.add_argument("--upstream", type=pathlib.Path, help="standalone suite checkout root")
    args = parser.parse_args()
    if args.selftest:
        unittest.main(argv=[__file__])
    else:
        problems = check(ROOT, args.upstream)
        for problem in problems:
            print(problem)
        if not problems:
            print("AgentSuite fixture parity: bytes, modes and inventory match")
        raise SystemExit(bool(problems))
