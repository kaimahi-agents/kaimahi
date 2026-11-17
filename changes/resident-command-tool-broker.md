### Added

- Resident local Markdown agents can use enabled fixed `commandTools` through a
  separate sibling broker process with fake/Copilot inference. Other endpoint
  routes remain tool-free. The model worker receives typed calls/results
  but no configured executable or argv; configuration and executable content
  identities participate in lifecycle drift. The initial credential-free bounded-process backend retains fixed argv,
  explicit empty environment, output limits and process-group cleanup and is
  disclosed as guarded execution rather than a sandbox.

- Separate portable tool contracts from private local bindings. Bindings record
  distinct agent/tool runtimes, actual filesystem/network exposure, credential
  references without values, and concurrent/per-turn/per-session/per-instance
  call budgets. Apple-container, Linux-native and rootless-OCI policies are
  representable but fail closed until their launchers are implemented; local
  bindings and execution remain ineligible for package or Deploy authority.

- Add broker-owned compatibility/readiness checks and schema-constrained command
  arguments. Activation probes use the frozen executable and return only safe pass
  status; `kmx run FILE --check-tools` runs them without a model. Dynamic values remain
  single argv elements restricted to reviewed enums/anchored strings, clean
  relative paths or bounded integers, with no shell or model-selected subcommand.

### Fixed

- Preserve cleanup-failure classification through sanitized tool errors, including
  cancellation, so uncertain cleanup refuses later broker calls and successful
  Stop. Protect inherited worker descriptors before credential helpers run and
  close command exit observers even after failed waits. Darwin accepts zombie-only
  process-group signal refusal only after complete owned-group observation.
- Keep required resident CI hermetic by excluding the exact opt-in hosted MCP
  test while retaining the no-skip gate. Resolve macOS test temporary roots before
  checked filesystem reads without weakening production symlink refusals.

- Match argument/probe regexes against the complete value, including alternations.
  Budget startup for all declared probes and cleanup; cancellation/EOF stops owned
  probe groups, and inherited broker descriptors cannot reach commands.
- Bind complete resolved inputs and exact schemas before activation, reject
  unsupported credential resolvers/backends, and reject repeated session-scoped
  call keys without replay. Retain cleanup uncertainty and credential checks across
  normalized command output and model/tool boundaries.
- Record host-user network ingress/egress separately from broker dispatch. Tool
  effects are conservatively mutating. Sessions API reuse and private tool/binding
  digests do not claim durable Sessions journaling or public AgentSuite artifacts.
  See `docs/resident-tools.md` for the tested setup demo and primitive mapping.
