# Per-PR release notes

Add one Markdown fragment named after the pull request, for example `changes/627.md`.
Use a unique flat lowercase slug while the PR number is unknown: replace branch
name separators such as `/` with `-`, or choose another valid name. Then rename
it to the actual PR number. Each PR owns its fragment instead of editing a
shared Unreleased entry. Names are flat lowercase letters, digits, dots, hyphens
or underscores, start with a letter/digit and end in `.md`; no `..`, whitespace,
subdirectories or symlinks. Fragments must be regular UTF-8 files. This README
is guidance, not a release fragment.

Use the existing changelog categories: `Added`, `Changed`, `Fixed`, `Breaking`,
and `Upgrading`. Include each category at most once, only with a nonempty note:

```markdown
### Changed

- Describe the operator-visible change and any action required.
```

Use absolute URLs for links so moving a note into the root changelog does not
change its meaning. Do not add level-two (`## `) lines, even inside code fences:
the release extractor treats those as version boundaries. Unknown categories,
empty fragments and empty category bodies are errors.

## Validation and exemptions

```bash
python3 scripts/check-changelog-fragments.py
base=$(git merge-base origin/main HEAD)
python3 scripts/check-changelog-fragments.py --base "$base" --head HEAD
python3 scripts/check-changelog-fragments.py --selftest
```

Default validation checks all pending fragments. Supply **both** Git revisions
to enforce the PR requirement; unavailable comparisons fail, not skip. Product
changes require a newly added valid fragment in that committed diff. An old
pending note or an uncommitted new file does not count. A product PR may not
modify, delete or rename another existing fragment. There is no label, API or
tooling opt-out.

The closed docs/test-only exemption covers `docs/**`, any `*.md`, `*_test.go`,
files under a `testdata/` directory at any depth, and direct children of
`scripts/` matching `test_*.py`, `*-test.py` or `*-test.sh`. Everything else,
including CI, tooling, internal embedded assets and other shipped assets, needs
a new fragment. Deletions and both sides of renames count. A docs-only release
preparation may remove reviewed fragments after including them in the changelog.

## Prepare a stable release without losing notes

Keep existing Unreleased entries and released history in `CHANGELOG.md` until
release preparation. The helper prints a complete candidate to stdout; it never
edits the changelog or deletes notes. Use a stable `vX.Y.Z` and an explicit valid
calendar date; release-candidate versions are not render targets.

```bash
included=$(mktemp)
notes=$(mktemp)
python3 scripts/check-changelog-fragments.py --list > "$included"
python3 scripts/check-changelog-fragments.py --render vX.Y.Z --date YYYY-MM-DD > "$notes"
```

Substitute the intended stable version and calendar date. Stop if either command
fails. Review **both** outputs against the same frozen inputs before copying the
candidate to `CHANGELOG.md`. Listing prints only the included fragment paths,
one per line; failures go to stderr. Rendering validates all inputs before any
output, rejects an already-present target version, preserves the preamble,
empty Unreleased heading and historical tail bytes, and moves old Unreleased
entries before the fragments. Categories appear once in the fixed order Added,
Changed, Fixed, Breaking, Upgrading. Fragments sort lexicographically by filename:
`1000.md` comes before `623.md`, not in numeric PR order.

After copying and reviewing the candidate, remove **only** the exact paths in
the reviewed listing, for example:

```bash
while IFS= read -r path; do
  git rm -- "$path"
done < "$included"
```

Never wildcard-delete `changes/*.md`: that would also remove this README or
new incoming notes. If any input changes or another PR merges, regenerate and
review the candidate **and** listing before copying or removing anything. The
helper deliberately does not guess whether similar bullets were already copied
into history; manual copying or incomplete cleanup can duplicate notes. Review
that cleanup explicitly. A new release cycle includes only the remaining notes
and new Unreleased entries, leaving the prepared release untouched.

The existing [release process](https://github.com/kaimahi-agents/kaimahi/blob/main/docs/releases.md)
and release-note extractor still own publication: RCs continue to fall back to
their base release's notes, and the dry run still reads Unreleased. Rendering
changes neither behavior.
