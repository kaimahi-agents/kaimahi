#!/usr/bin/env python3
"""Check that the README leads with an installable, runnable KMX first answer.

The synthetic self-test covers the structure; platform claims require review
against the installed version and the detailed bundle guide.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ORDER = [
    ("ketu icon", r'src="brand/ketu\.svg"'),
    ("product line", r"^\*\*Agent Builder CLI for Kubernetes\.\*\*$"),
    ("journey heading", r"^## Create, Prove, Lift$"),
    ("Quickstart heading", r"^## Quickstart$"),
    ("runtime contract heading", r"^## Runtime Contract$"),
    ("Status heading", r"^## Status$"),
    ("documentation heading", r"^## Documentation$"),
    ("Development heading", r"^## Development$"),
    ("experimental notice", r"^> \[!IMPORTANT\]\n> \*\*Kaimahi is experimental and under active development\.\*\*"),
    ("runtime ownership note", r"^> \[!NOTE\]\n> KMX is the Agent Builder and lifecycle layer, not a runtime or generic$"),
]
BADGE_PATTERNS = [
    ("CI badge", r"actions/workflows/ci\.yml/badge\.svg\?branch=main"),
    ("release badge", r"img\.shields\.io/github/v/release/kaimahi-agents/kaimahi"),
    ("license badge", r"img\.shields\.io/github/license/kaimahi-agents/kaimahi"),
]
# Install options are deliberately independent lines, not a chain that could
# run a stale kmx after a failed install. The guide handles lifecycle flags.
INSTALL_COMMANDS = [
    ("Homebrew install", r"^brew install kaimahi-agents/tap/kmx$"),
    ("release installer", r"^curl -fsSL https://raw\.githubusercontent\.com/kaimahi-agents/kaimahi/main/install\.sh \| sh$"),
    ("pinned Go install", r"^go install github\.com/kaimahi-agents/kaimahi/cmd/kmx@v0\.4\.1$"),
]
FIRST_ANSWER_COMMANDS = [("kmx quickstart", r"^kmx quickstart$")]
JOURNEY_COMMANDS = [
    ("kmx up", r"^`kmx up`"),
    ("kmx quickstart --interactive", r"^`kmx quickstart --interactive`"),
    ("kmx agent create", r"^`kmx agent create`"),
    ("kmx agent lift", r"^`kmx agent lift`"),
    ("kmx agent status", r"^`kmx agent status`"),
    ("kmx agent evaluate", r"^`kmx agent evaluate`"),
    ("bundle lift guide", r"\[bundle lift guide\]\(docs/agent-lift\.md\)"),
]
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.M | re.S)
NEXT_SECTION = re.compile(r"^## ", re.M)


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
        if label == "journey heading":
            end = NEXT_SECTION.search(text, position)
            section = text[position:end.start() if end else len(text)]
            blocks = list(FENCE.finditer(section))
            if not blocks:
                return "README front door: install routes are missing from the journey command block"
            missing = ordered_in(blocks[0].group(1), INSTALL_COMMANDS)
            if missing is not None:
                return f"README front door: {missing} is missing from the install routes"
            if len(blocks) < 2:
                return "README front door: kmx quickstart is missing from the journey command block"
            missing = ordered_in(blocks[1].group(1), FIRST_ANSWER_COMMANDS)
            if missing is not None:
                return f"README front door: {missing} is missing from the journey command block"
            missing = ordered_in(section[blocks[1].end():], JOURNEY_COMMANDS)
            if missing is not None:
                return f"README front door: {missing} is missing from the journey prose"
    for label, pattern in BADGE_PATTERNS:
        if re.search(pattern, text) is None:
            return f"README front door: {label} is missing"
    return None


def main(path: Path) -> int:
    problem = check(path.read_text())
    if problem:
        print(problem, file=sys.stderr)
        return 1
    print("README front door: runnable install, first answer, bundle journey and runtime boundary valid")
    return 0


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "README.md"
    raise SystemExit(main(target))
