### Added

- Open and test Copilot `.agent.md` files with YAML frontmatter and a free-form prompt in the canvas and file CLI. Charter headings and inline Examples are optional. Preserve unknown frontmatter and prompt formatting on edits.

### Changed

- Copilot agents with omitted tools run tool-free with an explicit notice and an Add tools action in the canvas. Exact model IDs and explicit tool declarations use existing preview execution; unmapped host settings remain visible and preserved.
- Before adding or changing inline tests in a Copilot file, the canvas and MCP ask for acknowledgment that Copilot includes those tests in its prompt. Existing kmx charters keep working unchanged.
