### Added

- A Copilot-default Release writer sample and a checksum-verified, user-confirmed
  setup helper for macOS and Windows Copilot with WSL. The preview guide explains
  the chat-based Run/Test loop, model changes, uninstall and safe setup logs.
- Native Linux/macOS isolated plugin install checks on pull requests and on the
  exact cross-built release archives before publication. The helper uses HTTPS
  and checksums, not independent signature verification; older bundles refuse.
