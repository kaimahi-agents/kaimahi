#!/usr/bin/env python3
"""Self-test for check-readme-front-door.py against synthetic READMEs."""
import importlib.util
import subprocess
import sys
import tempfile
from pathlib import Path

CHECKER = Path(__file__).with_name("check-readme-front-door.py")
spec = importlib.util.spec_from_file_location("front_door", CHECKER)
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
## Status
| row |
## Proposed CLI direction
npx kaimahi create agent
"""

CASES = [
    ("valid README", GOOD, None),
    # Every marker the checker claims to enforce needs a case that FAILS
    # without it. A marker with no failing case is a marker the checker
    # could quietly stop enforcing: the regex could be loosened to match
    # anything and this file would still print all-ok.
    ("hero image missing", GOOD.replace('<img src="brand/hero.png">\n', ""),
     "hero image is missing"),
    # The one-line product claim is the first thing a reader reads, and the
    # thing most likely to be reworded into something else by accident.
    ("product line missing",
     GOOD.replace("## Build and govern cloud-native AI agents on Kubernetes.\n", ""),
     "product line is missing"),
    # The three outcomes are the promise the rest of the page has to keep;
    # each one is enforced separately, so each one is tested separately.
    ("outcome 'control model spend' missing",
     GOOD.replace("### Control model spend\n", ""),
     "outcome: control model spend is missing"),
    ("outcome 'constrain tool calls' missing",
     GOOD.replace("### Constrain tool calls\n", ""),
     "outcome: constrain tool calls is missing"),
    ("outcome 'approve consequential actions' missing",
     GOOD.replace("### Approve consequential actions\n", ""),
     "outcome: approve consequential actions is missing"),
    # A stray command mention in the intro must not satisfy the quickstart marker.
    ("duplicate marker before its section", GOOD.replace("# Kaimahi\n", "# Kaimahi\nRun kmx up first.\n"), None),
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
    # Talking to the agent is the payoff of the whole install sequence; a
    # quickstart that installs and never converses is a setup guide.
    ("kmx agent chat missing",
     GOOD.replace('kmx agent chat hello-world "Who are you?"\n', ""),
     "kmx agent chat is missing"),
    # `kmx govern` on its own: the block can keep the plane and the ledger
    # and still lose the command that actually puts a seam under governance.
    ("kmx govern missing", GOOD.replace("kmx govern hello-world\n", ""),
     "kmx govern is missing"),
    # ORDER, with nothing missing. Every command is still in the block, two
    # of them swapped — the sequence is the instruction, and a reader who
    # chats before the cluster is up gets an error, not an agent.
    ("quickstart commands shuffled",
     GOOD.replace('kmx up\nkmx agent chat hello-world "Who are you?"\n',
                  'kmx agent chat hello-world "Who are you?"\nkmx up\n'),
     "kmx agent chat is missing"),
    # A command has to START a line to be a command someone can paste. Mid
    # line it is prose about a command, however plausible the sentence.
    ("command mid-line rather than starting one",
     GOOD.replace("\nkmx up\n", "\n$ kmx up\n"),
     "kmx up is missing"),
    # The governed half is the claim; a quickstart that stops at a
    # conversation is an agent runtime, which kagent already ships.
    ("the governed half missing", GOOD.replace("kmx plane\nkmx govern hello-world\nkmx ledger\n", ""),
     "kmx plane is missing"),
    ("govern without the ledger", GOOD.replace("kmx ledger\n", ""), "kmx ledger is missing"),
    ("outcome after the diagram",
     GOOD.replace('### Approve consequential actions\n<img src="docs/assets/architecture.svg">',
                  '<img src="docs/assets/architecture.svg">\n### Approve consequential actions'),
     "architecture diagram is missing or out of order"),
    # Prose that starts a line with the command name is not a runnable path.
    ("kmx path only in Quickstart prose",
     GOOD.replace('```bash\ncurl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx up\nkmx agent chat hello-world "Who are you?"\nkmx plane\nkmx govern hello-world\nkmx ledger\n```\n',
                  "go install the binary, then\nkmx up the cluster.\n"),
      "Quickstart has no fenced command block"),
    ("kmx commands in prose, a fenced block without them",
     GOOD.replace('```bash\ncurl -fsSL https://raw.githubusercontent.com/kaimahi-agents/kaimahi/main/install.sh | sh -s -- --quickstart\ngo install github.com/kaimahi-agents/kaimahi/cmd/kmx@main\nkmx up\nkmx agent chat hello-world "Who are you?"\nkmx plane\nkmx govern hello-world\nkmx ledger\n```\n',
                  "go install the binary, then\nkmx up the cluster.\n```bash\nkmx status\n```\n"),
     "the one-command install is missing"),
    ("heading only mentioned in prose", GOOD.replace("## Status\n", "See the Status section.\n"), "Status heading is missing"),
    ("CLI before the quickstart", GOOD.replace("## Quickstart\n", "npx kaimahi create\n## Quickstart\n"), "proposed CLI appears before"),
    ("CLI between quickstart and Status", GOOD.replace("```\n## Status", "```\nnpx kaimahi create\n## Status"), "proposed CLI appears before"),
]

failed = 0
for name, text, expected in CASES:
    result = front_door.check(text)
    ok = (result is None) if expected is None else (result is not None and expected in result)
    print(("ok  " if ok else "FAIL") + f" [{name}] -> {result or 'valid'}")
    failed += not ok

# The cases above call check() directly, which proves the rules and nothing
# about the script. A checker whose entry point stops reporting its verdict
# passes every one of them while always exiting 0 from the command line, and
# the command line is how CI runs it. So run it as a script, both ways
# round: a good README must exit 0, and a broken one must exit non-zero.
SCRIPT_CASES = [("a valid README", GOOD, 0), ("a README with no hero image",
                                              GOOD.replace('<img src="brand/hero.png">\n', ""), 1)]
with tempfile.TemporaryDirectory() as tmp:
    for name, text, want in SCRIPT_CASES:
        readme = Path(tmp) / "README.md"
        readme.write_text(text)
        got = subprocess.run([sys.executable, str(CHECKER), str(readme)],
                             capture_output=True, text=True)
        ok = got.returncode == want
        if want == 0:
            ok = ok and "order valid" in got.stdout
        print(("ok  " if ok else "FAIL")
              + f" [as a script: {name}] -> exit {got.returncode}, want {want}")
        failed += not ok

print(f"check-readme-front-door self-test: {len(CASES) + len(SCRIPT_CASES)} case(s), "
      f"{failed} failure(s)")
sys.exit(1 if failed else 0)
