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
SCALAR = re.compile(r"^(?P<prefix>\s*(?:-\s+)?)(?:(?P<key>[^\s:][^:]*):\s+)?"
                    r"[|>](?P<options>[1-9][+-]?|[+-][1-9]?|)\s*(?:#.*)?$")


def quote_context(text: str, quote: str, shell: bool) -> str:
    """Carry quotes across lines without interpreting quotes inside comments."""
    index = 0
    while index < len(text):
        char = text[index]
        if char == "\\" and (quote != "'" if shell else quote == '"'):
            index += 2
            continue
        if quote:
            if char == quote:
                if not shell and quote == "'" and text[index:index + 2] == "''":
                    index += 2
                    continue
                quote = ""
        elif char == "#" and (index == 0 or text[index - 1].isspace()
                              or (shell and text[index - 1] in ";|&()")):
            break
        elif char in "\"'" and (shell or re.search(r"(?:^|:\s|[\[{,?])\s*(?:-\s+)?$",
                                                text[:index])):
            quote = char
        index += 1
    return quote


def hash_comments(path: pathlib.Path):
    """Yield whole-line comments, including shell comments in YAML run scalars."""
    yaml = path.suffix in {".yaml", ".yml"}
    quote = ""
    block = None
    for line, text in enumerate(path.read_text().splitlines(), 1):
        if not text.strip():
            continue
        indent = len(text) - len(text.lstrip())
        shell = not yaml
        if block is not None:
            parent_indent, content_indent, run = block
            if content_indent is None and indent > parent_indent:
                content_indent = indent
                block = (parent_indent, content_indent, run)
            if content_indent is not None and indent >= content_indent:
                if not run:
                    continue
                shell = True
            else:
                block = None
                quote = ""
        if not quote and text.lstrip().startswith("#"):
            yield line, text
            continue
        if yaml and block is None and not quote:
            header = SCALAR.match(text)
            if header:
                # A sequence marker is outside a mapping key's indentation.
                prefix = header["prefix"]
                parent_indent = len(prefix) if header["key"] else indent
                explicit = re.search(r"[1-9]", header["options"])
                content_indent = parent_indent + int(explicit[0]) if explicit else None
                key = (header["key"] or "").strip().strip("\"'")
                block = (parent_indent, content_indent, key == "run")
                continue
        quote = quote_context(text, quote, shell)


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
            for line, text in hash_comments(path):
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
            "multiline.sh": ('single=\'text\n# W31 the lane\n\'\n'
                             '# W31 after single quotes\n'
                             'double="text \\"quoted\\"\n# this lane\n"\n'
                             '# W31 after double quotes\n'
                             '# A comment with an unmatched " quote\n'
                             'echo ok # Another unmatched \' quote\n'
                             '# W31 after comments\n'),
            "multiline.yaml": ('description: |\n  # W31 the lane\n\n  # this lane\n'
                               '# W31 after literal scalar\n'
                               'nested:\n  text: |- # header\n    # the lane\n'
                               '  # W31 after nested scalar\n'
                               'text: >2-\n  # this lane\n'
                               '# W31 after folded scalar\n'
                               'quoted: "text\n  # W31 the lane\n  end"\n'
                               '# W31 after quoted scalar\n'
                               "plain: don't open a quote\n"
                               '# W31 after plain scalar\n'
                               'plain2: a " quote in plain text\n'
                               '# W31 after another plain scalar\n'
                               "single: 'it''s text\n  # W31 string content\n  end'\n"
                               '# W31 after single quoted scalar\n'),
            "workflow.yml": ('jobs:\n  test:\n    steps:\n'
                             '      - run: |\n'
                             '          # W31 in a shell comment\n'
                             '          text="start\n          # W31 string content\n          end"\n'
                             '          # the lane in a shell comment\n'
                             '        name: |\n          # W31 scalar content\n'
                             '      # W31 after run block\n'
                             '      - run: >-\n          # this lane\n'
                             '          text=\'unfinished\n'
                             '      # W31 after run context ends\n'),
        }
        for name, content in examples.items():
            (root / name).write_text(content)
        fixtures = [root / name for name in examples if pathlib.Path(name).suffix in SUFFIXES]
        findings = scan(fixtures)
        expected = {"sample.go:3", "sample.go:4", "sample.py:2", "sample.sh:2",
                    "sample.yaml:2", "sample.yml:2",
                    "multiline.sh:4", "multiline.sh:8", "multiline.sh:11",
                    "multiline.yaml:5", "multiline.yaml:9", "multiline.yaml:12",
                    "multiline.yaml:16", "multiline.yaml:18",
                    "multiline.yaml:20", "multiline.yaml:24",
                    "workflow.yml:5", "workflow.yml:9", "workflow.yml:12",
                    "workflow.yml:14", "workflow.yml:16"}
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
