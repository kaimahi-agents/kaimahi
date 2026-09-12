# Inbound bridge — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** The inbound runtime is
> removed, not just this guide. Start at the [documentation index](README.md)
> for current paths.

## Existing interface

There is no inbound listener on 8082, webhook delivery interface, inbound
request filing or inbound audit API/CLI view. `kmx flow` and `kmx watch` read
only model, tool and approval trails. Historical SQL migrations and stored
inbound audit/replay/attribution records remain; no database cleanup is implied.

## Slack and the public edge

Slack approval commands and notifications are removed. Tool and budget
[approvals](approvals.md) still use the admin path; the retained Slack MCP
[posting connector](slack.md) does not restore webhook or approver support.

Existing installations must follow the [retirement upgrade procedure](operations.md#upgrading-after-inbound-retirement):
old inbound/notifier configuration is rejected, and applying new manifests does
not delete the old public edge. Disable external subscriptions before releasing
its DNS name, then review and explicitly remove the obsolete owned resources.
