### Added

- File Run/Test can use selected stdio MCP servers from folder `kmx.yaml`, with repeatable `--mcp-server` and a `--no-mcp-servers` override for recorded-only CI. Launch environment and mappings are frozen, Disabled/replayed tools do not launch, and cleanup is checked before completion/capture. App-host tools and native Windows policy are unchanged.
