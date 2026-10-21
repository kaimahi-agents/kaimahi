# Per-PR release notes

Add one Markdown fragment named after the pull request, for example `changes/623.md`.
Use a unique lowercase branch name while the PR number is unknown. Each PR owns
its fragment instead of editing a shared Unreleased entry.

Use the existing changelog categories: `Added`, `Changed`, `Fixed`, `Breaking`,
and `Upgrading`. Include only categories with a nonempty note:

```markdown
### Changed

- Describe the operator-visible change and any action required.
```

Use absolute URLs for links so moving a note into the root changelog does not
change its meaning. Do not add version headings to a fragment. Keep existing
Unreleased entries and released history in `CHANGELOG.md`; release preparation
combines those entries with the pending fragments.

Documentation/test-only changes do not require a fragment. Other changes do.
Do not remove pending notes until a reviewed release-preparation change includes
them in the changelog. This README is guidance, not a release fragment.
