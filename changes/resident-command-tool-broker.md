### Added

- Resident local Markdown agents can use enabled fixed `commandTools` through a
  separate sibling broker process. The model worker receives typed calls/results
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
