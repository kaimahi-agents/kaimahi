# Accounts-payable demo — retired guide

The ERP fixture server/corpus, AP agents, gateway wiring and scenario drivers
are **removed**. This is not a payment integration or a current demonstration
path. The former fixture arithmetic, approval limits and evidence are retained
only in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/ap-demo.md).

Existing fixture deployments, tool credentials and gateway references require
[deliberate cleanup](operations.md#upgrading-after-gateway-retirement); applying
the reduced manifests does not remove them. Historical tool requests/grants
remain stored, but cannot authorize new work. No database reset is required.

For current paths use the [documentation index](README.md), [native Orka
creation](orka.md) or [model-traffic migration](migrate.md), not this retired
business scenario.
