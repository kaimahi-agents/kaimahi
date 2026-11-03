### Added

- Companion files now extend canonical CLI Run/Test without switching provider,
  journaling or exit semantics; persistent execution requires explicit --type md.
  Add composed snapshot identity and an approved CLI-command host adapter, with
  recorded/live source distinction and explicit nonzero command exit observations.
  Multi-file recording writes/history and native Windows companions refuse or
  disclose their unsupported boundary rather than silently ignoring authored input.
- Preserve created console definitions across refresh through symlinked project roots.

- Same-stem agent/tool/test files work beside a standalone agent or inside one
  directory. Create writes NAME/NAME.agent.md, NAME.tools.yaml and NAME.tests.md;
  import --into copies exact source and referenced companions without execution.
  Tool/test indexes support bounded one-level file organization. Tool companion
  digests bind approval and test identity, while running grants retain frozen specs.
  Retire inline executable fences and consolidate the duplicate spike guides.

- Console file-pane creation collects name, custom instructions and optional model,
  authors a definition with empty tests, and selects the unstarted draft. An optional
  hosted tool-suggestion pass supports per-tool review and source-only acceptance.
  Cluster panes label their existing authored-workspace action as deployment.

- Console agent panes use compact aligned tables when their available width fits
  name/type/state/model, adding version/provider/tool counts at wider sizes and
  falling back to multiline entries in narrow side-by-side panes.

- Interactive chat uses a lighter filled input surface with the selected agent
  beneath the draft, replacing the repeated per-line YOU prompt.

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
