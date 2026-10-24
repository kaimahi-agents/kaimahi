### Added

- The Copilot canvas can choose a model for a run without changing the agent file.
A runtime choice overrides the authored model; omitting both uses `gpt-5.6-terra`.
Model choices load on demand through a read-only MCP resource. Unknown models
report available choices without switching models. Changing models makes old
results stale until a new run confirms the selected model.
