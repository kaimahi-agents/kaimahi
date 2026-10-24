### Breaking

- Remove `kmx suite diff` and its unified, split and JSON package-comparison
  output. The upstream AgentSuite `oci.DiffPaths` API remains available.

### Upgrading

- Stop invoking `kmx suite diff` in scripts. There is no replacement package-diff
  command; `kmx test diff` still compares passing evaluation receipts, not packages.
