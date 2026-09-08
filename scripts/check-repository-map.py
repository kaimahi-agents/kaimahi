#!/usr/bin/env python3
"""Hold docs/repository-map.md to the tree it describes.

The map asserts several dozen facts about this repository: how many files
are in each area, which manifests ride inside the binary, which script
calls which, which package has no source at all. Every one of them was
true when it was written and none of them was checked. The ERP relocation
that happened the day before it was written would have invalidated four of
its rows.

WHAT THIS CHECKS, AND WHAT IT DELIBERATELY DOES NOT.

  Counts and paths are CHECKABLE. A number, a filename, a caller, a
  membership list — the tree answers these, and a disagreement is a fact
  about the tree, not an opinion about it.

  Classifications are REVIEWABLE. Whether a manifest is product or
  demonstration is a judgement the tree cannot settle. A checker that
  enforced one would either be wrong or would freeze an opinion the tree
  is allowed to change, and the first person it inconvenienced would
  delete it. So nothing here reads the word "Product" or
  "Demonstration" and asks whether it is deserved.

  What IS enforced about a classification is that one exists: every
  tracked file in the areas the map enumerates lands in exactly one of
  its lists, and the lists' own counts add up. Add a script and this
  fails until somebody has said what it is — without this file ever
  saying which answer is right.

  The three GENUINELY UNCLEAR cases are a standing question, not a gap to
  close. They need a product decision, and the risk is not that they stay
  unresolved — it is that the ambiguity quietly disappears. So the section
  is required to survive, to say how many cases it holds, to hold that
  many, and to name paths that still exist. The number is read FROM the
  map, so resolving one is an edit to the map and not to this file.

DERIVED, NOT COPIED. The map is the claim and the tree is the truth, and
no assertion here holds a third copy: the embedded manifest list comes out
of embed.go's own patterns, the excluded list out of the Go test that
names them, every count out of `git ls-files`. A guard that restates what
it guards agrees with it forever.

THE EMPTY CASE IS A FAILURE. Every claim below has an anchor in the map —
a heading, a table, a sentence. An anchor that matches nothing, a table
with no rows, an enumeration that produced no files: each is a failure
here rather than a claim that quietly stopped being made.

TWO KINDS OF MUTATION, AND THEY ARE NOT THE SAME. scripts/check-mutations.py
breaks THIS FILE and requires it to notice. That cannot see an assertion
whose fixture already satisfies it by another route, because the harness
never touches the fixture. So --selftest below breaks the OTHER side: it
edits the map, and it adds files to the tree the map has not classified,
and requires each edit to be caught. A claim proven by both is proven from
both directions.

Run:  python3 scripts/check-repository-map.py
      python3 scripts/check-repository-map.py --selftest
"""
from __future__ import annotations

import os
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
MAP = "docs/repository-map.md"

# Files that talk ABOUT the tree rather than using it. Every "who names
# this?" question below skips them, because a map that names everything
# would make its own no-orphans claim true by writing it down — and this
# checker, which has to quote the map's paths to anchor to them, would
# then read as a caller of every script the map mentions.
NOT_EVIDENCE = {MAP, "scripts/check-repository-map.py",
                "scripts/mutations/check-repository-map.json"}

NUMBER_WORDS = {
    "zero": 0, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
    "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11,
    "twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
    "sixteen": 16, "seventeen": 17, "eighteen": 18, "nineteen": 19,
    "twenty": 20, "twenty-one": 21, "twenty-two": 22, "twenty-three": 23,
    "twenty-four": 24, "twenty-five": 25, "twenty-six": 26,
}
WORD_PATTERN = "|".join(sorted(NUMBER_WORDS, key=len, reverse=True))
# A count in the map may be written either way, and which one is a
# sentence-level style choice rather than a fact — so every anchor accepts
# both and this is the one place that knows the difference.
NUM = rf"(\d+|{WORD_PATTERN})"


def phrase(template: str) -> str:
    """A regex for a sentence in the map, written as the sentence.

    The map is prose wrapped at the width somebody's editor chose, so a
    claim is regularly split across two lines and a pattern written with
    literal spaces would silently stop matching — which is an anchor
    failure, not a pass, but still a checker that stopped checking. Every
    run of whitespace here matches a line break, `{n}` is a number written
    either as digits or as a word, and everything else is literal.
    """
    out = []
    for part in re.split(r"(\{n\})", template):
        if part == "{n}":
            out.append(NUM)
        else:
            out.append(r"\s+".join(re.escape(w) for w in part.split()))
            if part[:1].isspace():
                out[-1] = r"\s+" + out[-1]
            if part[-1:].isspace():
                out[-1] = out[-1] + r"\s+"
    return "".join(out)


def number(text: str) -> int:
    t = text.strip().lower()
    if t.isdigit():
        return int(t)
    if t in NUMBER_WORDS:
        return NUMBER_WORDS[t]
    raise Anchor(f"{text!r} is not a number this file can read")


class Anchor(Exception):
    """The map no longer says the thing a claim was anchored to.

    Raised rather than returned because it is not a disagreement between
    the map and the tree — it is this checker having lost its grip, which
    must be as loud as a real finding and must never be a pass.
    """


def worktree(start: pathlib.Path) -> pathlib.Path:
    """The repository this file belongs to, following symlinks to reach it.

    scripts/check-mutations.py runs a checker from a throwaway directory
    whose every entry except scripts/ is a symlink to the real tree — so
    the checker sees a Makefile and a docs/ but stands in something git
    knows nothing about. Asking git where the MAP actually lives resolves
    that: the mutated copy then checks the real repository, which is the
    only thing it can usefully be mutated against.
    """
    for where in (start, (start / MAP).resolve().parent):
        got = subprocess.run(["git", "-C", str(where), "rev-parse", "--show-toplevel"],
                             capture_output=True)
        if got.returncode == 0 and got.stdout.strip():
            return pathlib.Path(got.stdout.decode().strip())
    return start


class Tree:
    """The tracked tree: what `git ls-files` says is in the repository.

    Tracked rather than a directory walk, for the reason the credential
    scanner gives: a working directory is full of build output and, here,
    of linked worktrees under .claude/ — each a whole second copy of this
    repository, which a walk would count as part of it.

    `files` is settable so the self-test can hand this a tree with one
    more file in it than the map classifies, which is the drift no edit to
    the map alone can simulate.
    """

    def __init__(self, root: pathlib.Path, files: list[str] | None = None):
        self.root = worktree(root)
        self.files = sorted(files) if files is not None else self._tracked()
        if not self.files:
            raise Anchor(f"`git ls-files` listed nothing under {root} — "
                         "refusing to check a map against an empty tree")
        self._text: dict[str, str] = {}

    def _tracked(self) -> list[str]:
        got = subprocess.run(["git", "ls-files", "-z"], cwd=self.root, capture_output=True)
        if got.returncode != 0:
            raise Anchor(f"`git ls-files` failed in {self.root}: "
                         f"{got.stderr.decode().strip()} — the map is checked against the tracked "
                         "tree, and without one there is nothing to check it against")
        return sorted(p for p in got.stdout.decode().split("\0") if p)

    def under(self, prefix: str) -> set[str]:
        prefix = prefix.rstrip("/") + "/"
        return {p for p in self.files if p.startswith(prefix)}

    def directly_under(self, prefix: str) -> set[str]:
        """The files IN a directory, not in its subdirectories."""
        return {p for p in self.under(prefix) if "/" not in p[len(prefix.rstrip("/")) + 1:]}

    def dirs_under(self, prefix: str) -> set[str]:
        prefix = prefix.rstrip("/") + "/"
        return {prefix + p[len(prefix):].split("/")[0]
                for p in self.under(prefix) if "/" in p[len(prefix):]}

    def read(self, path: str) -> str:
        if path not in self._text:
            self._text[path] = (self.root / path).read_text(encoding="utf-8", errors="replace")
        return self._text[path]


def flat(text: str) -> str:
    """One line, single-spaced — so a claim survives being re-wrapped."""
    return " ".join(text.split())


def non_test(paths: set[str]) -> set[str]:
    return {p for p in paths if not p.endswith("_test.go")}


class Doc:
    """docs/repository-map.md, split into its `##` sections.

    Each section carries the directory its backticked names are relative
    to, taken from the heading itself — inside the `k8s/` section
    `models/` means `k8s/models/`, which is how the map is written and how
    a reader reads it.
    """

    def __init__(self, text: str):
        self.text = text
        if not text.strip():
            raise Anchor(f"{MAP} is empty")
        self.sections: list[tuple[str, str]] = []
        parts = re.split(r"^## ", text, flags=re.M)
        self.preamble = parts[0]
        for part in parts[1:]:
            heading, _, body = part.partition("\n")
            self.sections.append((heading.strip(), body))

    def section(self, needle: str) -> tuple[str, str]:
        """(heading, body) of the one section whose heading contains `needle`."""
        hits = [s for s in self.sections if needle in s[0]]
        if len(hits) != 1:
            raise Anchor(f"{len(hits)} sections in {MAP} have a heading containing {needle!r}, not one")
        return hits[0]

    def headings(self) -> list[str]:
        return [h for h, _ in self.sections]


def once(pattern: str, text: str, what: str, flags: int = 0) -> re.Match:
    """The single match, or a failure naming what stopped being said.

    Zero matches means the map no longer makes the claim; two means the
    anchor is not specific enough to know which one is being checked.
    Both leave the claim unproven, and an unproven claim must not pass.
    """
    hits = list(re.finditer(pattern, text, flags | re.I))
    if len(hits) != 1:
        raise Anchor(f"{what}: the map has {len(hits)} places matching this claim, not one\n"
                     f"      pattern: {pattern}")
    return hits[0]


def ticks(text: str) -> list[str]:
    """Everything in single backticks, in order, duplicates kept."""
    return re.findall(r"`([^`\n]+)`", text)


def globbed(path: str, pattern: str) -> bool:
    """A glob that does not cross directories.

    `fnmatch`'s `*` matches a slash, so `scripts/*-probe.sh` also matches
    `scripts/ci/status-unknown-probe.sh` — which moved a file from the CI
    bucket into the probe bucket and made both counts right by being wrong
    twice. A pattern here spans one directory level, the way a shell glob
    does and the way the map reads.
    """
    import fnmatch
    return (path.count("/") == pattern.count("/")
            and fnmatch.fnmatch(path, pattern))


def resolve(token: str, base: str, tree: Tree) -> set[str]:
    """The tracked files a backticked name in the map refers to.

    Tried in order: the name as written, the name relative to the
    section's directory, either of those as a glob, either as a directory,
    and finally a bare glob against file names anywhere under the
    section's directory — which is what `lift*.go` in the `internal/`
    section means.
    """
    # The section's own directory first: inside the `k8s/` section `plane/`
    # is k8s/plane/ and not the plane module at the root, and inside
    # `docs/` `README.md` is the documentation index and not the front
    # door. Reading those the other way round is not a near miss — it
    # resolves to a real, wrong file and the claim passes.
    candidates = []
    if base:
        candidates.append(base.rstrip("/") + "/" + token.rstrip("/"))
    candidates.append(token.rstrip("/"))
    files = set(tree.files)
    for c in candidates:
        if c in files:
            return {c}
    for c in candidates:
        if "*" in c:
            hit = {p for p in files if globbed(p, c)}
            if hit:
                return hit
    for c in candidates:
        hit = tree.under(c)
        if hit:
            return hit
    if "*" in token and "/" not in token:
        scope = tree.under(base) if base else files
        hit = {p for p in scope if globbed(os.path.basename(p), token)}
        if hit:
            return hit
    # Last: the name as a suffix of a tracked path. The map's summary table
    # and its prose both use short names on purpose — `reviews/`, `kmx/`,
    # `workflow_run.go` — and reading those as "somewhere in the tree" is
    # what a reader does. It is deliberately the loosest step and it is only
    # ever reached by the existence claim; every membership and count claim
    # above resolves exactly, so a file that MOVED is caught there.
    scope = tree.under(base) if base else files
    tail = token.rstrip("/")
    hit = {p for p in scope if p == tail or p.startswith(tail + "/")
           or p.endswith("/" + tail) or ("/" + tail + "/") in p}
    return hit


# Names in backticks that are not, and are not meant to be, paths in this
# tree. Each is exempt for a stated reason; an entry with no reason is a
# way to make a broken claim pass.
NOT_A_PATH = {
    "k8s/demo/": "hypothetical — the map is explaining why this split was NOT made",
}


def section_base(heading: str) -> str:
    """The directory a section's short names are relative to.

    Only when the heading names exactly one — the section covering
    `blueprints/`, `.github/` and the root files names three, and reading
    its names as relative to the first of them would put the root's files
    inside blueprints/.
    """
    dirs = [t for t in ticks(heading) if t.endswith("/")]
    return dirs[0] if len(dirs) == 1 else ""


def is_path_candidate(token: str) -> bool:
    """Whether a backticked name is claiming to be a path at all.

    Commands, module paths and embed directives are also written in
    backticks. The test is shape: only the characters a path in this tree
    uses, and either a slash or a suffix the tree actually has.
    """
    if not re.fullmatch(r"[A-Za-z0-9_.*/-]+", token):
        return False
    if token.startswith("-") or token in {".", ".."}:
        return False
    # `.svg` names a file kind, `plane/...` names a family of import paths.
    # Neither is a path in this tree and neither is meant to be.
    if re.fullmatch(r"\.[a-z]+", token) or re.search(r"(^|/)\.{2,}(/|$)", token):
        return False
    suffixes = (".go", ".py", ".sh", ".yaml", ".yml", ".json", ".md", ".svg",
                ".png", ".mmd", ".mod", ".sum", ".conf", ".txt")
    return "/" in token or token.lower().endswith(suffixes)


# --------------------------------------------------------------------------
# The claims. Each takes (doc, tree) and returns the disagreements it found,
# in words. Anchor failures are raised, not returned: a claim that cannot
# find what it checks has not checked anything.
# --------------------------------------------------------------------------

CLAIMS: list = []


def claim(fn):
    CLAIMS.append(fn)
    return fn


def compare_count(doc_value: int, tree_value: int, what: str) -> list[str]:
    if doc_value == tree_value:
        return []
    return [f"{what}: the map says {doc_value}, the tree has {tree_value}"]


def listed(text: str, base: str, tree: Tree) -> tuple[set[str], list[str]]:
    """The files a bolded list in the map names, and the names that failed.

    A token resolving to the section's own directory is prose, not a list
    entry — `docs/` inside the docs section is a reference to the
    directory, and counting it would quietly make every list contain
    everything and every membership check pass.
    """
    files, unresolved = set(), []
    whole = tree.under(base) if base else set(tree.files)
    for token in ticks(text):
        if not is_path_candidate(token):
            continue
        hit = resolve(token, base, tree)
        if not hit:
            unresolved.append(token)
        elif hit != whole:
            files |= hit
    return files, unresolved


def compare_sets(named: set[str], actual: set[str], what: str) -> list[str]:
    problems = []
    for missing in sorted(actual - named):
        problems.append(f"{what}: {missing} is in the tree and the map does not list it")
    for extra in sorted(named - actual):
        problems.append(f"{what}: the map lists {extra}, which the tree does not have")
    return problems


@claim
def every_area_of_the_tree_has_a_section(doc: Doc, tree: Tree) -> list[str]:
    """A whole directory can appear without the map noticing.

    The map's sections are per top-level directory, so a new one is the
    largest thing that can go undescribed — and the least visible, because
    every existing row stays correct.
    """
    headings = " ".join(doc.headings())
    top = {p.split("/")[0] for p in tree.files if "/" in p}
    if not top:
        raise Anchor("the tree has no directories — the enumeration is broken")
    return [f"the tree has a top-level {d}/ and no section of the map names it"
            for d in sorted(top) if f"`{d}/`" not in headings]


@claim
def every_path_the_map_names_is_in_the_tree(doc: Doc, tree: Tree) -> list[str]:
    """The plainest drift there is: a file the map names, moved or gone.

    Resolution is relative to the section, because that is how the map is
    written — `models/` inside the `k8s/` section is `k8s/models/`.
    """
    problems, examined = [], 0
    for heading, body in [("", doc.preamble)] + doc.sections:
        base = section_base(heading)
        for token in ticks(heading + "\n" + body):
            if token in NOT_A_PATH or not is_path_candidate(token):
                continue
            examined += 1
            if not resolve(token, base, tree):
                where = heading or "the opening section"
                problems.append(f"{where}: names `{token}`, which is not in the tree"
                                + (f" (relative to {base})" if base else ""))
    if not examined:
        raise Anchor("no backticked path was found anywhere in the map — the extractor is broken, "
                     "not the map")
    return problems


@claim
def the_cmd_table_covers_cmd(doc: Doc, tree: Tree) -> list[str]:
    _, body = doc.section("`cmd/`")
    rows = re.findall(r"^\| `(cmd/[^`]+)` \(" + NUM + r" files?\)", body, re.M | re.I)
    if not rows:
        raise Anchor("the `cmd/` section no longer has a table of binaries with file counts")
    problems = []
    for path, count in rows:
        problems += compare_count(number(count), len(tree.under(path)), f"{path} file count")
    dirs = {os.path.dirname(p) for p in tree.under("cmd")}
    return problems + compare_sets({p for p, _ in rows}, dirs, "cmd/ coverage")


@claim
def the_package_table_covers_internal(doc: Doc, tree: Tree) -> list[str]:
    """Every package under internal/, with its non-test source count.

    The count is the map's own column, and the reason it is worth checking
    is `delegation`: a package whose whole point is that the number is
    zero, which stops being true the moment somebody puts a file there.
    """
    _, body = doc.section("`internal/`")
    rows = re.findall(r"^\| `([a-z/]+)` \| \*?\*?" + NUM + r"\*?\*? \|", body, re.M | re.I)
    if not rows:
        raise Anchor("the `internal/` section no longer has a package table with file counts")
    problems, named = [], set()
    for pkg, count in rows:
        path = "internal/" + pkg
        if path not in tree.dirs_under("internal") | {os.path.dirname(p) for p in tree.files}:
            problems.append(f"the package table names `{pkg}`, which is not a directory in the tree")
            continue
        named.add(path)
        problems += compare_count(number(count), len(non_test(tree.directly_under(path))),
                                  f"{path} non-test source files")
    actual = {os.path.dirname(p) for p in tree.under("internal")}
    return problems + compare_sets(named, actual, "internal/ package coverage")


@claim
def the_package_count_matches(doc: Doc, tree: Tree) -> list[str]:
    _, body = doc.section("`internal/`")
    m = once(phrase("`internal/kmx/` is {n} packages"), body, "the internal/kmx package count")
    _, short = doc.section("The short version")
    s = once(phrase("`kmx/` ({n} packages)"), short, "the short version's package count")
    got = len(tree.dirs_under("internal/kmx"))
    return (compare_count(number(m.group(1)), got, "internal/kmx package count")
            + compare_count(number(s.group(1)), got, "internal/kmx package count (short version)"))


@claim
def the_lift_split_is_still_five_files(doc: Doc, tree: Tree) -> list[str]:
    """The rule the map states for the package boundary, in numbers.

    `lift` is the half that needs no cloud and `app` is the half that runs
    `az`; the count is how a reader checks the claim without reading forty
    files.
    """
    _, body = doc.section("`internal/`")
    m = once(phrase("the {n} `lift*.go` files in `app`"), body, "the lift split")
    got = {p for p in non_test(tree.directly_under("internal/kmx/app"))
           if os.path.basename(p).startswith("lift")}
    return compare_count(number(m.group(1)), len(got), "lift*.go files in internal/kmx/app")


@claim
def the_plane_is_thirteen_packages_and_ten_migrations(doc: Doc, tree: Tree) -> list[str]:
    _, body = doc.section("`plane/`")
    pkgs = once(phrase("{n} internal packages and {n} binary"), body, "the plane's package count")
    migs = once(phrase("(Postgres and {n} migrations)"), body, "the plane's migration count")
    return (compare_count(number(pkgs.group(1)), len(tree.dirs_under("plane/internal")),
                          "plane internal packages")
            + compare_count(number(pkgs.group(2)), len(tree.dirs_under("plane/cmd")), "plane binaries")
            + compare_count(number(migs.group(1)),
                            len(tree.under("plane/internal/db/migrations")), "plane migrations"))


@claim
def the_root_module_does_not_import_the_plane(doc: Doc, tree: Tree) -> list[str]:
    """The module boundary the map calls the point of the split.

    An import would compile, and nothing else in the tree would object —
    which is why the map says it and why it is worth a check. Import
    declarations only: the plane's module path appears all over
    `planebuild` as a string constant, and that coupling is the map's next
    paragraph rather than a violation of this one.
    """
    _, body = doc.section("`plane/`")
    once(phrase("no `require`, and no `plane/...` import anywhere in root `cmd/` or `internal/`"),
         body, "the no-import claim")
    module = "github.com/kaimahi-agents/kaimahi/plane"
    problems, examined = [], 0
    for path in sorted(tree.under("cmd") | tree.under("internal")):
        if not path.endswith(".go"):
            continue
        examined += 1
        for block in re.findall(r"^import \(\n(.*?)^\)", tree.read(path), re.M | re.S):
            for line in block.splitlines():
                if re.search(rf'"{re.escape(module)}[/"]', line):
                    problems.append(f"{path} imports the plane module: {line.strip()}")
        for line in re.findall(rf'^import (?:\w+ )?"{re.escape(module)}[^"]*"', tree.read(path), re.M):
            problems.append(f"{path} imports the plane module: {line.strip()}")
    if not examined:
        raise Anchor("no Go files were found under cmd/ or internal/ — the no-import check read nothing")
    if re.search(rf"^\s+{re.escape(module)}\s", tree.read("go.mod"), re.M):
        problems.append("go.mod requires the plane module")
    return problems


@claim
def the_embedded_manifests_are_the_ones_embed_go_names(doc: Doc, tree: Tree) -> list[str]:
    """The map's list of 25, against embed.go's own patterns.

    Derived rather than copied: this expands the `go:embed` directives the
    same way the compiler does — a directory pattern takes the directory —
    so adding a manifest to the binary and not to the map is caught, and
    so is the reverse.
    """
    _, body = doc.section("`k8s/`")
    m = once(r"\*\*Product — embedded in `kmx`.*?\(" + NUM + r"\):\*\*(.*?)\n\n",
             body, "the embedded list", re.S)
    named, unresolved = listed(m.group(2), "k8s", tree)
    if unresolved:
        return [f"the embedded list names `{t}`, which is not in the tree" for t in unresolved]
    # ...including the counted shorthands the list uses for whole
    # directories, which are claims of their own.
    problems = []
    for word, directory in re.findall(phrase("all {n} of ") + r"`([^`]+)`", m.group(2), re.I):
        problems += compare_count(number(word), len(resolve(directory, "k8s", tree)),
                                  f"files in k8s/{directory.strip('/')}")
    problems += compare_count(number(m.group(1)), len(named), "the embedded manifest count")
    return problems + compare_sets(named, embedded_k8s(tree), "the embedded manifest list")


def embedded_k8s(tree: Tree) -> set[str]:
    """What embed.go's patterns actually reach under k8s/."""
    return {p for p in embedded(tree) if p.startswith("k8s/")}


def embedded(tree: Tree) -> set[str]:
    directives = re.findall(r"^//go:embed (.+)$", tree.read("embed.go"), re.M)
    if not directives:
        raise Anchor("embed.go has no //go:embed directives — the embedded set cannot be derived")
    out: set[str] = set()
    for line in directives:
        for pattern in line.split():
            hit = resolve(pattern, "", tree)
            if not hit:
                raise Anchor(f"embed.go names {pattern!r}, which is not in the tree")
            out |= hit
    return out


@claim
def the_manifests_not_embedded_are_the_ones_the_go_test_names(doc: Doc, tree: Tree) -> list[str]:
    """The map's three non-embedded lists, against the tree and the test.

    The repository already has a ledger for this — the Go test that names
    every manifest which must NOT ride along — so this compares against
    that rather than against a list of its own. The JSON corpus is the one
    file in neither: not embedded, and not a manifest for the test to
    exclude, which is exactly the kind of gap a count hides.
    """
    _, body = doc.section("`k8s/`")
    lists = re.findall(r"^\*\*(Product — applied from a checkout only|Product — the release agent|"
                       r"Demonstration) \(" + NUM + r"\):\*\*(.*?)\n\n", body, re.M | re.S | re.I)
    if len(lists) != 3:
        raise Anchor(f"the `k8s/` section has {len(lists)} non-embedded lists, not three")
    problems, named = [], set()
    for label, count, text in lists:
        entry, unresolved = listed(text, "k8s", tree)
        problems += [f"the {label} list names `{t}`, which is not in the tree" for t in unresolved]
        problems += compare_count(number(count), len(entry), f"the {label} count")
        overlap = entry & named
        if overlap:
            problems.append(f"the {label} list repeats {sorted(overlap)} from an earlier list")
        named |= entry

    everything = tree.under("k8s")
    problems += compare_sets(named, everything - embedded_k8s(tree), "the non-embedded k8s files")

    excluded = excluded_by_the_go_test(tree)
    m = once(phrase("{n} of `k8s/`'s {n} files are embedded, {n} are named by that test"),
             body, "the k8s arithmetic")
    problems += compare_count(number(m.group(1)), len(embedded_k8s(tree)), "embedded k8s files")
    problems += compare_count(number(m.group(2)), len(everything), "files in k8s/")
    problems += compare_count(number(m.group(3)), len(excluded), "manifests the Go test names")
    thirteenth = once(phrase("the thirteenth is ") + r"`([^`]+)`", body, "the file in neither list")
    return problems + compare_sets(named - excluded, {thirteenth.group(1)},
                                   "the non-embedded files the Go test does not name")


def excluded_by_the_go_test(tree: Tree) -> set[str]:
    """The manifests TestTheConnectorFamiliesAreNotEmbedded names."""
    path = "internal/kmx/app/manifests_test.go"
    m = re.search(r"func TestTheConnectorFamiliesAreNotEmbedded\(t \*testing\.T\) \{\s*"
                  r"for _, name := range \[\]string\{(.*?)\}", tree.read(path), re.S)
    if not m:
        raise Anchor(f"{path} no longer has TestTheConnectorFamiliesAreNotEmbedded with a literal "
                     "list — the map's ledger for the embedded boundary is gone")
    names = re.findall(r'"([^"]+)"', m.group(1))
    if not names:
        raise Anchor("TestTheConnectorFamiliesAreNotEmbedded names no manifests")
    return {"k8s/" + n for n in names}


@claim
def the_script_buckets_cover_scripts(doc: Doc, tree: Tree) -> list[str]:
    """Every tracked file under scripts/ is classified exactly once.

    The buckets are resolved in the order the table lists them and a file
    already claimed is not claimed again — which is the table's own rule,
    written into it twice: `kube-guard.sh` is "counted once above", and
    the probes are "`*-probe.sh`, minus the one that is embedded".

    This is where a new file makes the check fail, and the message says
    only that it is unclassified. Which bucket it belongs in is not a
    question this file has an opinion about.
    """
    heading, body = doc.section("`scripts/`")
    total = once(phrase("`scripts/` — {n} tracked files"), heading, "the scripts/ total")
    everything = tree.under("scripts")
    problems = compare_count(number(total.group(1)), len(everything), "tracked files under scripts/")

    rows = re.findall(r"^\| (\*\*.+?) \| " + NUM + r" \| (.+?) \|$", body, re.M | re.I)
    if not rows:
        raise Anchor("the `scripts/` section no longer has a bucket table")
    claimed: set[str] = set()
    for label, count, files in rows:
        label = label.replace("*", "")
        bucket: set[str] = set()
        for token in ticks(files):
            hit = resolve(token, "scripts", tree)
            if not hit:
                problems.append(f"the {label.strip()} bucket names `{token}`, which is not in the tree")
                continue
            bucket |= hit
        bucket -= claimed
        claimed |= bucket
        problems += compare_count(number(count), len(bucket), f"the {label.strip()} bucket")
        # A number word standing in front of a glob is a claim too.
        for word, glob in re.findall(NUM + r" `([^`]*\*[^`]*)`", files, re.I):
            problems += compare_count(number(word), len(resolve(glob, "scripts", tree)),
                                      f"the {label.strip()} bucket's `{glob}`")
    for missing in sorted(everything - claimed):
        problems.append(f"{missing} is in the tree and no bucket of the map's scripts/ table "
                        "classifies it — say what it is")
    for extra in sorted(claimed - everything):
        problems.append(f"the scripts/ table classifies {extra}, which is not in the tree")
    return problems


@claim
def the_short_version_agrees_with_the_long_one(doc: Doc, tree: Tree) -> list[str]:
    """The summary table at the top, against the same tree.

    A summary is where drift hides best: it is read first, it is not where
    anybody edits, and its numbers are a second copy of numbers that
    already exist further down.
    """
    _, short = doc.section("The short version")
    row = once(phrase("| `scripts/` | {n} ({n} embedded in the binary, {n} operator) | {n} | {n}"),
               short, "the short version's scripts row")
    product, embedded_n, operator, demonstration, scaffolding = (number(g) for g in row.groups())
    everything = tree.under("scripts")
    problems = []
    if product != embedded_n + operator:
        problems.append(f"the short version's scripts row says {product} product = "
                        f"{embedded_n} embedded + {operator} operator, which does not add up")
    if product + demonstration + scaffolding != len(everything):
        problems.append(f"the short version's scripts row totals "
                        f"{product + demonstration + scaffolding}, and scripts/ has {len(everything)}")
    problems += compare_count(embedded_n, len({p for p in embedded(tree) if p.startswith("scripts/")}),
                              "the short version's count of embedded scripts")
    brand_row = once(phrase("| `brand/` | {n} assets used by the README"), short,
                     "the short version's brand row")
    _, brand = doc.section("`brand/`")
    brand_long = once(phrase("{n} image files plus a README"), brand, "the brand asset count")
    problems += compare_count(number(brand_row.group(1)), number(brand_long.group(1)),
                              "the short version's brand-asset count")
    docs_row = once(phrase("| `docs/` | {n} capability docs"), short, "the short version's docs row")
    _, long_docs = doc.section("`docs/`")
    long_count = once(phrase("**Product documentation ({n})**"), long_docs, "the docs product count")
    return problems + compare_count(number(docs_row.group(1)), number(long_count.group(1)),
                                    "the short version's capability-doc count")


@claim
def the_number_of_embedded_shell_scripts_is_the_same_in_both_places(doc: Doc, tree: Tree) -> list[str]:
    """The opening argument for why shell in `scripts/` can be product.

    The number is stated twice — once in the reasoning and once in the
    summary table — and a second copy of a number is where drift lands
    first, because only one of them is ever edited.
    """
    m = once(phrase("including {n} shell scripts"), doc.preamble, "the embedded shell scripts")
    return compare_count(number(m.group(1)),
                         len({p for p in embedded(tree) if p.startswith("scripts/")}),
                         "shell scripts embedded in the binary")


@claim
def the_mutation_harness_breaks_every_checker_the_map_counts(doc: Doc, tree: Tree) -> list[str]:
    """How many checkers are proven, against the specifications on disk.

    scripts/check-mutations.py refuses to pass a checker with no
    specification, so the number of files in that directory IS the number
    of checkers proven — and it changes every time one is added, which is
    what makes it worth stating and worth checking.
    """
    _, body = doc.section("`scripts/`")
    m = once(phrase("is one of the {n} checkers the mutation harness breaks on purpose"),
             body, "the count of proven checkers")
    return compare_count(number(m.group(1)), len(tree.under("scripts/mutations")),
                         "mutation specifications")


@claim
def the_docs_lists_cover_docs(doc: Doc, tree: Tree) -> list[str]:
    """The four lists partition docs/, and their counts are the tree's."""
    heading, body = doc.section("`docs/`")
    total = once(phrase("`docs/` — {n} tracked files"), heading, "the docs/ total")
    everything = tree.under("docs")
    problems = compare_count(number(total.group(1)), len(everything), "tracked files under docs/")

    marker = re.compile(r"\*\*([A-Z][^*(]+) \(" + NUM + r"\)[:*]", re.I)
    starts = list(marker.finditer(body))
    if len(starts) < 2:
        raise Anchor(f"the `docs/` section has {len(starts)} counted lists — a list that stopped "
                     "being counted is a list nothing checks")
    lists = []
    for i, start in enumerate(starts):
        end = starts[i + 1].start() if i + 1 < len(starts) else len(body)
        # ...and never past the end of the list's own paragraph: the prose
        # after the last one names documents too, and a slice that ran on
        # would collect them into it.
        para = body.find("\n\n**", start.end())
        if 0 <= para < end:
            end = para
        lists.append((start.group(1), start.group(2), body[start.end():end]))
    named: set[str] = set()
    for label, count, text in lists:
        entry, unresolved = listed(text, "docs", tree)
        problems += [f"the {label.strip()} list names `{t}`, which is not in docs/" for t in unresolved]
        problems += compare_count(number(count), len(entry), f"the {label.strip()} count")
        overlap = entry & named
        if overlap:
            problems.append(f"the {label.strip()} list repeats {sorted(overlap)} from an earlier list")
        named |= entry
    for missing in sorted(everything - named):
        problems.append(f"{missing} is in the tree and no list in the map's docs/ section names it")
    return problems + [f"the docs/ section names {extra}, which is not in the tree"
                       for extra in sorted(named - everything)]


@claim
def the_brand_directory_is_what_the_map_says(doc: Doc, tree: Tree) -> list[str]:
    _, body = doc.section("`brand/`")
    m = once(phrase("{n} image files plus a README"), body, "the brand asset count")
    everything = tree.under("brand")
    images = {p for p in everything if not p.endswith(".md")}
    problems = compare_count(number(m.group(1)), len(images), "image files in brand/")
    readmes = everything - images
    if readmes != {"brand/README.md"}:
        problems.append(f"brand/ holds {sorted(readmes)} where the map expects one README")
    hero = once(r"`README\.md:(\d+)` embeds `([^`]+)`", body, "the one brand asset the tree uses")
    line = tree.read("README.md").splitlines()[int(hero.group(1)) - 1]
    if hero.group(2) not in line:
        problems.append(f"README.md:{hero.group(1)} does not name {hero.group(2)}: {line.strip()!r}")
    for token in ticks(body):
        if token.endswith((".svg", ".png")) and "/" not in token and f"brand/{token}" not in everything:
            problems.append(f"the brand/ section names `{token}`, which is not in brand/")
    return problems


@claim
def the_root_files_table_covers_the_root(doc: Doc, tree: Tree) -> list[str]:
    """Every tracked file at the root, in .github/ and in blueprints/.

    The map says this section was missing from its first version, which is
    the point: a reader checking whether something is covered needs the
    map to cover everything, and the root is where a new file is least
    likely to be noticed.
    """
    heading, body = doc.section("the root files")
    rows = re.findall(r"^\| (`.+?`.*?) \| \*\*.+?\*\* \|", body, re.M)
    if not rows:
        raise Anchor("the root-files section no longer has a table of paths and classes")
    named: set[str] = set()
    problems = []
    for token in ticks("\n".join(rows)):
        if token in NOT_A_PATH:
            continue
        hit = resolve(token, "", tree)
        if hit:
            named |= hit
        else:
            problems.append(f"the root-files table names `{token}`, which is not in the tree")
    if not named:
        raise Anchor("the root-files table names no paths — the extractor is broken")
    actual = ({p for p in tree.files if "/" not in p}
              | tree.under(".github") | tree.under("blueprints"))
    return problems + compare_sets(named, actual, "the root, .github/ and blueprints/ coverage")


@claim
def nothing_under_scripts_is_orphaned(doc: Doc, tree: Tree) -> list[str]:
    """The map's "none is orphaned", computed the way it describes.

    A file is named if any OTHER tracked file mentions its name — the map
    itself excepted, because a map that names everything would make its
    own claim true by writing it down. The exception is the mutation
    specifications, which nothing names because the harness globs the
    directory, and the map says so.
    """
    _, body = doc.section("`scripts/`")
    m = once(phrase("{n} of the {n} are named by something outside themselves, and the {n} "
                    "`scripts/mutations/*.json` are named by nothing"),
             body, "the orphan arithmetic")
    everything = sorted(tree.under("scripts"))
    others = [p for p in tree.files if p not in NOT_EVIDENCE]
    named, unnamed = 0, []
    for path in everything:
        base = os.path.basename(path)
        if any(base in tree.read(p) for p in others if p != path and readable(tree, p)):
            named += 1
        else:
            unnamed.append(path)
    problems = compare_count(number(m.group(1)), named, "scripts named by something outside themselves")
    problems += compare_count(number(m.group(2)), len(everything), "tracked files under scripts/")
    problems += compare_count(number(m.group(3)), len(unnamed), "scripts named by nothing")
    return problems + compare_sets(set(unnamed), tree.under("scripts/mutations"),
                                   "the files nothing outside names")


def readable(tree: Tree, path: str) -> bool:
    p = tree.root / path
    return p.is_file() and p.suffix.lower() not in {".png", ".jpg", ".gif", ".ico", ".pdf"}


@claim
def the_scripts_that_name_a_k8s_path(doc: Doc, tree: Tree) -> list[str]:
    """The count behind "not moved, deliberately".

    It is the argument for leaving k8s/ interleaved, so it is the number
    that decides whether that reasoning still holds.
    """
    _, body = doc.section("What moved")
    m = once(phrase("{n} tracked files under `scripts/` contain the literal `k8s/`"),
             body, "the count of scripts naming a k8s/ path")
    got = [p for p in sorted(tree.under("scripts")) if readable(tree, p) and "k8s/" in tree.read(p)]
    return compare_count(number(m.group(1)), len(got), "tracked files under scripts/ naming k8s/")


@claim
def the_release_agent_quote_is_where_the_map_cites_it(doc: Doc, tree: Tree) -> list[str]:
    """A quotation with a line number is a claim about two files.

    Both halves rot: the quote can be reworded, and the lines can move
    under it while the words stay somewhere else in the file — which is
    the version a reader would never catch.
    """
    m = once(r"`docs/release-agent\.md:(\d+)-(\d+)`[.:]?\s*\*\"(.+?)\"\*", doc.text,
             "the release-agent citation", re.S)
    start, end, quoted = int(m.group(1)), int(m.group(2)), m.group(3)
    lines = tree.read("docs/release-agent.md").splitlines()
    if end > len(lines):
        return [f"docs/release-agent.md has {len(lines)} lines and the map cites {start}-{end}"]
    there = flat(" ".join(lines[start - 1:end]))
    # A quotation is regularly cut short and closed with a full stop the
    # source does not have. The words are the claim; the punctuation
    # closing them is the map's own sentence.
    want = flat(quoted).rstrip(".")
    if want not in there:
        return [f"docs/release-agent.md:{start}-{end} does not say what the map quotes.\n"
                f"      map:  {want}\n      file: {there}"]
    return []


@claim
def the_erp_says_what_it_is(doc: Doc, tree: Tree) -> list[str]:
    """The map files the ERP as a demonstration on the strength of a
    comment in its own source. If that comment goes, the evidence goes."""
    m = once(phrase("The comment at the top of ") + r"`(internal/demo/erp/server\.go)`"
             + r"(?:.|\n)*?" + phrase(" says ") + r"\"([^\"]+)\"",
             doc.text, "the ERP's own words")
    if flat(m.group(2)) not in flat(tree.read(m.group(1)).replace("//", " ")):
        return [f"{m.group(1)} no longer says \"{m.group(2)}\", which is the map's evidence "
                "for filing the ERP as a demonstration"]
    return []


@claim
def show_turn_and_await_approval_have_the_callers_the_map_names(doc: Doc, tree: Tree) -> list[str]:
    """Two scripts filed as product on the strength of who calls them.

    Both look like demo leftovers and are not, and the only thing making
    that true is the caller — so the caller is what is checked.
    """
    _, body = doc.section("`scripts/`")
    once(phrase("`scripts/release-run.sh` calls it twice"), body, "the await-approval caller")
    once(phrase("`show-turn.py` renders one agent turn and is called only by `release-run.sh`"),
         body, "the show-turn caller")
    problems = []
    run = tree.read("scripts/release-run.sh")
    calls = len(re.findall(r"await-approval\.sh", run))
    if calls != 2:
        problems.append(f"scripts/release-run.sh names await-approval.sh {calls} times, not twice")
    callers = {p for p in tree.files
               if p not in NOT_EVIDENCE | {"scripts/show-turn.py"} and readable(tree, p)
               and "show-turn.py" in tree.read(p)}
    if callers != {"scripts/release-run.sh"}:
        problems.append("show-turn.py's callers are now " + (", ".join(sorted(callers)) or "nobody")
                        + ", and the map says only scripts/release-run.sh")
    return problems


@claim
def nothing_but_ci_and_one_script_runs_verify_chat(doc: Doc, tree: Tree) -> list[str]:
    """The map's sharpest "a name does not mean ownership" case.

    `verify-chat.py` reads like it belongs to `make chat`, and the reason
    it does not is that every mention in the Makefile is a comment. That
    is mechanical, so it is checked; whether a dozen comments are
    misleading is not.
    """
    _, body = doc.section("`scripts/`")
    m = once(phrase("`.github/workflows/ci.yml` ({n} invocations among {n} mentions"),
             body, "the verify-chat invocation count")
    once(phrase("every occurrence in the Makefile is a comment line rather than a recipe"), body,
         "the Makefile claim")
    ci = [line for line in tree.read(".github/workflows/ci.yml").splitlines()
          if "verify-chat.py" in line]
    runs = [line for line in ci if not line.strip().startswith("#")]
    problems = compare_count(number(m.group(1)), len(runs), "verify-chat.py invocations in ci.yml")
    problems += compare_count(number(m.group(2)), len(ci), "verify-chat.py mentions in ci.yml")
    for n, line in enumerate(tree.read("Makefile").splitlines(), 1):
        if "verify-chat.py" in line and not line.lstrip().startswith("#"):
            problems.append(f"Makefile:{n} names verify-chat.py outside a comment: {line.strip()!r}")
    return problems


@claim
def exposure_scan_still_has_one_caller(doc: Doc, tree: Tree) -> list[str]:
    """The evidence for the first genuinely-unclear case.

    It is unclear BECAUSE it has one caller and no documentation of its
    own. A second caller would not resolve the question, but it would
    change it, and the map would be describing a tree that had moved.
    """
    _, unclear = doc.section("Genuinely unclear")
    once(phrase("`scripts/exposure-scan.sh`.** One caller — a make target"), unclear,
         "the exposure-scan reasoning")
    recipes = [line for line in tree.read("Makefile").splitlines()
               if line.startswith("\t") and "scripts/exposure-scan.sh" in line]
    if len(recipes) != 1:
        return [f"the Makefile has {len(recipes)} recipe lines running scripts/exposure-scan.sh, "
                "and the map's reasoning rests on there being one"]
    return []


@claim
def the_unclear_cases_survive(doc: Doc, tree: Tree) -> list[str]:
    """The standing question, kept standing.

    These three need a product decision and no script can make it. What a
    script CAN do is refuse to let the question evaporate: the section
    exists, it says how many cases it holds, it holds that many, and every
    path it names is still in the tree. The number comes from the map, so
    settling one is an edit to the map — never to this file.
    """
    heading, body = doc.section("Genuinely unclear")
    m = once(phrase("Genuinely unclear — {n}, and this is a result"), heading,
             "the count of unresolved cases")
    items = re.findall(r"^\d+\. \*\*(.+?)\*\*", body, re.M)
    if not items:
        raise Anchor("the Genuinely unclear section lists no cases — a standing question that "
                     "quietly emptied is the failure this section exists to prevent")
    problems = compare_count(number(m.group(1)), len(items), "unresolved cases")
    for token in ticks(body):
        if is_path_candidate(token) and not resolve(token, "k8s", tree):
            problems.append(f"the Genuinely unclear section rests on `{token}`, "
                            "which is no longer in the tree")
    return problems


@claim
def the_architecture_svg_still_has_no_trailing_newline(doc: Doc, tree: Tree) -> list[str]:
    """The map warns that `wc -l` reports this file as empty. The warning
    is only worth carrying while it is true."""
    m = once(phrase("the `.svg` has no trailing newline, so `wc -l` reports it as ") + r"(\d+)",
             doc.text, "the architecture.svg warning")
    data = (tree.root / "docs/assets/architecture.svg").read_bytes()
    if not data:
        return ["docs/assets/architecture.svg is empty"]
    lines = data.count(b"\n")
    if data.endswith(b"\n") or lines != int(m.group(1)):
        return [f"docs/assets/architecture.svg now ends with a newline or is {lines} lines by "
                f"`wc -l`, and the map warns it reads as {m.group(1)}"]
    return []


@claim
def the_two_unfindable_docs_are_still_unfindable(doc: Doc, tree: Tree) -> list[str]:
    """A legibility problem the map reports rather than fixes.

    Worth checking in both directions: the day somebody links these from
    the index, this paragraph becomes the misleading thing.
    """
    _, body = doc.section("`docs/`")
    once(phrase("`isolation.md` appears nowhere in `docs/README.md`"), body,
         "the isolation.md claim")
    once(phrase("`docs/reviews/` is referenced exactly once anywhere in `docs/` outside this map"),
         body, "the docs/reviews claim")
    index = tree.read("docs/README.md")
    problems = []
    if "isolation.md" in index:
        problems.append("docs/README.md now names isolation.md — the map says it appears nowhere there")
    if "reviews/" in index:
        problems.append("docs/README.md now names docs/reviews/ — the map says it never does")
    # Scoped to docs/, which is what the claim is about: whether a reader
    # moving through the documentation can find the directory. A mention in
    # a changelog entry is not a way in, and counting one would make this
    # say something the map does not.
    elsewhere = sorted(p for p in tree.under("docs")
                       if p not in NOT_EVIDENCE and readable(tree, p)
                       and "docs/reviews" in tree.read(p))
    if len(elsewhere) != 1:
        problems.append(f"docs/reviews/ is referenced from {elsewhere or 'nowhere'} outside the map, "
                        "and the map says exactly one place")
    return problems


# --------------------------------------------------------------------------


def check(tree: Tree, map_text: str) -> tuple[list[str], int]:
    """(problems, claims evaluated). An Anchor failure is a problem too."""
    problems, ran = [], 0
    try:
        doc = Doc(map_text)
    except Anchor as e:
        return [f"the map could not be read: {e}"], 0
    for fn in CLAIMS:
        ran += 1
        try:
            found = fn(doc, tree)
        except Anchor as e:
            problems.append(f"[{fn.__name__}] {e}")
            continue
        except OSError as e:
            problems.append(f"[{fn.__name__}] could not read a file it checks: {e}")
            continue
        problems.extend(f"[{fn.__name__}] {p}" for p in found)
    return problems, ran


def exit_code(problems: list[str]) -> int:
    """The verdict. Its own function because a verdict that is computed
    correctly and then not acted on is indistinguishable from a clean run,
    and a self-test that only called the comparing code would never see
    it."""
    return 1 if problems else 0


def main(argv) -> int:
    if argv[:1] == ["--selftest"]:
        return selftest()
    if argv:
        print(f"check-repository-map: unexpected argument {argv[0]!r}", file=sys.stderr)
        return 2
    if not CLAIMS:
        print("check-repository-map: no claims are registered — refusing to report the map checked.",
              file=sys.stderr)
        return 1
    try:
        tree = Tree(ROOT)
        map_text = tree.read(MAP)
    except (Anchor, OSError) as e:
        print(f"check-repository-map: {e}", file=sys.stderr)
        return 1
    problems, ran = check(tree, map_text)
    if problems:
        print(f"\ncheck-repository-map: {MAP} disagrees with the tree", file=sys.stderr)
        for p in problems:
            print("  " + p, file=sys.stderr)
        print(f"\n{len(problems)} disagreement(s). The tree is the truth: fix the map, or fix "
              "the tree and then the map.", file=sys.stderr)
        return exit_code(problems)
    print(f"check-repository-map: {ran} claim(s) checked against {len(tree.files)} tracked files, "
          "the map agrees with the tree")
    return exit_code(problems)


# --------------------------------------------------------------------------
# The self-test breaks the FIXTURE — the map, and the tree — because
# scripts/check-mutations.py breaks the checker and the two are different
# failures. A claim can be dead in a way no edit to this file reveals, if
# the map happens to satisfy it by another route.
# --------------------------------------------------------------------------

# (search, replace, what the edit makes the map say). Each must make the
# check FAIL. A replacement that is not present in the map is itself a
# failure: the claim moved and this case stopped testing it.
MAP_EDITS = [
    ("on the authority of `docs/release-agent.md:2-5`:",
     "on the authority of `docs/release-agent.md:200-205`:",
     "cites the release-agent quote at lines it is not on"),
    ("`internal/demo/erp/server.go`", "`internal/demo/erp/absent.go`",
     "names a source file that is not in the tree"),
    ("Genuinely unclear — three", "Genuinely unclear — two",
     "says two unresolved cases and lists three"),
    ("**`scripts/exposure-scan.sh`.** One caller", "**`scripts/gone.sh`.** One caller",
     "rests the first unclear case on a script that is not there"),
    ("| `kmx/app` | 38 |", "| `kmx/app` | 37 |",
     "gets a package's source-file count wrong"),
    ("`kaimahi-tools.yaml`, `egress-hosted.yaml`", "`egress-hosted.yaml`",
     "drops a manifest from the embedded list that embed.go embeds"),
    ("`ap-agent.yaml`, `erp-mcp.yaml`", "`ap-agent.yaml`, `erp-mcp.yaml`, `hello-world.yaml`",
     "files an embedded manifest under demonstration as well"),
    ("`plane-admin.sh`, `plane-secrets.sh`", "`plane-secrets.sh`",
     "leaves a script out of every bucket"),
    ("`getting-started.md`, `kmx.md`", "`kmx.md`",
     "leaves a doc out of every list"),
    ("Thirteen internal packages and one binary", "Twelve internal packages and one binary",
     "miscounts the plane's packages"),
    ("Postgres and eleven migrations", "Postgres and ten migrations",
     "miscounts the plane's migrations"),
    ("is called only by\n`release-run.sh`", "is called only by\n`ap-demo.sh`",
     "names the wrong caller for show-turn.py"),
    ("`scripts/release-run.sh` calls it twice", "`scripts/release-run.sh` calls it three times",
     "no longer makes the await-approval claim in a form this file can find"),
    ("`isolation.md` appears nowhere in\n`docs/README.md`",
     "`isolation.md` is listed in\n`docs/README.md`",
     "no longer makes the unfindable-docs claim"),
    ("no `require`, and no `plane/...`\nimport anywhere in root `cmd/` or `internal/`",
     "the root module imports it freely",
     "no longer makes the module-boundary claim"),
    ("| `cmd/kmx` (15 files)", "| `cmd/kmx` (14 files)",
     "gets a binary's file count wrong"),
    ("`internal/kmx/` is fifteen packages", "`internal/kmx/` is fourteen packages",
     "miscounts the packages under internal/kmx"),
    ("the\nfive `lift*.go` files in `app`", "the\nsix `lift*.go` files in `app`",
     "miscounts the cloud-running half of the AKS lift"),
    ("| `scripts/` | 22 (6 embedded in the binary, 16 operator) | 3 | 47",
     "| `scripts/` | 22 (6 embedded in the binary, 15 operator) | 3 | 47",
     "has a summary row whose own parts no longer add up"),
    ("Six image files plus a README", "Seven image files plus a README",
     "miscounts the brand assets"),
    ("including six shell scripts", "including seven shell scripts",
     "miscounts the shell scripts inside the binary"),
    ("one of the twelve checkers the\nmutation harness", "one of the ten checkers the\nmutation harness",
     "miscounts the checkers the mutation harness proves"),
    ("| `staticcheck.conf` |", "| `staticcheck.conf.gone` |",
     "leaves a root file out of its table"),
    ("60 of the 72 are named", "59 of the 72 are named",
     "miscounts which scripts anything outside names"),
    ("fourteen tracked files under `scripts/` contain the literal `k8s/`",
     "fifteen tracked files under `scripts/` contain the literal `k8s/`",
     "miscounts the scripts that would have to change if k8s/ were split"),
    ("(fourteen invocations among nineteen\nmentions", "(thirteen invocations among nineteen\nmentions",
     "miscounts how often CI runs the chat verifier"),
    ("`wc -l` reports it as 0", "`wc -l` reports it as 1",
     "gets the architecture asset's line count wrong"),
    ("The comment at the top of\n  `internal/demo/erp/server.go`",
     "The comment at the bottom of\n  `internal/demo/erp/server.go`",
     "no longer makes the ERP-evidence claim in a form this file can find"),
]


def selftest() -> int:
    import copy

    failed = 0
    tree = Tree(ROOT)
    real = tree.read(MAP)

    problems, ran = check(tree, real)
    if problems:
        print("FAIL the map does not pass on the real tree, so no case below proves anything:",
              file=sys.stderr)
        for p in problems:
            print("     " + p, file=sys.stderr)
        return 1
    print(f"ok   the real map passes ({ran} claims), so a failure below is the edit and not the tree")
    if not CLAIMS:
        print("FAIL no claims are registered")
        return 1

    proven: set[str] = set()

    def note(problems: list[str]) -> None:
        for p in problems:
            m = re.match(r"\[(\w+)\]", p)
            if m:
                proven.add(m.group(1))

    for find, replace, what in MAP_EDITS:
        if real.count(find) != 1:
            print(f"FAIL the map has {real.count(find)} places matching {find!r}, not one — "
                  f"the case '{what}' has stopped testing anything")
            failed += 1
            continue
        problems, _ = check(tree, real.replace(find, replace))
        note(problems)
        if problems:
            print(f"ok   a map that {what} is caught ({len(problems)} disagreement(s))")
        else:
            print(f"FAIL a map that {what} was reported as agreeing with the tree")
            failed += 1

    # The other side of the comparison, which no edit to the map can
    # reach: a file appears in the tree and nobody has said what it is.
    for phantom, where in [("scripts/brand-new.sh", "scripts/"),
                           ("docs/brand-new.md", "docs/"),
                           ("k8s/brand-new.yaml", "k8s/"),
                           ("brand-new.txt", "the repository root"),
                           ("newarea/thing.txt", "a top-level directory the map has no section for")]:
        grown = copy.copy(tree)
        grown.files = sorted(tree.files + [phantom])
        problems, _ = check(grown, real)
        note(problems)
        if problems:
            print(f"ok   an unclassified file in {where} is caught ({len(problems)} disagreement(s))")
        else:
            print(f"FAIL a new file in {where} was not noticed by any claim")
            failed += 1

    # And the empty case, from both ends.
    try:
        Tree(ROOT, files=[])
        print("FAIL an empty tree was accepted")
        failed += 1
    except Anchor:
        print("ok   an empty tree is refused rather than reported clean")
    problems, ran = check(tree, "")
    if problems and ran == 0:
        print("ok   an empty map is refused rather than reported clean")
    else:
        print(f"FAIL an empty map produced {len(problems)} problem(s) over {ran} claim(s)")
        failed += 1
    problems, _ = check(tree, "# a map with no sections at all\n\nnothing here.\n")
    if problems:
        print("ok   a map that makes none of these claims is refused")
    else:
        print("FAIL a map that makes none of these claims was reported clean")
        failed += 1

    # A tree whose files are listed and cannot be read. Unreadable is not
    # clean: a claim that could not open what it checks has not checked it,
    # and a checker that treats a missing file as agreement is one that
    # gets quieter the more the tree moves.
    import tempfile
    with tempfile.TemporaryDirectory() as tmp:
        hollow = Tree(pathlib.Path(tmp), files=tree.files)
        problems, _ = check(hollow, real)
        note(problems)
        if any("could not read" in p for p in problems):
            print("ok   a tree whose files cannot be read is reported, not passed")
        else:
            print(f"FAIL a tree with no readable files produced {len(problems)} problem(s), "
                  "none about reading")
            failed += 1

    # The standing question, emptied in the one way that is otherwise
    # consistent: the cases deleted AND the heading honestly saying zero.
    emptied = re.sub(r"(## Genuinely unclear — )three(, and this is a result\n)(?:.|\n)*?(?=\n## )",
                     r"\1zero\2\nNone.\n", real)
    if emptied == real:
        print("FAIL the Genuinely unclear section could not be emptied — the case has stopped "
              "testing anything")
        failed += 1
    else:
        problems, _ = check(tree, emptied)
        note(problems)
        # Named, not merely counted: other claims anchor into that section
        # too, and their complaints would make this case pass without the
        # guard it exists to prove ever running.
        if any(p.startswith("[the_unclear_cases_survive]") for p in problems):
            print("ok   a map whose standing question has emptied itself is refused")
        else:
            print("FAIL the standing question emptied itself and the claim that guards it "
                  f"said nothing ({len(problems)} unrelated problem(s))")
            failed += 1

    # And the verdict itself.
    if exit_code(["a disagreement"]) == 1 and exit_code([]) == 0:
        print("ok   a run with disagreements exits non-zero and a clean one exits zero")
    else:
        print("FAIL the verdict does not follow from the findings")
        failed += 1

    # The question scripts/check-mutations.py cannot ask, because it never
    # touches the fixture: for each assertion, what would have to change
    # for it to fail? A claim no case above breaks is a claim whose
    # fixture satisfies it by some other route, and it is not proven.
    for fn in CLAIMS:
        if fn.__name__ not in proven:
            print(f"FAIL nothing in this self-test breaks [{fn.__name__}], so nothing shows it "
                  "would notice")
            failed += 1

    if failed:
        print(f"\ncheck-repository-map self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print(f"\ncheck-repository-map self-test: {len(CLAIMS)} claims, each broken by at least one "
          f"of {len(MAP_EDITS)} map edits and 5 tree changes, every one caught")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
