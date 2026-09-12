# Demo paths

For the current platform direction, demonstrate [Orka installation](orka.md)
and [model-traffic migration](migrate.md): the application already exists, its
owner applies the generated patch, and model ledger rows prove traffic crossed
the seam. Do not present that as an Orka-managed Deployment or governed tools.

The checklist below exercises the **existing legacy kagent/plane implementation**
pending the code transition. It is not a decision against kagent YAML over Orka;
that authoring question remains open. The bridge shrinking as Orka supplies the
capabilities is success.

## Local legacy checklist

Use a checkout for standalone probes/fixtures, with the [prerequisites](getting-started.md#prerequisites).
Choose an isolated kind cluster and run native kmx where supported:

```bash
export KIND_CLUSTER=demo-local
export KUBE_CTX=kind-demo-local
kmx up
kmx agent chat hello-world 'Who are you?'
kmx plane
kmx govern hello-world
kmx agent chat hello-world 'Who are you?'
kmx ledger hello-world
kmx budget hello-world --tokens 1
kmx agent chat hello-world 'Who are you?'   # expect budget denial
kmx approvals
```

Inspect the budget request; approve a bounded overage, then explicitly retry:

```bash
kmx approve <budget-request-id> --ttl 5m --uses 1 --amount 100000
kmx agent chat hello-world 'Who are you?'
kmx grants hello-world
kmx budget hello-world --tokens 100000
kmx tools govern --tools k8s_get_resources
kmx agent chat hello-tools 'List the configmaps in the default namespace.'
kmx audit tool hello-tools
KUBECTL="kubectl --context $KUBE_CTX" bash scripts/tool-denial-probe.sh k8s_get_events
make netpol-verify
```

Expected evidence: a model row; a denied budget call before inference; a bounded
grant/use; an allowed tool audit row; an unallowlisted tool refusal; the network
probe's positive control and enforced denial matrix. An answer alone proves
none of those. Native kagent approvals and Kaimahi approvals are separate.
See [spend](spend.md), [tools](tools.md), [approvals](approvals.md), [egress](egress.md).

For the fixture business scenario, follow [accounts payable](ap-demo.md):
`make erp`, `make govern-ap`, `make ap-demo`, `make ap-injection`. The payment
approval binds the transaction; a persuaded model cannot spend it on a different
payee. Keep fixture claims separate from a real ERP integration.

## Managed demonstration

Follow [AKS](aks.md), rather than replaying the kind setup on a remote context.
The full lift is still Copilot/kagent-shaped and bills money. The inbound edge,
Slack approval commands and notifier are removed; [Slack MCP posting](slack.md)
remains separate. The AP fixture driver approves through admin itself and
establishes no verified human identity. The old `AP_HUMAN=1` wait mode is
rejected before the scenario starts; use the separate approval CLI for manual
operations, not this automatically approved fixture.

## Teardown and evidence

Local: `kmx down` deletes everything, including the ledger. Managed:
[AKS teardown](aks.md#teardown) distinguishes created-group deletion from BYO
monitoring cleanup. Older public-edge installations also need the
[retirement cleanup](operations.md#upgrading-after-inbound-retirement), including
disabling external subscriptions before releasing the DNS name. Verify owned
cloud resources are gone, not merely that a delete was submitted. Scan transcripts
with `scripts/check-no-azure-ids.sh` and manually redact bare infrastructure/workspace
names before sharing.
