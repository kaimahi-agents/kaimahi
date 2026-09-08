#!/usr/bin/env python3
"""Fail when a Makefile recipe re-implements what kmx owns.

There is ONE implementation of the developer journey: kmx implements it, and
the Makefile targets that carry it are thin aliases that call it. That is what
lets CI prove the code a developer actually runs — a target that reimplements
the journey instead of delegating breaks the proof. The failure this guards
against is not a missing alias — that would be obvious — but the slow kind:
someone fixes a wait or adds a flag in the Makefile because that is where they
were looking, and the two implementations drift while both stay green.

WHICH targets those are is not a list somebody keeps up to date. The set is
derived from make's own database: every target on the kind path whose recipe
invokes kmx. `OWNED` below then says what each of them must hand kmx, and the
two must name exactly the same targets. That is what makes deletion loud —
emptying `OWNED`, or dropping one entry, is a failure and not a shorter run,
and a target that starts delegating without anyone listing it is a failure
too.

The check asks make itself rather than reading the file, so it sees the
recipe after every conditional and variable expansion — the same lines a
developer's invocation would run. Neither `make -n` nor `make -qp` runs
anything.

The rule for an owned target on the kind path: the recipe may build kmx,
may fetch the pinned kagent CLI, and must otherwise reach the cluster ONLY
through kmx. A bare kubectl, helm or kind command in one of these recipes is
a second implementation.

The managed path (TARGET=aks) is deliberately NOT checked: kmx does not own
AKS, so those recipes are still the Makefile's own.

Run:  python3 scripts/check-kmx-delegation.py [--selftest]
"""
from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

# Every target kmx owns, with the command it must delegate to.
OWNED = {
    "up": "kmx up",
    "cluster": "kmx up --step cluster",
    "ollama": "kmx up --step ollama",
    "model": "kmx up --step model",
    "kagent": "kmx up --step kagent",
    "agent": "kmx up --step agent",
    "tools-agent": "kmx up --step tools-agent",
    "chat": "kmx agent chat",
    "status": "kmx status",
    "down": "kmx down",
    # The governance half. Same rule, same reason — a wait or a
    # fail-closed check fixed in the recipe rather than in kmx is a fix the
    # clone-free path never gets.
    "plane": "kmx plane --source .",
    "plane-image": "kmx plane --step image --source .",
    "plane-secrets": "kmx plane --step secrets",
    "plane-certificate": "kmx plane --step certificate",
    "govern": "kmx govern",
    "ledger": "kmx ledger",
    "grants": "kmx grants",
    "tool-audit": "kmx audit tool",
    "approval-audit": "kmx audit approval",
    # The verbs an operator reaches for once the plane is up. Same rule,
    # same reason — and `use`, `govern-tools` and
    # `ungovern-tools` in particular, because all three ended in the
    # `wait_switched` macro and a fourth copy of that wait is exactly the
    # drift this check exists to catch.
    "use": "kmx use",
    "use-ollama": "kmx use ollama",
    "budget": "kmx budget",
    "approvals": "kmx approvals",
    "approve": "kmx approve",
    "deny": "kmx deny",
    "request": "kmx request",
    "govern-tools": "kmx tools govern",
    "ungovern-tools": "kmx tools ungovern",
    "tool-allow": "kmx tools allow",
    "tool-allowlist": "kmx tools allowlist",
    "backup": "kmx backup",
    "restore": "kmx restore",
    "plane-metrics": "kmx metrics",
    # Credentials that expire: the view an operator watches, and the one
    # verb that moves a deadline. Renewal mints nothing and moves no bytes.
    "credentials": "kmx credentials",
    "credential-renew": "kmx credential renew",
    # And capturing the credential an upstream needs. This one used to be
    # outside the rule on the grounds that kmx accepted credential material in
    # no form; it accepts it here, at a prompt, on a terminal, and so it owns
    # the target — which matters more than most, because the capture is the
    # step that used to make a checkout compulsory.
    "github-secret": "kmx credential capture github",
    "release-secret": "kmx credential capture github-release",
    "ado-secret": "kmx credential capture ado",
}

# Targets whose recipe passes an operator-settable argument (a credential, an
# agent, a question). For these the delegation is a PREFIX match; every other
# target must match its argument list exactly, which is what stops "up" from
# being satisfied by "up --step cluster".
CARRIES_ARGUMENTS = {
    "chat", "govern", "ledger", "tool-audit",
    # The operator verbs take a preset, a credential, caps, a request id, a
    # file, a tool list. Each is empty in this dry run and non-empty in real
    # use, so the exact form is checked by the empty case and the prefix
    # covers the rest.
    "use", "budget", "approve", "deny", "request",
    "govern-tools", "tool-allow", "tool-allowlist", "backup", "restore",
    # POD= names one replica; without it the recipe expands to a bare
    # `kmx metrics`, which the exact-match arm below still pins.
    "plane-metrics",
    # NAME= and TTL= are empty in the dry run; the prefix covers the rest.
    "credential-renew",
    # GITHUB_REPO= and ADO_ORG= name the repository or organization the
    # credential is scoped to. Empty here, named in real use.
    "github-secret", "release-secret", "ado-secret",
}

# A line that reaches the cluster itself. Anchored to a command position —
# the start of a line, or after a shell operator — so that `KIND_CLUSTER=x`
# in an environment prefix and `--kube-context` in an argument do not match.
CLUSTER_TOOL = re.compile(r"(?:^|[;&|(]\s*|^\s*)(kubectl|helm|kind)\s", re.M)

# There is deliberately no allow-list of "safe" lines.
#
# There was one, and it was the bug: an exemption that matched the START of a
# line (`curl …`, `go build …`) exempted the WHOLE line, so
# `curl … && kubectl apply -f extra.yaml` passed. Every legitimate line in
# these recipes — the kmx build, the pinned kagent fetch — runs no cluster
# tool at all, so the rule needs no exceptions: a line that puts kubectl, helm
# or kind in command position is a re-implementation, whatever else it does.
# The same mistake in the other direction (exempting a line because it
# mentions kmx) is covered by the self-test below.

# The kmx invocation inside a recipe line, and everything it was asked to do.
KMX_CALL = re.compile(r"\bbin/kmx\s+(?P<args>.*)$")

# $(KMX) in COMMAND position in an unexpanded recipe line: at the start of the
# line (after make's `@`, `-` and `+` prefixes and any environment prefix such
# as `$(KMX_ENV)` or `FOO=bar`), or after a shell operator. Command position is
# the whole point — `build`'s recipe is `@echo "kmx ready: $(abspath $(KMX))"`
# and the link rule's is `go build -o $(KMX) ./cmd/kmx`, and neither of those
# runs the journey. A plain "does the recipe mention $(KMX)" would count both.
_ENV_PREFIX = r"(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+|\$\([A-Za-z_]+\)\s+)*"
KMX_INVOCATION = re.compile(r"(?:^|[;&|(]\s*)[-@+]*\s*" + _ENV_PREFIX + r"\$\(KMX\)\s+[^)\s]")


def invokes_kmx(recipe: str) -> bool:
    """Does this recipe RUN kmx, as opposed to merely naming the binary?"""
    for line in recipe.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if KMX_INVOCATION.search(line):
            return True
    return False


def parse_recipes(database: str) -> dict:
    """Each target's recipe, as make itself records it in `make -qp` output.

    Question mode prints the database and runs nothing, and the recipes come
    back UNEXPANDED — `$(KMX)` rather than `bin/kmx` — which is what lets the
    derivation below tell a real invocation from a mention. Comment and blank
    lines separate database entries but do not end a recipe (make interleaves
    them), so only a new target line does.
    """
    db = {}
    target, body = "", []
    for line in database.splitlines():
        if line.startswith("\t"):
            if target:
                body.append(line)
        elif line.startswith("#") or not line.strip():
            continue
        else:
            if target:
                db[target] = "\n".join(body)
            target, body = "", []
            name, sep, _ = line.partition(":")
            # A variable assignment, a multi-target rule and a pattern rule
            # are not what this is after.
            if sep and name and not any(c in name for c in " =$%"):
                target = name
    if target:
        db[target] = "\n".join(body)
    return db


def delegating_targets() -> set:
    """Every kind-path target whose recipe invokes kmx, from make's database.

    This is the floor: the tree itself says which targets delegate, so the
    hand-written OWNED table below cannot be emptied, trimmed or left behind
    without this check going red.
    """
    result = subprocess.run(
        ["make", "-qp", "TARGET=kind"],
        capture_output=True,
        text=True,
        cwd=Path(__file__).resolve().parents[1],
    )
    # -q exits non-zero when a target is out of date; the database is still
    # printed, so the exit status is not the signal here. An empty database is.
    if not result.stdout.strip():
        raise SystemExit(f"make -qp printed no database:\n{result.stderr}")
    return {t for t, recipe in parse_recipes(result.stdout).items() if invokes_kmx(recipe)}


def floor_problems(derived: set, owned: set) -> list:
    """Where the derived set and the OWNED table disagree, in words.

    A derivation that finds nothing is a broken derivation, not a clean tree:
    the Makefile has delegating targets, so an empty floor means the parse or
    the pattern stopped working and every comparison below it is vacuous.
    """
    if not derived:
        return ["no target in the Makefile invokes kmx at all — "
                "that is a broken derivation, not a clean tree"]
    problems = []
    for target in sorted(derived - owned):
        problems.append(f"{target}: its recipe calls kmx but OWNED does not say what it must call — "
                        "add it, so the command it delegates is checked too")
    for target in sorted(owned - derived):
        problems.append(f"{target}: OWNED says kmx owns it, but its recipe no longer calls kmx")
    return problems


def kmx_invocations(recipe: str) -> list[str]:
    """The argument list of every `bin/kmx …` call in a recipe."""
    calls = []
    for line in recipe.splitlines():
        if line.lstrip().startswith("#"):
            continue
        match = KMX_CALL.search(line)
        if match:
            calls.append(match.group("args").strip())
    return calls


def offending_lines(recipe: str) -> list[str]:
    """Lines in an owned recipe that reach the cluster without kmx.

    Nothing exempts a line from this: not mentioning kmx (`$(KMX) up --step
    ollama && kubectl apply -f extra.yaml` delegates and re-implements at the
    same time), and not starting with something harmless (`curl … && kubectl
    apply -f extra.yaml`). Both are the shape that would drift, and both
    slipped past earlier versions of this check.
    """
    bad = []
    for line in recipe.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if CLUSTER_TOOL.search(line):
            bad.append(line.strip())
    return bad


def dry_run(target: str) -> str:
    result = subprocess.run(
        ["make", "-n", target, "TARGET=kind"],
        capture_output=True,
        text=True,
        cwd=Path(__file__).resolve().parents[1],
    )
    if result.returncode != 0:
        raise SystemExit(f"make -n {target} failed:\n{result.stdout}{result.stderr}")
    return result.stdout


def delegates(target: str, expected: str, recipe: str) -> bool:
    """Does the recipe hand kmx exactly the work this target owns?

    Exact match on the argument list, not a substring of the whole dry run:
    `make -n up` also prints its prerequisites' recipes, and "up" is a prefix
    of "up --step cluster" — so a substring search would report that `up`
    delegates even if the `up` target lost its recipe entirely and only its
    steps still called kmx.
    """
    wanted = expected.removeprefix("kmx ")
    for args in kmx_invocations(recipe):
        if args == wanted:
            return True
        # Some recipes carry an operator-settable argument — the agent and
        # the question for `chat`, the credential for `govern` and the
        # reads. Those match on the prefix; everything else is exact.
        if target in CARRIES_ARGUMENTS and args.startswith(wanted + " "):
            return True
    return False


def check() -> int:
    # The tree first: which targets delegate is derived, and OWNED only says
    # what each of them must delegate. If the two disagree there is nothing
    # useful to say about the recipes yet, so that is reported on its own.
    derived = delegating_targets()
    problems = floor_problems(derived, set(OWNED))
    if problems:
        print("kmx delegation:", *problems, sep="\n  ", file=sys.stderr)
        print("\nThe list of owned targets and the Makefile have to agree.", file=sys.stderr)
        return 1

    # Driven by the derived set rather than by OWNED, so the table cannot
    # shrink the work: every delegating target is looked up here, and one
    # that is missing is an error rather than a target quietly not checked.
    for target in sorted(derived):
        expected = OWNED[target]
        recipe = dry_run(target)
        if not delegates(target, expected, recipe):
            problems.append(f"{target}: does not delegate — expected `{expected}`")
        for line in offending_lines(recipe):
            problems.append(f"{target}: reaches the cluster without kmx: {line}")
    if problems:
        print("kmx delegation:", *problems, sep="\n  ", file=sys.stderr)
        print("\nThese targets are kmx's. Change cmd/kmx and internal/kmx, not the recipe.", file=sys.stderr)
        return 1
    print(f"kmx delegation: {len(derived)} targets delegate, all of them listed, "
          "none re-implement the journey")
    return 0


SELFTEST = [
    ("a delegating recipe", "KIND_CLUSTER='x' bin/kmx up --step agent", []),
    ("the kmx build", "go build -o bin/kmx ./cmd/kmx", []),
    ("the kagent fetch", "curl -sSfLo bin/kagent https://example/kagent-linux-amd64", []),
    ("a comment", "# kubectl apply -f k8s/ollama.yaml", []),
    ("a re-implemented apply", "kubectl --context kind-x apply -f k8s/ollama.yaml",
     ["kubectl --context kind-x apply -f k8s/ollama.yaml"]),
    ("a re-implemented helm install", "\thelm upgrade --install kagent oci://...",
     ["helm upgrade --install kagent oci://..."]),
    ("a re-implemented cluster create", "kind create cluster --name x",
     ["kind create cluster --name x"]),
    # An environment prefix is not a command: KIND_CLUSTER= must not match.
    ("an environment prefix", "KIND_CLUSTER='x' KUBE_CTX='kind-x' bin/kmx down", []),
    # A kubectl hidden behind a shell operator is still a kubectl.
    ("a chained kubectl", "true; kubectl -n kagent get pods", ["true; kubectl -n kagent get pods"]),
    # …including one chained after a genuine delegation. Mentioning kmx must
    # not exempt the rest of the line.
    ("a kubectl chained after kmx", "bin/kmx up --step ollama && kubectl apply -f k8s/extra.yaml",
     ["bin/kmx up --step ollama && kubectl apply -f k8s/extra.yaml"]),
    # Nothing is exempt, so a line that merely mentions kagent is still
    # checked — naming the file after something legitimate is not a licence
    # to reach the cluster on the same line.
    ("a kubectl on a file named after kagent-dev", "kubectl apply -f kagent-dev-values.yaml",
     ["kubectl apply -f kagent-dev-values.yaml"]),
    # …and one chained after a line that starts harmlessly. A leading `curl`
    # or `go build` used to exempt the whole line.
    ("a kubectl chained after curl", "curl -sSfLo bin/kagent https://example/x && kubectl apply -f extra.yaml",
     ["curl -sSfLo bin/kagent https://example/x && kubectl apply -f extra.yaml"]),
    ("a helm chained after go build", "go build -o bin/kmx ./cmd/kmx; helm upgrade --install x oci://y",
     ["go build -o bin/kmx ./cmd/kmx; helm upgrade --install x oci://y"]),
]

# `up` is a prefix of `up --step cluster`, so a substring search would say the
# `up` target delegates even when its recipe is gone and only its steps call
# kmx. These pin the exact-match rule.
DELEGATION_SELFTEST = [
    ("exact match", "up", "kmx up", "KIND_CLUSTER='x' bin/kmx up", True),
    ("only the steps delegate, `up` has no recipe of its own", "up", "kmx up",
     "bin/kmx up --step cluster\nbin/kmx up --step ollama", False),
    ("no recipe at all", "up", "kmx up", "", False),
    ("a step", "agent", "kmx up --step agent", "KIND_CLUSTER='x' bin/kmx up --step agent", True),
    ("chat carries the agent and the question", "chat", "kmx agent chat",
     'bin/kmx agent chat hello-world "Who are you?"', True),
    ("chat with no arguments is not the chat recipe", "chat", "kmx agent chat", "bin/kmx agent", False),
    # The governance half: the plane's steps are exact, the
    # credential-carrying reads are prefixes.
    ("the plane, built from the checkout", "plane", "kmx plane --source .",
     "KIND_CLUSTER='x' bin/kmx plane --source .", True),
    ("the plane without the checkout is not the recipe CI runs", "plane", "kmx plane --source .",
     "bin/kmx plane", False),
    ("a plane step", "plane-secrets", "kmx plane --step secrets", "bin/kmx plane --step secrets", True),
    ("govern carries the credential", "govern", "kmx govern",
     "bin/kmx govern hello-world --agent hello-world --preset governed-ollama", True),
    ("govern with no credential is not the recipe", "govern", "kmx govern", "bin/kmx", False),
    ("an audit read names its trail", "tool-audit", "kmx audit tool",
     "bin/kmx audit tool hello-tools", True),
    ("the approval trail is not the tool trail", "tool-audit", "kmx audit tool",
     "bin/kmx audit approval", False),
    # The operator verbs: the noun-grouped tool verbs must not satisfy each
    # other.
    ("the governed-tools switch", "govern-tools", "kmx tools govern",
     "bin/kmx tools govern --credential hello-tools --tools \"k8s_get_resources\"", True),
    ("ungovern is not govern", "govern-tools", "kmx tools govern", "bin/kmx tools ungovern", False),
    ("the allowlist read is not the allowlist write", "tool-allowlist", "kmx tools allowlist",
     "bin/kmx tools allow \"k8s_get_resources\" --credential hello-tools", False),
    ("`use` with no PRESET is still the use recipe", "use", "kmx use", "bin/kmx use", True),
    ("`use ollama` is not the generic use recipe", "use-ollama", "kmx use ollama",
     "bin/kmx use ollama", True),
    ("...and the generic one does not satisfy use-ollama", "use-ollama", "kmx use ollama",
     "bin/kmx use", False),
    ("restore is not backup", "restore", "kmx restore", "bin/kmx backup", False),
    ("metrics with no POD", "plane-metrics", "kmx metrics", "bin/kmx metrics", True),
    ("metrics for one replica", "plane-metrics", "kmx metrics",
     "bin/kmx metrics --pod kaimahi-proxy-1", True),
    ("...but not some other command that starts the same way", "plane-metrics", "kmx metrics",
     "bin/kmx metricsx", False),
]


# The derivation that makes the OWNED table a claim rather than the whole
# truth. These run on fixture text, so they say what the parse and the
# command-position rule mean without needing a Makefile.
FLOOR_SELFTEST = [
    ("a recipe that runs kmx", "\t@$(KMX_ENV) $(KMX) up", True),
    ("...with an environment prefix instead", "\tKIND_CLUSTER=x $(KMX) status", True),
    ("...and one hidden behind a shell operator", "\ttrue; $(KMX) down", True),
    # `build` names the binary to print its path; the link rule names it as an
    # output. Counting either would put a target in the floor that nobody can
    # write a delegation for.
    ("the binary's path, echoed", '\t@echo "kmx ready: $(abspath $(KMX))"', False),
    ("the binary, built", "\tgo build -o $(KMX) ./cmd/kmx", False),
    ("the managed path, which is the Makefile's own", "\t@KUBECTL=\"$(KUBECTL)\" bash scripts/plane-deploy.sh", False),
    ("a commented-out invocation", "\t# $(KMX) up", False),
    ("no recipe at all", "", False),
]

# `make -qp` output, in miniature: a delegating target, a target that only
# names the binary, a variable, and a rule with no recipe.
FLOOR_DATABASE = """\
# Make data base

KMX ?= bin/kmx

up: bin/kmx
#  Phony target (prerequisite of .PHONY).
#  recipe to execute (from 'Makefile', line 468):
\t@$(KMX_ENV) $(KMX) up

build: bin/kmx
\t@echo "kmx ready: $(abspath $(KMX))"

lint:
"""

FLOOR_DERIVATION = [
    ("a database with one delegating target", FLOOR_DATABASE, {"up"}),
    ("a database with none", "# Make data base\n\nlint:\n\tgolangci-lint run\n", set()),
]

# What the floor is FOR: the disagreements it has to report. An empty derived
# set is the important one — that is the checker having nothing to check.
FLOOR_COMPARISON = [
    ("the table matches the tree", {"up", "down"}, {"up", "down"}, 0),
    ("the table was emptied", {"up", "down"}, set(), 2),
    ("one entry was deleted", {"up", "down"}, {"up"}, 1),
    ("a new delegating target nobody listed", {"up", "down"}, {"up", "down", "chat"}, 1),
    ("the derivation found nothing", set(), {"up", "down"}, 1),
    ("...even when the table is empty too", set(), set(), 1),
]


def selftest() -> int:
    failed = 0
    for name, recipe, want in FLOOR_SELFTEST:
        got = invokes_kmx(recipe)
        if got != want:
            failed += 1
            print(f"FAIL [{name}] -> invokes_kmx={got}, want {want}")
        else:
            print(f"ok   [{name}]")
    for name, database, want in FLOOR_DERIVATION:
        got = {t for t, recipe in parse_recipes(database).items() if invokes_kmx(recipe)}
        if got != want:
            failed += 1
            print(f"FAIL [{name}] -> {sorted(got)}, want {sorted(want)}")
        else:
            print(f"ok   [{name}]")
    for name, derived, owned, want in FLOOR_COMPARISON:
        got = floor_problems(derived, owned)
        if len(got) != want:
            failed += 1
            print(f"FAIL [{name}] -> {len(got)} problem(s), want {want}: {got}")
        else:
            print(f"ok   [{name}]")
    for name, line, expected in SELFTEST:
        got = offending_lines(line)
        if got != expected:
            failed += 1
            print(f"FAIL [{name}] -> {got}, want {expected}")
        else:
            print(f"ok   [{name}]")
    for name, target, expected_command, recipe, want in DELEGATION_SELFTEST:
        got = delegates(target, expected_command, recipe)
        if got != want:
            failed += 1
            print(f"FAIL [{name}] -> delegates={got}, want {want}")
        else:
            print(f"ok   [{name}]")
    if failed:
        print(f"kmx delegation self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print("kmx delegation self-test: all cases passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(selftest() if "--selftest" in sys.argv else check())
