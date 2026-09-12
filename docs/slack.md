# Slack connector — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The Slack governance demo is
> retired as an onboarding story. Its MCP posting connector remains in this
> slice; inbound mentions, Slack approvals and the notifier are removed.
> Use the [documentation index](README.md) for the current path.

## The boundary that actually holds

[k8s/slack-mcp.yaml](../k8s/slack-mcp.yaml) pins the third-party
`korotovsky/slack-mcp-server` v1.3.0 image by digest and uses HTTP MCP.
**That pinned server does not enforce `SLACK_MCP_API_KEY` on its HTTP
transport.** Injection by the gateway is wired, but is not an independent
access control. The actual direct-access boundary is proxy-only ingress
in [NetworkPolicy](../k8s/plane/network-policy.yaml), enforced by the CNI.
Other policies can widen it: Kubernetes policies are additive.

The server may reach public TCP 443, not only Slack hostnames. A compromised
server can exfiltrate on that allowance. The gateway's policy, the server's
channel restriction and Slack scopes do not turn it into a hostname rule.
See [egress](egress.md) for the probe and remaining network limitations.

## Custody and existing operation

- `kaimahi-slack-bot` holds the bot token and posting-channel restriction.
  Only the MCP server receives those values; the proxy retains its separate
  MCP credential mount. Agents hold only an opaque Kaimahi credential.
- [slack-secret.sh](../scripts/slack-secret.sh) captures on stdin into
  restricted temporary files, requires an `xoxb` bot token, checks
  `auth.test`, and refuses a channel unless it is private and the bot is a
  member. Do not substitute user/session tokens or commit workspace IDs.
- The demo scopes are `chat:write`, `groups:read`, `groups:history`,
  `users:read`; `chat:write.public` unnecessarily widens posting.
- The server runs without its optional workspace directory cache.
  It reads its Secret-backed environment at startup: after rotating the
  bot Secret, restart `deployment/kaimahi-slack-mcp` in `kaimahi`.
- Removing inbound does not revoke credentials or retire this posting
  connector. Review Secret/token custody separately; do not delete material
  still used by the MCP server.

## Permissions and discovery

The shipped read allowlist excludes `conversations_add_message`. A
bounded [tool grant](approvals.md) can admit it. The gateway projects
callable tools into `tools/list`; kagent additionally selects its configured
`toolNames`. Therefore asking an agent for an invisible tool may produce
no call and no approval request. A direct MCP attempt exercises the denial.

Policy is checked on every call. Discovery can lag a permission change;
rediscover the RemoteMCPServer and restart the agent if its loaded tool
list is stale. Enforcement does not wait for discovery. A consumed grant
is **not proof of a posted message**: a missing upstream credential or
an upstream failure can consume the use before anything is delivered.
Inspect `kmx audit tool hello-slack` and the actual response before retrying.

## Related legacy paths and evidence

[Inbound](inbound.md), its public edge and the notifier are removed. No Slack
approver path remains; use `kmx approvals` and `kmx approve`/`kmx deny` for
retained tool/budget requests. Old edge deployments require explicit
[upgrade cleanup](operations.md#upgrading-after-inbound-retirement).

Keyless CI tests gateway decisions without deploying a live Slack server.
An admitted 502 there proves a forward was attempted, **not** that Slack
accepted a post, that the configured URL works, or that a human approved.
The original live-run transcript and connector survey are intentionally
not duplicated here; the pinned manifest and operational caveats remain.
