# Models for resident Markdown agents

Unreleased Unix/WSL resident `kmx run FILE` uses the shared `modelaccess` route
resolver. Model names are exact; KMX does not pick a different model after a
failure. `model list` discovers installed local models without downloading or
starting runtimes. `model list --provider copilot` uses the existing Copilot
catalog and credential path.

## Select a provider

```bash
kmx model list
kmx run helper.agent.md --provider ollama --model qwen3:8b
kmx run helper.agent.md --provider foundry-local --model YOUR_CACHED_MODEL
```

Omitting the URL selects the exact installed model through existing local
discovery. Foundry's URL comes from runtime status; it is never guessed. Ollama
discovery uses its usual local inventory endpoint. Supply `--provider-url` for
a different endpoint. Discovery never falls back to another provider or model.

```bash
kmx run helper.agent.md --provider compatible --model YOUR_MODEL \
  --provider-url http://127.0.0.1:8080/v1
kmx run helper.agent.md --provider openai --model YOUR_OPENAI_MODEL
kmx run helper.agent.md --provider anthropic --model YOUR_EXACT_CLAUDE_MODEL
```

OpenAI defaults to `https://api.openai.com/v1` and the `OPENAI_API_KEY`
environment variable. Anthropic defaults to `https://api.anthropic.com/v1` and
`ANTHROPIC_API_KEY`. Set the key in your shell using your usual credential
manager. `--provider-key-env NAME` selects another environment variable; it
never accepts the key value. Custom compatible endpoints can also use this flag.

Anthropic uses its Messages API with the version header and `x-api-key`.
OpenAI/compatible/local endpoints use Chat Completions. External endpoints require
HTTPS; loopback HTTP is supported. Requests do not follow redirects or retry.
Only completed text answers with the exact selected model identity are accepted;
use concrete model IDs rather than aliases that return a different identity.
Tool calls, incomplete replies, credential echoes, and model mismatches cannot
become successful answers. Private reasoning blocks are not answer content.

Endpoint environment keys are checked against authored source before snapshot
caching, then transferred through the owned worker pipe and kept only in memory. An environment key is a
startup snapshot: changing it requires `--upgrade --yes` to replace the worker.
Copilot retains its existing per-turn access/refresh behavior. Endpoint, key
reference, capabilities, and requirements are frozen with the generation. A
changed selection requires an upgrade or a parallel `--new --name NAME` instance.
Existing fake/Copilot lifecycle records remain readable. Records written with
the new route/mapping fields require this updated binary; older strict readers
may refuse them.

## Declare and match capabilities

Resident agents can declare requirements in frontmatter:

```yaml
---
name: helper
tools: []
modelRequirements:
  contextTokens: 32768
  outputTokens: 2048
  reasoning: true
---
```

Run flags `--require-context`, `--require-output`, and `--require-reasoning` can
strengthen these requirements, never weaken them. Token minima use the same
context/output contract as AgentSuite. Reasoning is a capability requirement,
not a provider-specific effort setting or a request to expose hidden thoughts.

For Ollama, required capabilities are read from the selected model's read-only
`/api/show` metadata: context length and explicit thinking/tool support. Output
limits absent from that API remain unknown. Context metadata describes the
advertised model limit, not a measurement of a running server's allocation.

For a complete reviewed mapping, create an exact endpoint profile:

```json
{
  "route": {
    "provider": "openai",
    "model": "YOUR_EXACT_MODEL",
    "endpoint": "https://api.openai.com/v1",
    "auth": "env",
    "keyEnv": "OPENAI_API_KEY"
  },
  "capabilities": {
    "contextTokens": 32768,
    "outputTokens": 4096,
    "reasoning": true,
    "toolCalling": false,
    "streaming": true
  }
}
```

Replace the model and capability values with those supported by your actual
connection. Profiles are operator declarations, not authenticated provider claims.
A profile must include a full route; bare model-name mappings cannot authorize a
different endpoint. For local unauthenticated profiles use `"auth": "none"` and
omit `keyEnv`; Copilot profiles use `"auth": "copilot"` and its pinned endpoint.

```bash
kmx model match model.json --require-context 32768 --require-reasoning
kmx run helper.agent.md --model-profile model.json
kmx list -o json
kmx chat helper --runtime local
```

`model match` is offline and prints JSON with `match`, `mismatch`, or `unknown`.
Only a match exits successfully. Missing limits and boolean metadata are unknown,
not inferred from names. Run refuses unmet or unknown required capabilities before
creating lifecycle state. A profile cannot be combined with provider, URL, model,
or key-environment overrides. Inventory JSON includes the frozen route and mapping.

Context matching is selection-time compatibility, not tokenizer-based conversation
truncation. There is no automatic model substitution or history summarization.
Endpoint output budgets use `outputTokens` when required, otherwise 1024 tokens;
OpenAI sends `max_completion_tokens`, other endpoints send `max_tokens`. Copilot
retains its provider-owned request policy. Actual context-limit or quota refusals
remain failed turns, with no replay.

Resident execution remains text-only and tool-free. Recorded `toolCalling` and
`streaming` capabilities do not enable resident tools or streaming. One-shot
`run --once`, file Test, and MCP retain their existing preview contracts; resident
`modelRequirements` is not a new suite evaluation or deployment gate. Native
Windows resident execution remains deferred. Transport and lifecycle tests use
local scripted HTTP fixtures; they are not live hosted-model qualification.
