# Legacy reference: approvals and bounded grants

> **Retained budget approvals, not an Orka approval contract.** The model seam
> still uses the plane's budget request/approval/grant machinery until the final
> retirement slice. Tool and inbound authority are retired. Start at the
> [documentation index](README.md) for current onboarding.

## Existing procedure

Budget admission uses **deny-and-retry**, not a held-open call or durable
workflow. A denied call can file a pending budget request; an operator decides
it, and the client must attempt the model call again.

```sh
kmx approvals
kmx request budget tokens --credential hello-world
kmx approve <budget-request-id> --ttl 10m --uses 1 --amount 100000
kmx grants hello-world
kmx audit approval hello-world
```

Requests name `tokens` or `cents`; approval requires `--amount` in those units
and at least one positive time/use bound. Choose approval **or** `kmx deny <id>`.
Administrative decisions use cluster access and the admin bearer, recording
`decided_by=admin`, not a verified person. Bounds and flags are in the
[CLI reference](kmx.md#existing-plane-and-operator-commands).

## Historical tool and inbound requests

New tool/inbound requests and approvals are refused. Existing requests remain
readable through `kmx approvals` and deniable with `kmx deny`; neither kind can
be approved into a new grant. Their stored grants are **inactive**, regardless
of remaining uses or expiry, and cannot admit work. Historical argument digests,
summaries and audit records remain readable data, not executable policy.

The gateway, tool-audit endpoint, standing constraints and Slack decision path
are removed. Their former semantics are available in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/approvals.md),
not as procedures to run on the reduced plane.

## Grant lifetime and consumption

Budget headroom is the cap plus live budget-grant amounts. Only calls needing
the overage consume budget-grant uses. Expiry and exhaustion are checked in SQL
under the credential-row lock across replicas; expired/exhausted rows stay as
history. A grant never extends its credential's lifetime. See [spend](spend.md)
and [identity](identity.md).

Approval/denial, grant creation and their audit record commit together; a
decision that cannot be recorded is not committed. Decided requests are
immutable. Denying a pending request does not revoke an already-issued grant.
Sources: [approvals.go](../plane/internal/store/approvals.go) and
[spend.go](../plane/internal/store/spend.go).

## Remaining limits and evidence

`kmx approvals` is the queue of record; there is no automatic notification or
Slack approver path. `flow` and `watch` read model and approval-history trails,
not tool or inbound audit. Historical `slack:<user id>` decisions remain stored.

All twelve SQL migrations and existing data are preserved. See
[upgrade cleanup](operations.md#upgrading-after-gateway-retirement) before
redeploying an older installation. Budget bounds limit model spend, not tool
arguments, model reasoning, application ownership or every network route.
