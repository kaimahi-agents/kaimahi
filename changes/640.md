### Fixed

- Copilot plugin integration tests consume structured preview results rather than
  parsing compact human summaries as JSON. The skill preserves completed example
  failures as normal tool outcomes, without treating `isError: false` as a pass.
