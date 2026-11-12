### Fixed

- Endpoint worker startup verifies exact-model catalog access before readiness,
  preserving the old worker and sessions when an upgrade cannot reach or
  authenticate to its replacement endpoint.
- Reuse endpoint connection pools across turns with request-local credentials.
- Keep default output budgets within the frozen declared model limit without
  weakening explicit output requirements.
- Read Ollama metadata from explicit HTTPS endpoints and gateway prefixes using
  the selected credential reference, without redirects or inference.
- Register the extracted filesystem consumers and normalize macOS test fixture
  paths. Exercise resident lifecycle tests on Linux and macOS in model-access CI.
