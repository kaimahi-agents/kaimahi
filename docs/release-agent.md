# Release agent — retired guide

The gateway-backed release agent, GitHub/ADO tool fixtures, credential capture,
blueprint and workflow runner are **removed**. The former procedure and its
consequential-action limits remain only in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/release-agent.md).
This is not a current Orka workflow contract.

Review old operator-owned release workflows, application references, tool
credentials and network allowances during [upgrade cleanup](operations.md#upgrading-after-gateway-retirement).
Existing tool grants are inactive; stored history is preserved, not authorization
to publish again. Do not replay old approval or credential-capture commands.

Repository release packaging is separate and remains documented in
[releases](releases.md). Current agent/migration paths start at the
[documentation index](README.md).
