# Slack connector — retired guide

The Slack MCP posting fixture, agent, credential helper and Kaimahi gateway
wiring are **removed**. Inbound mentions, Slack approvals and notifications
were already retired. There is no supported Slack connector procedure in this
repository now.

The pinned connector's former security limits and custody details remain in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/slack.md).
Those historical measurements do not certify a current server or deployment.

Existing installations need [gateway cleanup](operations.md#upgrading-after-gateway-retirement)
and, where applicable, [public-edge cleanup](operations.md#upgrading-after-inbound-retirement).
Review owned workloads and Secrets, and arrange external token revocation
separately; deleting a Secret does not revoke a token at its issuer. Do not
remove an application's independent Slack integration merely because this
fixture is retired. [Budget approvals](approvals.md) remain admin-operated.

Start at the [documentation index](README.md) for current operator paths.
