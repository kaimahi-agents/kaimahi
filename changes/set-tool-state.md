### Added

- Add the revision-checked `set_tool_state` MCP tool to set one declared tool Enabled or Disabled in its agent folder’s `kmx.yaml`, preserving other keys. The original server exposes five tools; internal preview servers retain four. Read-only agent resources include an opaque settings revision; uncertain writes remain explicit and are never retried automatically.
