# Inbound bridge — retired guide

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This webhook guide is retired,
> not the running bridge code. Start at the [documentation index](README.md)
> for the current direction. This page remains for existing hooks and
> source references; it is not a recommendation to deploy another plane.

## Existing interface

The bridge in [inbound.go](../plane/internal/inbound/inbound.go) listens
on 8082 and accepts only hooks in the committed
[upstream table](../k8s/plane/upstreams.yaml). It invokes kagent at
`/api/a2a/<namespace>/<agent>/` using `message/send`, a hook-scoped
`x-user-id`, and kagent usage metadata. It is not a generic Orka invoker.

[verify.go](../plane/internal/inbound/verify.go) defines authentication:

- Generic HMAC: `X-Kaimahi-Delivery`, `X-Kaimahi-Timestamp` (Unix seconds),
  and `X-Kaimahi-Signature: v1=<hex HMAC-SHA256>` over the exact bytes
  `v1:<timestamp>:<delivery>:<body>`. Delivery IDs match
  `[A-Za-z0-9._:-]{1,128}`; timestamps must be within five minutes.
- Slack: its v0 request signature and timestamp, with the same window;
  replay identity comes from the signed envelope's `event_id`.
- Bearer: the hook's own opaque credential plus `X-Kaimahi-Delivery`.
  The proof travels with the request; prefer signatures where possible.

Generic event text is the JSON `text` field when present, otherwise the
body. It becomes prompt input, not trusted instructions or authorization.

## Admission and delivery limits

A live bounded [inbound grant](approvals.md), valid credentials, target
budget headroom and a writable audit trail are required. The budget
preview consumes nothing; the model proxy enforces spend separately.
The configured budget credential is not verified against the agent's mount.

Defaults are 64 KiB per body, 60 events/minute with burst 10 **per
replica**, and 16 outstanding invocations with two workers. A full queue
returns 503 before consuming a use. Admission and replay deduplication
are transactional in Postgres; an admitted duplicate is 409, whereas a
previously denied delivery remains retryable.

**202 means admitted, not delivered.** Workers append `completed` or
`failed`; an `admitted` row without an outcome can mean work lost on a
restart. The bounded queue is not durable. See [operations](operations.md)
for shutdown and recovery limits; use `kmx flow` to inspect the trails.

## Slack and the public edge

Only human `app_mention` events from configured channels trigger a turn;
other supported events are acknowledged and audited as ignored. Posting
needs its own tool permission. Approval commands run before the inbound
grant gate but require the separate [approver list](approvals.md#deciding-from-slack).
Permanent refusals carry `X-Slack-No-Retry: 1`; 429/503 remain retryable.

The optional AKS [edge](../k8s/inbound-edge.yaml) exposes only
`POST /hook/slack-events` over public TLS; kind has no public route.
Existing exposure/teardown mechanics remain in
[inbound-expose.sh](../scripts/inbound-expose.sh) and
[exposure-scan.sh](../scripts/exposure-scan.sh).
Remove Slack's Request URL or disable its subscription **before releasing
an edge DNS name**, which somebody else can claim. Socket Mode can prevent
HTTP event delivery even while URL verification succeeds. No public model,
MCP, admin or database ingress is implied by this edge.
