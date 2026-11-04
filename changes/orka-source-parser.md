### Added

- Add a source-only native Orka agent-file contract, separate from host previews. Native authoring, compilation and execution remain separate follow-up work.

### Changed

- Files declaring a `kmx` native profile refuse host preview instead of silently ignoring native settings. Ordinary unmarked agent files keep their existing preview behavior.
