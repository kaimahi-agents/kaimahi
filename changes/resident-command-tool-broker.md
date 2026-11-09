### Added

- Resident local Markdown agents can use enabled fixed `commandTools` through a
  separate sibling broker process. The model worker receives typed calls/results
  but no configured executable or argv; configuration and executable content
  identities participate in lifecycle drift. The initial credential-free bounded-process backend retains fixed argv,
  explicit empty environment, output limits and process-group cleanup and is
  disclosed as guarded execution rather than a sandbox.
