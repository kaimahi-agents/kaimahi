### Added

- `kmx version -o json` reports versioned binary capabilities without loading
  configuration or accessing credentials. Version argument errors use a bounded,
  fixed-message machine result. Text defaults and existing command JSON remain
  unchanged; setup and file-Test JSONL are not yet available.
