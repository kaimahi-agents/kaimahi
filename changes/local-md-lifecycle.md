### Added

- Add explicit lifecycle registration for local Markdown agents with
  `run FILE --persist`, local `list`, and `stop`. Run/Test continue through the
  canonical invocation-owned file path. Lifecycle records store no credentials
  or tool processes; owner-only
  snapshot cache retains exact authored agent/test bytes for local Run.
