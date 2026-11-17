### Changed

- Guest model Connect returns sanitized available choices with a missing-model refusal for Copilot and API-key connections, while retaining the connection needed for cleanup. Unavailable catalogs remain distinct from empty lists.
- OpenAI selected routes for `gpt-4.1`, `gpt-4.1-mini`, `gpt-4.1-nano`, `gpt-4o` and `gpt-4o-mini` declare both Responses and Chat Completions without narrowing account discovery.
