### Fixed

- Synthetic release rehearsals include the workflow run ID and attempt in the
  stamped version, keeping repeated same-commit runs' binaries and checksum
  manifests distinct for transparency-log signing. Real tag versions and
  explicitly supplied dry-run versions are unchanged; signing and log upload
  remain required.
