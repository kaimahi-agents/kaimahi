# Legacy reference: MCP gateway and argument policy

The Kaimahi MCP gateway, its 8081 listener, tool allowlists, argument-bound
approvals, standing constraints and tool-audit API are **retired**, not merely
undocumented. The tool-governance CLI and scaffolding are removed with them.
There is no replacement Kaimahi tool-governance procedure.

Historical implementation and policy semantics are preserved in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/tool-governance.md).
All [custom approvals/grants](approvals.md), including budget exceptions, are now
retired. Historical requests/grants/audits remain unchanged in SQL/backups, not
readable or deniable through the removed APIs. SQL migrations remain intact.

Existing installations need [deliberate upgrade cleanup](operations.md#upgrading-after-gateway-retirement).
Tool traffic remains the application owner's responsibility; do not silently
repoint tools around the retired gateway. [Direct kagent MCP](tools.md) and
[native Orka authoring](orka.md) remain, separately from this retired runtime.
For current onboarding start at the [documentation index](README.md).
