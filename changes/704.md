### Added

- Install the Windows Copilot plugin directly from `kaimahi-agents/kaimahi:plugins/copilot-wsl`. It includes shared skills and canvas assets and starts `/usr/local/bin/kmx` through WSL, without a bundled engine or folder/model setup.

### Changed

- Unbound MCP previews use Copilot for the authored real model when no provider is configured. Authored `fake`, explicit routes and legacy root-bound behavior remain unchanged.
- Replace the interactive try-it installer with the plain Linux-binary and repository-plugin install steps. Native bundle packaging and its isolated release installation gate remain available.
