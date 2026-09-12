# Demo paths

The gateway-backed tool, workflow and business-scenario demonstrations are
**retired with their code**, not current operator procedures. Their former
checklist is preserved in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/demo.md).

For current demonstrations use [native Orka creation](orka.md),
[model-traffic migration](migrate.md), or the retained [kagent first-answer
path](getting-started.md#one-command-and-an-agent-that-answers). The original
[direct kagent MCP example](tools.md) remains; not all MCP support is retired.
Model ledger rows prove traffic crossed the model seam, not governance of tools
or ownership of the application Deployment.

Existing deployments need [explicit retirement cleanup](operations.md#upgrading-after-gateway-retirement).
For disposable kind clusters, `kmx down` destroys the cluster and ledger;
managed-cluster cleanup follows the [AKS ownership rules](aks.md#teardown).
No new live kind or cloud proof is claimed by this documentation update.
