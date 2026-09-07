#!/usr/bin/env python3
"""Refuse to carry anything credential-shaped in the tree.

This repository is public and fork-exposed, and every credential it uses is
supplied by an operator at run time — typed at a prompt, or read from stdin
into a Kubernetes Secret. Nothing key-shaped has any reason to be in a file
here, including in a test fixture: a fixture that needs one assembles it at
run time, so "no credential shape anywhere in the tree" stays a claim with
no exceptions to remember.

The shapes are NOT written here. They are
internal/kmx/secretshapes/shapes.json, which the manifest scaffolder and
the blueprint parser read too — one list, three readers, one place to add
a shape. Before this, the three lists had eight, seven and three shapes
respectively, and the shortest was CI's: a GitHub token, a Slack token, an
Azure DevOps PAT or this plane's own credential could sit in a
documentation file and pass the gate whose whole job was to notice.

Scope, honestly: these are SHAPE rules, and a credential with no shape
cannot be caught this way. A bare password, or an opaque token from an
upstream this project has never used, is invisible here. What the rules do
cover is every credential this project or its upstreams actually issue.

Scope of the FILES: what is actually in the tree — tracked files plus
untracked ones git would accept. A developer's working directory is full
of gitignored run artifacts, and scanning those would turn a precise gate
into noise people learn to ignore. Pass explicit paths to scan those.

Run:  python3 scripts/check-secret-shapes.py [path...]
      python3 scripts/check-secret-shapes.py --selftest
"""
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
SHAPES_JSON = ROOT / "internal" / "kmx" / "secretshapes" / "shapes.json"

# Directories and file kinds with no text to leak. Kept short on purpose:
# every entry here is a place a credential could hide, so the list earns
# its keep only by covering things that cannot carry one (compiled
# binaries, images) rather than things that are inconvenient to fix.
SKIP_DIRS = {".git", "bin", ".claude", "node_modules", "__pycache__"}
SKIP_SUFFIX = {".png", ".jpg", ".jpeg", ".gif", ".pdf", ".ico", ".woff", ".woff2"}


class Shape:
    def __init__(self, raw):
        self.name = raw["name"]
        self.what = raw["what"]
        self.why = raw.get("why", "")
        self.re = re.compile(raw["regexp"])
        # Assembled, for the same reason shapes.json holds them in parts:
        # a whole example in a committed file is the thing being forbidden.
        self.example = "".join(raw.get("example", []))
        self.counterexample = "".join(raw.get("counterexample", []))


def load_shapes(path=SHAPES_JSON):
    """The shared list, or a refusal. A shape list that failed to load is
    not an empty list — every file would read clean and the gate would be
    gone with nothing to show for it."""
    try:
        doc = json.loads(path.read_text())
    except (OSError, ValueError) as e:
        sys.exit(f"check-secret-shapes: cannot read the shared shape list {path}: {e}")
    shapes = [Shape(s) for s in doc.get("shapes", [])]
    if not shapes:
        sys.exit(f"check-secret-shapes: {path} declares no shapes — refusing to report a clean tree.")
    return shapes


def files_to_scan(argv):
    if argv:
        out = []
        for arg in argv:
            p = pathlib.Path(arg)
            out.extend(sorted(q for q in p.rglob("*") if q.is_file()) if p.is_dir() else [p])
        return out
    inside = subprocess.run(["git", "rev-parse", "--is-inside-work-tree"],
                            capture_output=True, cwd=ROOT).returncode == 0
    if not inside:
        return sorted(q for q in ROOT.rglob("*") if q.is_file())
    listed = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        capture_output=True, cwd=ROOT, check=True,
    ).stdout.decode()
    return [ROOT / p for p in listed.split("\0") if p]


def skipped(p):
    """True for a file with no text to leak.

    The directory names are matched against the path RELATIVE to the
    repository root, never the absolute one. An absolute path carries
    whatever the checkout happens to sit under, and this repository can sit
    under `.claude/worktrees/...` — which made an earlier version of this
    skip every file in the tree and report it clean, in the exact
    fail-open shape the whole check exists to prevent.
    """
    try:
        parts = p.resolve().relative_to(ROOT).parts
    except ValueError:
        parts = p.parts  # outside the repository: judge it as given
    return p.suffix.lower() in SKIP_SUFFIX or bool(SKIP_DIRS & set(parts))


def scan(paths, shapes):
    """(findings, files actually read).

    A finding is (path, line number, shape, matched text). Unreadable is
    not clean: a file that could not be read is reported, because a
    permissions problem must not quietly shrink the scanned set. The read
    count is returned because "nothing was found" and "nothing was looked
    at" are different answers and only one of them is a pass.
    """
    findings, examined = [], 0
    for p in paths:
        if skipped(p):
            continue
        try:
            text = p.read_text()
        except UnicodeDecodeError:
            continue  # binary: nothing text-shaped to leak
        except OSError as e:
            findings.append((p, 0, None, f"could not be read (not scanned): {e}"))
            continue
        examined += 1
        for n, line in enumerate(text.splitlines(), 1):
            for shape in shapes:
                for m in shape.re.finditer(line):
                    findings.append((p, n, shape, m.group(0)))
    return findings, examined


def report(findings):
    for path, n, shape, what in findings:
        try:
            where = path.relative_to(ROOT)
        except ValueError:
            where = path
        # The matched text is NOT printed. If it is real, printing it copies
        # it into a CI log that outlives the revocation.
        named = shape.what if shape else what
        print(f"{where}:{n}: something shaped like {named}")
    if findings:
        print(f"\n{len(findings)} credential shape(s) in the tree — this repository is public.")
        print("Revoke anything real, then keep it out of files: a Secret is referenced, never carried.")
        print("A fixture that needs a credential-shaped value assembles it at run time.")
        return 1
    return 0


def selftest():
    """Prove each shape still catches what it names, through the same scan
    the tree gets — a file on disk, enumerated, read and matched — not just
    the regex in isolation. A scanner is a gate that fails OPEN when a
    pattern quietly stops matching, and every document reads clean when it
    does."""
    shapes = load_shapes()
    failed = 0
    with tempfile.TemporaryDirectory() as tmp:
        d = pathlib.Path(tmp)
        for shape in shapes:
            f = d / f"{shape.name}.txt"
            f.write_text(f"a line of ordinary prose\nvalue = {shape.example}\nand more prose\n")
            got = [s.name for _, _, s, _ in scan([f], shapes)[0] if s]
            if shape.name in got:
                print(f"ok   {shape.name}: caught in a scanned file")
            else:
                print(f"FAIL {shape.name}: its own example was not caught (found: {got or 'nothing'})")
                failed += 1
            if not shape.counterexample:
                print(f"FAIL {shape.name}: has no counterexample, so nothing pins how wide it is")
                failed += 1
                continue
            g = d / f"{shape.name}-near.txt"
            g.write_text(f"value = {shape.counterexample}\n")
            near = [s.name for _, _, s, _ in scan([g], shapes)[0] if s and s.name == shape.name]
            if near:
                print(f"FAIL {shape.name}: also fires on {shape.counterexample!r}")
                failed += 1
        # A scanner that examined nothing must not report clean, and the
        # empty case has two halves: no files, and no shapes.
        empty = d / "empty-dir"
        empty.mkdir()
        if files_to_scan([str(empty)]):
            print("FAIL an empty directory yielded files to scan")
            failed += 1
        else:
            print("ok   an empty directory yields no files (main refuses to call that clean)")
        no_shapes = d / "no-shapes.json"
        no_shapes.write_text('{"shapes": []}')
        try:
            load_shapes(no_shapes)
            print("FAIL a shape list with no shapes was accepted")
            failed += 1
        except SystemExit:
            print("ok   a shape list with no shapes is refused")
    if failed:
        print(f"check-secret-shapes self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print(f"check-secret-shapes self-test: {len(shapes)} shapes, each caught in a real scan")
    return 0


def main(argv):
    if argv[:1] == ["--selftest"]:
        return selftest()
    shapes = load_shapes()
    paths = files_to_scan(argv)
    # An enumeration that produced nothing is a broken gate, not a clean
    # tree — the same fail-open shape as a grep whose error is read as
    # "no match".
    if not paths:
        print("check-secret-shapes: no files to scan — refusing to report a clean tree.", file=sys.stderr)
        return 1
    findings, examined = scan(paths, shapes)
    # Enumerated is not examined. Every file can be enumerated and then
    # skipped — by a suffix rule, or by a directory rule matching something
    # in the absolute path — and the result is a clean report over nothing.
    if not examined:
        print(f"check-secret-shapes: {len(paths)} file(s) enumerated and none read — "
              "refusing to report a clean tree.", file=sys.stderr)
        return 1
    rc = report(findings)
    if rc == 0:
        print(f"check-secret-shapes: {examined} file(s) read, {len(shapes)} shapes, nothing credential-shaped")
    return rc


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
