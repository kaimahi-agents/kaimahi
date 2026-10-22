### Changed

- Run hermetic kagent contracts on PRs and routine main pushes, explicitly not live qualification. Move the unchanged full A2A/evaluation/push/lift proof to nightly, manual and fresh exact-commit release checks. Required checks remain intact; live failures may be detected at nightly or before release rather than on the PR.
