# Copilot inference and tools

## Markdown-agent spike (unreleased)

See the [Markdown agent guide](markdown-agent-inner-loop.md) for standalone files,
named tool/test companions, directory import, local instances and console actions.
The persistent SDK path uses the installed Copilot CLI's own authentication.
Ordinary file preview uses the shared parser/model-access route described below.

<a id="same-file-cli-tools-and-compatibility-checks"></a>
## Named CLI tools and compatibility checks

Command bindings live in `<name>.tools.yaml` beside `<name>.agent.md`, not in the
model prompt. See [tool declarations](markdown-agent-inner-loop.md#tool-declarations)
and [approval](markdown-agent-inner-loop.md#suggest-approve-activate).

## Suggest and approve PR-monitor tools

Default `tools suggest FILE_OR_DIRECTORY` proposes bounded CLI declarations.
Explicit `--repo OWNER/REPO` selects the fixed GitHub read-template workflow;
its private overlay binds the definition, repository and executable. GitHub reads
use fixed GET routes without redirects; credentials stay in memory. Both workflows
require review and neither makes local preview evidence deployable.

## Existing host chat integration (Orka)

Host Copilot chat is independent from native workspace Test and Deploy.
`/inference-copilot` discovers models through the authenticated installed CLI
without rewriting the saved Orka Provider. On local kind it opens a model picker;
remote targets use a cluster Provider. `/inference-local` returns to the native
Provider. `/retry` is an explicit new request. Native Test/Deploy consumes its
exact packaged model and native binding, not host Copilot execution.

## Direct model access

`kmx model login`, `login --gh`, `logout` and `list --provider copilot` manage the
independent direct model route. See the [access decision](copilot-access.md) and
[sign-in/storage contract](models.md#explicit-copilot-model-access).
Runtime credentials prefer explicit `COPILOT_GITHUB_TOKEN`, then an existing
github.com gh sign-in, then the owned OS keychain when gh is unavailable. Runtime
gh access is memory-only. Service refusals do not switch credentials. No token
files, token flags or plaintext-cache fallback are introduced.

File Run/Test and MCP use embedded Sessions with a provider-owned credential and
API-path snapshot before journaling. Chat Completions and Responses share the
Chat-shaped harness contract. Hosted inference sends instructions/inputs and any
judging content to Copilot. Private journals retain content in cleartext under the
documented retention rules. Known provider failures stop later sends without
inference retries or model substitution. See [file preview](kmx.md#agent-file-local-preview-unreleased-integration).

Internal callers use `copilot.New(store)` and `Client(ctx, route)` for a validated
`modelaccess.Route`. The selected transport preserves model identity and incomplete,
refusal and tool outcomes. Models supporting both protocols prefer Chat. Requests
are bounded to 4 MiB, auth/catalog/error responses to 1 MiB and credential-process
output to 16 KiB. Successful inference uses the caller's response budget.

Auth starts with GET `/models`. Only credential refusal authorizes the legacy
`copilot_internal/v2/token` exchange; subscription, policy and quota failures do
not. Exchange tokens remain in memory and honor expiry. Logout removes only KMX's
owned keychain/memory state, not gh/environment credentials or in-flight requests.
Safe error classes include authentication, subscription, policy, quota, unavailable
model, cancellation, timeout, malformed response and keychain failures. Raw provider
bodies and credential-bearing causes are not diagnostic output.

References: [vekil authenticator](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/auth/authenticator.go),
[request headers](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/proxy/handler.go).
KMX does not reuse their disk cache, proxy or retry behavior. Hermetic package tests
do not establish live service or native Windows model/provider qualification.

## Tool translation

The separate Orka host-chat adapter supplies registered HTTP tool descriptions to
`copilot -p`, validates returned actions and executes scoped calls. It permits at
most eight steps and five minutes per turn. Supported tools are POST HTTP tools
targeting Services in the Agent namespace, without special auth headers, Secrets,
outbound policy or MCP configuration. Calls use owned loopback forwards and the
selected Kubernetes context. Policy-backed tools remain on native Orka.

Unsupported tools are reported, not bypassed. Native Copilot tools are restricted
to the adapter allowlist; each turn runs in an empty temporary directory without
repository custom instructions. CLI-managed credentials remain separate from the
direct model-access keychain. This adapter does not claim Orka-worker authorization.
