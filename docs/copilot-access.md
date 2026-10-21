# Copilot access decision

Decision recorded **2026-10-21** for the unreleased direct model-access commands
in [#610](https://github.com/kaimahi-agents/kaimahi/pull/610), part of
[#596](https://github.com/kaimahi-agents/kaimahi/issues/596).

## Selected route and scope

The project selected an explicit, experimental standalone compatibility route:
GitHub device-code sign-in using the public OAuth client used by vekil, or an
opt-in copy of the user's github.com `gh` credential, followed by direct Chat
Completions. No proxy, agent subprocess or SDK session runtime is introduced.
This implementation decision does **not** assert that GitHub documents or
endorses reuse of VS Code's OAuth client as a general third-party model API.
OAuth success alone is not proof of service entitlement or permission for a
new deployment mode.

| Surface | What this change provides |
|---|---|
| VS Code/editor-connected access | Not used. No editor credential extraction or implicit editor login reuse. |
| Standalone local sign-in and model listing | Explicit `kmx model login`, opt-in `login --gh`, `logout` and `list --provider copilot`. |
| Direct selected-model requests | An internal in-process JSON/SSE transport with a non-secret route and kmx-owned credential facility. No model substitution or automatic inference replay. |
| Agent Run/Test integration | Not implemented here; [#597](https://github.com/kaimahi-agents/kaimahi/issues/597) consumes the route separately. |
| CI, unattended/headless and hosted runners | No runner or automatic sign-in/credential-import mode is added or qualified here. Hermetic CI tests use fake credentials and endpoints, not a live Copilot account. |

Listing does not initiate OAuth or invoke `gh`. Explicit sign-in is a user
operation, not a CI consent prompt hidden inside discovery. Native OS credential
facilities may display their own unlock UI. No hosted multi-user credential
sharing, environment-token fallback or deployment qualification is implied.
When a selected Copilot request fails, the caller receives an error; it does not
silently switch to a local model. [Local model discovery](models.md#installed-local-model-discovery)
is a separate explicit choice, and fake orientation remains offline.

## Consent, ownership and lifecycle

Device sign-in displays GitHub's verification URI and user code, never the device
secret or reusable token. `login --gh` alone authorizes capturing the existing
GitHub CLI token privately; no editor credentials are read. The reusable token
belongs to kmx's named OS credential item, not a workspace, journal, receipt,
plaintext cache, command argument or environment export.

The direct GitHub bearer is tried first through the model catalog. Only a
credential refusal permits the legacy exchange; subscription, organization-policy
and quota refusals do not. Exchanged bearers remain in memory and refresh before
their reported expiry. Revoked or unusable reusable credentials require explicit
sign-in again; no OAuth refresh-token machinery or implicit credential-source
switch is added. Logout deletes kmx's item and clears its memory. It does not
revoke the account's upstream authorization, log out gh/editor sessions or
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
