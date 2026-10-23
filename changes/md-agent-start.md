### Added

- Experimental persistent Copilot-native Markdown agents with paired tests,
  shared local sessions, short IDs, lifecycle/activity inventory, interactive
  editing/import, explicit version updates, removal and completion.
- Automatic Markdown-to-AgentSuite conversion in deploy/lift, plus offline suite
  map; native evaluation/publication gates remain mandatory. Demonstrated a
  two-case kind evaluation, ACR publication and fresh remote AKS answer before
  rebasing; clean-machine onboarding qualification remains outstanding (#604).
- YAML presentation for JSON-capable reports and UTC-start-based agent age.

### Changed

- Local status aliases list (running by default, --all includes stopped); info
  reports local setup. Canonical Squad charter Run/Test and MCP behavior remain
  available alongside explicit --type md native-header instances.

### Fixed

- Align the PTY transcript regression with the solid user-message rail while
  retaining checks for preserved tool details and removal of the input frame.
