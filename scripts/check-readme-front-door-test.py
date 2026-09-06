#!/usr/bin/env python3
"""Self-test for check-readme-front-door.py against synthetic READMEs."""
import importlib.util
import sys
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "front_door", Path(__file__).with_name("check-readme-front-door.py")
)
front_door = importlib.util.module_from_spec(spec)
spec.loader.exec_module(front_door)

GOOD = """<img src="brand/hero.png">
# Kaimahi
## Build and govern cloud-native AI agents on Kubernetes.
### Control model spend
### Constrain tool calls
### Approve consequential actions
<img src="docs/assets/architecture.svg">
## Quickstart
```bash
curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart
go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main
kmx up
kmx agent chat hello-world "Who are you?"
kmx plane
kmx govern hello-world
kmx ledger
```
From a clone:
```bash
make up
make chat
```
## Status
| row |
## Proposed CLI direction
npx kaimahi create agent
"""

CASES = [
    ("valid README", GOOD, None),
    # A stray "make up" in the intro must not satisfy the quickstart marker.
    ("duplicate marker before its section", GOOD.replace("# Kaimahi\n", "# Kaimahi\nRun make up first.\n"), None),
    # The install line is the whole point of the kmx path: without it the
    # commands below it are not runnable on a machine that has no clone.
    ("install line missing",
     GOOD.replace("go install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\n", ""),
     "go install .../cmd/kmx is missing"),
    # The one-command install is the front door. Leading with `go install`
    # leads with a prerequisite, which is the number the project is moving.
    ("the one-command install missing",
     GOOD.replace("curl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart\n", ""),
     "the one-command install is missing"),
    ("kmx up missing", GOOD.replace("kmx up\n", ""), "kmx up is missing"),
    # The governed half is the claim; a quickstart that stops at a
    # conversation is an agent runtime, which kagent already ships.
    ("the governed half missing", GOOD.replace("kmx plane\nkmx govern hello-world\nkmx ledger\n", ""),
     "kmx plane is missing"),
    ("govern without the ledger", GOOD.replace("kmx ledger\n", ""), "kmx ledger is missing"),
    # The clone path may move down the section, but it may not vanish: it is
    # what CI runs and what every other doc's commands assume.
    ("clone path deleted",
     GOOD.replace("From a clone:\n```bash\nmake up\nmake chat\n```\n", ""),
     "the clone path (make up, make chat) is missing"),
    ("clone path only in prose",
     GOOD.replace("```bash\nmake up\nmake chat\n```", "make up, then make chat."),
     "the clone path (make up, make chat) is missing"),
    # The kmx block must be FIRST: a clone path above it is the old order.
    ("clone path first",
     GOOD.replace(
         '```bash\ncurl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx up\nkmx agent chat hello-world "Who are you?"\nkmx plane\nkmx govern hello-world\nkmx ledger\n```\nFrom a clone:\n```bash\nmake up\nmake chat\n```',
         '```bash\nmake up\nmake chat\n```\n```bash\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx up\nkmx agent chat hello-world "Who are you?"\nkmx plane\nkmx govern hello-world\nkmx ledger\n```'),
     "the one-command install is missing"),
    ("outcome after the diagram",
     GOOD.replace('### Approve consequential actions\n<img src="docs/assets/architecture.svg">',
                  '<img src="docs/assets/architecture.svg">\n### Approve consequential actions'),
     "architecture diagram is missing or out of order"),
    ("clone command missing", GOOD.replace("make chat\n", ""), "the clone path (make up, make chat) is missing"),
    # Prose that starts a line with the command name is not a runnable path.
    ("kmx path only in Quickstart prose",
     GOOD.replace('```bash\ncurl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx up\nkmx agent chat hello-world "Who are you?"\nkmx plane\nkmx govern hello-world\nkmx ledger\n```\n',
                  "go install the binary, then\nkmx up the cluster.\n"),
     "the one-command install is missing"),
    ("kmx commands in prose, a fenced block without them",
     GOOD.replace('```bash\ncurl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx up\nkmx agent chat hello-world "Who are you?"\nkmx plane\nkmx govern hello-world\nkmx ledger\n```\n',
                  "go install the binary, then\nkmx up the cluster.\n```bash\nkmx status\n```\n"),
     "the one-command install is missing"),
    ("clone path only in a later section's block",
     GOOD.replace("From a clone:\n```bash\nmake up\nmake chat\n```\n", "").replace("| row |\n", "| row |\n```bash\nmake up\nmake chat\n```\n"),
     "the clone path (make up, make chat) is missing"),
    ("heading only mentioned in prose", GOOD.replace("## Status\n", "See the Status section.\n"), "Status heading is missing"),
    ("CLI before the quickstart", GOOD.replace("## Quickstart\n", "npx kaimahi create\n## Quickstart\n"), "proposed CLI appears before"),
    ("CLI between make up and make chat", GOOD.replace("make up\nmake chat", "make up\nnpx kaimahi create\nmake chat"), "proposed CLI appears before"),
    ("CLI between make chat and Status", GOOD.replace("```\n## Status", "```\nnpx kaimahi create\n## Status"), "proposed CLI appears before"),
]

failed = 0
for name, text, expected in CASES:
    result = front_door.check(text)
    ok = (result is None) if expected is None else (result is not None and expected in result)
    print(("ok  " if ok else "FAIL") + f" [{name}] -> {result or 'valid'}")
    failed += not ok
sys.exit(1 if failed else 0)
