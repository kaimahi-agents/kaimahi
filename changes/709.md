### Fixed

- Create agent files on Windows-backed WSL mounts that lack no-replace rename support, without overwriting existing files. Use hard-link publication where available; mounts without hard links use exclusive creation and a synced copy, which is not atomically visible.
