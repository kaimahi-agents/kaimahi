# Legacy reference: workflow blueprints

The `kmx workflow` commands, blueprint parser/runner and bundled release
blueprint are **removed** with the Kaimahi tool gateway. They are not an Orka
workflow API, and there is no replacement Kaimahi runner to configure.

The former commands, format and execution limits remain in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/workflows.md).
Do not replay that procedure against the reduced plane: old tool approvals
cannot authorize execution, and historical tool grants are inactive.

Review operator-owned workflow files, tool overlays, credentials and application
references during [upgrade cleanup](operations.md#upgrading-after-gateway-retirement).
SQL migrations and stored history are preserved; no destructive database cleanup
is required. Current onboarding starts at the [documentation index](README.md).
