### Added

- Bare `kmx test` tests direct agent files in the current folder and `.github/agents/`, using their companion tests, with one text/JSON/GitHub summary. Assertion failures continue; operational failures abort and report remaining files as unrun. Unknown or incomplete sets exit 2, failures exit 1, all-pass exits 0. `--tests` and `--case` still require an explicit agent file. Native workspace evaluation remains `kmx package test`; guarded native Windows still requires an explicit file.
