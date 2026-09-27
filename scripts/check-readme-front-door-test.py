#!/usr/bin/env python3
"""Exercise the KMX front door without coupling tests to the live README."""
import importlib.util
import os
import subprocess
import sys
import tempfile
from pathlib import Path

CHECKER = Path(__file__).with_name("check-readme-front-door.py")
spec = importlib.util.spec_from_file_location("front_door", CHECKER)
front_door = importlib.util.module_from_spec(spec)
spec.loader.exec_module(front_door)

JOURNEY = """kmx agent create
kmx agent lift
"""
QUICKSTART = """(
  installer=$(mktemp) || exit
  trap 'rm -f "$installer"' EXIT
  curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh -o "$installer" || exit
  sh "$installer" --quickstart
)
"""
GO_INSTALL = """GOBIN="$HOME/.local/bin" go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.2.0 && "$HOME/.local/bin/kmx" quickstart
"""
GOOD = """<img src="brand/ketu.svg" alt="Kaimahi ketu mark">
# Kaimahi
**Agent Builder CLI for Kubernetes.**
[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)
## Create, Prove, Lift
```bash
""" + JOURNEY + """```
## Quickstart
```bash
""" + QUICKSTART + """```
```bash
""" + GO_INSTALL + """```
## Runtime Contract
Runtimes execute agents; KMX is the developer experience.
## Migrate Model Traffic
The owner keeps the application's Deployment.
## Status
Current and proposed capabilities are separated.
## Documentation
See the documentation index.
## Development
See the contribution guide.
> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** Interfaces may change.
> [!NOTE]
> KMX is the Agent Builder and lifecycle layer, not a runtime or generic
> governance control plane.
"""

CASES = [("valid KMX front door", GOOD, None)]
# These expectations are independent of the checker's marker lists: deleting
# a marker or emptying a list must make the self-test fail.
for label, literal in [
    ("ketu icon", '<img src="brand/ketu.svg" alt="Kaimahi ketu mark">\n'),
    ("product line", "**Agent Builder CLI for Kubernetes.**\n"),
    ("journey heading", "## Create, Prove, Lift\n"),
    ("Quickstart heading", "## Quickstart\n"),
    ("runtime contract heading", "## Runtime Contract\n"),
    ("migration heading", "## Migrate Model Traffic\n"),
    ("Status heading", "## Status\n"),
    ("documentation heading", "## Documentation\n"),
    ("Development heading", "## Development\n"),
    ("experimental notice", "> [!IMPORTANT]\n> **Kaimahi is experimental and under active development.** Interfaces may change.\n"),
    ("runtime ownership note", "> [!NOTE]\n> KMX is the Agent Builder and lifecycle layer, not a runtime or generic\n> governance control plane.\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))
for label, literal in [
    ("CI badge", "[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)\n"),
    ("release badge", "[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)\n"),
    ("license badge", "[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))
for label, literal in [
    ("kmx agent create", "kmx agent create\n"),
    ("kmx agent lift", "kmx agent lift\n"),
    ("temporary installer", "  installer=$(mktemp) || exit\n"),
    ("installer cleanup", "  trap 'rm -f \"$installer\"' EXIT\n"),
    ("release installer", "  curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh -o \"$installer\" || exit\n"),
    ("installed kmx quickstart", "  sh \"$installer\" --quickstart\n"),
    ("conditional Go quickstart", "GOBIN=\"$HOME/.local/bin\" go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.2.0 && \"$HOME/.local/bin/kmx\" quickstart\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))

for command, label in (("kmx agent create", "kmx agent create"),
                       ("kmx agent lift", "kmx agent lift"),
                       ('  sh "$installer" --quickstart', "installed kmx quickstart")):
    CASES.append((f"hyphen-suffixed {command}", GOOD.replace(command + "\n", command + "-old\n"),
                  f"{label} is missing"))

CASES += [
    ("installer from wrong repository", GOOD.replace("kaimahi/main/install.sh", "other/main/install.sh"),
     "release installer is missing"),
    ("unverified direct binary", GOOD.replace("https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh", "https://example.com/kmx"),
     "release installer is missing"),
    ("download failure is ignored", GOOD.replace(' -o "$installer" || exit', ' -o "$installer"'),
     "release installer is missing"),
    ("subshell wrapper is removed", GOOD.replace("```bash\n(\n", "```bash\n").replace("  sh \"$installer\" --quickstart\n)\n", "  sh \"$installer\" --quickstart\n"),
     "quickstart subshell is missing"),
    ("subshell closure is removed", GOOD.replace("  sh \"$installer\" --quickstart\n)\n", "  sh \"$installer\" --quickstart\n"),
     "quickstart subshell closure is missing"),
    ("old two-command pipeline can run a stale binary", GOOD.replace(QUICKSTART,
     "curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh\n$HOME/.local/bin/kmx quickstart\n"),
     "quickstart subshell is missing"),
    ("old Go release", GOOD.replace("cmd/kmx@v0.2.0", "cmd/kmx@v0.1.0"),
     "conditional Go quickstart is missing"),
    ("moving Go source", GOOD.replace("cmd/kmx@v0.2.0", "cmd/kmx@main"),
     "conditional Go quickstart is missing"),
    ("missing Go alternative", GOOD.replace("```bash\n" + GO_INSTALL + "```\n", ""),
     "pinned Go install is missing"),
    ("Go install failure can run a stale kmx", GOOD.replace(GO_INSTALL,
     "go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.2.0\nkmx quickstart\n"),
     "conditional Go quickstart is missing"),
    ("successful Go install can run an older kmx on PATH", GOOD.replace(GO_INSTALL,
     "go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.2.0 && kmx quickstart\n"),
     "conditional Go quickstart is missing"),
    ("empty document", "", "ketu icon is missing"),
    ("journey commands only in prose", GOOD.replace("```bash\n" + JOURNEY + "```", JOURNEY),
     "create/prove/lift has no fenced command block"),
    ("quickstart commands only in prose", GOOD.replace("```bash\n" + QUICKSTART + "```", QUICKSTART),
     "quickstart subshell is missing"),
    ("empty journey first block", GOOD.replace("```bash\n", "```bash\n```\n```bash\n", 1),
     "kmx agent create is missing"),
    ("empty quickstart first block", GOOD.replace("```bash\n" + QUICKSTART, "```bash\n```\n```bash\n" + QUICKSTART),
     "quickstart subshell is missing"),
    ("quickstart commands only in a later section",
     GOOD.replace(QUICKSTART, "kmx version\n").replace("## Status", "```bash\n" + QUICKSTART + "```\n## Status"),
     "quickstart subshell is missing"),
    ("journey commands out of order", GOOD.replace("kmx agent create\nkmx agent lift", "kmx agent lift\nkmx agent create"),
     "kmx agent lift is missing"),
    ("quickstart commands out of order", GOOD.replace(QUICKSTART,
     '  sh "$installer" --quickstart\n' + QUICKSTART.replace('  sh "$installer" --quickstart\n', '')),
     "installed kmx quickstart is missing"),
    ("command inside prose", GOOD.replace('  sh "$installer" --quickstart\n', '  Run sh "$installer" --quickstart first.\n'),
     "installed kmx quickstart is missing"),
    ("stray command before section", GOOD.replace("## Quickstart", "kmx quickstart\n## Quickstart"), None),
    ("legacy journey cannot replace KMX quickstart", GOOD.replace('  sh "$installer" --quickstart\n', "  kmx up\n"),
     "installed kmx quickstart is missing"),
    ("headings out of order", GOOD.replace("## Status", "## Documentation").replace("## Documentation\nSee", "## Status\nSee"),
     "documentation heading is missing"),
    ("heading mentioned only in prose", GOOD.replace("## Status", "See Status below."),
     "Status heading is missing"),
    ("incidental early heading is not the section", GOOD.replace("# Kaimahi", "## Status\n# Kaimahi"), None),
]

failed = 0
for name, text, expected in CASES:
    result = front_door.check(text)
    ok = (result is None) if expected is None else (result is not None and expected in result)
    print(("ok  " if ok else "FAIL") + f" [{name}] -> {result or 'valid'}")
    failed += not ok

# CLI exit codes are part of the check: importing check() alone cannot prove
# that CI receives a failing verdict.
with tempfile.TemporaryDirectory() as tmp:
    for name, text, want in [("valid", GOOD, 0), ("invalid", "# Empty README\n", 1)]:
        readme = Path(tmp) / "README.md"
        readme.write_text(text)
        got = subprocess.run([sys.executable, str(CHECKER), str(readme)], capture_output=True, text=True)
        ok = got.returncode == want and (want != 0 or "order valid" in got.stdout)
        print(("ok  " if ok else "FAIL") + f" [as a script: {name}] -> exit {got.returncode}, want {want}")
        failed += not ok

# Run the documented block with a failed download and a stale installed binary.
# A command that ignores curl's failure must neither invoke that binary nor
# report successful setup. Only curl is fake; the shell and trap are real.
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    bindir = root / "bin"
    bindir.mkdir()
    curl = bindir / "curl"
    curl.write_text("#!/bin/sh\nexit 22\n")
    curl.chmod(0o755)
    stale = root / ".local/bin/kmx"
    stale.parent.mkdir(parents=True)
    stale.write_text("#!/bin/sh\nprintf 'stale ran\\n' > \"$HOME/stale-called\"\n")
    stale.chmod(0o755)
    tmpdir = root / "tmp"
    tmpdir.mkdir()
    env = dict(os.environ, HOME=tmp, TMPDIR=str(tmpdir), PATH=f"{bindir}:{os.environ['PATH']}")
    got = subprocess.run(["sh", "-c", QUICKSTART], env=env, capture_output=True, text=True)
    ok = got.returncode == 22 and not (root / "stale-called").exists() and not any(tmpdir.iterdir())
    print(("ok  " if ok else "FAIL") + f" [failed download stops and cleans up without running stale kmx] -> exit {got.returncode}")
    failed += not ok

# A failed Go install must not invoke an older kmx earlier on PATH.
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    bindir = root / "bin"
    bindir.mkdir()
    go = bindir / "go"
    go.write_text("#!/bin/sh\nexit 17\n")
    go.chmod(0o755)
    kmx = bindir / "kmx"
    kmx.write_text("#!/bin/sh\nprintf 'stale ran\\n' > \"$HOME/stale-called\"\n")
    kmx.chmod(0o755)
    env = dict(os.environ, HOME=tmp, PATH=f"{bindir}:{os.environ['PATH']}")
    got = subprocess.run(["sh", "-c", GO_INSTALL], env=env, capture_output=True, text=True)
    ok = got.returncode == 17 and not (root / "stale-called").exists()
    print(("ok  " if ok else "FAIL") + f" [failed Go install cannot run stale kmx] -> exit {got.returncode}")
    failed += not ok

    go.write_text("#!/bin/sh\nmkdir -p \"$GOBIN\"\nprintf '#!/bin/sh\\nprintf fresh > \"$HOME/fresh-called\"\\n' > \"$GOBIN/kmx\"\nchmod +x \"$GOBIN/kmx\"\n")
    got = subprocess.run(["sh", "-c", GO_INSTALL], env=env, capture_output=True, text=True)
    ok = got.returncode == 0 and (root / "fresh-called").read_text() == "fresh" and not (root / "stale-called").exists()
    print(("ok  " if ok else "FAIL") + f" [successful Go install runs its own binary, not PATH's stale kmx] -> exit {got.returncode}")
    failed += not ok

print(f"check-readme-front-door self-test: {len(CASES) + 5} case(s), {failed} failure(s)")
sys.exit(1 if failed else 0)
