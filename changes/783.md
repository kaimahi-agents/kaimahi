### Fixed

- Start the macOS Copilot plugin's MCP server at `/usr/local/bin/kmx`, matching the documented installer destination without relying on a Dock-launched app's PATH. The pinned rc.4 plugin is unchanged; until a newer paired release, configure a local stdio server with command `/usr/local/bin/kmx` and argument `mcp`, then start a fresh session.
