### Added

- `kmx cluster up -o jsonl` and `kmx cluster orka install -o jsonl` provide bounded
  safe progress/results with conservative partial effects and cooperative cancel.
  Default text and existing JSON formats are unchanged; version capabilities
  advertise setup JSONL on Unix only. File-Test streaming is not yet available.
