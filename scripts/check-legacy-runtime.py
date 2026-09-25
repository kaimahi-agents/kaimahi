#!/usr/bin/env python3
"""Refuse to carry support for the retired legacy agent runtime.

kmx installed a v0.x third-party agent runtime once. It does not any more:
the installer, its steps, its manifests, its chart, its CLI pin, its model
presets and its conversion spike are all gone. What is left over from a
removal like that is not code — it is CLAIMS. A comment that still explains
a namespace nothing writes to, a probe whose default targets a namespace
nothing creates, a manifest rule admitting traffic from a namespace that is
never created, a doc row linking a runtime somebody could try to install.
Each of those reads as support, and the cost of a false claim of support is
paid by a reader, months later, who believes it.

So this is a CLAIM scanner, not a grep for a word, and it has two rules
because there are two different ways a claim survives.

  HARD IDENTIFIERS are things that could only ever mean the v0.x runtime:
  its API group and kinds, its images and charts, its version pin, its three
  CLI spellings. None of them has an innocent reading. They are refused in
  EVERY tracked file, including the workflows — a workflow that names one is
  either installing it or claiming something about it.

  THE BARE NAME is refused from the surfaces a reader takes as current:
  product code, configuration, documentation, scripts, manifests AND the
  workflows. The name in a sentence can be honest ("that runtime is gone")
  or a claim ("point it at the runtime's namespace"), and no regex tells
  those apart. So each surviving line is named, one at a time, with a
  category and a reason, in scripts/legacy-runtime-allowlist.json.

WHAT MAY STAY, AND NOTHING ELSE. The allowlist has four categories, they
are the whole policy, and each one is bound to the paths where its claim
could be true — a category is an argument for why a line may stay, and the
same argument is not available everywhere:

  historical    the record of what used to be. Two shapes: the files that
                ARE the record — the changelog and the dated reviews —
                named as whole files, and the coordination board, whose
                past-tense rows are named one line at a time because the
                board also carries live policy. No current documentation
                directory may be listed as a whole file, and a floor below
                fails if one is.
  retirement    the exact production lines that refuse the retired runtime
                BY NAME, plus the historical teardown sentinel. A refusal
                has to spell what it refuses, or it refuses nothing. Only
                in the four production files that do the refusing.
  negative      exact lines asserting the runtime is ABSENT: the CI
                tripwires and the bounded test cases. A negative assertion
                is the opposite of support — but only a test or the CI
                tripwires can make one, so a document cannot borrow the
                category to keep an instruction.
  future        exact lines noting an explicitly UNSUPPORTED future: a v1
                authoring surface that is an open question. A note that
                something is not supported is not support — and the line
                or its reason has to SAY so.

HOW IT FAILS, AND WHY THAT MATTERS MORE THAN HOW IT PASSES. A scanner is a
gate that fails OPEN. Every way this one could quietly stop working is a way
the tree reads clean with the residue still in it, so each is refused
explicitly rather than trusted:

  - an enumeration that produced no files is not a clean tree;
  - files enumerated and none read is not a clean tree;
  - fewer files than the tree is known to have is a shrunken scan;
  - an allowlist entry matching nothing is stale, and a stale allowlist is
    how an exemption outlives the line it was written for;
  - a rule whose pattern stops matching its own example is gone;
  - a rule whose pattern also matches its counterexample is too wide to
    mean anything;
  - an exemption that names a category its path cannot support is an
    argument borrowed from somewhere it was earned;
  - a whole file declared historical outside the record itself is a
    directory-sized exemption wearing a filename.

Run:  python3 scripts/check-legacy-runtime.py
      python3 scripts/check-legacy-runtime.py --selftest
"""
from __future__ import annotations

import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
ALLOWLIST = ROOT / "scripts" / "legacy-runtime-allowlist.json"

# This file and its allowlist have to spell what they forbid. Nothing else
# is exempt — not even this checker's own mutation specification, which
# quotes lines of source from here and is written so that none of the lines
# it quotes carries a banned literal. The floor below pins the set to
# exactly these two, so a third name cannot be added in silence.
SELF = {
    "scripts/check-legacy-runtime.py",
    "scripts/legacy-runtime-allowlist.json",
}

# Binary and image files: nothing text-shaped to claim anything.
SKIP_SUFFIX = {".png", ".jpg", ".jpeg", ".gif", ".pdf", ".ico", ".woff", ".woff2", ".svg"}

# The surfaces a reader takes as current. The bare name is refused on all of
# them, workflows included: a workflow runs commands, and `kubectl create
# namespace <name>` is as much a claim of support as a sentence in a guide.
# The assertions that a cluster does NOT have the runtime are still allowed,
# but one line at a time and by name, like every other exemption.
CURRENT_SURFACES = (
    "cmd/", "internal/", "plane/", "k8s/", "scripts/", "docs/", ".github/",
    "Makefile", "install.sh", "embed.go", "go.mod", "CONTRIBUTING.md", "README.md",
)

# A tree this size cannot shrink to a handful of files without something
# being wrong with the enumeration rather than with the tree.
MIN_TRACKED = 200
MIN_READ = 150

# The files that ARE the record, whole. Nothing else may be declared
# history wholesale, and a floor below fails on any other name: a whole
# file is the widest exemption in this policy, so the set is closed here
# rather than in the allowlist it judges.
HISTORICAL_FILES = ("CHANGELOG.md", "docs/reviews/")

# The production files whose job is to REFUSE the retired runtime by name.
# A retirement exemption anywhere else is a mention arguing it is a refusal.
RETIREMENT_FILES = (
    "internal/kmx/app/chat.go", "internal/kmx/app/lift.go",
    "internal/kmx/lift/plan.go", "internal/kmx/lift/record.go",
)

# The one current document whose past-tense rows are recorded line by line.
# The board carries live policy beside its history, so it cannot be a whole
# historical file — the dated rows are named, the live text is not exempt.
HISTORICAL_LINE_FILES = ("docs/COORDINATION.md",)

# A `future` entry has to SAY the future is unsupported. Without this the
# category is a free-form note, and a free-form note beside a mention is
# indistinguishable from documentation of a supported path.
UNSUPPORTED = ("unsupported", "not supported", "no support", "remains open", "open question")

CATEGORIES = ("retirement", "negative", "historical", "future")


def current_doc(path: str) -> bool:
    """True for documentation a reader is expected to act on today."""
    return path.endswith(".md") and (path.startswith("docs/")
                                     or path in {"README.md", "CONTRIBUTING.md"})


def category_problem(entry) -> str | None:
    """Why this entry's category cannot be true of this entry's path.

    Every category is an ARGUMENT for keeping a line, and an argument is
    only available where it could hold. A test may assert an absence; a
    guide may not call an instruction an assertion. Production code may
    refuse the runtime by name, but only the four files that do. Without
    this, the widest category in the list is one word away from any line in
    the tree, and the reason column stops being reviewable.
    """
    path, category = entry["path"], entry["category"]
    if category == "negative":
        if not (path.endswith("_test.go") or path == ".github/workflows/ci.yml"):
            return ("only a Go test or the CI tripwires in .github/workflows/ci.yml can assert "
                    "the runtime is ABSENT; on any other surface the same words are an instruction")
    elif category == "retirement":
        if path not in RETIREMENT_FILES:
            return ("a retirement refusal is production code that refuses the runtime BY NAME, "
                    f"and only these do so: {', '.join(RETIREMENT_FILES)}")
    elif category == "historical":
        if path not in HISTORICAL_LINE_FILES:
            return ("a past-tense line may only be recorded in "
                    f"{', '.join(HISTORICAL_LINE_FILES)} — the whole-file record is "
                    f"{', '.join(HISTORICAL_FILES)}, and everything else is current")
    elif category == "future":
        if not current_doc(path):
            return "a note about an unsupported future belongs in current documentation"
        said = (entry["text"] + " " + entry["why"]).lower()
        if not any(marker in said for marker in UNSUPPORTED):
            return ("neither the line nor its reason says the future is unsupported "
                    f"(looked for: {', '.join(UNSUPPORTED)})")
    return None


class Rule:
    """One way the retired runtime survives, with the two samples that keep
    it honest: what it must catch, and what it must not."""

    def __init__(self, name, what, regexp, example, counterexample, everywhere):
        self.name = name
        self.what = what
        self.re = re.compile(regexp)
        self.example = example
        self.counterexample = counterexample
        # True: refused in every tracked file. False: refused only on the
        # surfaces a reader takes as current.
        self.everywhere = everywhere


# The runtime's own name, assembled rather than written, so this file does
# not become the one place the tree carries it as a literal.
NAME = "k" + "agent"

RULES = [
    Rule("api-group", "the retired runtime's API group",
         NAME + r"\.dev", NAME + ".dev/v1alpha2", "core.orka.ai/v1alpha1", True),
    Rule("api-kind", "a v0.x API kind",
         r"\b(?:ModelConfig|RemoteMCPServer)s?\b|\b(?:modelconfig|remotemcpserver)s?\b",
         "kind: ModelConfig", "kind: Provider", True),
    Rule("schema-version", "the retired runtime's schema version",
         r"\bv1alpha2\b", "apiVersion: " + NAME + ".dev/v1alpha2",
         "apiVersion: core.orka.ai/v1alpha3", True),
    Rule("image", "a published image or chart of the retired runtime",
         NAME + r"-dev\b", "ghcr.io/" + NAME + "-dev/" + NAME + "/tools:0.2.1",
         "ghcr.io/kaimahi-agents/kaimahi-proxy:p10", True),
    Rule("pin", "the retired runtime's version pin",
         r"\b" + NAME.upper() + r"_VERSION\b", NAME.upper() + "_VERSION ?= 0.9.12",
         "ORKA_VERSION ?= v0.1.3", True),
    Rule("asset", "a named asset of the retired runtime",
         NAME + r"-(?:tools?|tool-server|values|shim|cli)\b|\b" + NAME + r"cli\b",
         NAME + "-tool-server", "orka-agent-harness-wrapper", True),
    Rule("cli-surface", "a command spelling that selects the retired runtime",
         r"--(?:step|payload|runtime)(?:=|\s+)" + NAME + r"\b",
         "kmx up --step " + NAME, "kmx up --step orka", True),
    Rule("retired-preset-command", "a retired preset-switch command",
         r"\b(?:kmx|make)\s+use\b",
         "make use PRESET=example", "kmx models credential copilot", True),
    # A boundary on the LEFT only, and both halves of that are deliberate.
    #
    # A trailing `\b` missed `NAME_usage_metadata` in a migration comment:
    # an underscore is a word character, so there was no boundary after the
    # name, and the rule read a line naming the retired runtime's own
    # telemetry field as clean. Dropping the boundary entirely goes too far
    # the other way — it fires on `pickAgent`, an Orka chat helper that
    # merely ENDS in the letters. A word ending in the name is not the name;
    # a word starting with it is.
    #
    # The second alternative is the CamelCase spelling, case-SENSITIVE and
    # scoped so the insensitive flag above cannot leak onto it. Without it
    # the historical teardown sentinel `PayloadKagent` and the tests named
    # after it would be invisible here — present in the tree, accounted for
    # nowhere. They are allowed, but they are allowed BY NAME.
    Rule("bare-name", "the retired runtime named on a current surface",
         r"(?i:\b" + NAME + r")|" + NAME.capitalize(),
         "the " + NAME + "_usage_metadata field",
         "return b.pickAgent(ctx)  // orka-system", False),
]


def load_allowlist(path=ALLOWLIST):
    """The named exemptions, or a refusal.

    An allowlist that failed to load is not an empty allowlist: the scan
    would go red on every line it names and somebody would delete the gate
    rather than the residue. An allowlist that IS empty is equally a
    failure — this tree has a historical record, and a policy claiming
    otherwise has stopped describing anything.
    """
    try:
        doc = json.loads(path.read_text())
    except (OSError, ValueError) as e:
        sys.exit(f"check-legacy-runtime: cannot read the allowlist {path}: {e}")
    files = doc.get("historical_files") or []
    lines = doc.get("lines") or []
    if not files and not lines:
        sys.exit(f"check-legacy-runtime: {path} exempts nothing at all — "
                 "refusing to report a clean tree against a policy that describes no tree.")
    for entry in lines:
        for field in ("path", "text", "category", "why"):
            if not str(entry.get(field, "")).strip():
                sys.exit(f"check-legacy-runtime: an allowlist entry is missing {field}: {entry}")
        if entry["category"] not in CATEGORIES:
            sys.exit(f"check-legacy-runtime: unknown category {entry['category']!r} "
                     f"for {entry['path']} — allowed: {', '.join(CATEGORIES)}")
        wrong = category_problem(entry)
        if wrong:
            sys.exit(f"check-legacy-runtime: {entry['path']} may not be exempted as "
                     f"{entry['category']!r}: {wrong}\n      {entry['text'].strip()!r}")
    return files, lines


def historical_covers(path: str, files: list[str]) -> bool:
    """True when this path IS the historical record.

    A directory entry has to end in a slash, so `docs/reviews/` covers the
    dated reviews and `docs/` would have to be written as such to cover the
    current documentation — which the floor below then refuses by name.
    """
    for entry in files:
        if entry.endswith("/"):
            if path.startswith(entry):
                return True
        elif path == entry:
            return True
    return False


def candidates(root=ROOT) -> list[pathlib.Path]:
    """Where the real repository might be, nearest first.

    Usually it is the directory this script sits in. It is NOT when
    scripts/check-mutations.py is running: that harness executes a copy of
    this file from a throwaway directory whose entries are symlinks to the
    tree, and git will not work there. The tree under judgement is the real
    one on the other end of those links, so it is tried too — or the
    enumeration comes back empty, the floors (correctly) call the scan
    broken, and every mutation then fails for that reason instead of for the
    one it was written to prove.
    """
    out = [root]
    for probe in ("docs", "internal", "cmd", "plane"):
        p = root / probe
        if p.exists():
            real = p.resolve().parent
            if real not in out:
                out.append(real)
    return out


def repo_root(root=ROOT) -> pathlib.Path:
    """The directory the tracked paths below are relative to."""
    for candidate in candidates(root):
        got = subprocess.run(["git", "rev-parse", "--show-toplevel"],
                             cwd=candidate, capture_output=True)
        if got.returncode == 0:
            return candidate
    return root


def tracked(root=None) -> list[str]:
    """Every tracked file, from git rather than from a walk: the claim is
    about what this repository PUBLISHES, and a walk would also read a
    developer's untracked scratch.

    A git that refuses is a refusal here too. An enumeration that failed is
    not an empty tree, and reading it as one is the exact fail-open shape
    this whole file exists to prevent.
    """
    for candidate in ([root] if root else candidates()):
        got = subprocess.run(["git", "ls-files", "-z"], cwd=candidate, capture_output=True)
        if got.returncode == 0 and got.stdout:
            return sorted(p for p in got.stdout.decode().split("\0") if p)
    sys.exit("check-legacy-runtime: cannot list the tracked files "
             f"(tried {[str(c) for c in ([root] if root else candidates())]}) — "
             "refusing to read a failed enumeration as an empty tree.")


def judge(paths, historical, lines, root=None):
    """(findings, files read, allowlist entries that matched).

    A finding is (path, line number, rule, the line). The matched-entry set
    comes back because an exemption nothing matches is stale, and a stale
    exemption is how a rule stops applying to a file nobody is looking at.
    """
    exact = {}
    root = root or repo_root()
    for entry in lines:
        exact.setdefault(entry["path"], {}).setdefault(entry["text"].strip(), []).append(entry)

    findings, read, used = [], 0, set()
    for path in paths:
        if path in SELF or pathlib.Path(path).suffix.lower() in SKIP_SUFFIX:
            continue
        p = root / path
        try:
            text = p.read_text()
        except UnicodeDecodeError:
            continue  # binary: nothing to claim
        except OSError as e:
            findings.append((path, 0, None, f"could not be read (not scanned): {e}"))
            continue
        read += 1
        record = historical_covers(path, historical)
        current = path.startswith(CURRENT_SURFACES)
        for n, line in enumerate(text.splitlines(), 1):
            stripped = line.strip()
            for rule in RULES:
                if not rule.re.search(line):
                    continue
                if not rule.everywhere and not current:
                    continue
                # The historical record may say what used to be true.
                if record:
                    continue
                hit = exact.get(path, {}).get(stripped)
                if hit:
                    for entry in hit:
                        used.add(id(entry))
                    continue
                findings.append((path, n, rule, stripped))
    return findings, read, used


def report(findings, lines, used, paths, read):
    """The verdict, and every way of reaching it that is not a verdict."""
    problems = []

    # An exemption that matches nothing outlives the line it was written
    # for, and the next person to add residue to that file inherits it.
    for entry in lines:
        if id(entry) not in used:
            problems.append(f"{entry['path']}: the allowlist still exempts a line that is no longer "
                            f"there (or no longer matches a rule):\n      {entry['text'].strip()!r}\n"
                            f"      Remove the entry: an exemption nobody needs is one nobody reviews.")

    for path, n, rule, line in findings:
        what = rule.what if rule else line
        where = f"{path}:{n}" if n else path
        problems.append(f"{where}: {what}\n      {line[:160]}")

    if problems:
        print("check-legacy-runtime: the retired runtime is still supported here", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        print(f"\n{len(problems)} place(s). Remove the claim, or — if the line refuses the runtime, "
              "asserts its absence, or notes an explicitly unsupported future — name it in "
              f"{ALLOWLIST.relative_to(ROOT)} with a category and a reason.", file=sys.stderr)
        return 1
    print(f"check-legacy-runtime: {len(paths)} tracked file(s), {read} read, {len(RULES)} rules, "
          f"{len(lines)} named exemption(s) — no support for the retired runtime")
    return 0


def floors(paths, read, historical) -> list[str]:
    """The ways a clean report can mean nothing at all."""
    bad = []
    if not paths:
        bad.append("no tracked files were enumerated — a clean tree cannot be read off an empty list")
    elif len(paths) < MIN_TRACKED:
        bad.append(f"only {len(paths)} tracked file(s) enumerated, fewer than the {MIN_TRACKED} "
                   "this tree is known to have — the enumeration shrank, not the residue")
    if paths and not read:
        bad.append(f"{len(paths)} file(s) enumerated and none read — enumerated is not examined")
    elif read and read < MIN_READ:
        bad.append(f"only {read} file(s) were read, fewer than the {MIN_READ} floor — "
                   "something is skipping the tree")
    if set(SELF) != {"scripts/check-legacy-runtime.py",
                     "scripts/legacy-runtime-allowlist.json"}:
        bad.append(f"the self-exemption set is no longer this checker and its allowlist: {sorted(SELF)}")
    # The one that stops a whole current directory — or a whole current
    # file, which is the same edit one name smaller — being declared
    # history. A whitelist rather than a list of files to protect: the
    # record is three known places, and everything else in the tree is
    # current until somebody argues otherwise line by line.
    for entry in historical:
        if entry not in HISTORICAL_FILES:
            bad.append(f"{entry!r} is declared historical as a WHOLE FILE. Only "
                       f"{', '.join(HISTORICAL_FILES)} are the record itself; anything else "
                       "that still carries past-tense evidence is named line by line, "
                       "with a category and a reason.")
    return bad


def main(argv, allowlist=None):
    if argv[:1] == ["--selftest"]:
        return selftest()
    historical, lines = load_allowlist(allowlist or ALLOWLIST)
    paths = tracked()
    findings, read, used = judge(paths, historical, lines)
    bad = floors(paths, read, historical)
    if bad:
        print("check-legacy-runtime: this scan proves nothing", file=sys.stderr)
        for b in bad:
            print("  " + b, file=sys.stderr)
        return 1
    return report(findings, lines, used, paths, read)


# --------------------------------------------------------------------------
# The self-test breaks the OTHER side: it plants residue in a fixture tree
# and requires each rule to catch it, and it removes the ground under each
# floor and requires the floor to notice. scripts/check-mutations.py breaks
# THIS file; between the two, a rule is proven from both directions.
# --------------------------------------------------------------------------

def selftest():
    failed = 0
    historical, lines = load_allowlist()

    def case(ok, good, bad_msg):
        nonlocal failed
        if ok:
            print("ok   " + good)
        else:
            print("FAIL " + bad_msg)
            failed += 1

    with tempfile.TemporaryDirectory() as tmp:
        d = pathlib.Path(tmp)

        # Every rule catches its own example, in a real file, read through
        # the same judge() the tree gets — not as a regex in isolation.
        for rule in RULES:
            where = "docs/planted.md" if not rule.everywhere else ".github/workflows/planted.yml"
            f = d / where
            f.parent.mkdir(parents=True, exist_ok=True)
            f.write_text(f"ordinary prose\n{rule.example}\nmore prose\n")
            got = {r.name for _, _, r, _ in judge([where], [], [], root=d)[0] if r}
            case(rule.name in got, f"{rule.name}: its example is caught in a scanned file",
                 f"{rule.name}: its own example was not caught (found: {sorted(got) or 'nothing'})")

            f.write_text(f"{rule.counterexample}\n")
            near = {r.name for _, _, r, _ in judge([where], [], [], root=d)[0] if r}
            case(rule.name not in near, f"{rule.name}: does not fire on {rule.counterexample!r}",
                 f"{rule.name}: also fires on {rule.counterexample!r}, so it pins nothing")
            f.unlink()

        # A word that merely ENDS in the name is not the name. This is
        # `pickAgent`, an Orka helper a boundary-free pattern refused.
        (d / "internal" / "kmx" / "app").mkdir(parents=True, exist_ok=True)
        ending = "internal/kmx/app/chat.go"
        (d / ending).write_text("func (b *orkaChatBackend) pickAgent(ctx context.Context) {\n")
        case(not judge([ending], [], [], root=d)[0],
             "a word ending in the name is not refused",
             "an unrelated identifier ending in the letters was refused as the runtime")
        # ...while a word STARTING with it is, underscore or not.
        (d / ending).write_text(f"    -- ({NAME}_usage_metadata), on outcome rows\n")
        case(judge([ending], [], [], root=d)[0],
             "a word starting with the name is refused, underscore or not",
             "the name followed by an underscore was read as clean")

        # The bare name is refused on a current surface AND in a workflow:
        # a workflow that creates the runtime's namespace is installing it,
        # whatever the hard identifiers say.
        (d / "docs").mkdir(exist_ok=True)
        (d / ".github" / "workflows").mkdir(parents=True, exist_ok=True)
        (d / "docs" / "guide.md").write_text(f"install the {NAME} runtime\n")
        (d / ".github" / "workflows" / "ci.yml").write_text(f"          kubectl create namespace {NAME}\n")
        doc_hits = {r.name for _, _, r, _ in judge(["docs/guide.md"], [], [], root=d)[0] if r}
        wf_hits = {r.name for _, _, r, _ in judge([".github/workflows/ci.yml"], [], [], root=d)[0] if r}
        case("bare-name" in doc_hits, "the bare name on a current surface is refused",
             "the bare name in a document was not refused")
        case("bare-name" in wf_hits, "a workflow creating the runtime's namespace is refused",
             "a workflow line creating the retired runtime's namespace passed "
             f"(found: {sorted(wf_hits) or 'nothing'})")
        # ...and a HARD identifier in that same workflow is refused by its
        # own rule rather than by the surface, which is what keeps "hard
        # identifiers are refused everywhere" a rule and not a sentence.
        (d / ".github" / "workflows" / "ci.yml").write_text(f"          helm install oci://ghcr.io/{NAME}-dev/x\n")
        wf_hard = {r.name for _, _, r, _ in judge([".github/workflows/ci.yml"], [], [], root=d)[0] if r}
        case("image" in wf_hard, "a hard identifier in a workflow is still refused",
             f"a workflow naming a published legacy image passed (found: {sorted(wf_hard) or 'nothing'})")

        # An exemption is an EXACT line. A substring or prefix match would
        # let the next edit to that line inherit the exemption.
        planted = "docs/exact.md"
        (d / "docs" / "exact.md").write_text(
            f"the {NAME} runtime is gone\nthe {NAME} runtime is gone, so install it from here\n")
        entry = {"path": planted, "text": f"the {NAME} runtime is gone",
                 "category": "future", "why": "fixture: not supported"}
        found, _, used = judge([planted], [], [entry], root=d)
        case(len(found) == 1 and found[0][1] == 2,
             "an exemption matches one exact line and not the line that extends it",
             f"an exact exemption leaked onto a longer line (caught {len(found)})")
        case(id(entry) in used, "a matched exemption is recorded as used",
             "a matched exemption was not recorded, so staleness cannot be judged")

        # A stale exemption is a failure, not a pass.
        (d / "docs" / "exact.md").write_text("nothing here at all\n")
        found, _, used = judge([planted], [], [entry], root=d)
        case(report(found, [entry], used, [planted], 1) == 1,
             "an exemption that matches nothing is reported as stale",
             "a stale exemption was accepted, so an exemption can outlive its line")

        # The historical record may say what used to be true; a current
        # document under the same rules may not. The planted line carries a
        # HARD identifier, so this tests the exemption rather than the
        # surface rule that would not reach a root file anyway.
        (d / "CHANGELOG.md").write_text(f"- the {NAME}.dev Agents and ModelConfigs went with the installer\n")
        found, _, _ = judge(["CHANGELOG.md"], ["CHANGELOG.md"], [], root=d)
        case(not found, "the historical record may carry past-tense evidence",
             f"the historical record was refused ({[f[2].name for f in found if f[2]]}), "
             "which would make the policy unusable")
        found, _, _ = judge(["CHANGELOG.md"], [], [], root=d)
        case(found, "the historical record is only exempt because it is NAMED",
             "an unnamed file was treated as history")

        # ...and a directory exemption needs its slash to cover a
        # directory, so a file entry cannot spread onto every path it
        # happens to prefix.
        case(historical_covers("docs/reviews/2026-09-09-x.md", ["docs/reviews/"])
             and not historical_covers("docs/reviews-plan.md", ["docs/reviews/"]),
             "a directory exemption covers its directory and nothing beside it",
             "a directory exemption leaked onto a sibling path")
        case(historical_covers("CHANGELOG.md", ["CHANGELOG.md"])
             and not historical_covers("CHANGELOG.md.bak", ["CHANGELOG.md"])
             and not historical_covers("docs/README.md", ["docs/R"]),
             "a file exemption covers that file and nothing it is a prefix of",
             "a file exemption spread onto every path beginning with its name")

        # Unreadable is not clean: a permissions problem must be reported,
        # never allowed to quietly shrink the scanned set.
        locked = d / "docs" / "locked.md"
        locked.write_text("nothing to see\n")
        locked.chmod(0o000)
        try:
            found, read_count, _ = judge(["docs/locked.md"], [], [], root=d)
            if os.geteuid() == 0:
                print("ok   (skipped) running as root, where nothing is unreadable")
            else:
                case(found and read_count == 0, "an unreadable file is reported, not skipped",
                     "an unreadable file was skipped in silence")
        finally:
            locked.chmod(0o600)

        # The self-exemption is this checker and its allowlist. A third
        # name there is a file that may carry anything.
        global SELF
        original = SELF
        try:
            SELF = original | {"docs/kmx.md"}
            case(floors(["a"] * MIN_TRACKED, MIN_READ, historical),
                 "a grown self-exemption set is refused",
                 "a third file could exempt itself from the checker that names it")
        finally:
            SELF = original

        # The floors. Each is the ground under a clean report.
        case(floors([], 0, historical), "an empty enumeration is refused",
             "an empty enumeration was called clean")
        case(floors(["a"] * MIN_TRACKED, 0, historical), "files enumerated and none read is refused",
             "a scan that read nothing was called clean")
        case(floors(["a"] * (MIN_TRACKED - 1), MIN_READ, historical),
             "a shrunken enumeration is refused",
             "an enumeration below the floor was called clean")
        case(floors(["a"] * MIN_TRACKED, MIN_READ - 1, historical),
             "a shrunken read count is refused",
             "a read count below the floor was called clean")
        case(not floors(["a"] * MIN_TRACKED, MIN_READ, historical),
             "a full enumeration over a sound allowlist clears the floors",
             "the floors refuse a sound scan, so they cannot distinguish anything")
        for widened in ("docs/", "internal/", "Makefile", "docs/COORDINATION.md",
                        "docs/kmx.md", "docs/README.md", "internal/kmx/app/up.go"):
            case(floors(["a"] * MIN_TRACKED, MIN_READ, [widened]),
                 f"declaring {widened!r} historical is refused",
                 f"{widened!r} could be declared historical, exempting current work wholesale")
        case(not floors(["a"] * MIN_TRACKED, MIN_READ, list(HISTORICAL_FILES)),
             "the record itself may be declared historical",
             "the changelog and the dated reviews were refused as history")

        # An allowlist that exempts nothing, or names a category nobody
        # reviewed, or claims a category its path cannot support, is refused
        # rather than silently applied. The doc-instruction case is the one
        # that matters most: "install the runtime" labelled `negative` is
        # the single edit that turns this list back into a way of not
        # fixing things.
        def allowlist_with(entry):
            return {"historical_files": ["CHANGELOG.md"], "lines": [entry]}

        for doc, why in (
                ({"historical_files": [], "lines": []}, "an allowlist that exempts nothing"),
                (allowlist_with({"path": "internal/kmx/app/x_test.go", "text": "y",
                                 "category": "because", "why": "z"}),
                 "an unknown exemption category"),
                (allowlist_with({"path": "internal/kmx/app/x_test.go", "text": "y",
                                 "category": "negative", "why": ""}),
                 "an exemption with no reason"),
                (allowlist_with({"path": "docs/getting-started.md",
                                 "text": f"install the {NAME} runtime",
                                 "category": "negative",
                                 "why": "planted: an instruction calling itself an assertion"}),
                 "an active documentation instruction labelled `negative`"),
                (allowlist_with({"path": "internal/kmx/app/up.go", "text": "y",
                                 "category": "retirement", "why": "planted"}),
                 "a `retirement` exemption outside the files that do the refusing"),
                (allowlist_with({"path": "docs/kmx.md", "text": "y",
                                 "category": "historical", "why": "planted"}),
                 "a `historical` line in a document that is not the board"),
                (allowlist_with({"path": "docs/kmx.md", "text": "y",
                                 "category": "future", "why": "planted, and silent about support"}),
                 "a `future` note that never says the future is unsupported"),
                (allowlist_with({"path": "Makefile", "text": "y",
                                 "category": "future", "why": "planted: not supported"}),
                 "a `future` note outside current documentation")):
            f = d / "allowlist.json"
            f.write_text(json.dumps(doc))
            try:
                load_allowlist(f)
                case(False, "", f"{why} was accepted")
            except SystemExit:
                case(True, f"{why} is refused", "")

        # ...and each category still loads where it IS earned, or the rule
        # above would be a way of refusing the policy itself.
        for entry, what in (
                ({"path": "internal/kmx/app/x_test.go", "text": "y", "category": "negative",
                  "why": "a bounded assertion"}, "a `negative` line in a test"),
                ({"path": ".github/workflows/ci.yml", "text": "y", "category": "negative",
                  "why": "a tripwire"}, "a `negative` line in the CI tripwires"),
                ({"path": "internal/kmx/lift/plan.go", "text": "y", "category": "retirement",
                  "why": "the payload refusal"}, "a `retirement` line in a refusing file"),
                ({"path": "docs/COORDINATION.md", "text": "y", "category": "historical",
                  "why": "a merged row"}, "a `historical` line on the board"),
                ({"path": "docs/COORDINATION.md", "text": "remains OPEN", "category": "future",
                  "why": "an open question"}, "a `future` note that says it is open")):
            f = d / "allowlist.json"
            f.write_text(json.dumps(allowlist_with(entry)))
            try:
                load_allowlist(f)
                case(True, f"{what} is accepted", "")
            except SystemExit:
                case(False, "", f"{what} was refused, so the policy cannot describe this tree")

        # And the verdict itself, through main() rather than through
        # judge(): a finding that is computed correctly and then not acted
        # on is indistinguishable from a clean tree.
        real = report([("docs/x.md", 3, RULES[0], "bad")], [], set(), ["docs/x.md"], 1)
        case(real == 1, "a finding exits non-zero", "a finding was reported clean")
        case(report([], [], set(), ["docs/x.md"], 1) == 0,
             "a clean file exits zero", "a clean file was refused")

        # The floors reach an exit code, not just a list. A verdict that is
        # computed and then not acted on is the same as no verdict, and the
        # widest exemption there is — a current document declared historical
        # as a whole file — is checked through main() for that reason.
        #
        # The fixture is the REAL allowlist with one entry added, and the
        # file it widens is deliberately one NO line entry names: a
        # directory full of exempted lines would go red for staleness
        # instead, and this case would pass with the floors switched off.
        # Built from a fresh read rather than from `historical`/`lines`
        # above, because an entry list that has already been through judge()
        # carries the identities staleness is tracked by.
        real = json.loads(ALLOWLIST.read_text())
        real["historical_files"] = list(real["historical_files"]) + ["docs/kmx.md"]
        widened = d / "widened.json"
        widened.write_text(json.dumps(real))
        case(main([], allowlist=widened) == 1,
             "a run whose allowlist declares a current document historical exits non-zero",
             "a widened exemption reached an exit code of zero, so the floors decide nothing")

    # Finally, the real tree, through the real entry point. This is what
    # makes a widened exemption or a neutered rule visible even when every
    # fixture above still passes.
    rc = main([])
    case(rc == 0, "the tree this checker ships in passes its own scan",
         "the tree does not pass its own scan (output above)")

    if failed:
        print(f"check-legacy-runtime self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print(f"check-legacy-runtime self-test: {len(RULES)} rules and every floor proved on a real scan")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
