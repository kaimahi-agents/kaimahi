# Foreign-runtime seam — retired investigation

The horizontal tool-governance investigation and its MCP gateway are **retired**.
Tool onboarding, gateway credentials/sidecars and runtime-independent tool
approval are no longer supported paths. Their historical constraints remain in
the [pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/foreign-runtime.md).

[Model-traffic migration](migrate.md) survives: the application owner keeps its
Deployment and lifecycle, reviews/applies its patch, and remains responsible
for tool traffic. The retained model seam requires its credential, verified CA
trust and a reviewed network allowance; a model ledger row is not evidence of
tool governance or verified human identity. See [identity](identity.md).

Existing gateway references require [explicit upgrade cleanup](operations.md#upgrading-after-gateway-retirement),
not automatic repointing to direct tool access. The bridge shrinking is success,
not an obligation to preserve another platform. Start at the
[documentation index](README.md) for current paths.
