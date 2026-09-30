#!/usr/bin/env python3
"""Tripwire for effort IDs and lane references in source comments."""
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys
import tempfile
import tokenize

ROOT = pathlib.Path(__file__).resolve().parents[1]
PATTERN = re.compile(r"(?i)(?<![a-z0-9_])w[0-9]+(?![a-z0-9_])|\b(?:this|the) lane\b")
SUFFIXES = {".go", ".py", ".sh", ".yaml", ".yml"}


def scan(paths: list[pathlib.Path]) -> list[str]:
    findings = []
    go = [str(path) for path in paths if path.suffix == ".go"]
    if go:
        output = subprocess.check_output(["go", "run", str(ROOT / "scripts/comment-history-go.go"), "--", *go], cwd=ROOT)
        for item in json.loads(output):
            if PATTERN.search(item["text"]):
                findings.append(f'{item["path"]}:{item["line"]}: {item["text"].strip()}')
    for path in paths:
        if path.suffix == ".py":
            with path.open("rb") as source:
                comments = ((token.start[0], token.string) for token in tokenize.tokenize(source.readline)
                            if token.type == tokenize.COMMENT)
                findings.extend(f"{path}:{line}: {text.strip()}" for line, text in comments
                                if PATTERN.search(text))
        elif path.suffix in {".sh", ".yaml", ".yml"}:
            for line, text in enumerate(path.read_text().splitlines(), 1):
                if text.lstrip().startswith("#") and PATTERN.search(text):
                    findings.append(f"{path}:{line}: {text.strip()}")
    return findings


def selftest() -> None:
    with tempfile.TemporaryDirectory() as directory:
        root = pathlib.Path(directory)
        examples = {
            "sample.go": ('package sample\nvar text = "W31 the lane"\n// W31 is a stale tag\n'
                          '/* this lane describes old work */\n// W3C trace context and W31a suffix\n'),
            "sample.py": ('text = "W31 the lane"\n# W31 is a stale tag\n'
                          'other = "# the lane"\n# W3C trace context and W31a suffix\n'),
            "sample.sh": ('echo "# W31 the lane"\n# the lane is gone\n'
                          'echo ok # W42 in a trailing comment\n# W3C trace context and W31a suffix\n'),
            "sample.yaml": ('key: "# W31 the lane"\n# this lane is gone\n'
                            'key2: value # W42 in a trailing comment\n# W3C trace context and W31a suffix\n'),
            "sample.yml": ('key: "# W31 the lane"\n# W31 is a stale tag\n'
                           'key2: value # W42 in a trailing comment\n# W3C trace context and W31a suffix\n'),
        }
        for name, content in examples.items():
            (root / name).write_text(content)
        fixtures = [root / name for name in examples if pathlib.Path(name).suffix in SUFFIXES]
        findings = scan(fixtures)
        expected = {"sample.go:3", "sample.go:4", "sample.py:2", "sample.sh:2",
                    "sample.yaml:2", "sample.yml:2"}
        actual = {"%s:%s" % (pathlib.Path(item.split(":", 1)[0]).name,
                               item.split(":", 2)[1]) for item in findings}
        assert actual == expected, (actual, expected)
        for name in examples:
            (root / name).write_text('text = "W31 the lane"\n' if name.endswith('.py') else
                                     'key: "# W31 the lane"\n' if name.endswith(('.yaml', '.yml')) else
                                     'echo "# W31 the lane"\n' if name.endswith('.sh') else
                                     'package sample\nvar text = "W31 the lane"\n')
        assert not scan(fixtures), "strings were flagged"
    print("comment-history self-test: ok")


if __name__ == "__main__":
    if sys.argv[1:] == ["--selftest"]:
        selftest()
    elif sys.argv[1:]:
        sys.exit("usage: check-comment-history.py [--selftest]")
    else:
        tracked = subprocess.check_output(["git", "ls-files", "-z"], cwd=ROOT).decode().split("\0")
        paths = [ROOT / name for name in tracked if pathlib.Path(name).suffix in SUFFIXES
                 and not name.startswith("docs/reviews/") and name != "CHANGELOG.md"
                 and (ROOT / name).is_file()]
        findings = scan(paths)
        for finding in findings:
            print(finding)
        if findings:
            sys.exit(1)
        print("comment-history: ok")
