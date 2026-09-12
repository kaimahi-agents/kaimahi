# Bring-your-own seams — retired guide

The standalone tool-governance onboarding path is **removed**: no tool-upstream
scaffolding, credential sidecar, allowlist or gateway-governance command remains.
The former procedure is available only as
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/govern-your-agent.md),
not as a supported setup path.

[Model-traffic migration](migrate.md) remains. The application owner keeps its
Deployment, lifecycle, tools and their security boundaries. Existing gateway
references and tool overlays need [explicit upgrade review](operations.md#upgrading-after-gateway-retirement);
apply does not prune them or convert them into safe direct routes.

[Native Orka authoring](orka.md) and the [direct kagent MCP example](tools.md)
are not this retired gateway. Start at the [documentation index](README.md).
