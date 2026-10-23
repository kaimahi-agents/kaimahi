### Changed

- The Copilot agent canvas uses compact main and test-detail views, keeps unsaved drafts when navigating, shows known pre-edit results in grey and server-stale observations neutrally, and supports testing one selected test through the existing revision-checked MCP flow. Light and narrow layouts share the same controls.
- Canonical agent files may save an explicit empty test list while authoring. Ad-hoc Run still works; Test asks for a test. Zero-test files remain ineligible for run-profile export and cannot supply packaging or deployment evidence.

### Fixed

- Preserve drafts when an agent changes outside the canvas, with explicit Compare, Keep my edits and Use the new version choices before another save. A refused or uncertain action never restores a previous green result as a fresh test.
