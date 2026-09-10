#!/usr/bin/env python3
"""Fail when the README's public hierarchy regresses.

Every marker is anchored — a heading at the start of a line, an image
reference, a command at the start of a code-block line — and each one is
searched for only after the previous marker, so a stray mention earlier in
the file (an old paragraph that says "make up", a second heading with the
same name) cannot satisfy the check. The proposed CLI may be mentioned only
after the Status section begins: the working path and the honest status
come first.
"""
# PEP 604 annotations (`str | None`) are evaluated at import time on
# Python 3.9, which is what macOS still ships as `python3` — the script
# died with a TypeError before running a single check. CONTRIBUTING.md
# tells contributors to run this locally, so it has to work on the
# interpreter they actually have, not just CI's.
from __future__ import annotations

import re
import sys
from pathlib import Path

ORDER = [
    ("hero image", r'src="brand/hero\.png"'),
    ("product line", r"^## Build and govern cloud-native AI agents on Kubernetes\.$"),
    ("outcome: control model spend", r"^### Control model spend$"),
    ("outcome: constrain tool calls", r"^### Constrain tool calls$"),
    ("outcome: approve consequential actions", r"^### Approve consequential actions$"),
    ("architecture diagram", r'src="docs/assets/architecture\.svg"'),
    ("Quickstart heading", r"^## Quickstart$"),
    ("Status heading", r"^## Status$"),
]
# The runnable path: these commands must be lines of the FIRST fenced code
# block in the Quickstart section, in this order, not prose that happens to
# name them. That path is `kmx` — install, up, talk to the agent — because it
# is the one that works without a clone.
QUICKSTART_COMMANDS = [
    # The FIRST line a reader sees must be the one that works on a machine
    # with a container engine and nothing else. A quickstart that
    # opens with `go install` opens with a prerequisite, and the prerequisite
    # count is the number this project is trying to move.
    ("the one-command install", r"^curl -fsSL https://raw\.githubusercontent\.com/kaimahi-agents/kaimahi/[^|]*install\.sh \|"),
    ("go install .../cmd/kmx", r"^go install github\.com/kaimahi-agents/kaimahi/cmd/kmx@"),
    ("kmx up", r"^kmx up\b"),
    ("kmx agent chat", r"^kmx agent chat\b"),
    # The governed half is one command too, and it is the product's whole
    # claim. A quickstart that stops at a conversation
    # sells an agent runtime; kagent already ships one.
    ("kmx plane", r"^kmx plane\b"),
    ("kmx govern", r"^kmx govern\b"),
    ("kmx ledger", r"^kmx ledger\b"),
]
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.M | re.S)
NEXT_SECTION = re.compile(r"^## ", re.M)
PROPOSED_CLI = re.compile(r"npx kaimahi create")


def quickstart_blocks(text: str, quickstart_end: int) -> list[str]:
    """The fenced blocks between the Quickstart heading and the next ## heading."""
    section_end = NEXT_SECTION.search(text, quickstart_end)
    section = text[quickstart_end : section_end.start() if section_end else len(text)]
    return [block.group(1) for block in FENCE.finditer(section)]


def ordered_in(block: str, commands: list[tuple[str, str]]) -> str | None:
    """Return the first command that is missing, or out of order, in a block."""
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
    status_start = None
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
        if label == "Status heading":
            status_start = match.start()
    cli = PROPOSED_CLI.search(text)
    if cli is not None and cli.start() < status_start:
        return "README front door: proposed CLI appears before the Status section"
    return None


def main(path: Path) -> int:
    problem = check(path.read_text())
    if problem:
        print(problem, file=sys.stderr)
        return 1
    print("README front door: identity, outcomes, architecture, quickstart, and status order valid")
    return 0


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "README.md"
    raise SystemExit(main(target))
