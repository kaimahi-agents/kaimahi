#!/usr/bin/env python3
"""Check the current suite-first workflow and its stable-release boundary."""
from __future__ import annotations

import re
import sys
from pathlib import Path

ORDER = [
    ("ketu icon", r'src="brand/ketu\.svg"'),
    ("product line", r"^\*\*Agent Builder CLI for Kubernetes\.\*\*$"),
    ("journey heading", r"^## Create, Prove, Lift$"),
    ("first-answer heading", r"^## Getting Starter: Get Your First Answer$"),
    ("workspace/live heading", r"^## Workspace And Live Paths$"),
    ("runtime boundaries heading", r"^## Runtime Boundaries$"),
    ("safety/status heading", r"^## Safety And Status$"),
    ("experimental notice", r"^> \[!IMPORTANT\]\n> \*\*Kaimahi is experimental and under active development\.\*\*"),
    ("runtime ownership note", r"^> \[!NOTE\]\n> kmx is the Agent Builder and lifecycle layer, not a runtime or generic$"),
    ("guides heading", r"^## Guides$"),
    ("development heading", r"^## Development$"),
]
BADGE_PATTERNS = [
    ("CI badge", r"actions/workflows/ci\.yml/badge\.svg\?branch=main"),
    ("release badge", r"img\.shields\.io/github/v/release/kaimahi-agents/kaimahi"),
    ("license badge", r"img\.shields\.io/github/license/kaimahi-agents/kaimahi"),
]
INSTALL_COMMANDS = [
    ("Homebrew install", r"^brew install kaimahi-agents/tap/kmx$"),
    ("release installer", r"^curl -fsSL https://raw\.githubusercontent\.com/kaimahi-agents/kaimahi/main/install\.sh \| sh$"),
    ("pinned Go install", r"^go install github\.com/kaimahi-agents/kaimahi/cmd/kmx@v0\.7\.0$"),
]
FIRST_ANSWER_COMMANDS = [
    ("kmx version", r"^kmx version$"),
    ("kmx quickstart", r"^kmx quickstart$"),
]
JOURNEY_ROWS = [
    ("Create", r"`kmx create NAME\b"),
    ("Prove", r"`kmx eval --workspace DIR\b"),
    ("Publish", r"`kmx push --workspace DIR --evaluation RECEIPT REF`"),
    ("Lift", r"`kmx lift REF@sha256:DIGEST --to-context CTX --evidence RECEIPT`"),
]
FENCE = re.compile(r"^```[^\n]*\n(.*?)^```", re.M | re.S)
NEXT_SECTION = re.compile(r"^## ", re.M)
JOURNEY_HEADER = "| Stage | Current checkout |"
JOURNEY_SEPARATOR = "|---|---|"
DEVELOPER_LINKS = [
    ("developer guide", "[developer guide](docs/development.md)"),
    ("codebase overview", "[codebase overview](docs/codebase-overview.md)"),
    ("interface baseline", "[interface baseline](docs/interface-baseline.md)"),
    ("repository map", "[repository map](docs/repository-map.md)"),
]
PATH_ROWS = ["Workspace", "Local environment", "Live Agent", "Legacy bundle"]


def ordered_in(block: str, commands: list[tuple[str, str]]) -> str | None:
    """Return the first command missing or out of order in a block."""
    position = 0
    for label, pattern in commands:
        found = re.compile(pattern, re.M).search(block, position)
        if found is None:
            return label
        position = found.end()
    return None


def section_after(text: str, heading_match: re.Match[str]) -> str:
    """Return the Markdown section following a matched level-two heading."""
    end = NEXT_SECTION.search(text, heading_match.end())
    return text[heading_match.end():end.start() if end else len(text)]


def journey_rows(section: str) -> tuple[list[tuple[str, str]], str | None]:
    """Parse the exact four-row, two-column suite journey table."""
    lines = section.splitlines()
    try:
        start = lines.index(JOURNEY_HEADER)
    except ValueError:
        return [], "suite journey table must use the exact two-column header"
    if start + 1 >= len(lines) or lines[start + 1] != JOURNEY_SEPARATOR:
        return [], "suite journey table separator is missing or malformed"
    raw_rows = []
    for line in lines[start + 2:]:
        if not line.startswith("|"):
            break
        raw_rows.append(line)
    if len(raw_rows) != len(JOURNEY_ROWS):
        return [], "suite journey table must contain exactly four rows"
    rows = []
    for line in raw_rows:
        parts = line.split("|")
        if len(parts) != 4 or parts[0] != "" or parts[-1] != "":
            return [], "suite journey table must contain exactly two columns"
        first, second = (part.strip() for part in parts[1:3])
        rows.append((first.strip("* "), second))
    return rows, None


def path_rows(section: str) -> dict[str, str]:
    """Parse the exact workspace/live path table into its four named rows."""
    lines = section.splitlines()
    try:
        start = lines.index("| Path | What it means |")
    except ValueError:
        return {}
    if start + 1 >= len(lines) or lines[start + 1] != JOURNEY_SEPARATOR:
        return {}
    rows: dict[str, str] = {}
    for line in lines[start + 2:]:
        if not line.startswith("|"):
            break
        parts = line.split("|")
        name = parts[1].strip() if len(parts) == 4 else ""
        if len(parts) != 4 or name in rows:
            return {}
        rows[name] = parts[2].strip()
    return rows


def check(text: str) -> str | None:
    """Return a failure message, or None when the hierarchy is valid."""
    position = 0
    matches: dict[str, re.Match[str]] = {}
    for label, pattern in ORDER:
        match = re.compile(pattern, re.M).search(text, position)
        if match is None:
            return f"README front door: {label} is missing or out of order"
        matches[label] = match
        position = match.end()

    journey = section_after(text, matches["journey heading"])
    rows, table_problem = journey_rows(journey)
    if table_problem is not None:
        return "README front door: " + table_problem
    table_at = journey.find(JOURNEY_HEADER)
    release = "The table below describes v0.7.0's one-member workflow; these docs follow main and may also describe unreleased behavior."
    release_at = journey.find(release)
    if not 0 <= release_at < table_at:
        return "README front door: v0.7.0 release note must precede the suite journey table"
    for index, (label, purpose_pattern) in enumerate(JOURNEY_ROWS):
        if rows[index][0] != label or re.search(purpose_pattern, rows[index][1]) is None:
            return f"README front door: {label} row is missing or out of order"
    if "only when the full policy passes" not in rows[1][1]:
        return "README front door: Prove row must require a full pass"
    if "optional `runs`/`minPasses` (default once)" not in rows[1][1]:
        return "README front door: Prove row must describe declared repeats"
    if "stops after a case misses its threshold or any unknown/error" not in rows[1][1]:
        return "README front door: Prove row must preserve fail-fast execution"
    if "Neither form deploys" not in rows[2][1]:
        return "README front door: Publish row must say it does not deploy"
    if "Plain `kmx push REF` requires passing evidence; `--unevaluated` explicitly opts out" not in rows[2][1]:
        return "README front door: Publish row must require evidence or explicit opt-out"
    if "without building, repacking, or publishing" not in rows[3][1]:
        return "README front door: Lift row must stay deploy-only"
    if "`--plan` is GET-only" not in rows[3][1]:
        return "README front door: Lift row must preserve the GET-only plan boundary"
    if "Orka `--inference provider:NAME` or kagent `--bindings FILE`" not in rows[3][1]:
        return "README front door: Lift row must preserve runtime-specific bindings"
    if "kagent refuses occupied targets" not in rows[3][1]:
        return "README front door: Lift row must preserve kagent create-only placement"
    normalized_preamble = " ".join(journey[:table_at].split())
    if "qualifies root Lift for Orka exact reuse, bounded absent-pair registry promotion and kagent fresh-target creation" not in normalized_preamble:
        return "README front door: Lift qualification scope is missing"
    if "it does not prove every promotion, creation or partial-outcome path" not in normalized_preamble:
        return "README front door: Lift qualification limits are missing"

    answer = section_after(text, matches["first-answer heading"])
    blocks = list(FENCE.finditer(answer))
    if len(blocks) < 2:
        return "README front door: install and first-answer command blocks are required"
    missing = ordered_in(blocks[0].group(1), INSTALL_COMMANDS)
    if missing is not None:
        return f"README front door: {missing} is missing from the install routes"
    missing = ordered_in(blocks[1].group(1), FIRST_ANSWER_COMMANDS)
    if missing is not None:
        return f"README front door: {missing} is missing from the first-answer commands"

    paths = path_rows(section_after(text, matches["workspace/live heading"]))
    if list(paths) != PATH_ROWS:
        return "README front door: workspace/live paths must contain the exact four rows"
    for label, required in {
        "Workspace": ["`kmx create`", "root `eval`", "root `push`", "does not consume workspace source", "separate private evidence"],
        "Local environment": ["`kmx local up`", "without creating an Agent", "setup-only", "not wired"],
        "Live Agent": ["`kmx run --agent NAME --prompt TEXT`", "`kmx chat NAME`", "Suite-owned Agents refuse legacy invocation"],
        "Legacy bundle": ["Removed outright", "`agent.yaml` readers/writers", "Old receipts are unreadable", "no conversion, migration or on-disk/resource deletion", "Public AgentSuite implementation bundles"],
    }.items():
        if any(marker not in paths[label] for marker in required):
            return f"README front door: {label} path boundary is missing"

    runtime = section_after(text, matches["runtime boundaries heading"])
    runtime_boundary = "A **runtime** executes and governs the agent. An **inference provider** is the separate model-access binding; selecting one does not select the other."
    if runtime_boundary not in " ".join(runtime.split()):
        return "README front door: runtime/inference distinction is missing"
    for label, pattern in [
        ("Orka default", r"\*\*Orka\*\* is the default runtime\..*packaged workspace path supports\s+evaluation, evaluated publication and root digest Lift with exact evaluated\s+reuse;.*independent live Orka run and chat remain"),
        ("kagent version boundary", r"\*\*kagent\*\* support is exact-version.*v0\.10\.3\s+Go/linux-amd64 packaged workspace authoring, `eval --bindings`, evaluated\s+publication and create-only root digest Lift\. Standalone run/chat, Python,\s+RemoteMCP, suite status and retirement remain unavailable\."),
        ("agentsessions boundary", r"\*\*agentsessions\*\* supports pinned-chat offline suite create/prepare/load/ordinary\s+push and explicit private `eval --development-host FILE`: one fresh\s+Session/config/Exec per declared run, full-pass private proof and exact-S evaluated\s+push\..*literal-loopback development path loads no cluster\s+settings; explicit judging and repeated cases share Orka/kagent semantics\. It supports no tools, Lift/chat/status or remote lifecycle\.\s+Receipt diff refuses its proof\."),
    ]:
        if re.search(pattern, runtime, re.S) is None:
            return f"README front door: {label} is missing"

    if re.search(r"\]\(docs/agent-lift\.md(?:#[^)]+)?\)", text):
        return "README front door: removed agent-lift guide remains linked"

    development = section_after(text, matches["development heading"])
    for label, link in DEVELOPER_LINKS:
        if link not in development:
            return f"README front door: {label} link is missing"

    for label, pattern in BADGE_PATTERNS:
        if re.search(pattern, text) is None:
            return f"README front door: {label} is missing"
    return None


def main(path: Path) -> int:
    """Check one README path and return a process exit status."""
    problem = check(path.read_text())
    if problem:
        print(problem, file=sys.stderr)
        return 1
    print("README front door: stable quickstart, suite stages, runtime boundaries and developer path valid")
    return 0


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "README.md"
    raise SystemExit(main(target))
