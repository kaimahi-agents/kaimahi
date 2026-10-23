### Added

- `kmx run-profile export FILE` writes a deterministic frozen canonical agent-file profile to stdout, with instructions, exact authored model/judge identities, ordered examples/assertions and SHA-256 hashes. Every target example is `not_run`; export is offline and is not evaluation evidence, a package or deployment authority. Output includes authored content and has no managed output-file privacy guarantee.
