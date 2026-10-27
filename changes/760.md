### Added

- `kmx test FILE -o json` now includes total and per-test timings plus effective
  judge and content-free tool setting/source/call details. Optional
  `--github-summary` appends the same safe result to GitHub Actions while
  preserving Test exit codes; saved JSON reports remain local-preview diagnostics,
  not package evidence or publication/deployment authority.
