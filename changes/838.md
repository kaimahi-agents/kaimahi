### Added

- Add explicit native Orka source parsing and checked companion loading, preserving original bytes, Soul/Skill references, source-pair revisions and ordered tests with unsupported-content diagnostics. These are library-only foundations; native file authoring, compilation and execution remain separate follow-up work.

### Changed

- Files declaring a `kmx` native profile refuse host preview instead of silently ignoring native settings. Ordinary unmarked agent files keep their existing preview behavior.
