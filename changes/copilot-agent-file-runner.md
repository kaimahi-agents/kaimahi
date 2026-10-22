### Added

- Markdown-file `kmx run FILE` and `kmx test FILE`, plus their shared MCP path,
  accept explicit `--provider copilot` using prior model login and an exact
  authored Chat model. The endpoint is pinned, credentials stay in the existing
  model-access transport, and safe Copilot failures stop later sends. Selecting
  Copilot sends instructions and inputs (plus answers/rubrics for judging) to
  the hosted service. Execution remains tool-free preview without a Sessions
  journal, inference retries, provider/model fallback or deployment evidence;
  default fake/local behavior is unchanged.
