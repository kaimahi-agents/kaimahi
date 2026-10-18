#!/usr/bin/env python3
"""Exercise the KMX front door with synthetic README pages, not archived prose."""
import importlib.util
import subprocess
import sys
import tempfile
from pathlib import Path

CHECKER = Path(__file__).with_name("check-readme-front-door.py")
spec = importlib.util.spec_from_file_location("front_door", CHECKER)
front_door = importlib.util.module_from_spec(spec)
spec.loader.exec_module(front_door)

INSTALL = """brew install kaimahi-agents/tap/kmx
# or
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
# or
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.8.1
"""
RELEASE_NOTE = """> [!NOTE]
> The table below describes v0.8.1's one-member workflow; these docs follow main and may also describe unreleased behavior.
"""
COMPOSED = """Start in the workspace root with a prepared runtime and inference, registry authentication,
and kmx.yaml containing publishRepository, context/namespace/inference selectors
and the destination's evaluationTarget. Create supplies none of these.
```bash
kmx create NAME --workspace . --model MODEL --instructions FILE
kmx lift --to NAME
```
Lift freezes inputs and runs missing evaluation, publication and placement stages.
It discloses source-only promotion, not destination evaluation.

"""
JOURNEY = RELEASE_NOTE + """| Stage | Current checkout |
|---|---|
| **Create** | `kmx create NAME --workspace DIR` authors source. |
| **Prove** | `kmx eval --workspace DIR` evaluates ordered cases with optional `runs`/`minPasses` (default once), stops after a case misses its threshold or any unknown/error, and writes evidence only when the full policy passes. |
| **Publish** | `kmx push --workspace DIR --evaluation RECEIPT REF` copies the suite. Plain `kmx push REF` requires passing evidence; `--unevaluated` explicitly opts out. Neither form deploys. |
| **Lift** | `kmx lift REF@sha256:DIGEST --to-context CTX --evidence RECEIPT` plus Orka `--inference provider:NAME --promote` for candidate evidence or kagent `--bindings FILE` deploys without building, repacking, or publishing. `--plan` is GET-only; kagent refuses occupied targets; legacy bundle lift is separate. Orka candidate evidence requires `--promote`, even in the same environment. |
"""
GOOD = """<img src="brand/ketu.svg" alt="Kaimahi ketu mark">
**Agent Builder CLI for Kubernetes.**
[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)
## Create, Prove, Lift
""" + COMPOSED + """Bounded CI qualifies root Lift for Orka exact reuse, bounded absent-pair registry promotion and kagent fresh-target creation; it does not prove every promotion, creation or partial-outcome path.
""" + JOURNEY + """## Getting Started: Get Your First Answer
```bash
""" + INSTALL + """```
```bash
kmx version
kmx quickstart
```
## Workspace And Live Paths
| Path | What it means |
|---|---|
| Workspace | Default `kmx create`, root `eval`, and root `push` operate on editable source. Digest-based root `lift` does not consume workspace source; it uses the packaged artifact and separate private evidence with explicit target and inference inputs. Run/chat still target live Agents. |
| Local environment | `kmx local up` prepares kind, model and runtime without creating an Agent. The experimental Go `AgentEnvironment` interface is setup-only and is not wired to this command yet. |
| Live Agent | `kmx run --agent NAME --prompt TEXT` performs one Task; `kmx chat NAME` opens an Orka session. Both target live Agents, including suite-owned Agents; neither is workspace-local execution or suite evaluation. |
| Legacy bundle | Removed outright: `agent.yaml` readers/writers, commands and receipts, `create --live`. Old receipts are unreadable; no conversion, migration or on-disk/resource deletion. Public AgentSuite implementation bundles are a different format. |
## Runtime Boundaries
A **runtime** executes and governs the agent. An **inference provider** is the separate model-access binding; selecting one does not select the other.
- **Orka** is the default runtime. Its packaged workspace path supports evaluation, evaluated publication and root digest Lift with exact evaluated reuse; independent live Orka run and chat remain.
- **kagent** support is exact-version and operation-specific: v0.10.3 Go/linux-amd64 packaged workspace authoring, `eval --bindings`, evaluated publication and create-only root digest Lift. Standalone run/chat, Python, RemoteMCP and retirement remain unavailable. Read-only destination status observes placement without enabling those execution/lifecycle capabilities.
- **agentsessions** supports pinned-chat offline suite create/prepare/load/ordinary push and explicit private `eval --development-host FILE`: one fresh Session/config/Exec per declared run, full-pass private proof and exact-S evaluated push. This unauthenticated literal-loopback development path loads no cluster settings; explicit judging and repeated cases share Orka/kagent semantics. It supports no tools, Lift/chat/status or remote lifecycle. Receipt diff refuses its proof.
## Safety And Status
> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** Interfaces may change.
> [!NOTE]
> kmx is the Agent Builder and lifecycle layer, not a runtime or generic
> governance control plane.
## Guides
[Workspace lifecycle](docs/suite-workspace.md)
## Development
[developer guide](docs/development.md)
[codebase overview](docs/codebase-overview.md)
[interface baseline](docs/interface-baseline.md)
[repository map](docs/repository-map.md)
"""

CASES = [("valid front door", GOOD, None)]
for label, literal in [
    ("ketu icon", '<img src="brand/ketu.svg" alt="Kaimahi ketu mark">\n'),
    ("product line", "**Agent Builder CLI for Kubernetes.**\n"),
    ("journey heading", "## Create, Prove, Lift\n"),
    ("first-answer heading", "## Getting Started: Get Your First Answer\n"),
    ("workspace/live heading", "## Workspace And Live Paths\n"),
    ("runtime boundaries heading", "## Runtime Boundaries\n"),
    ("safety/status heading", "## Safety And Status\n"),
    ("guides heading", "## Guides\n"),
    ("development heading", "## Development\n"),
    ("experimental notice", "> [!IMPORTANT]\n> **Kaimahi is experimental and under active development.** Interfaces may change.\n"),
    ("runtime ownership note", "> [!NOTE]\n> kmx is the Agent Builder and lifecycle layer, not a runtime or generic\n> governance control plane.\n"),
    ("CI badge", "actions/workflows/ci.yml/badge.svg?branch=main"),
    ("release badge", "img.shields.io/github/v/release/kaimahi-agents/kaimahi"),
    ("license badge", "img.shields.io/github/license/kaimahi-agents/kaimahi"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))

for label, literal in [
    ("Homebrew install", "brew install kaimahi-agents/tap/kmx\n"),
    ("release installer", "curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh\n"),
    ("pinned Go install", "go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.8.1\n"),
    ("kmx version", "kmx version\n"),
    ("kmx quickstart", "kmx quickstart\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))

CASES += [
    ("missing composed path", GOOD.replace(COMPOSED, ""), "two-command workspace path"),
    ("composed commands reversed", GOOD.replace("kmx create NAME --workspace . --model MODEL --instructions FILE\nkmx lift --to NAME", "kmx lift --to NAME\nkmx create NAME --workspace . --model MODEL --instructions FILE"), "two-command workspace path"),
    ("composed path after manual stages", GOOD.replace(COMPOSED, "").replace("## Getting Started", COMPOSED + "## Getting Started"), "two-command workspace path"),
    ("missing runtime prerequisite", GOOD.replace("prepared runtime and inference", "inference"), "prepared runtime"),
    ("missing inference prerequisite", GOOD.replace("prepared runtime and inference", "prepared runtime"), "inference prerequisite"),
    ("missing registry authentication", GOOD.replace("registry authentication", "registry access"), "registry authentication"),
    ("missing operational config", GOOD.replace("kmx.yaml", "workspace.yaml"), "kmx.yaml"),
    ("missing repository", GOOD.replace("publishRepository", "repository"), "publishRepository"),
    ("missing target selectors", GOOD.replace("context/namespace/inference selectors", "target name"), "destination selectors"),
    ("missing evaluation target", GOOD.replace("evaluationTarget", "evalTarget"), "evaluationTarget"),
    ("create claims bootstrap", GOOD.replace("Create supplies none of these", "Create supplies all of these"), "offline create prerequisite boundary"),
    ("missing source-only disclosure", GOOD.replace("source-only promotion, not destination evaluation", "destination evaluation"), "source-only promotion"),
    ("missing candidate promotion", GOOD.replace("Orka candidate evidence requires `--promote`, even in the same environment.", ""), "candidate evidence promotion"),
    ("candidate command omits promotion", GOOD.replace("--inference provider:NAME --promote", "--inference provider:NAME"), "Lift row must preserve runtime-specific bindings"),
    ("command in purpose column only", GOOD.replace("| **Create** | `kmx create", "| Author | **Create** with `kmx create"), "Create row is missing"),
    ("suite rows out of order", GOOD.replace("| **Create** |", "| **TEMP** |").replace("| **Prove** |", "| **Create** |").replace("| **TEMP** |", "| **Prove** |"), "Create row is missing"),
    ("missing eval", GOOD.replace("`kmx eval --workspace DIR`", "`kmx run`"), "Prove row is missing"),
    ("partial evidence accepted", GOOD.replace("only when the full policy passes", "after any case"), "Prove row must require a full pass"),
    ("repeat policy missing", GOOD.replace("optional `runs`/`minPasses` (default once)", "unbounded repeats"), "Prove row must describe declared repeats"),
    ("later cases continue after failure", GOOD.replace("stops after a case misses its threshold or any unknown/error", "continues through every case"), "Prove row must preserve fail-fast execution"),
    ("unknown ignored", GOOD.replace(" or any unknown/error", ""), "Prove row must preserve fail-fast execution"),
    ("push implies deployment", GOOD.replace("Neither form deploys", "Then it deploys"), "Publish row must say it does not deploy"),
    ("plain push bypasses evidence", GOOD.replace("Plain `kmx push REF` requires passing evidence; `--unevaluated` explicitly opts out. ", ""), "Publish row must require evidence or explicit opt-out"),
    ("lift builds", GOOD.replace("without building, repacking, or publishing", "after rebuilding and publishing"), "Lift row must stay deploy-only"),
    ("lift plan writes", GOOD.replace("`--plan` is GET-only", "`--plan` applies resources"), "Lift row must preserve the GET-only plan boundary"),
    ("lift runtime binding missing", GOOD.replace(" or kagent `--bindings FILE`", ""), "Lift row must preserve runtime-specific bindings"),
    ("kagent occupied reuse claimed", GOOD.replace("kagent refuses occupied targets", "kagent reuses occupied targets"), "Lift row must preserve kagent create-only placement"),
    ("lift qualification missing", GOOD.replace("qualifies root Lift for Orka exact reuse, bounded absent-pair registry promotion and kagent fresh-target creation", "has tests"), "Lift qualification scope is missing"),
    ("lift qualification broadened", GOOD.replace("it does not prove every promotion, creation or partial-outcome path", "root Lift proves every path"), "Lift qualification limits are missing"),
    ("lift qualification after table", GOOD.replace("Bounded CI qualifies root Lift for Orka exact reuse, bounded absent-pair registry promotion and kagent fresh-target creation; it does not prove every promotion, creation or partial-outcome path.\n", "").replace("## Getting Started: Get Your First Answer", "Bounded CI qualifies root Lift for Orka exact reuse, bounded absent-pair registry promotion and kagent fresh-target creation; it does not prove every promotion, creation or partial-outcome path.\n## Getting Started: Get Your First Answer"), "Lift qualification scope is missing"),
    ("late release note", GOOD.replace(RELEASE_NOTE, "").replace("## Getting Started", RELEASE_NOTE + "## Getting Started"), "v0.8.1 release note must precede"),
    ("missing release note", GOOD.replace(RELEASE_NOTE, ""), "v0.8.1 release note must precede"),
    ("old release note", GOOD.replace("The table below describes v0.8.1's one-member workflow; these docs follow main and may also describe unreleased behavior.", "The table below describes unreleased main; v0.6.0 keeps its released defaults."), "v0.8.1 release note must precede"),
    ("outdated table label", GOOD.replace("| Stage | Current checkout |", "| Stage | Current development build |"), "exact two-column header"),
    ("malformed table separator", GOOD.replace("|---|---|", "|---|"), "separator is missing or malformed"),
    ("extra suite row", GOOD.replace("| **Lift** |", "| **Inspect** | Future status. |\n| **Lift** |"), "exactly four rows"),
    ("extra table column", GOOD.replace("| **Create** | `kmx create NAME --workspace DIR` authors source. |", "| **Create** | `kmx create NAME --workspace DIR` authors source. | extra |"), "exactly two columns"),
    ("removed legacy guide", GOOD.replace("docs/suite-workspace.md", "docs/agent-lift.md"), "removed agent-lift guide"),
    ("Orka suite support broadened", GOOD.replace("packaged workspace path supports evaluation, evaluated publication and root digest Lift with exact evaluated reuse", "owns every suite operation"), "Orka default is missing"),
    ("wrong kagent version", GOOD.replace("v0.10.3 Go", "v1.0 Python"), "kagent version boundary is missing"),
    ("kagent suite evaluation removed", GOOD.replace("`eval --bindings`, evaluated publication and ", ""), "kagent version boundary is missing"),
    ("kagent digest lift removed", GOOD.replace("create-only root digest Lift.", ""), "kagent version boundary is missing"),
    ("kagent status grants execution", GOOD.replace("Read-only destination status observes placement without enabling those execution/lifecycle capabilities.", "Destination status evaluates and repairs agents."), "kagent version boundary is missing"),
    ("kagent retirement claimed", GOOD.replace("RemoteMCP and retirement remain unavailable.", "RemoteMCP and retirement are supported."), "kagent version boundary is missing"),
    ("agentsessions deployment claimed", GOOD.replace("no tools, Lift/chat/status or remote lifecycle", "a KMX deployment target"), "agentsessions boundary is missing"),
    ("agentsessions run freshness missing", GOOD.replace("Session/config/Exec per declared run", "Session/config/Exec per case"), "agentsessions boundary is missing"),
    ("agentsessions judge policy missing", GOOD.replace("explicit judging and repeated cases share Orka/kagent semantics", "judging remains unsupported"), "agentsessions boundary is missing"),
    ("runtime provider distinction removed", GOOD.replace("A **runtime** executes and governs the agent. An **inference provider** is the separate model-access binding; selecting one does not select the other.\n", ""), "runtime/inference distinction is missing"),
    ("wrong tap", GOOD.replace("kaimahi-agents/tap/kmx", "other/tap/kmx"), "Homebrew install is missing"),
    ("moving Go source", GOOD.replace("cmd/kmx@v0.8.1", "cmd/kmx@main"), "pinned Go install is missing"),
    ("old Go release", GOOD.replace("cmd/kmx@v0.8.1", "cmd/kmx@v0.8.0"), "pinned Go install is missing"),
    ("wrong installer", GOOD.replace("kaimahi/main/install.sh", "other/main/install.sh"), "release installer is missing"),
    ("install routes out of order", GOOD.replace("brew install kaimahi-agents/tap/kmx\n# or\ncurl", "curl").replace("install.sh | sh\n# or\ngo install", "install.sh | sh\n# or\nbrew install kaimahi-agents/tap/kmx\n# or\ngo install"), "release installer is missing"),
    ("first answer out of order", GOOD.replace("kmx version\nkmx quickstart", "kmx quickstart\nkmx version"), "kmx quickstart is missing"),
    ("missing workspace path", GOOD.replace("| Workspace | Default `kmx create`, root `eval`, and root `push` operate on editable source. Digest-based root `lift` does not consume workspace source; it uses the packaged artifact and separate private evidence with explicit target and inference inputs. Run/chat still target live Agents. |\n", ""), "workspace/live paths must contain the exact four rows"),
    ("duplicate workspace path", GOOD.replace("| Local environment |", "| Workspace | Duplicate. |\n| Local environment |"), "workspace/live paths must contain the exact four rows"),
    ("workspace runs directly", GOOD.replace("does not consume workspace source", "runs workspace source directly"), "Workspace path boundary is missing"),
    ("environment interface wired", GOOD.replace("is setup-only and is not wired", "is fully wired"), "Local environment path boundary is missing"),
    ("suite-owned invocation incorrectly refused", GOOD.replace("including suite-owned Agents", "Suite-owned Agents refuse legacy invocation"), "Live Agent path boundary is missing"),
    ("live invocation claims suite execution", GOOD.replace("neither is workspace-local execution or suite evaluation", "both execute workspace source and evaluate the suite"), "Live Agent path boundary is missing"),
    ("legacy bundle made callable", GOOD.replace("Removed outright:", "Hidden but callable:"), "Legacy bundle path boundary is missing"),
    ("missing developer guide", GOOD.replace("[developer guide](docs/development.md)\n", ""), "developer guide link is missing"),
    ("missing codebase overview", GOOD.replace("[codebase overview](docs/codebase-overview.md)\n", ""), "codebase overview link is missing"),
    ("missing interface baseline", GOOD.replace("[interface baseline](docs/interface-baseline.md)\n", ""), "interface baseline link is missing"),
    ("missing repository map", GOOD.replace("[repository map](docs/repository-map.md)\n", ""), "repository map link is missing"),
    ("empty document", "", "ketu icon is missing"),
]

failed = 0
for name, text, expected in CASES:
    got = front_door.check(text)
    ok = (got is None) if expected is None else (got is not None and expected in got)
    print(("ok  " if ok else "FAIL") + f" [{name}] -> {got or 'valid'}")
    failed += not ok

with tempfile.TemporaryDirectory() as tmp:
    for name, text, want in [("valid", GOOD, 0), ("invalid", "# Empty README\n", 1)]:
        readme = Path(tmp) / "README.md"
        readme.write_text(text)
        got = subprocess.run([sys.executable, str(CHECKER), str(readme)], capture_output=True, text=True)
        ok = got.returncode == want
        print(("ok  " if ok else "FAIL") + f" [as a script: {name}] -> exit {got.returncode}, want {want}")
        failed += not ok

print(f"check-readme-front-door self-test: {len(CASES) + 2} case(s), {failed} failure(s)")
sys.exit(1 if failed else 0)
