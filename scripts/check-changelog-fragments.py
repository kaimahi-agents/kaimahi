#!/usr/bin/env python3
"""Validate pending per-PR notes, enforce a Git diff, or render a release candidate.

No command edits the changelog or removes fragments. --list prints only included
paths; --render VERSION --date YYYY-MM-DD prints the complete candidate.
"""
from __future__ import annotations

import argparse
from datetime import date
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
CATEGORIES = ("Added", "Changed", "Fixed", "Breaking", "Upgrading")
NAME = re.compile(r"[a-z0-9][a-z0-9._-]*\.md\Z")
VERSION = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\Z")
HEADING = re.compile(rb"^## +([^\s]+)[^\n]*(?:\n|$)", re.M)


def parse_note(data: bytes, source: str, *, allow_empty: bool = False) -> dict[str, str]:
    """Only category headings and nonempty bodies; level-two lines are boundaries.

    The release extractor does not interpret fences, so neither do we when
    refusing a line that it would mistake for another release.
    """
    text = data.decode("utf-8")
    sections: dict[str, list[str]] = {}
    current = None
    for line in text.splitlines():
        if re.match(r"^##(?:[ \t]|$)", line):
            raise ValueError(f"{source}: level-two heading is not a note")
        if line.startswith("### "):
            current = line[4:].strip()
            if current not in CATEGORIES or current in sections:
                raise ValueError(f"{source}: unknown or duplicate category {current!r}")
            sections[current] = []
        elif current is not None:
            sections[current].append(line)
        elif line.strip():
            raise ValueError(f"{source}: content must follow a category heading")
    bodies = {category: "\n".join(lines).strip("\n") for category, lines in sections.items()}
    if (not bodies and not allow_empty) or any(not body.strip() for body in bodies.values()):
        raise ValueError(f"{source}: empty fragment or category")
    return bodies


def fragment_name(path: str) -> bool:
    """README is guidance; all other inputs must be flat, safely listable names."""
    if path == "changes/README.md":
        return False
    name = path.removeprefix("changes/")
    if not path.startswith("changes/") or not NAME.fullmatch(name) or ".." in name:
        raise ValueError(f"unsafe fragment path: {path!r}")
    return True


def regular_bytes(path: Path) -> bytes:
    if not stat.S_ISREG(path.lstat().st_mode):
        raise ValueError(f"{path}: expected a regular file, not a symlink or directory")
    return path.read_bytes()


def read_fragments(root: Path) -> dict[str, dict[str, str]]:
    directory = root / "changes"
    if not stat.S_ISDIR(directory.lstat().st_mode):
        raise ValueError("changes: expected a real directory, not a symlink")
    fragments = {}
    for entry in sorted(directory.iterdir()):
        path = "changes/" + entry.name
        data = regular_bytes(entry)
        if fragment_name(path):
            fragments[path] = parse_note(data, path)
    return fragments


def exempt(path: str) -> bool:
    """Closed docs/test-only policy; CI, tools and shipped assets are product."""
    item = PurePosixPath(path)
    return (path.startswith("docs/") or path.endswith(".md")
            or path.endswith("_test.go") or "testdata" in item.parts[:-1]
            or (item.parent == PurePosixPath("scripts")
                and ((item.name.startswith("test_") and item.name.endswith(".py"))
                     or item.name.endswith(("-test.py", "-test.sh")))))


def git(root: Path, *args: str) -> bytes:
    try:
        # Do not let user configuration silently choose between ambiguous refs.
        result = subprocess.run(["git", "-C", str(root), "-c", "core.warnAmbiguousRefs=true", *args],
                                capture_output=True, check=True)
    except (OSError, ValueError, subprocess.CalledProcessError) as problem:
        raise ValueError(f"Git comparison unavailable: {problem}") from problem
    if result.stderr.strip():
        raise ValueError("Git comparison emitted diagnostics; use unambiguous commit refs")
    return result.stdout


def resolve_commit(root: Path, ref: str) -> str:
    # Resolve first: a user ref must never occupy Git diff's option positions.
    resolved = git(root, "rev-parse", "--verify", "--end-of-options", ref + "^{commit}").strip()
    if not re.fullmatch(rb"[0-9a-f]{40}|[0-9a-f]{64}", resolved):
        raise ValueError(f"Git comparison did not resolve to one commit: {ref!r}")
    return resolved.decode("ascii")


def committed_fragments(root: Path, commit: str) -> dict[str, dict[str, str]]:
    fragments = {}
    entries = git(root, "ls-tree", "-rz", "--full-tree", commit, "--", "changes")
    for entry in entries.split(b"\0"):
        if not entry:
            continue
        metadata, raw_path = entry.split(b"\t", 1)
        mode, kind, oid = metadata.split()
        path = os.fsdecode(raw_path)
        if mode not in (b"100644", b"100755") or kind != b"blob":
            raise ValueError(f"{path!r}: expected a committed regular fragment")
        if fragment_name(path):
            fragments[path] = parse_note(git(root, "cat-file", "blob", oid.decode("ascii")), path)
    return fragments


def check_diff(root: Path, base: str, head: str) -> None:
    base_commit = resolve_commit(root, base)
    head_commit = resolve_commit(root, head)
    fragments = committed_fragments(root, head_commit)
    raw = git(root, "diff", "--no-ext-diff", "--no-renames", "--name-status", "-z",
              base_commit, head_commit, "--")
    fields = raw.split(b"\0")
    if fields[-1] != b"" or (len(fields) - 1) % 2:
        raise ValueError("Git comparison returned an unknown path/status format")
    changes = []
    for index in range(0, len(fields) - 1, 2):
        status, path = fields[index], os.fsdecode(fields[index + 1])
        if status not in (b"A", b"M", b"D", b"T") or not path:
            raise ValueError("Git comparison returned an unknown change status")
        changes.append((status, path))
    product = any(not exempt(path) for _, path in changes)
    if not product:
        return
    for status, path in changes:
        if path.startswith("changes/") and path != "changes/README.md" and status != b"A":
            raise ValueError(f"product PR cannot change an existing fragment: {path!r}")
    added = {path for status, path in changes if status == b"A"}
    if not added.intersection(fragments):
        raise ValueError("product change requires a new added valid fragment in changes/")


def render(root: Path, version: str, release_date: str) -> bytes:
    if not VERSION.fullmatch(version):
        raise ValueError("render version must be stable vX.Y.Z (no leading zeroes)")
    if not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}", release_date):
        raise ValueError("render date must be YYYY-MM-DD")
    date.fromisoformat(release_date)
    fragments = read_fragments(root)
    changelog = regular_bytes(root / "CHANGELOG.md")
    changelog.decode("utf-8")
    headings = list(HEADING.finditer(changelog))
    if any(match[1] == version.encode("ascii") for match in headings):
        raise ValueError(f"CHANGELOG.md already contains {version}")
    if not headings or headings[0][1] != b"Unreleased" or sum(
            match[1] == b"Unreleased" for match in headings) != 1:
        raise ValueError("CHANGELOG.md must start its sections with one Unreleased heading")
    unreleased = headings[0]
    history_start = headings[1].start() if len(headings) > 1 else len(changelog)
    old = changelog[unreleased.end():history_start]
    bodies = parse_note(old, "CHANGELOG.md Unreleased", allow_empty=True)
    merged = {category: [bodies[category]] if category in bodies else [] for category in CATEGORIES}
    for path in sorted(fragments):
        for category, body in fragments[path].items():
            merged[category].append(body)
    if not any(merged.values()):
        raise ValueError("cannot render an empty release")
    newline = b"\r\n" if unreleased[0].endswith(b"\r\n") else b"\n"
    blank = re.match(rb"(?:[ \t]*\r?\n)*", old)[0] or newline
    heading = unreleased[0]
    if not heading.endswith(b"\n"):
        heading += newline
    release = f"## {version} — {release_date}\n\n"
    for category in CATEGORIES:
        if merged[category]:
            release += f"### {category}\n\n" + "\n\n".join(merged[category]) + "\n\n"
    # Only the new section is normalized. Preamble and historical tail are slices
    # of the original bytes, including trailing spaces, CRLF and a missing EOF LF.
    return (changelog[:unreleased.start()] + heading + blank
            + release.encode("utf-8").replace(b"\n", newline) + changelog[history_start:])


def selftest() -> int:
    # Never validate the live checkout here: mutation mirrors symlink changes/.
    # This single test module owns independent temporary repositories only.
    environment = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
    result = subprocess.run([sys.executable, str(Path(__file__).with_name("test_changelog_fragments.py")), "-v"],
                            env=environment)
    if result.returncode:
        return 1
    print("changelog-fragments self-test: independent fragment lifecycle fixtures passed")
    return 0


def main(argv: list[str], root: Path = ROOT) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="base Git commit/ref; requires --head")
    parser.add_argument("--head", help="head Git commit/ref; requires --base")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--render", metavar="VERSION")
    mode.add_argument("--list", action="store_true", help="print only included fragment paths")
    mode.add_argument("--selftest", action="store_true")
    parser.add_argument("--date", help="explicit ISO release date; requires --render")
    args = parser.parse_args(argv)
    if any(value == "" for value in (args.base, args.head, args.render, args.date)):
        parser.error("option values must not be empty")
    if (args.base is None) != (args.head is None):
        parser.error("--base and --head must be supplied together")
    if (args.render is None) != (args.date is None):
        parser.error("--render and --date must be supplied together")
    if args.selftest:
        if args.base or args.head or args.date:
            parser.error("--selftest cannot be combined with comparison or render options")
        return selftest()
    try:
        fragments = read_fragments(root)
        if args.base:
            check_diff(root, args.base, args.head)
        if args.render:
            candidate = render(root, args.render, args.date)
            sys.stdout.buffer.write(candidate)
        elif args.list:
            sys.stdout.write("".join(path + "\n" for path in fragments))
        else:
            print(f"changelog fragments: {len(fragments)} pending fragment(s) valid")
    except (OSError, ValueError) as problem:
        print(f"changelog fragments: {problem}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
