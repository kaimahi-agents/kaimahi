### Added

- File `kmx run FILE` and `kmx test FILE` accept `--verbose` for content-free
  execution diagnostics and timings on stderr, including safe transport failure
  categories. JSON stdout and exit contracts are unchanged; content still
  requires `--show-content`. Diagnostic write failures stop later execution.
