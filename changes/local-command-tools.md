### Added

- File Run/Test can execute declared fixed-argument command tools configured in
  the agent folder's `kmx.yaml`, with explicit named environment references,
  bounded checked diagnostics, recorded replay and Unix process-group cleanup.
  This focused slice was extracted from the broader prototype in
  https://github.com/kaimahi-agents/kaimahi/pull/654 for
  https://github.com/kaimahi-agents/kaimahi/issues/698.
