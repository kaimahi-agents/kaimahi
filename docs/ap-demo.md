# Accounts-payable demo — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The AP demo is retired as the
> product story; its fixtures, driver and tests remain. This stub preserves
> the interpretation and safety limits of those tests, not a payment-system
> integration tutorial. Start at the [documentation index](README.md).

## What is real and what is simulated

The ERP is the in-memory fixture server in
[internal/demo/erp](../internal/demo/erp/) and
[cmd/demo/kaimahi-erp](../cmd/demo/kaimahi-erp/), with its corpus in
[k8s/erp-fixtures.json](../k8s/erp-fixtures.json). There is no bank,
payment rail, vendor master integration or real money movement.

The legacy gateway's allowlists, argument constraints, call-bound grants,
audit rows and network rules are real code. The ERP accepts the payee
it is given; the fixture is not the control being tested.

The [scenario driver](../scripts/ap-demo.sh) asserts gateway decisions
using **fixed arguments, not model reasoning**. Its agent investigation
is informational. A successful CI run does not establish that a model
understands an invoice, arrives at the right amount or resists injection.

## What the fixture proves

The exception arithmetic is 3,255,000 cents paid, 945,000 held and
600,000 disputed against a 4,800,000-cent invoice. Fixture tests check
that arithmetic; the runtime refuses an inconsistent corpus.

The committed [standing constraint](../k8s/plane/upstreams.yaml) admits
`payment_schedule` only at or below 1,000,000 cents and to the two named
fixture payees. The six read tools are allowlisted; payment, dispute and
notification are not. An above-bound payment needs its own call-bound
grant; dispute and notification each need separate authority.

The [injection driver](../scripts/ap-injection.sh) attempts changed
payment arguments regardless of the model's response. It checks denial,
audit attribution and that an earlier exact-call grant is not spent.
This is **not prevention of prompt injection** or a universal promise
that a manipulated agent cannot act. An allowed or in-bound call needs
no new human decision, and the rules only cover fields they name.

## Limits existing operators must keep

- The amount ceiling alone does not catch a downward units error or an
  unexpected payee. Keep the payee restriction as well as the ceiling.
- Per-field constraints do not check that an amount matches an invoice
  or that the payee owns it. Those relationships need separate controls.
- Tool responses are not redacted; readable fixture data is visible to
  the agent. This demo proves no confidentiality property.
- The driver approves through the admin path for the fixture test;
  that is not a human decision. `AP_HUMAN=1` is rejected before the scenario
  starts: the old human-wait helper retired with the connector path. The
  recorded `admin` actor is not a verified person. Operators may use the
  separate [approval CLI](approvals.md); no Slack approval path remains.
- An admitted audit row is not proof of downstream completion or money
  movement. Read the response and the system of record.

Existing deployment and teardown wiring remains in the
[Makefile](../Makefile), [erp-deploy.sh](../scripts/erp-deploy.sh),
[ERP manifest](../k8s/erp-mcp.yaml) and [agent manifest](../k8s/ap-agent.yaml).
The private-registry path is not public publication of the demo image.
Long setup transcripts and presentation scripts have been removed rather
than maintained as a second onboarding path.
