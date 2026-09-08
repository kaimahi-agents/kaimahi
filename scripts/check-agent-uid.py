#!/usr/bin/env python3
"""Pin every agent's runAsUser to the uid the kagent image actually ships.

kagent's agent image declares its user by NAME (`python`). Kubernetes
refuses to start a `runAsNonRoot: true` container whose user it cannot
prove is non-root, so every Agent manifest here states the numeric id
instead. The failure when that number is wrong is
`image has non-numeric user (python)` at CreateContainer time, on every
agent at once, with no mention of the image or its version — which is
why a kagent bump that moved the id would be diagnosed as anything but a
kagent bump.

So the number is checked against the image rather than asserted in a
comment. The image is the one the chart at the pinned version resolves
to, with this repository's own values file layered on the chart's
defaults, and the uid comes from running `id -u` inside it.

Two rules, and the second is the one that matters after a bump:

  * every kagent Agent manifest in k8s/ pins a numeric runAsUser, and
  * that number is the image's.

An agent added without the pin fails here rather than at CreateContainer
time on somebody's cluster. So does a run that found no manifests at
all: a checker that passes because it examined nothing is the failure
mode this repository has already paid for once.

Run:  python3 scripts/check-agent-uid.py [--selftest]

The self-test uses fixtures and a fixed uid, so it needs neither a
network nor a container engine; the real run needs both.
"""
from __future__ import annotations

import contextlib
import io
import re
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
CHART = "oci://ghcr.io/kagent-dev/kagent/helm/kagent"
# The chart's own default when `registry` is left empty, and the values
# file this repository installs with does not set one.
VALUES_FILE = ROOT / "k8s" / "kagent-values.yaml"


# ---------------------------------------------------------------- the pin

def read_pin(makefile_text: str, config_text: str) -> str:
    """The kagent version, from both places that state it.

    kmx and the Makefile each carry their own pin. Checking the image at
    one of them while the other installs something else would test a
    version nobody runs, so a disagreement is a failure here rather than a
    coin toss.
    """
    makefile = re.search(r"^KAGENT_VERSION \?= (\S+)$", makefile_text, re.M)
    source = re.search(r'DefaultKagentVersion = "([^"]+)"', config_text)
    if not makefile or not source:
        raise LookupError("cannot find the pinned kagent version in Makefile and internal/kmx/config/config.go")
    if makefile.group(1) != source.group(1):
        raise LookupError(f"Makefile pins kagent {makefile.group(1)} and kmx pins {source.group(1)}; "
                          "they install different images, so neither can be checked")
    return makefile.group(1)


def pinned_version() -> str:
    return read_pin((ROOT / "Makefile").read_text(),
                    (ROOT / "internal" / "kmx" / "config" / "config.go").read_text())


def merge(base, over):
    """A deep merge, values-file over chart defaults, as helm does it."""
    if isinstance(base, dict) and isinstance(over, dict):
        out = dict(base)
        for k, v in over.items():
            out[k] = merge(base.get(k), v)
        return out
    return over


def resolve_image(defaults: dict, overrides: dict, version: str) -> str:
    """The agent image, as helm would resolve it from these two files.

    Read rather than written down: the repository, and the registry it
    comes from, are the chart's to change, and a hardcoded image path here
    would be one more claim nobody checks. The per-image registry and tag
    win over the chart-wide ones, which win over the chart version — the
    chart's own precedence, and the reason this repository's values file
    is layered on before anything is read out.
    """
    values = merge(defaults, overrides or {})
    image = values.get("controller", {}).get("agentImage", {})
    registry = image.get("registry") or values.get("registry")
    repository = image.get("repository")
    tag = image.get("tag") or values.get("tag") or version
    if not registry or not repository:
        raise LookupError("the chart's values name no agent image; the chart's shape changed")
    return f"{registry}/{repository}:{tag}"


def agent_image(version: str) -> str:
    defaults = yaml.safe_load(subprocess.run(
        ["helm", "show", "values", CHART, "--version", version],
        check=True, capture_output=True, text=True).stdout)
    return resolve_image(defaults, yaml.safe_load(VALUES_FILE.read_text()) or {}, version)


def image_uid(image: str) -> int:
    """The numeric uid the image's declared user resolves to.

    `id -u` inside the image is the same resolution the kubelet performs
    when it refuses to start a runAsNonRoot container — the image's own
    /etc/passwd, not a label anyone can write.
    """
    subprocess.run(["docker", "pull", "--quiet", image], check=True,
                   capture_output=True, text=True)
    out = subprocess.run(["docker", "run", "--rm", "--entrypoint", "id", image, "-u"],
                         check=True, capture_output=True, text=True).stdout.strip()
    return int(out)


# ------------------------------------------------------------ the manifests

def agents(root: Path) -> list[tuple[str, dict]]:
    """Every kagent Agent document under k8s/, named by file.

    Enumerating and examining are separate counts on purpose. A skip rule
    that quietly matched everything once made a scanner read zero files
    and report the tree clean, so what was found and what was looked at
    are both reported below.
    """
    found = []
    for path in sorted((root / "k8s").glob("*.yaml")):
        try:
            docs = list(yaml.safe_load_all(path.read_text()))
        except yaml.YAMLError as e:
            raise LookupError(f"{path}: {e}")
        for doc in docs:
            if not isinstance(doc, dict):
                continue
            if doc.get("kind") == "Agent" and str(doc.get("apiVersion", "")).startswith("kagent.dev/"):
                found.append((str(path.relative_to(root)), doc))
    return found


def pinned_uids(spec) -> list:
    """Every runAsUser under a podSecurityContext anywhere in the spec.

    Found by walking rather than by a fixed path: the Agent CRD nests the
    deployment under the agent's TYPE (`declarative.deployment` today),
    and a path written out here would stop finding anything the day a new
    type is used — silently, with the manifests still unpinned. Anything
    called podSecurityContext in an Agent's spec governs an agent pod.
    """
    out = []
    if isinstance(spec, dict):
        for key, value in spec.items():
            if key == "podSecurityContext" and isinstance(value, dict):
                if "runAsUser" in value:
                    out.append(value["runAsUser"])
            else:
                out.extend(pinned_uids(value))
    elif isinstance(spec, list):
        for item in spec:
            out.extend(pinned_uids(item))
    return out


def problems(found: list[tuple[str, dict]], uid: int) -> list[str]:
    """Each manifest against the image's uid, in words."""
    out = []
    for name, doc in found:
        pinned = pinned_uids(doc.get("spec"))
        got = pinned[0] if pinned else None
        if got is None:
            out.append(f"{name}: agent {doc.get('metadata', {}).get('name', '?')} pins no runAsUser. "
                       f"The kagent image declares its user by name, so without the numeric id "
                       f"({uid}) this agent fails at CreateContainer time.")
        else:
            for got in pinned:
                if got != uid:
                    out.append(f"{name}: agent {doc.get('metadata', {}).get('name', '?')} pins runAsUser {got}, "
                               f"but the image runs as {uid}. Change runAsUser to {uid} in this file "
                               f"(every agent manifest in k8s/ carries the same number).")
    return out


def report(found, uid, out=sys.stdout) -> int:
    found_names = sorted({name for name, _ in found})
    if not found:
        print("check-agent-uid: no kagent Agent manifests found in k8s/ — refusing to report clean. "
              "Either the manifests moved or this stopped recognising them; a check that examined "
              "nothing has not checked anything.", file=sys.stderr)
        return 1
    bad = problems(found, uid)
    for line in bad:
        print(line, file=sys.stderr)
    if bad:
        print(f"check-agent-uid: {len(bad)} of {len(found)} agent(s) do not run as the image's uid {uid}",
              file=sys.stderr)
        return 1
    print(f"check-agent-uid: {len(found)} agent(s) in {len(found_names)} manifest(s) pin runAsUser {uid}, "
          f"which is what the image runs as", file=out)
    return 0


# -------------------------------------------------------------- the self-test

def selftest() -> int:
    """Fixture pairs for each rule, and for the empty case.

    Synthetic on purpose: a self-test asserting against this repository's
    own manifests would start passing for the wrong reason the day
    somebody edited one, and it could never exercise the manifest the
    tree does not contain — the agent that forgot the pin.
    """
    failed = 0

    def agent(name, uid, api="kagent.dev/v1alpha2"):
        # Nested under the agent type, as the CRD nests it.
        spec = {"declarative": {"deployment": {"podSecurityContext": {"runAsNonRoot": True}}}}
        if uid is not None:
            spec["declarative"]["deployment"]["podSecurityContext"]["runAsUser"] = uid
        return {"apiVersion": api, "kind": "Agent", "metadata": {"name": name}, "spec": spec}

    def case(what, found, uid, want):
        nonlocal failed
        # The findings are the fixture's, not this run's: printing them
        # would make a passing self-test look like a failing tree.
        quiet = io.StringIO()
        with contextlib.redirect_stderr(quiet):
            got = report(found, uid, out=quiet)
        if got == want:
            print(f"ok   {what}")
        else:
            print(f"FAIL {what} (exit {got}, wanted {want})")
            failed += 1

    case("an agent pinned to the image's uid passes",
         [("k8s/a.yaml", agent("a", 1001))], 1001, 0)
    case("an agent pinned to a different uid fails",
         [("k8s/a.yaml", agent("a", 1001))], 1002, 1)
    case("one wrong agent among several fails",
         [("k8s/a.yaml", agent("a", 7)), ("k8s/b.yaml", agent("b", 8))], 7, 1)
    case("an agent that pins no runAsUser at all fails",
         [("k8s/a.yaml", agent("a", None))], 1001, 1)
    case("no agent manifests at all is a failure, not a clean tree",
         [], 1001, 1)

    # The recogniser itself, over a real file tree: an Agent among other
    # kinds is found, and a document that only looks like one is not. A
    # rule that matched everything would make the pin meaningless, and a
    # rule that matched nothing would empty the check.
    with tempfile.TemporaryDirectory() as tmp:
        k8s = Path(tmp) / "k8s"
        k8s.mkdir()
        (k8s / "mixed.yaml").write_text(yaml.safe_dump_all([
            {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "cm"}},
            agent("real", 1001),
            {"apiVersion": "apps/v1", "kind": "Agent", "metadata": {"name": "impostor"}, "spec": {}},
            "not a mapping",
        ]))
        names = [doc.get("metadata", {}).get("name") for _, doc in agents(Path(tmp))]
        if names == ["real"]:
            print("ok   only kagent Agent documents are examined")
        else:
            print(f"FAIL only kagent Agent documents are examined (found {names})")
            failed += 1

    # The version pin, and the image it resolves to. Neither is exercised
    # by the manifest cases above, and both decide WHICH image the uid is
    # read from — a check reading the right number out of the wrong image
    # passes while proving nothing.
    def pin(what, makefile, config, want):
        nonlocal failed
        try:
            got = read_pin(makefile, config)
        except LookupError:
            got = "refused"
        if got == want:
            print(f"ok   {what}")
        else:
            print(f"FAIL {what} (got {got!r}, wanted {want!r})")
            failed += 1

    agreeing = ('KAGENT_VERSION ?= 9.9.9\n', 'DefaultKagentVersion = "9.9.9"\n')
    pin("two pins that agree give the version", *agreeing, "9.9.9")
    pin("two pins that disagree are refused, not picked between",
        'KAGENT_VERSION ?= 9.9.9\n', 'DefaultKagentVersion = "8.8.8"\n', "refused")
    pin("a pin that has moved out of the Makefile is refused",
        'NOTHING = here\n', 'DefaultKagentVersion = "9.9.9"\n', "refused")

    def image(what, defaults, overrides, want):
        nonlocal failed
        try:
            got = resolve_image(defaults, overrides, "9.9.9")
        except LookupError:
            got = "refused"
        if got == want:
            print(f"ok   {what}")
        else:
            print(f"FAIL {what} (got {got!r}, wanted {want!r})")
            failed += 1

    chart = {"registry": "cr.example", "tag": "",
             "controller": {"agentImage": {"registry": "", "repository": "org/app", "tag": ""}}}
    image("an empty tag means the pinned chart version", chart, {}, "cr.example/org/app:9.9.9")
    image("a values file that moves the registry moves the image tested",
          chart, {"registry": "other.example"}, "other.example/org/app:9.9.9")
    image("a per-image registry beats the chart-wide one",
          chart, {"controller": {"agentImage": {"registry": "per.example"}}}, "per.example/org/app:9.9.9")
    image("a chart naming no agent image is refused rather than guessed",
          {"registry": "cr.example", "controller": {}}, {}, "refused")

    if failed:
        print(f"check-agent-uid self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print("check-agent-uid self-test: every rule proved by a passing and a failing fixture, "
          "the empty case included")
    return 0


def main(argv) -> int:
    if argv[:1] == ["--selftest"]:
        return selftest()
    try:
        version = pinned_version()
        image = agent_image(version)
        uid = image_uid(image)
    except (LookupError, ValueError) as e:
        print(f"check-agent-uid: {e}", file=sys.stderr)
        return 1
    except subprocess.CalledProcessError as e:
        print(f"check-agent-uid: {' '.join(e.cmd)} failed:\n{e.stderr}", file=sys.stderr)
        return 1
    print(f"check-agent-uid: kagent {version} runs agents as {image}")
    return report(agents(ROOT), uid)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
