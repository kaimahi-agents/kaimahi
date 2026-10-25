### Fixed

- The agent canvas shows when Copilot approval is waiting in chat and distinguishes a host-tool approval deadline from a test that did not run. The existing deadline is unchanged; a pending tool can still execute if approved after it expires.
- Pending tests show Waiting while Stop settles. Live text-check failures use the same explanation as completed results.
- Invalid agent-file test fields are identified as a file-format problem when opening the canvas, rather than an unavailable MCP server.
