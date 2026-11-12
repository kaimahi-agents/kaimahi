### Added

- Resident Markdown agents use shared model-route discovery for Ollama and
  Foundry Local, and support explicit OpenAI-compatible and Anthropic Messages
  endpoints. Keys are environment references transferred privately to workers.
- Match context/output token minima and reasoning requirements using exact
  operator-declared model profiles or selected Ollama metadata. Unknown
  capabilities refuse required matches. `model match` inspects profiles offline;
  resident `modelRequirements` frontmatter and stronger Run flags bind selection.
