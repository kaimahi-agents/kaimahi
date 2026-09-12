# Inbound bridge — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** The inbound runtime is
> removed, not just this guide. Start at the [documentation index](README.md)
> for current paths.

## Existing interface

There is no inbound listener on 8082, webhook delivery interface, inbound
request filing or inbound audit API/CLI view. `kmx flow` and `kmx watch` read
only the model ledger. Historical SQL migrations and stored
inbound audit/replay/attribution records remain; no database cleanup is implied.

## Slack and the public edge

Slack approval commands, notifications and the [posting fixture](slack.md) are
removed. The remaining [custom approvals/grants](approvals.md), including budget
exceptions, are also retired. Historical requests/grants/audits remain unchanged
in SQL/backups, not readable or deniable through the removed APIs.

Existing installations must follow the [retirement upgrade procedure](operations.md#upgrading-after-inbound-retirement):
old inbound/notifier configuration is rejected, and applying new manifests does
not delete the old public edge. Disable external subscriptions before releasing
its DNS name, then review and explicitly remove the obsolete owned resources.
