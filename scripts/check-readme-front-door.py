#!/usr/bin/env python3
"""Check the README's KMX-first narrative and runnable setup sequence.

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
    ("product line", r"^\*\*Create locally\. Prove locally\. Lift to Kubernetes\.\*\*$"),
    ("KMX journey heading", r"^## The KMX Journey$"),
    ("Day 0 heading", r"^### Day 0: Two Verbs$"),
    ("Quickstart heading", r"^## Quickstart$"),
    ("platform boundary heading", r"^## Platform Boundary$"),
    ("migration heading", r"^## Migrate Model Traffic$"),
    ("Status heading", r"^## Status$"),
    ("documentation heading", r"^## Documentation$"),
]
# The KMX Day 0 surface is deliberately stable even while its standalone lift
# implementation is still directional. Keep both verbs visible without treating
# the snippet as evidence that every command is implemented.
DAY_ZERO_COMMANDS = [
    ("kmx agent create", r"^kmx agent create(?=[ \t]*(?:#.*)?$)"),
    ("kmx agent lift", r"^kmx agent lift(?=[ \t]*(?:#.*)?$)"),
]
# Orka helpers postdate the latest tagged release. The current-main build
# prerequisite is explicit rather than promising these commands in an old tag.
# Accept main or a commit ID; this verifies syntax, not the contents of a commit.
# Revisit the prerequisite when an Orka-capable tagged CLI is published.
QUICKSTART_COMMANDS = [
    ("go install .../cmd/kmx", r"^go install github\.com/kaimahi-agents/kaimahi/cmd/kmx@(?:main|[0-9a-f]{7,40})(?=[ \t]*(?:#.*)?$)"),
    ("kmx quickstart-wizard", r"^kmx quickstart-wizard(?=[ \t]*(?:#.*)?$)"),
]
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.M | re.S)
NEXT_SECTION = re.compile(r"^## ", re.M)
NEXT_DAY_ZERO_SECTION = re.compile(r"^#{2,3} ", re.M)


def quickstart_blocks(text: str, quickstart_end: int) -> list[str]:
    """Return fenced blocks inside Quickstart, not subsequent sections."""
    section_end = NEXT_SECTION.search(text, quickstart_end)
    section = text[quickstart_end : section_end.start() if section_end else len(text)]
    return [block.group(1) for block in FENCE.finditer(section)]


def day_zero_blocks(text: str, day_zero_end: int) -> list[str]:
    """Return fenced blocks in Day 0 before the next H2/H3 section."""
    section_end = NEXT_DAY_ZERO_SECTION.search(text, day_zero_end)
    section = text[day_zero_end : section_end.start() if section_end else len(text)]
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
        if label == "Day 0 heading":
            blocks = day_zero_blocks(text, position)
            if not blocks:
                return "README front door: Day 0 has no fenced command block"
            missing = ordered_in(blocks[0], DAY_ZERO_COMMANDS)
            if missing is not None:
                return f"README front door: {missing} is missing from the Day 0 command block"
        elif label == "Quickstart heading":
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
    print("README front door: KMX Day 0, runnable quickstart, boundaries, and status order valid")
    return 0


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "README.md"
    raise SystemExit(main(target))
