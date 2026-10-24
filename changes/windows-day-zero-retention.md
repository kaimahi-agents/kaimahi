### Changed

- Windows day-zero internals now use checked private storage for local-preview
  journals and retained evaluation-failure diagnostics. Native amd64 package
  tests cover fake preview/replay, interrupted clear recovery and publication
  uncertainty. A guarded command policy is staged for phase 5, while the Windows
  CLI still does not build or ship and deferred operations remain unavailable.
