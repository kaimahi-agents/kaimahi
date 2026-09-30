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
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.0
"""
JOURNEY = """`kmx quickstart` proves an answer.
`kmx up` provisions the runtime.
`kmx quickstart-wizard` creates your own Agent.
`kmx agent create` writes a bundle.
`kmx agent lift` deploys it.
`kmx agent status` inspects it.
`kmx agent evaluate` tests its deployed revision.
See the [bundle lift guide](docs/agent-lift.md).
"""
GOOD = """<img src="brand/ketu.svg" alt="Kaimahi ketu mark">
**Agent Builder CLI for Kubernetes.**
[![CI](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/kaimahi-agents/kaimahi/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/kaimahi-agents/kaimahi)](https://github.com/kaimahi-agents/kaimahi/releases)
[![License](https://img.shields.io/github/license/kaimahi-agents/kaimahi)](LICENSE)
## Create, Prove, Lift
```bash
""" + INSTALL + """```
```bash
kmx quickstart
```
""" + JOURNEY + """## Quickstart
The wizard offers an interactive path.
## Runtime Contract
Runtimes own execution.
## Migrate Model Traffic
Migration is separate.
## Status
Limitations.
## Documentation
Links.
## Development
Build.
> [!IMPORTANT]
> **Kaimahi is experimental and under active development.** Interfaces may change.
> [!NOTE]
> KMX is the Agent Builder and lifecycle layer, not a runtime or generic
> governance control plane.
"""

CASES = [("valid front door", GOOD, None)]
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
    ("CI badge", "actions/workflows/ci.yml/badge.svg?branch=main"),
    ("release badge", "img.shields.io/github/v/release/kaimahi-agents/kaimahi"),
    ("license badge", "img.shields.io/github/license/kaimahi-agents/kaimahi"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))
for label, literal in [
    ("Homebrew install", "brew install kaimahi-agents/tap/kmx\n"),
    ("release installer", "curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh\n"),
    ("pinned Go install", "go install github.com/kaimahi-agents/kaimahi/cmd/kmx@v0.4.0\n"),
    ("kmx quickstart", "kmx quickstart\n"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))
for label, literal in [
    ("kmx up", "`kmx up`"),
    ("kmx quickstart-wizard", "`kmx quickstart-wizard`"),
    ("kmx agent create", "`kmx agent create`"),
    ("kmx agent lift", "`kmx agent lift`"),
    ("kmx agent status", "`kmx agent status`"),
    ("kmx agent evaluate", "`kmx agent evaluate`"),
]:
    CASES.append((f"missing {label}", GOOD.replace(literal, ""), f"{label} is missing"))
CASES += [
    ("wrong tap", GOOD.replace("kaimahi-agents/tap/kmx", "other/tap/kmx"), "Homebrew install is missing"),
    ("moving Go source", GOOD.replace("cmd/kmx@v0.4.0", "cmd/kmx@main"), "pinned Go install is missing"),
    ("old Go release", GOOD.replace("cmd/kmx@v0.4.0", "cmd/kmx@v0.3.0"), "pinned Go install is missing"),
    ("wrong installer", GOOD.replace("kaimahi/main/install.sh", "other/main/install.sh"), "release installer is missing"),
    ("quickstart after suffix", GOOD.replace("kmx quickstart\n", "kmx quickstart-old\n"), "kmx quickstart is missing"),
    ("legacy Go quickstart chaining", GOOD.replace("cmd/kmx@v0.4.0\n", "cmd/kmx@v0.4.0 && kmx quickstart\n"), "pinned Go install is missing"),
    ("missing lift guide", GOOD.replace("See the [bundle lift guide](docs/agent-lift.md).\n", ""), "bundle lift guide is missing"),
    ("journey missing first block", GOOD.replace("```bash\n" + INSTALL + "```\n", ""), "Homebrew install is missing"),
    ("journey missing second block", GOOD.replace("```bash\nkmx quickstart\n```\n", ""), "kmx quickstart is missing"),
    ("journey commands in prose only", GOOD.replace("```bash\nkmx quickstart\n```\n", "kmx quickstart\n"), "kmx quickstart is missing"),
    ("bundle commands out of order", GOOD.replace("`kmx agent create` writes a bundle.\n`kmx agent lift` deploys it.", "`kmx agent lift` deploys it.\n`kmx agent create` writes a bundle."), "kmx agent lift is missing"),
    ("headings out of order", GOOD.replace("## Status\nLimitations.\n## Documentation\nLinks.", "## Documentation\nLinks.\n## Status\nLimitations."), "documentation heading is missing"),
    ("install routes out of order", GOOD.replace("brew install kaimahi-agents/tap/kmx\n# or\ncurl", "curl").replace("install.sh | sh\n# or\ngo install", "install.sh | sh\n# or\nbrew install kaimahi-agents/tap/kmx\n# or\ngo install"), "release installer is missing"),
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
