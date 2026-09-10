#!/usr/bin/env python3
"""Exercise the Orka front door without coupling tests to the live README."""
import importlib.util
import subprocess
import sys
import tempfile
from pathlib import Path

CHECKER = Path(__file__).with_name("check-readme-front-door.py")
spec = importlib.util.spec_from_file_location("front_door", CHECKER)
front_door = importlib.util.module_from_spec(spec)
spec.loader.exec_module(front_door)

COMMANDS = """go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx up
kmx orka install
kmx orka status
"""
GOOD = """<img src="brand/hero.png">
# Kaimahi
## Get agents onto Orka
Orka is the platform; Kaimahi is tooling, not a platform.
## Quickstart
```bash
""" + COMMANDS + """```
## Migrate model traffic
The owner keeps the application's Deployment.
## Status
Authoring is an open decision.
## Documentation
See the documentation index.
"""

CASES = [("valid Orka front door", GOOD, None)]
# These expectations are independent of the checker's marker lists: deleting
# a marker or emptying a list must make the self-test fail.
for label, literal in [
    ("hero image", '<img src="brand/hero.png">\n'),
    ("product line", "## Get agents onto Orka\n"),
    ("Quickstart heading", "## Quickstart\n"),
    ("migration heading", "## Migrate model traffic\n"),
    ("Status heading", "## Status\n"),
    ("documentation heading", "## Documentation\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))
for label, literal in [
    ("go install .../cmd/kmx", "go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\n"),
    ("kmx up", "kmx up\n"),
    ("kmx orka install", "kmx orka install\n"),
    ("kmx orka status", "kmx orka status\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))

CASES += [
    ("empty document", "", "hero image is missing"),
    ("commands only in prose", GOOD.replace("```bash\n" + COMMANDS + "```", COMMANDS),
     "Quickstart has no fenced command block"),
    ("empty first block", GOOD.replace("```bash\n", "```bash\n```\n```bash\n", 1),
     "go install .../cmd/kmx is missing"),
    ("commands only in a later section",
     GOOD.replace(COMMANDS, "kmx version\n").replace("## Status", "```bash\n" + COMMANDS + "```\n## Status"),
     "go install .../cmd/kmx is missing"),
    ("commands out of order", GOOD.replace("kmx orka install\nkmx orka status", "kmx orka status\nkmx orka install"),
     "kmx orka status is missing"),
    ("command inside prose", GOOD.replace("\nkmx up\n", "\nRun kmx up first.\n"),
     "kmx up is missing"),
    ("stray command before section", GOOD.replace("## Quickstart", "kmx orka install\n## Quickstart"), None),
    ("legacy journey cannot replace Orka", GOOD.replace("kmx orka install\nkmx orka status", "kmx plane\nkmx govern hello-world\nkmx ledger"),
     "kmx orka install is missing"),
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
