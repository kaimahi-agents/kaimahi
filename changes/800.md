### Added

- Read-only MCP selected-source/history resources preserve exact agent/tests bytes and label legacy history as agent-only. Paired execution, edits, capture and restore remain unavailable.
- Offline `kmx package run-profile export FILE [--tests FILE]` exports external tests as a versioned v2 profile with physical selected-source identity. Inline profiles retain v1; stdout is private content, not test or deployment evidence.
