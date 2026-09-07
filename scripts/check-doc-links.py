#!/usr/bin/env python3
"""Refuse broken relative links in the repo's Markdown.

Every link target that is not an absolute URL must resolve to a file in
the tree, and a `#fragment` on a Markdown target must match a heading in
that file under GitHub's slug rules (lowercase, punctuation dropped,
spaces to hyphens, duplicates suffixed -1, -2, ...).

A link is not only `[text](target)`. Markdown documents on GitHub carry
four forms that all break the same way, and this checker reads all of
them:

  * inline links and images, `[text](target)` and `![alt](target)`;
  * HTML `<img src="...">`, which is how the README's hero and
    architecture pictures are written — attributes spread over several
    lines, so the tag is read as a whole, not line by line;
  * HTML `<a href="...">`, the same tag family, for when a picture or a
    badge is wrapped in a link;
  * reference links, `[text][label]` and `[text][]`, resolved through a
    `[label]: target` definition elsewhere in the file. A definition is
    checked whether or not anything uses it, which is also what covers
    the shortcut form `[label]`: the target is verified at the
    definition, so every way of referring to it is verified with it.

For a while this checked only the first form, and the two pictures at the
top of the README — the most-looked-at links in the repository — were not
checked at all.

Fenced code blocks are not links, and neither is anything inside a
`backtick` code span: both are blanked out before the scan, so a sample
link in an example stays an example.

The docs were restructured by capability once; the old phase-named files
are stubs that forward. This is what keeps every forward, every README
pointer, and every FAQ anchor honest from now on.

Run:  python3 scripts/check-doc-links.py [file.md ...]
      (no arguments: every tracked *.md, plus untracked ones git would add)
      python3 scripts/check-doc-links.py --selftest
"""
import os
import re
import subprocess
import sys
import tempfile

LINK = re.compile(r"(?<!\\)\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
FENCE = re.compile(r"^\s*(```|~~~)")
CODESPAN = re.compile(r"`[^`\n]*`")
# The HTML tags are matched across newlines on purpose: the README writes
# its images with one attribute per line, and a line-by-line reader sees
# an `<img` with no src and nothing to check.
IMG = re.compile(r"<img\b[^>]*?\bsrc\s*=\s*[\"']([^\"']*)[\"']", re.IGNORECASE | re.DOTALL)
AHREF = re.compile(r"<a\b[^>]*?\bhref\s*=\s*[\"']([^\"']*)[\"']", re.IGNORECASE | re.DOTALL)
REF_DEF = re.compile(r"^ {0,3}\[([^\]]+)\]:[ \t]*<?([^>\s]+)>?", re.MULTILINE)
REF_USE = re.compile(r"(?<!\\)\[([^\]]*)\]\[([^\]]*)\]")


def slugs(path):
    """Heading anchors as GitHub renders them."""
    seen = {}
    out = set()
    in_fence = False
    with open(path, encoding="utf-8") as f:
        for line in f:
            if FENCE.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            m = HEADING.match(line)
            if not m:
                continue
            text = m.group(2)
            text = re.sub(r"`([^`]*)`", r"\1", text)          # code spans
            text = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", text)  # links
            text = re.sub(r"[*_]", "", text)                  # emphasis
            slug = text.strip().lower()
            slug = re.sub(r"[^\w\- ]", "", slug)
            slug = re.sub(r" ", "-", slug)
            n = seen.get(slug, 0)
            seen[slug] = n + 1
            out.add(slug if n == 0 else f"{slug}-{n}")
    return out


def tracked_markdown():
    ls = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
        check=True, capture_output=True,
    ).stdout.decode()
    return [p for p in ls.split("\0") if p.endswith(".md")]


def prose(path):
    """The file's text with everything that is not prose blanked out.

    Fenced blocks and code spans become spaces of the same width, so line
    numbers and offsets still point at the real file, and an example link
    in a code sample is not mistaken for a link the repository has to
    honour."""
    out = []
    in_fence = False
    with open(path, encoding="utf-8") as f:
        for line in f:
            if FENCE.match(line):
                in_fence = not in_fence
                out.append("\n")
                continue
            if in_fence:
                out.append("\n")
                continue
            out.append(CODESPAN.sub(lambda m: " " * len(m.group(0)), line))
    return "".join(out)


def label(raw):
    """Reference labels are case-insensitive and collapse their spaces."""
    return " ".join(raw.split()).lower()


def check(path, slug_cache):
    problems = []
    text = prose(path)

    def lineno(pos):
        return text.count("\n", 0, pos) + 1

    def verify(target, pos, what):
        if re.match(r"^[a-z][a-z0-9+.-]*:", target) or target.startswith("//"):
            return  # absolute URL, mailto:, etc.
        if target.startswith("<") and target.endswith(">"):
            target = target[1:-1]
        file_part, _, frag = target.partition("#")
        if file_part:
            resolved = os.path.normpath(os.path.join(os.path.dirname(path), file_part))
        else:
            resolved = path
        if not os.path.exists(resolved):
            problems.append(f"{path}:{lineno(pos)}: missing {what} {target}")
            return
        if frag and resolved.endswith(".md"):
            if resolved not in slug_cache:
                slug_cache[resolved] = slugs(resolved)
            if frag.lower() not in slug_cache[resolved]:
                problems.append(f"{path}:{lineno(pos)}: no heading for #{frag} in {resolved}")

    for m in LINK.finditer(text):
        verify(m.group(1), m.start(), "target")

    for m in IMG.finditer(text):
        verify(m.group(1), m.start(), "image source")

    for m in AHREF.finditer(text):
        verify(m.group(1), m.start(), "href")

    definitions = {}
    for m in REF_DEF.finditer(text):
        definitions[label(m.group(1))] = m
    for name, m in definitions.items():
        verify(m.group(2), m.start(), f"target for [{name}]")

    # A reference with no definition renders as literal brackets: the text
    # is still on the page, so nothing looks wrong, and the link is simply
    # gone.
    #
    # Only in a document that defines labels, though. `[a][b]` is a
    # reference link when a definition matches and ordinary text when none
    # does, so in a document with no definitions at all an index like
    # list[i][j] is prose and nothing else. Where the document does use
    # reference links, an unmatched label is a typo worth saying out loud.
    if definitions:
        for m in REF_USE.finditer(text):
            name = label(m.group(2) or m.group(1))
            if name not in definitions:
                problems.append(f"{path}:{lineno(m.start())}: no definition for reference [{name}]")

    return problems


def selftest():
    """Prove each form is really checked, by writing Markdown that is
    wrong and requiring the script to say so.

    Every case is a pair. A checker that always passes gets the good half
    right, and a checker that always fails gets the bad half right; only
    both halves together say the rule is being applied. The fixtures are
    synthetic and live in a temp directory on purpose — a self-test that
    asserted against this repository's own docs would start passing for
    the wrong reason the day someone fixed a link."""
    failed = 0

    def case(what, files, want, only=None):
        nonlocal failed
        with tempfile.TemporaryDirectory() as tmp:
            for name, body in files.items():
                p = os.path.join(tmp, name)
                os.makedirs(os.path.dirname(p), exist_ok=True)
                with open(p, "w", encoding="utf-8") as f:
                    f.write(body)
            names = only or [n for n in files if n.endswith(".md")]
            got = main([os.path.join(tmp, n) for n in names])
        if got == want:
            print(f"ok   {what}")
        else:
            print(f"FAIL {what} (exit {got}, wanted {want})")
            failed += 1

    real = {"there.md": "# There\n\n## A Real Heading\n", "pic.png": "not really a png\n"}

    # Inline links.
    case("an inline link to a file that exists passes",
         dict(real, **{"a.md": "see [there](there.md)\n"}), 0)
    case("an inline link to a file that does not exist fails",
         dict(real, **{"a.md": "see [gone](nowhere.md)\n"}), 1)

    # Anchors.
    case("an anchor that matches a heading passes",
         dict(real, **{"a.md": "see [x](there.md#a-real-heading)\n"}), 0)
    case("an anchor with no heading fails",
         dict(real, **{"a.md": "see [x](there.md#no-such-heading)\n"}), 1)

    # HTML images — the README's hero pictures are written this way, and
    # were unchecked while this checker read inline links only.
    case("an <img src> pointing at a file that exists passes",
         dict(real, **{"a.md": '<p><img src="pic.png" alt="a"></p>\n'}), 0)
    case("an <img src> pointing at a missing file fails",
         dict(real, **{"a.md": '<p><img src="gone.png" alt="a"></p>\n'}), 1)
    case("an <img> whose attributes span several lines is still read",
         dict(real, **{"a.md": '<p>\n  <img\n     src="gone.png"\n     alt="a">\n</p>\n'}), 1)
    case("...and passes when that multi-line src resolves",
         dict(real, **{"a.md": '<p>\n  <img\n     src="pic.png"\n     alt="a">\n</p>\n'}), 0)

    # HTML links.
    case("an <a href> to a file that exists passes",
         dict(real, **{"a.md": '<a href="there.md">there</a>\n'}), 0)
    case("an <a href> to a missing file fails",
         dict(real, **{"a.md": '<a href="nowhere.md">there</a>\n'}), 1)

    # Reference links.
    case("a reference link whose definition resolves passes",
         dict(real, **{"a.md": "see [there][t]\n\n[t]: there.md\n"}), 0)
    case("a reference link whose definition is dead fails",
         dict(real, **{"a.md": "see [there][t]\n\n[t]: nowhere.md\n"}), 1)
    case("a reference with no matching definition fails",
         dict(real, **{"a.md": "see [there][t]\n\n[other]: there.md\n"}), 1)
    case("a collapsed reference with a definition passes",
         dict(real, **{"a.md": "see [t][]\n\n[t]: there.md\n"}), 0)
    case("a shortcut reference is covered by checking its definition",
         dict(real, **{"a.md": "see [t]\n\n[t]: nowhere.md\n"}), 1)
    case("...and passes when the shortcut's definition resolves",
         dict(real, **{"a.md": "see [t]\n\n[t]: there.md\n"}), 0)
    case("brackets in a document that defines no labels are prose, not a link",
         dict(real, **{"a.md": "an index like list[i][j] is prose, not a link\n"}), 0)
    case("...but in a document that does use reference links they are a typo",
         dict(real, **{"a.md": "see [there][t]\n\nlist[i][j]\n\n[t]: there.md\n"}), 1)

    # Fenced blocks and code spans are examples, not claims.
    case("a dead link inside a fenced block is ignored",
         dict(real, **{"a.md": "```\n[x](nowhere.md)\n<img src=\"gone.png\">\n```\n"}), 0)
    case("...but the same link outside the fence fails",
         dict(real, **{"a.md": "[x](nowhere.md)\n"}), 1)
    case("a dead link inside a code span is ignored",
         dict(real, **{"a.md": "write `[x](nowhere.md)` to link\n"}), 0)

    # Absolute targets are somebody else's tree.
    case("an absolute URL is ignored",
         dict(real, **{"a.md": '[x](https://example.invalid/nowhere.md)\n'
                               '<img src="https://example.invalid/gone.png">\n'
                               "[t]: mailto:nobody@example.invalid\n"}), 0)
    case("...while the same paths taken as relative fail",
         dict(real, **{"a.md": '[x](nowhere.md)\n'}), 1)

    # A run that found nothing to read is not a clean run. The pair is a
    # repository with a document and a repository with none.
    for what, seed, want in (("a repository with a Markdown file is scanned", True, 0),
                             ("a repository with no Markdown file refuses to report clean", False, 1)):
        with tempfile.TemporaryDirectory() as tmp:
            subprocess.run(["git", "init", "-q", tmp], check=True, capture_output=True)
            if seed:
                with open(os.path.join(tmp, "a.md"), "w", encoding="utf-8") as f:
                    f.write("# Title\n\nsee [self](#title)\n")
            here = os.getcwd()
            try:
                os.chdir(tmp)
                got = main([])
            finally:
                os.chdir(here)
        if got == want:
            print(f"ok   {what}")
        else:
            print(f"FAIL {what} (exit {got}, wanted {want})")
            failed += 1

    if failed:
        print(f"check-doc-links self-test: {failed} case(s) failed", file=sys.stderr)
        return 1
    print("check-doc-links self-test: every link form proved by a passing and a failing fixture")
    return 0


def main(argv):
    if argv[:1] == ["--selftest"]:
        return selftest()
    files = argv or tracked_markdown()
    if not files:
        print("check-doc-links: no Markdown files to scan — refusing to report clean.", file=sys.stderr)
        return 1
    cache = {}
    problems = []
    for p in files:
        problems.extend(check(p, cache))
    for line in problems:
        print(line)
    if problems:
        print(f"check-doc-links: {len(problems)} broken link(s) in {len(files)} file(s)", file=sys.stderr)
        return 1
    print(f"check-doc-links: {len(files)} file(s), all relative links resolve")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
