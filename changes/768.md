### Changed

- Copilot source plugins are listed in the `kmx-plugins` marketplace with full release-commit pins. Choose `kmx-agent-builder` for Windows/WSL or `kmx-agent-builder-path` for macOS/PATH. Binary installation remains separate.

### Upgrading

- Replace an existing direct-source plugin registration before installing its marketplace variant. Install the paired binary, add `kaimahi-agents/kaimahi` as a marketplace, and install the selected `plugin@kmx-plugins`. Later upgrades require both the matching kmx binary and a catalog/plugin refresh; registration changes do not migrate or delete agent files.
