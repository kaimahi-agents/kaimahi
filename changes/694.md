### Fixed

- Cancelling a Copilot agent-file Run/Test during judging leaves the interrupted test and later tests `not_run`, without partial answers or passing checks. Fully finished tests keep their results. MCP clients cancel the existing request and recover the safe result through the agent resource; no new tool or canvas UI change is included.
