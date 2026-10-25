# Copilot access decision

Decision recorded **2026-10-21** for the unreleased direct model-access commands
in [#610](https://github.com/kaimahi-agents/kaimahi/pull/610), part of
[#596](https://github.com/kaimahi-agents/kaimahi/issues/596).

## Selected route and scope

The project selected an explicit, experimental standalone compatibility route:
GitHub device-code sign-in using the public OAuth client used by vekil, or an
opt-in copy of the user's github.com `gh` credential, followed by direct Chat
Completions. The 23 October follow-up adds memory-only runtime gh access before
the optional kmx keychain and catalog-selected Responses models. No proxy, agent subprocess or SDK session runtime is introduced.
This implementation decision does **not** assert that GitHub documents or
endorses reuse of VS Code's OAuth client as a general third-party model API.
OAuth success alone is not proof of service entitlement or permission for a
new deployment mode.

| Surface | What this change provides |
|---|---|
| VS Code/editor-connected access | Not used. No editor credential extraction or implicit editor login reuse. |
| Standalone local sign-in and model listing | Explicit `kmx model login`, opt-in `login --gh`, `logout` and `list --provider copilot`. |
| Direct selected-model requests | An internal in-process JSON/SSE transport with a non-secret route and kmx-owned credential facility. No model substitution or automatic inference replay. |
| Agent Run/Test integration | Markdown-file Run/Test accepts explicit `--provider copilot`; unbound MCP defaults non-fake authored models to Copilot when no startup route is selected. Both use journaled embedded Sessions with provider-owned pinned credentials for Chat Completions and Responses; Explicitly bound tools support the bounded Chat Completions/Responses loop; judging stays tool-free. The canvas binding path is not wired yet. See [file preview](kmx.md#agent-file-local-preview-unreleased-integration). Native workspace execution is separate. |
| CI, unattended/headless and hosted runners | Runtime credentials can supply auth without an OS keychain, but no runner deployment is qualified here. Hermetic CI tests use fake credentials and endpoints, not a live Copilot account. |

Listing does not initiate OAuth. Explicit sign-in is a user operation, not a CI
consent prompt hidden inside discovery. Native OS credential facilities may
display their own unlock UI. Runtime authentication prefers an explicitly supplied
`COPILOT_GITHUB_TOKEN`, then an existing github.com gh sign-in through bounded
private `gh auth token` capture, without requiring or opening kmx's keychain.
If gh is unavailable, kmx can use its existing owned keychain credential. Runtime tokens remain in memory; kmx does not persist
them. This adds no hosted multi-user credential sharing or deployment qualification.
Selecting Copilot for file Run/Test sends authored instructions and inputs to
the hosted service; a judge also receives the answer and rubric. Explicit
sign-in does not select a default provider or model.
When a selected Copilot request fails, the caller receives an error; it does not
silently switch to a local model. [Local model discovery](models.md#installed-local-model-discovery)
is a separate explicit choice, and fake orientation remains offline.

## Consent, ownership and lifecycle

The 22 October 2026 (UTC) follow-up permits these memory-only runtime sources when
native credential storage is unavailable, with an explicit environment token
taking precedence. The 23 October follow-up makes an existing gh sign-in the
ordinary runtime path regardless of keychain availability. Neither change expands
the upstream permission or runner claims.

Device sign-in displays GitHub's verification URI and user code, never the device
secret or reusable token. `login --gh` explicitly copies a GitHub CLI token to
kmx's named OS credential item when available; runtime gh access never copies it.
No editor credentials are read. kmx does not write tokens to a workspace, journal,
receipt, plaintext cache or command argument, or export them into the environment.
An explicit environment token is caller-supplied. Login explains the runtime
options instead of failing when the keychain is unavailable.

The direct GitHub bearer is tried first through the model catalog. Only a
credential refusal permits the legacy exchange; subscription, organization-policy
and quota refusals do not. Exchanged bearers remain in memory and refresh before
their reported expiry. Revoked or unusable reusable credentials require explicit
sign-in again; an empty or invalid explicit environment token fails closed.
A missing owned credential does not block a working gh sign-in. Revocation and
service refusals never select another credential source. Logout deletes kmx's item and clears its memory. It does not
unset caller environment tokens, revoke upstream authorization, log out gh/editor sessions or
retract in-flight requests. Upstream revocation remains controlled through the
user's GitHub account. See [transport and safe errors](copilot-inference.md#direct-model-access).

An active subscription, applicable organization policy, model availability and
quotas still govern requests. Authentication is not a substitute for those
checks. No live account/subscription acceptance or general hosted-use permission
is claimed by the package tests.

## Source boundaries

Sources inspected on the decision date:

- [GitHub OAuth device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow)
  documents device authorization for CLI/headless applications and the polling
  protocol. It is protocol documentation, not approval of this particular client
  ID and private endpoint combination.
- [Copilot SDK authentication](https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate)
  documents signed-in users, OAuth apps, CI/environment and server authentication
  **for the SDK**. Those modes do not establish a standalone OpenAI endpoint or
  transfer their authorization/configuration to this compatibility client.
- [GitHub's terms index](https://github.com/customer-terms),
  [Generative AI Services Terms](https://github.com/customer-terms/github-generative-ai-services-terms)
  and [additional product terms](https://docs.github.com/en/site-policy/github-terms/github-terms-for-additional-products-and-features)
  distinguish direct corporate, personal and Microsoft purchasing agreements.
  The old Copilot-specific corporate terms were replaced for applicable purchases
  from 5 March 2026. This page is not a legal determination or an exemption from
  the user's applicable agreement.
- [Pinned vekil authenticator](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/auth/authenticator.go)
  and [request implementation](https://github.com/sozercan/vekil/blob/3e77a9d0739baf7740b141bd88b3fdf40caf7c16/proxy/handler.go)
  are the compatibility reference for client ID, credential exchange and headers;
  they are third-party implementation evidence, not official service permission.
