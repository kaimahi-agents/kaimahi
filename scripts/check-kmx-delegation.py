#!/usr/bin/env python3
"""Fail when a Makefile recipe re-implements what kmx owns.

There is ONE implementation of the developer journey: kmx implements it, and
the Makefile's `up`, `cluster`, `ollama`, `model`, `kagent`, `agent`,
`tools-agent`, `chat`, `status` and `down` are thin aliases that call it. That
is what lets CI prove the code a developer actually runs — a target that
reimplements the journey instead of delegating breaks the proof. The failure
this guards against is not a missing alias — that would be obvious — but the
slow kind: someone fixes a wait or adds a flag in the Makefile because that is
where they were looking, and the two implementations drift while both stay
green.

The check asks make itself rather than reading the file, so it sees the
recipe after every conditional and variable expansion — the same lines a
developer's invocation would run. `make -n` runs nothing.

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
    problems = []
    for target, expected in OWNED.items():
        recipe = dry_run(target)
        if not delegates(target, expected, recipe):
            problems.append(f"{target}: does not delegate — expected `{expected}`")
        for line in offending_lines(recipe):
            problems.append(f"{target}: reaches the cluster without kmx: {line}")
    if problems:
        print("kmx delegation:", *problems, sep="\n  ", file=sys.stderr)
        print("\nThese targets are kmx's. Change cmd/kmx and internal/kmx, not the recipe.", file=sys.stderr)
        return 1
    print(f"kmx delegation: {len(OWNED)} targets delegate, none re-implement the journey")
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
    # The kagent-fetch exemption is anchored to the release URL, so a line
    # that merely mentions kagent-dev is still checked.
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


def selftest() -> int:
    failed = 0
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
