### Added

- File Run/Test reads per-tool Enabled/Disabled settings from the agent folder's committed `kmx.yaml`, defaulting to Enabled. The canvas shows the states read-only, results retain the settings used, and Disabled answers never execute or count as live calls. Native permissions and existing call, size, journal and replay bounds stay unchanged (#698).
