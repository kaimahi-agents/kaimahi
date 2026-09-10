#!/usr/bin/env python3
"""Check the README's Orka-first navigation and runnable setup sequence.

This checks structure, not platform capability claims. The latter need review
against the installed version. Self-tests use synthetic documents so removing
obsolete tutorials never requires preserving them as test fixtures in public docs.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ORDER = [
    ("hero image", r'src="brand/hero\.png"'),
    ("product line", r"^## Get agents onto Orka$"),
    ("Quickstart heading", r"^## Quickstart$"),
    ("migration heading", r"^## Migrate model traffic$"),
    ("Status heading", r"^## Status$"),
    ("documentation heading", r"^## Documentation$"),
]
# Orka helpers postdate the latest tagged release. The current-main build
# prerequisite is explicit rather than promising these commands in an old tag.
# Accept main or a commit ID; this verifies syntax, not the contents of a commit.
# Revisit the prerequisite when an Orka-capable tagged CLI is published.
QUICKSTART_COMMANDS = [
    ("go install .../cmd/kmx", r"^go install github\.com/kaimahi-agents/kaimahi/cmd/kmx@(?:main|[0-9a-f]{7,40})(?=[ \t]*(?:#.*)?$)"),
    ("kmx up", r"^kmx up\b"),
    ("kmx orka install", r"^kmx orka install\b"),
    ("kmx orka status", r"^kmx orka status\b"),
]
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.M | re.S)
NEXT_SECTION = re.compile(r"^## ", re.M)


def quickstart_blocks(text: str, quickstart_end: int) -> list[str]:
    """Return fenced blocks inside Quickstart, not subsequent sections."""
    section_end = NEXT_SECTION.search(text, quickstart_end)
    section = text[quickstart_end : section_end.start() if section_end else len(text)]
    return [block.group(1) for block in FENCE.finditer(section)]


def ordered_in(block: str, commands: list[tuple[str, str]]) -> str | None:
    """Return the first command missing or out of order in a block."""
    position = 0
    for label, pattern in commands:
        found = re.compile(pattern, re.M).search(block, position)
        if found is None:
            return label
        position = found.end()
    return None


def check(text: str) -> str | None:
    """Return a failure message, or None when the hierarchy is valid."""
    position = 0
    for label, pattern in ORDER:
        match = re.compile(pattern, re.M).search(text, position)
        if match is None:
            return f"README front door: {label} is missing or out of order"
        position = match.end()
        if label == "Quickstart heading":
            blocks = quickstart_blocks(text, position)
            if not blocks:
                return "README front door: Quickstart has no fenced command block"
            missing = ordered_in(blocks[0], QUICKSTART_COMMANDS)
            if missing is not None:
                return f"README front door: {missing} is missing from the Quickstart command block"
    return None


def main(path: Path) -> int:
    problem = check(path.read_text())
    if problem:
        print(problem, file=sys.stderr)
        return 1
    print("README front door: identity, Orka quickstart, migration, and status order valid")
    return 0


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "README.md"
    raise SystemExit(main(target))
