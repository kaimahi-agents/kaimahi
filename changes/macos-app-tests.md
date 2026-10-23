### Fixed

- Resolve macOS temporary paths in App test fixtures, remove a Linux-only shell
  utility path and synchronize pagination cancellation with the intended request.
  Local `make test-app` artifact directories honor `TMPDIR` on macOS.

### Changed

- Run all four App test shards on both Linux and macOS in CI, with OS-specific
  diagnostic artifacts and both platforms required by the hygiene gate.
