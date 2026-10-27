### Changed

- Root help leads with Create, Run, Test, Chat and Model, followed by Package,
  Cluster and Version. MCP, Completion and Help remain callable but are omitted
  from the root help list; MCP and Completion are also omitted from suggestions.
- Inner-loop help is concise with examples and compact flags. File Run/Test
  storage, privacy and evidence caveats are collected in `docs/local-preview.md`;
  Run distinguishes file-preview flags from live-Orka flags. Global flag parsing,
  supported operations and retained legacy Eval/Lift help remain unchanged.
- Canonical flag-parsing errors point to the resolved command's help, without
  repeating argument values. Legacy refusals and exit codes stay unchanged.
