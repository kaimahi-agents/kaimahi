# Copilot inference and tools

Host Copilot chat inference is independent from native workspace evaluation
and Deploy. Copilot wizard authoring is retired; automatic wizard tool attachment
and live source tools/inference edits remain retired. Outside the wizard,
`/inference-copilot` discovers models through the authenticated installed CLI
without rewriting the saved Orka Provider. Native eval/Deploy requires its exact
packaged model and native binding, not host Copilot execution.

On a local kind target, `/inference-copilot` opens a discovered-model picker.
Host Copilot inference is refused on remote targets; configure a cluster Provider
there instead. `/inference-local` returns to the Agent's Orka Provider; `/retry`
repeats the previous prompt using the chosen route. Each reply shows total elapsed
request time.

## Direct model access

The independent unreleased `kmx model login`, `login --gh`, `logout` and
`list --provider copilot [-o table|json]` commands manage an explicitly owned
Copilot route, not this existing CLI-chat adapter. The
[dated access decision](copilot-access.md) distinguishes the selected experimental
compatibility route from official OAuth/SDK documentation and unqualified
hosted modes. See [sign-in and storage](models.md#explicit-copilot-model-access).
Runtime auth prefers explicit `COPILOT_GITHUB_TOKEN`, otherwise the owned OS
keychain item. Only an unavailable keychain permits reading an existing gh login;
a missing item or service refusal does not. Runtime tokens are never copied to
storage, exported in argv/environment, or returned in routes. There are no token
flags or plaintext-cache fallback.

Markdown-file Run/Test and their shared MCP path consume this transport with
explicit `--provider copilot`, an exact authored model and the pinned endpoint.
They remain temporary tool-free, no-journal preview execution, not the CLI-chat
tool adapter below or an embedded Sessions harness. Selecting this route sends
instructions and inputs to Copilot; judging also sends answers and rubrics.
Known Copilot refusals stop later sends and expose only static safe reasons.
See [file preview usage](kmx.md#agent-file-local-preview-unreleased-integration).

Internal callers use `copilot.New(store)` (`nil` selects the native keychain),
then `Client(ctx, route)` to obtain an `http.RoundTripper` for a validated exact
`modelaccess.Route`. Use a standard request with POST
`https://api.githubcopilot.com/chat/completions`, a JSON body naming that same
model, and a caller-owned context/deadline. The transport preserves the caller's
Chat `Accept` header and successful JSON/SSE bytes; callers own closing the
response body. Non-2xx Chat responses return a classified error, not a raw
upstream response or error body. Request bodies are
bounded to 4 MiB, auth/catalog/error responses to 1 MiB, and credential/process
output to 16 KiB. Ambiguous top-level JSON keys and changed models or destinations
refuse before inference. No Responses API, agent loop, tool execution, listener
or OpenAI proxy is added.

Auth is established through GET `/models` before inference. The original GitHub
credential is tried directly as a bearer. Only 401 credential refusal or a 403
with explicit `unsupported_token` code authorizes the legacy GitHub
`copilot_internal/v2/token` exchange. Known subscription/policy/quota codes do
not authorize exchange, even with 401. Exchanged bearers remain in memory,
honor `expires_at` and refresh 30 seconds before expiry; the reusable keychain
credential is not replaced. Subscription, policy and quota refusals never select
another model or credential source. Catalog checks can be repeated; inference
is never automatically resent, including after a Chat credential refusal or
connection failure. The next explicit request resolves auth independently.
Logout clears memory and deletes only kmx's item, not upstream account tokens;
it cannot retract requests already in flight or disable gh/environment credentials.

Safe static error prefixes distinguish `copilot_auth_required`,
`copilot_auth_revoked`, `copilot_no_subscription`, `copilot_policy_denied`,
`copilot_quota`, `copilot_model_unavailable`, `copilot_cancelled`,
`copilot_timeout`, `copilot_malformed`, `copilot_keychain_unavailable` and
`copilot_unavailable`. Unknown 403s are policy refusals. Raw upstream errors,
bodies, URLs and credential-bearing causes are not included in diagnostics.
Caller-owned successful model content is not redacted by this transport.

Auth/header reference: [vekil authenticator](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/auth/authenticator.go)
and [request headers](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/proxy/handler.go).
KMX does not reuse its disk cache, proxy, retry or failover behavior. Hermetic
native package tests are not live GitHub/subscription or OS-keychain acceptance
proof, nor full Windows CLI qualification. Explicit credential access may show
OS unlock UI; Linux uses the existing login/default collection with kmx-specific service
and account attributes. Native keychain calls/prompts have no context-cancellation
guarantee. Unexpected native-dependency panics during credential access are
mapped to the safe keychain error without exposing their value.

## Tool translation

KMX supplies enabled registered HTTP Tool descriptions and parameter schemas to
`copilot -p --model <selected>`. The model returns one JSON action: a tool call or
a final answer. KMX validates the tool name and arguments, executes the HTTP call,
and feeds its result into the next prompt. It permits at most eight steps and
five minutes per turn, with bounded request and response sizes and no automatic
retry after execution failures. Ctrl-C cancels the subprocess and active requests.

Supported tools are POST HTTP tools pointing to a Service in the Agent namespace,
without special headers, auth Secrets, outbound policy or MCP configuration.
Calls use an owned loopback port-forward and the selected Kubernetes context.
The policy-backed Kubernetes listing tool runs through native Orka. Copilot
reports it unavailable rather than tunnelling around the gateway policy.

Tools requiring Orka approval, built-in memory tools, MCP tools and authenticated
or policy-backed tools are not executed by this adapter. Explicit unsupported
references are reported; use the local Orka path for those tools. This adapter
uses the operator's Kubernetes access and does not claim Orka worker execution
or its authorization semantics.

Copilot native tools are restricted to an adapter-only allowlist; the registered
Orka tools execute in KMX. Copilot output is decoded from JSON events to avoid
terminal wrapping corrupting tool arguments. Instructions come from the selected
Agent; each turn runs in an empty temporary directory with no repository custom
instructions and no inherited conversation. This adapter's credentials remain
managed by the installed CLI, including GitHub CLI login. It does not read
credential files or consume the independent direct model-access keychain item.
