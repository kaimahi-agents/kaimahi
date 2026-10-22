### Fixed

- MCP Run/Test now return completed failing or inconclusive assertions as normal tool results with a compact, bounded summary instead of duplicated JSON. Full safe structured results remain available. Parse, model, execution, cancellation and journal errors remain tool errors; CLI exit codes are unchanged.
