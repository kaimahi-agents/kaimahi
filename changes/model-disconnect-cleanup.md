### Fixed

- Guest model disconnect recognizes Kubernetes' native generation change during foreground deletion while retaining resource ownership and execution-spec checks.

### Changed

- Model setup JSONL can include a fixed `error.reason` for resource observation failures. Raw causes, credentials and resource contents remain withheld; existing error codes, messages and recovery guidance are unchanged.
