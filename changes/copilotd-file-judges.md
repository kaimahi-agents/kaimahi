### Added

- `kmx test FILE --runner copilotd` evaluates `llmJudge` assertions using kmx's separate tool-free judge transport. The agent still runs on the host. Judge samples share the case deadline, and unavailable, malformed or split scores stay unknown. Delegating agents report unknown, including child work linked by tools with other names. Recorded fixtures, capture and Disabled tools remain unsupported on this runner.
