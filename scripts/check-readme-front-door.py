#!/usr/bin/env python3
"""Check the README entry point and linked workflow/release boundaries."""
from __future__ import annotations

import re
import sys
from pathlib import Path

ORDER = [
    ("journey heading", r"^## Run, Test, Publish$"),
    ("first-answer heading", r"^## Getting Started: Get Your First Answer$"),
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
    ("pinned Go install", r"^go install github\.com/kaimahi-agents/kaimahi/cmd/kmx@v0\.8\.1$"),
]
FIRST_ANSWER_COMMANDS = [
    ("kmx version", r"^kmx version$"),
    ("kmx quickstart", r"^kmx quickstart$"),
]
JOURNEY_ROWS = [
    ("Create", r"`kmx create NAME\b"),
    ("Test", r"`kmx test --workspace DIR\b"),
    ("Publish", r"`kmx push --workspace DIR --evaluation RECEIPT REF`"),
    ("Deploy", r"`kmx deploy REF@sha256:DIGEST --to-context CTX --evidence RECEIPT`"),
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


def check_workflow(text: str) -> str | None:
    """Keep the detailed workflow and safety contract in the linked guide."""
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
    preamble = journey[:table_at]
    blocks = list(FENCE.finditer(preamble))
    if not blocks or ordered_in(blocks[0].group(1), [
        ("create", r"^kmx create NAME --workspace \. --model MODEL --instructions FILE$"),
        ("composed Deploy", r"^kmx deploy --to NAME$"),
    ]) is not None:
        return "README front door: two-command workspace path must precede manual stages"
    prerequisites = " ".join(preamble[:blocks[0].start()].split())
    for label, marker in [
        ("prepared runtime", "prepared runtime"),
        ("inference prerequisite", "runtime and inference"),
        ("registry authentication", "registry authentication"),
        ("kmx.yaml", "kmx.yaml"),
        ("publishRepository", "publishRepository"),
        ("destination selectors", "context/namespace/inference selectors"),
        ("evaluationTarget", "evaluationTarget"),
        ("offline create prerequisite boundary", "Create supplies none of these"),
    ]:
        if marker not in prerequisites.replace("`", ""):
            return f"README front door: {label} must accompany the two-command path"
    if "source-only promotion" not in preamble or "not destination evaluation" not in preamble:
        return "README front door: composed Deploy must disclose source-only promotion"
    release = "The table below describes main's one-member workflow. Test and Deploy are unreleased; v0.8.1 uses `eval` and `lift` with the same arguments."
    release_at = journey.find(release)
    if not 0 <= release_at < table_at:
        return "README front door: v0.8.1 release note must precede the suite journey table"
    for index, (label, purpose_pattern) in enumerate(JOURNEY_ROWS):
        if rows[index][0] != label or re.search(purpose_pattern, rows[index][1]) is None:
            return f"README front door: {label} row is missing or out of order"
    if "only when the full policy passes" not in rows[1][1]:
        return "README front door: Test row must require a full pass"
    if "optional `runs`/`minPasses` (default once)" not in rows[1][1]:
        return "README front door: Test row must describe declared repeats"
    if "stops after a case misses its threshold or any unknown/error" not in rows[1][1]:
        return "README front door: Test row must preserve fail-fast execution"
    if "Neither form deploys" not in rows[2][1]:
        return "README front door: Publish row must say it does not deploy"
    if "Plain `kmx push REF` requires passing evidence; `--unevaluated` explicitly opts out" not in rows[2][1]:
        return "README front door: Publish row must require evidence or explicit opt-out"
    if "without building, repacking, or publishing" not in rows[3][1]:
        return "README front door: Deploy row must stay deploy-only"
    if "`--plan` is GET-only" not in rows[3][1]:
        return "README front door: Deploy row must preserve the GET-only plan boundary"
    if "Orka `--inference provider:NAME --promote` for candidate evidence or kagent `--bindings FILE`" not in rows[3][1]:
        return "README front door: Deploy row must preserve runtime-specific bindings"
    if "kagent refuses occupied targets" not in rows[3][1]:
        return "README front door: Deploy row must preserve kagent create-only placement"
    if "Orka candidate evidence requires `--promote`, even in the same environment" not in rows[3][1]:
        return "README front door: Deploy row must describe candidate evidence promotion"
    normalized_preamble = " ".join(preamble.split())
    if "qualifies root Deploy for Orka exact reuse, bounded absent-pair registry promotion and kagent fresh-target creation" not in normalized_preamble:
        return "README front door: Deploy qualification scope is missing"
    if "it does not prove every promotion, creation or partial-outcome path" not in normalized_preamble:
        return "README front door: Deploy qualification limits are missing"

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
        "Workspace": ["`kmx create`", "root `test`", "root `push`", "does not consume workspace source", "separate private evidence"],
        "Local environment": ["`kmx local up`", "without creating an Agent", "setup-only", "not wired"],
        "Live Agent": ["`kmx run --agent NAME --prompt TEXT`", "`kmx chat NAME`", "including suite-owned Agents", "neither is workspace-local execution or suite evaluation"],
        "Legacy bundle": ["Removed outright", "`agent.yaml` readers/writers", "Old receipts are unreadable", "no conversion, migration or on-disk/resource deletion", "Public AgentSuite implementation bundles"],
    }.items():
        if any(marker not in paths[label] for marker in required):
            return f"README front door: {label} path boundary is missing"

    runtime = section_after(text, matches["runtime boundaries heading"])
    runtime_boundary = "A **runtime** executes and governs the agent. An **inference provider** is the separate model-access binding; selecting one does not select the other."
    if runtime_boundary not in " ".join(runtime.split()):
        return "README front door: runtime/inference distinction is missing"
    for label, pattern in [
        ("Orka default", r"\*\*Orka\*\* is the default runtime\..*packaged workspace path supports\s+evaluation, evaluated publication and root digest Deploy with exact evaluated\s+reuse;.*independent live Orka run and chat remain"),
        ("kagent version boundary", r"\*\*kagent\*\* support is exact-version.*v0\.10\.3\s+Go/linux-amd64 packaged workspace authoring, `test --bindings`, evaluated\s+publication and create-only root digest Deploy\. Standalone run/chat, Python,\s+RemoteMCP and retirement remain unavailable\. Read-only destination status\s+observes placement without enabling those execution/lifecycle capabilities\."),
        ("agentsessions boundary", r"\*\*agentsessions\*\* supports pinned-chat offline suite create/prepare/load/ordinary\s+push and explicit private `test --development-host FILE`: one fresh\s+Session/config/Exec per declared run, full-pass private proof and exact-S evaluated\s+push\..*literal-loopback development path loads no cluster\s+settings; explicit judging and repeated cases share Orka/kagent semantics\. Development evaluation supports no tools, chat or remote-host evaluation\. Named Agent deployment and destination status are separate Kubernetes operations\.\s+Eval\s+requires an `AgentSessionsBinding` \(`--bindings FILE`\) serving the packaged\s+model and records its digest\. Deploy creates an immutable Agent at a named destination; the offline --harness-image --plan render remains separate\.\s+Receipt diff refuses its proof\."),
    ]:
        if re.search(pattern, runtime, re.S) is None:
            return f"README front door: {label} is missing"

    if re.search(r"\]\((?:docs/)?agent-lift\.md(?:#[^)]+)?\)", text):
        return "README front door: removed agent-lift guide remains linked"

    development = section_after(text, matches["development heading"])
    for label, link in DEVELOPER_LINKS:
        if link.replace("](docs/", "](") not in development:
            return f"README front door: {label} link is missing"

    return None


def check(text: str, workflow: str) -> str | None:
    """Check the short entry point without requiring technical tables in it."""
    for label, pattern in [("ketu icon", r'src="brand/ketu\.svg"'), *BADGE_PATTERNS]:
        if re.search(pattern, text) is None:
            return f"README front door: {label} is missing"
    answer = re.search(r"^## Try it locally$", text, re.M)
    guides = re.search(r"^## Go deeper$", text, re.M)
    if answer is None or guides is None or guides.start() < answer.end():
        return "README front door: local trial must precede deeper guides"
    status = r"^\*\*Status: experimental\. Not recommended for production use\.\*\*$"
    if re.search(status, text[:answer.start()], re.M) is None:
        return "README front door: experimental status must precede the local trial"
    blocks = list(FENCE.finditer(section_after(text, answer)))
    if not blocks:
        return "README front door: local trial command block is missing"
    missing = ordered_in(blocks[0].group(1), INSTALL_COMMANDS[:1] + FIRST_ANSWER_COMMANDS)
    if missing is not None:
        return f"README front door: {missing} is missing from the local trial"
    if "](docs/workflow-overview.md)" not in section_after(text, guides):
        return "README front door: linked workflow guide is missing"
    return check_workflow(workflow)


def main(path: Path) -> int:
    """Check the README and its required operator guide; missing inputs refuse."""
    try:
        text = path.read_text()
        workflow = (path.parent / "docs/workflow-overview.md").read_text()
    except (OSError, UnicodeError) as error:
        print(f"README front door: cannot read required documentation: {error}", file=sys.stderr)
        return 1
    problem = check(text, workflow)
    if problem:
        print(problem, file=sys.stderr)
        return 1
    print("README front door: local trial and linked workflow boundaries valid")
    return 0


if __name__ == "__main__":
    target = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "README.md"
    raise SystemExit(main(target))
