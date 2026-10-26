### Changed

- Source Copilot plugins now carry paired kmx release versions. Tagged releases and named rehearsals check both source versions, including exact RC suffixes, before building. Synthetic rehearsals still check source consistency.
- The macOS/PATH source plugin has its own `kmx-agent-builder-path` identity. WSL and legacy native bundles keep `kmx-agent-builder`; the existing direct install commands are unchanged. Install only one variant per host profile.

### Upgrading

- Before installing the renamed PATH source plugin, remove an existing direct PATH installation under its old `kmx-agent-builder` name. This does not update the kmx binary. No marketplace installation is introduced by this change.
