#!/usr/bin/env python3
"""Break each checker on purpose, and fail if it does not notice.

A checker is a gate that fails OPEN. When a pattern quietly stops matching,
or a list it reads is emptied, or its verdict is computed and then thrown
away, nothing goes red — the tree simply reads clean, and the checker is
still counted as protection. A checker that passes because it checked
nothing is worse than no checker at all.

A self-test built from fixture pairs proves a checker gets today's answer
right. It does not prove the checker would NOTICE tomorrow's wrong answer,
and the difference is not academic: every finding this file exists to
close was found by editing a checker until it was worthless and watching
its own self-test stay green. Ten of thirteen edits to the chat verifier
went unnoticed. Eleven of fifteen to the README front-door checker.
Emptying the delegation checker's list of owned targets made it print "0
targets delegate" and exit 0.

So the contract here is stronger than a fixture pair. For every checker:

    the checker is copied, one named edit is applied to the copy, and the
    checker's own verification command is run against it. It MUST fail.

The edits are declared in scripts/mutations/<checker>.json, next to the
checker rather than in one shared table, so nobody has to touch a common
file to prove a new checker. Each entry names the edit in words, so a
reader can see what property it defends without reverse-engineering a
regex.

The rule this file applies to itself: a checker with no declared mutations
is not proven, and an empty mutation file is a failure, not a pass.

Run:  python3 scripts/check-mutations.py [checker.py ...]
"""
from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
MUTATIONS = SCRIPTS / "mutations"

# Every checker in scripts/ must be proven here. The exemptions are the
# scripts that are THEMSELVES a checker's verification: mutating the thing
# they verify is what proves them, and there is nothing left for a mutation
# of their own to say.
EXEMPT = {
    "check-no-azure-ids-test.sh": "the Azure scanner's verification; mutating check-no-azure-ids.sh proves it",
    "check-readme-front-door-test.py": "the front-door checker's verification; mutating check-readme-front-door.py proves it",
    "kube-guard-test.sh": "the context guard's verification; mutating kube-guard.sh proves it",
    "check-mutations.py": "this file",
}


def checkers():
    """The scripts that claim to be a gate, from the filesystem rather than
    a list here — so a checker added without mutations is a failure, and a
    mutation file deleted with its checker is not a silent gap."""
    found = []
    for p in sorted(SCRIPTS.iterdir()):
        if not p.is_file():
            continue
        # The naming convention, plus the two gates that predate it: the
        # context guard, and the extractor whose refusal is the only thing
        # making "every release has notes" true.
        if (p.name.startswith("check-") or p.name.startswith("verify-")
                or p.name in {"kube-guard.sh", "release-notes.py"}):
            found.append(p.name)
    return found


def mirror(mutated_name: str, mutated_text: str) -> pathlib.Path:
    """A throwaway copy of the repository whose scripts/ is real files.

    Everything outside scripts/ is a symlink, so the copy is cheap and the
    checkers still see a Makefile, a git directory and a docs tree — which
    several of them read. Only the mutated file differs from the tree.
    """
    tmp = pathlib.Path(tempfile.mkdtemp(prefix="kmx-mutation-"))
    for entry in ROOT.iterdir():
        if entry.name == "scripts":
            continue
        (tmp / entry.name).symlink_to(entry)
    (tmp / "scripts").mkdir()
    for entry in SCRIPTS.iterdir():
        (tmp / "scripts" / entry.name).symlink_to(entry)
    target = tmp / "scripts" / mutated_name
    target.unlink()
    target.write_text(mutated_text)
    target.chmod(0o755)
    return tmp


def apply(text: str, find: str, replace: str) -> str:
    """The edit, refusing anything ambiguous.

    A `find` that matches nothing means the checker changed under this
    mutation and the mutation is now proving nothing; a `find` that matches
    twice means the edit is not the one that was described. Both are
    failures here rather than quietly weaker coverage.
    """
    n = text.count(find)
    if n != 1:
        raise LookupError(f"the text to replace appears {n} times, not once: {find!r}")
    return text.replace(find, replace)


def run(cmd, cwd) -> subprocess.CompletedProcess:
    env = dict(os.environ)
    # A checker must not be steered by whatever the developer running this
    # happens to have exported.
    for leak in ("KAIMAHI_CONFIRM", "KUBE_CTX", "KUBE_NS", "KMX_TEST_ARGS"):
        env.pop(leak, None)
    return subprocess.run(cmd, cwd=cwd, env=env, capture_output=True, text=True, timeout=600)


def prove(spec_path: pathlib.Path) -> list[str]:
    """Every mutation in one file. Returns the failures, in words."""
    spec = json.loads(spec_path.read_text())
    checker = spec["checker"]
    source = (SCRIPTS / checker).read_text()
    verify = spec["verify"]
    failures = []

    mutations = spec.get("mutations", [])
    if not mutations:
        return [f"{spec_path.name}: declares no mutations, so it proves nothing about {checker}"]

    # The line the checker prints when it has actually finished its work.
    # A mutant "passes" only if it exits zero AND says this, because a
    # checker whose entry point was removed exits zero having done nothing
    # — the loudest possible failure, and invisible to an exit code alone.
    signature = spec["signature"]

    # Every verification used below has to pass on the UNMUTATED checker
    # first, or "the mutant failed" means nothing: a command that is
    # already red fails for every mutation without noticing any of them,
    # which is a passing condition looser than the claim it makes. That
    # covers the spec's own command AND any per-mutation override, because
    # an override is exactly where an always-red command would sit
    # unnoticed.
    for command in dedupe([verify] + [m["verify"] for m in mutations if "verify" in m]):
        clean = mirror(checker, source)
        try:
            got = run(command, clean)
            if got.returncode != 0:
                failures.append(f"{checker}: `{' '.join(command)}` fails on the UNMUTATED checker "
                                f"(exit {got.returncode}), so no mutation using it proves anything\n"
                                + indent(got.stdout + got.stderr))
                return failures
            if signature not in got.stdout + got.stderr:
                failures.append(f"{checker}: `{' '.join(command)}` never printed {signature!r} on the "
                                "unmutated checker, so that string cannot tell a real run from one "
                                "that did nothing")
                return failures
        finally:
            shutil.rmtree(clean, ignore_errors=True)

    for m in mutations:
        try:
            text = apply(source, m["find"], m["replace"])
        except LookupError as e:
            failures.append(f"{checker} [{m['name']}]: {e}")
            continue
        tmp = mirror(checker, text)
        try:
            got = run(m.get("verify", verify), tmp)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
        ran = signature in got.stdout + got.stderr
        if got.returncode == 0 and ran:
            failures.append(f"{checker} [{m['name']}]: the checker still passed with this edit applied.\n"
                            f"    {m.get('breaks', 'no description')}\n" + indent(got.stdout + got.stderr))
        else:
            how = f"exit {got.returncode}" if got.returncode else "it never ran"
            print(f"ok   {checker} [{m['name']}] — noticed ({how})")
    return failures


def dedupe(commands: list[list[str]]) -> list[list[str]]:
    """The distinct commands, in the order first seen."""
    seen, out = set(), []
    for command in commands:
        key = tuple(command)
        if key not in seen:
            seen.add(key)
            out.append(command)
    return out


def indent(text: str) -> str:
    return "".join("    | " + line + "\n" for line in text.strip().splitlines()[-12:])


def main(argv) -> int:
    if not MUTATIONS.is_dir():
        print(f"check-mutations: {MUTATIONS} does not exist — no checker is proven.", file=sys.stderr)
        return 1
    specs = sorted(MUTATIONS.glob("*.json"))
    if not specs:
        print(f"check-mutations: no mutation files in {MUTATIONS} — refusing to report every checker proven.",
              file=sys.stderr)
        return 1

    failures = []
    declared = {json.loads(p.read_text())["checker"] for p in specs}
    for name in checkers():
        if name in EXEMPT or name in declared:
            continue
        failures.append(f"{name}: is a checker with no scripts/mutations/ file. "
                        "A checker nobody has tried to break is not known to work.")

    if argv:
        specs = [p for p in specs if json.loads(p.read_text())["checker"] in argv]
        if not specs:
            print(f"check-mutations: no mutation file for {argv}", file=sys.stderr)
            return 1

    for spec in specs:
        failures.extend(prove(spec))

    if failures:
        print("\ncheck-mutations: a checker did not notice being broken", file=sys.stderr)
        for f in failures:
            print("  " + f, file=sys.stderr)
        return 1
    total = sum(len(json.loads(p.read_text())["mutations"]) for p in specs)
    print(f"\ncheck-mutations: {len(specs)} checker(s), {total} deliberate breakages, every one noticed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
