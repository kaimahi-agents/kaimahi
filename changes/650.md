### Fixed

- Accept native absolute Create/Edit/Run/Test paths whose cleaned, symlink-resolved targets remain inside the configured MCP root and supported agent-file layouts. Preserve rooted file access and refuse outside targets, dangling links and symlink escapes.
