### Added

- MCP Run/Test accept a separate version-3 selected-pair context from the read-only source resource. Run answers without checks; Test evaluates them, with exact source and selection identity. Legacy/native versions stay separate.
- Pair writes, capture and restore are not enabled. Pending recording capture refuses before any model send; frozen recordings remain replayable. Refresh process-local source contexts after source changes, eviction or restart.
