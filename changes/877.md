### Fixed

- Guest model disconnect recognizes Kubernetes' native generation change during foreground deletion, handles resources disappearing between read-only observations, and accepts static-Pod identities in the process inventory, while retaining resource ownership, execution-spec and process-stop checks.

### Changed

- Model setup JSONL can include a fixed `error.reason` for resource observation failures. Raw causes, credentials and resource contents remain withheld; existing error codes, messages and recovery guidance are unchanged.
