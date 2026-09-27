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
QUICKSTART = """curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh
$HOME/.local/bin/kmx quickstart
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
    ("release installer", "curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh\n"),
    ("installed kmx quickstart", "$HOME/.local/bin/kmx quickstart\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))

for command, label in (("kmx agent create", "kmx agent create"),
                       ("kmx agent lift", "kmx agent lift"),
                       ("$HOME/.local/bin/kmx quickstart", "installed kmx quickstart")):
    CASES.append((f"hyphen-suffixed {command}", GOOD.replace(command + "\n", command + "-old\n"),
                  f"{label} is missing"))

CASES += [
    ("installer from wrong repository", GOOD.replace("kaimahi/main/install.sh", "other/main/install.sh"),
     "release installer is missing"),
    ("unverified direct binary", GOOD.replace(QUICKSTART.splitlines()[0], "curl -fsSL https://example.com/kmx -o kmx"),
     "release installer is missing"),
    ("empty document", "", "ketu icon is missing"),
    ("journey commands only in prose", GOOD.replace("```bash\n" + JOURNEY + "```", JOURNEY),
     "create/prove/lift has no fenced command block"),
    ("quickstart commands only in prose", GOOD.replace("```bash\n" + QUICKSTART + "```", QUICKSTART),
     "Quickstart has no fenced command block"),
    ("empty journey first block", GOOD.replace("```bash\n", "```bash\n```\n```bash\n", 1),
     "kmx agent create is missing"),
    ("empty quickstart first block", GOOD.replace("```bash\n" + QUICKSTART, "```bash\n```\n```bash\n" + QUICKSTART),
     "release installer is missing"),
    ("quickstart commands only in a later section",
     GOOD.replace(QUICKSTART, "kmx version\n").replace("## Status", "```bash\n" + QUICKSTART + "```\n## Status"),
     "release installer is missing"),
    ("journey commands out of order", GOOD.replace("kmx agent create\nkmx agent lift", "kmx agent lift\nkmx agent create"),
     "kmx agent lift is missing"),
    ("quickstart commands out of order", GOOD.replace(QUICKSTART, "$HOME/.local/bin/kmx quickstart\n" + QUICKSTART.splitlines()[0] + "\n"),
     "installed kmx quickstart is missing"),
    ("command inside prose", GOOD.replace("\n$HOME/.local/bin/kmx quickstart\n", "\nRun $HOME/.local/bin/kmx quickstart first.\n"),
     "installed kmx quickstart is missing"),
    ("stray command before section", GOOD.replace("## Quickstart", "kmx quickstart\n## Quickstart"), None),
    ("legacy journey cannot replace KMX quickstart", GOOD.replace("$HOME/.local/bin/kmx quickstart\n", "kmx up\nkmx orka install\n"),
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

print(f"check-readme-front-door self-test: {len(CASES) + 2} case(s), {failed} failure(s)")
sys.exit(1 if failed else 0)
