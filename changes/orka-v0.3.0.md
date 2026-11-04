### Changed

- Fresh Orka installations pin the SHA-256-verified v0.3.0 Helm chart and all eight runtime image overrides. Offline schemas default to v0.3.0; explicit v0.2.0, v0.1.3 and the historical `main` snapshot remain available.

### Fixed

- Apply verified chart CRDs one at a time with server-side apply and field manager `kmx`, including server dry-run, because v0.3.0 CRDs exceed client-side annotation limits. A conflict names the CRD, preserves kubectl's ownership diagnostics and stops before later CRDs or Helm; no forced ownership transfer, replace or adoption is attempted. Stock Helm CI uses the same non-forcing server-side flags.

### Upgrading

- KMX still supports fresh Orka installations only, not a v0.2.0 runtime or SQLite migration. Existing old, foreign or partial installations are refused. Export and verify backups before replacing a target; local kind deletion loses Tasks, Secrets and volume-backed data. Keep the snapshot encryption key with the controller volume, and never print it.
