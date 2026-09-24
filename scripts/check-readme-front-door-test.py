#!/usr/bin/env python3
"""Exercise the KMX front door without coupling tests to the live README."""
import importlib.util
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
QUICKSTART = """go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx quickstart-wizard
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
    ("go install .../cmd/kmx", "go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\n"),
    ("kmx quickstart-wizard", "kmx quickstart-wizard\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))

for command in ("kmx agent create", "kmx agent lift", "kmx quickstart-wizard"):
    CASES.append((f"hyphen-suffixed {command}", GOOD.replace(command + "\n", command + "-old\n"),
                  f"{command} is missing"))

CASES += [
    ("reviewed commit build", GOOD.replace("cmd/kmx@main", "cmd/kmx@572f3a6"), None),
    ("release without current helpers", GOOD.replace("cmd/kmx@main", "cmd/kmx@v0.1.0"),
     "go install .../cmd/kmx is missing"),
    ("latest tag still predates current helpers", GOOD.replace("cmd/kmx@main", "cmd/kmx@latest"),
     "go install .../cmd/kmx is missing"),
    ("missing install revision", GOOD.replace("cmd/kmx@main", "cmd/kmx@"),
     "go install .../cmd/kmx is missing"),
    ("revision prefix is not a revision", GOOD.replace("cmd/kmx@main", "cmd/kmx@main-obsolete"),
     "go install .../cmd/kmx is missing"),
    ("empty document", "", "ketu icon is missing"),
    ("journey commands only in prose", GOOD.replace("```bash\n" + JOURNEY + "```", JOURNEY),
     "create/prove/lift has no fenced command block"),
    ("quickstart commands only in prose", GOOD.replace("```bash\n" + QUICKSTART + "```", QUICKSTART),
     "Quickstart has no fenced command block"),
    ("empty journey first block", GOOD.replace("```bash\n", "```bash\n```\n```bash\n", 1),
     "kmx agent create is missing"),
    ("empty quickstart first block", GOOD.replace("```bash\n" + QUICKSTART, "```bash\n```\n```bash\n" + QUICKSTART),
     "go install .../cmd/kmx is missing"),
    ("quickstart commands only in a later section",
     GOOD.replace(QUICKSTART, "kmx version\n").replace("## Status", "```bash\n" + QUICKSTART + "```\n## Status"),
     "go install .../cmd/kmx is missing"),
    ("journey commands out of order", GOOD.replace("kmx agent create\nkmx agent lift", "kmx agent lift\nkmx agent create"),
     "kmx agent lift is missing"),
    ("quickstart commands out of order", GOOD.replace("go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx quickstart-wizard", "kmx quickstart-wizard\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main"),
     "kmx quickstart-wizard is missing"),
    ("command inside prose", GOOD.replace("\nkmx quickstart-wizard\n", "\nRun kmx quickstart-wizard first.\n"),
     "kmx quickstart-wizard is missing"),
    ("stray command before section", GOOD.replace("## Quickstart", "kmx quickstart-wizard\n## Quickstart"), None),
    ("legacy journey cannot replace KMX quickstart", GOOD.replace("kmx quickstart-wizard\n", "kmx up\nkmx orka install\n"),
     "kmx quickstart-wizard is missing"),
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

print(f"check-readme-front-door self-test: {len(CASES) + 2} case(s), {failed} failure(s)")
sys.exit(1 if failed else 0)
