# Inbound bridge — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** The inbound runtime is
> removed, not just this guide. Start at the [documentation index](README.md)
> for current paths.

## Existing interface

There is no inbound listener on 8082, webhook delivery interface, inbound
request filing or inbound audit API/CLI view. `kmx flow` and `kmx watch` read
only model and approval-history trails. Historical SQL migrations and stored
inbound audit/replay/attribution records remain; no database cleanup is implied.

## Slack and the public edge

Slack approval commands, notifications and the [posting fixture](slack.md) are
removed. Only [budget approvals](approvals.md) still grant authority through
admin. Historical tool/inbound requests remain readable/deniable, not
approvable; their grants are inactive.

Existing installations must follow the [retirement upgrade procedure](operations.md#upgrading-after-inbound-retirement):
old inbound/notifier configuration is rejected, and applying new manifests does
not delete the old public edge. Disable external subscriptions before releasing
its DNS name, then review and explicitly remove the obsolete owned resources.
