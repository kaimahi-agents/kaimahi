### Added

- Add internal, operation-unwired private filesystem mutation primitives and
  explicit creating/non-creating state-root resolution. Native Windows amd64
  tests cover protected local-NTFS creation, process-held locks, exclusive
  publication and anchored flushes; arm64 remains compile/vet only, and the
  guarded Windows CLI and release assets remain unchanged.
