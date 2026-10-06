# Composable bundles

**Status:** offline validation and rendering only
([#306](https://github.com/kaimahi-agents/kaimahi/issues/306)). `kmx agent
create`, `lift`, `status` and `evaluate` still use the single-agent
[bundle format](bundle-format.md).

A composable bundle is a directory of independent resources. Providers and
Tools are defined once, and Agents use them by name, so several Agents can
share one definition.

```text
research-team/
├── Bundle.yaml
├── providers/default-chat.yaml
├── tools/fetch-page.yaml
├── tools/web-search.yaml
├── agents/researcher.yaml
└── agents/summarizer.yaml
```

The complete example is in
[`internal/kmx/bundle/testdata/research-team`](../internal/kmx/bundle/testdata/research-team).

The code is split by concern:

- `internal/kmx/krm` holds the Provider, Tool and Agent resource model,
  strict decoding, validation, the reference-checked graph and identity. It
  knows nothing about directories or runtimes.
- `internal/kmx/bundle` loads a bundle directory and `Bundle.yaml` into a
  `krm` graph.
- `internal/kmx/adapter/orka` renders a `krm` graph to Orka resources.

## Commands

```console
$ kmx bundle validate research-team
Bundle research-team 0.1.0: valid (providers=1 tools=2 agents=2)
  Agent researcher sha256:…
    provider: default-chat
    tools: web-search, fetch-page
    allowedAgents: summarizer
  Agent summarizer sha256:…
    provider: default-chat
    tools: fetch-page
    allowedAgents: none

$ kmx bundle render research-team --namespace research
```

A bundle is validated and rendered as a whole. `validate` prints each Agent's
[identity](#identity); `-o json` emits the same report as JSON. `render` prints
Orka YAML for every resource in the bundle and lists the Secrets the namespace
must already hold. Providers and Agents are checked against an embedded Orka
schema (`--orka-schema v0.2.0|v0.1.3|main`, default v0.2.0). Neither command
reads kmx configuration or contacts a cluster.

## Layout

- `Bundle.yaml` holds the bundle's `name`, a SemVer `version` and an optional
  `description`.
- `providers/`, `tools/` and `agents/` each hold one resource kind. A missing
  directory is empty. Every bundle needs at least one Agent.
- Each resource file holds exactly one document and is named
  `<metadata.name>.yaml`. Any other entry in those directories is refused.
  Files elsewhere in the bundle directory are ignored.
- Every document uses `apiVersion: kmx.kaimahi.dev/v1alpha1`.

Every file is decoded strictly, like `agent.yaml`. Unknown fields, duplicate
keys, aliases, merge keys, extra documents, symlinks and credential-shaped
values are refused.

## Provider

```yaml
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Provider
metadata:
  name: default-chat
spec:
  type: openai            # openai, anthropic or azure-openai
  model: qwen2.5:3b       # for azure-openai, the deployment name
  openAI:
    baseURL: http://ollama.ollama:11434/v1
  credentials:
    secret: {name: local-model, key: api-key}
  rateLimits:             # optional
    requestsPerMinute: 60
```

`azure-openai` uses `azure: {endpoint, apiVersion}` instead of `openAI`. The
endpoint must be the HTTPS resource root. Credentials always name an existing
Secret and key; values are never stored in a bundle.

## Tool

```yaml
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Tool
metadata:
  name: web-search
spec:
  description: Search the web and return the top results
  parameters:             # optional JSON Schema; the root must be type: object
    type: object
    properties:
      query: {type: string}
  http:
    method: POST
    url: https://search.example.com/query
    timeout: 30s
    credentials:
      secret: {name: search-credentials, key: api-key}
```

HTTP is the only Tool implementation. `Authorization`, `Proxy-Authorization` and
`Cookie` headers are refused. Use `credentials` instead.

## Agent

```yaml
apiVersion: kmx.kaimahi.dev/v1alpha1
kind: Agent
metadata:
  name: researcher
spec:
  description: Researches a requested topic
  instructions: Search authoritative sources and summarize the findings.
  provider: default-chat
  tools: [web-search, fetch-page]
  allowedAgents: [summarizer]
```

`provider`, `tools` and `allowedAgents` must name resources in the same bundle.
`allowedAgents` is an explicit delegation allowlist. When present, it must name
at least one other Agent. Bundle membership never grants delegation.

## Identity

An Agent's **identity** is a SHA-256 digest over the exact authored bytes of
everything it runs with: the Agent, its Provider and Tools, and every Agent it
may delegate to, followed transitively with their own dependencies. The digest starts with `Agent <name>\n`, so
Agents that delegate to each other keep distinct identities. Resources follow
in kind order (Provider, Tool, Agent) and then name order, each framed as
`<kind>/<name> <byte length>\n<bytes>\n`. If a shared Provider or Tool changes,
or an Agent it may delegate to, the identity changes too. `Bundle.yaml` is not
part of the identity.

## Orka rendering

`kmx bundle render` emits Providers, then Tools, then Agents, each
`core.orka.ai/v1alpha1` resource once in the chosen namespace. It also lists the Secret references the
namespace must already hold. Agents reference Providers with `providerRef` and
Tools by name. `allowedAgents` becomes enabled coordination with exactly those
Agents. Provider `rateLimits` render as Orka `rateLimit`, which Orka v0.1.3
accepts and v0.2.0's schema refuses.
