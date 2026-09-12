# Approvals and bounded grants — retired guide

The Kaimahi request/approval/grant subsystem is **retired**, including budget
exceptions. Request filing, approval/denial, grants and approval-audit APIs and
CLI commands are removed. `kmx flow` and `kmx watch` read the model ledger only.
Native kagent questions/tool approvals and Orka's own runtime approvals are
separate boundaries, not this retired subsystem.

The last source carrying the budget-approval procedure is
[`5e7c5da`](https://github.com/kaimahi-agents/kaimahi/blob/5e7c5da2590610a33f0ec6094354c219139ce3b8/docs/approvals.md),
published before deletion. It is historical reference, not a procedure to run
against the reduced plane.

Ordinary monthly token/cents caps, accounting and spend reservations remain.
A cap denial returns 429 without filing a request or advising approval. The
operator may deliberately change the budget, or wait for the calendar-month
reset in UTC; there is no grant override. See [spend](spend.md) for the distinct
403 metering-unavailable and 503 ledger-breaker paths.

All twelve SQL migrations and historical requests, grants and audit rows remain
unchanged. Retirement neither normalizes pending requests nor exhausts or
rewrites grants. History is accessible through SQL/backups, **not the removed
APIs**; there is no new archive interface.

Upgrade kmx and the plane together. Contract 5 marks retirement, not compatibility
negotiation. Old replicas can still consume grants during a rolling update;
declare retirement effective only after **every replica reports the new build**.
Rolling back to an approval-capable binary can reactivate stored grants. Follow
the [upgrade review](operations.md#upgrading-after-approval-retirement), not a
database reset. Start at the [documentation index](README.md) for current paths.
