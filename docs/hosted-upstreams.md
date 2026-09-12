# Legacy reference: hosted upstream custody and dialing

This reference covers the retained **model** proxy, not Orka's platform
contract. Hosted GitHub/ADO tool routes and their credential capture are removed
with the gateway. Their former procedures remain only in the
[pre-retirement source at `10c561d`](https://github.com/kaimahi-agents/kaimahi/blob/10c561d4a890244e240d9d223d20059b1464e957/docs/hosted-upstreams.md).
Start at the [documentation index](README.md) for current onboarding.

## Existing paths

The [committed table](../k8s/plane/upstreams.yaml) marks the Copilot model
endpoint `internet: true`. The retained [hardened client](../plane/internal/egress/egress.go)
restricts hosted dialing. Unmarked entries require in-cluster hostnames;
omission is not an internet bypass.

A keyed forward needs both a usable custody file and a network allowance.
[Copilot egress](../k8s/egress-copilot.yaml) is separate from the base plane;
it permits public TCP 443, not only one hostname. NetworkPolicy is additive:
old hosted-tool allowances can keep that port reachable after an upgrade.
See [explicit cleanup](operations.md#upgrading-after-gateway-retirement).

## Custody

`kmx models credential copilot` retains its device login, private OAuth cache
and short-lived exchange into plane custody. It applies egress and restarts an
existing proxy; it does not populate the direct kagent preset's Secret.
[Models](models.md) and [AKS](aks.md#the-credential-handoff) document the retained
paths. Tool capture commands and Slack credential helpers are removed.

Custody files are read per request after Kubernetes projection. The plane does
not renew upstream tokens itself. Deleting a Secret does not revoke an external
token or recall an in-flight request. Review unused tool-only custody with its
owner; preserve model/Copilot/Orka credentials still used by the bridge.

## The dialer's refusals

| Boundary | Existing behavior |
|---|---|
| Scheme/port/host | HTTPS on 443 to configured hosted hosts only |
| Resolution | every returned address checked; one prohibited answer refuses the call |
| Private reach | private, link-local, loopback, carrier-NAT, multicast, reserved and metadata ranges rejected, including embedded IPv4 forms |
| Rebinding | connects to the checked address; resolves again for each call, without connection reuse |
| Redirects | surfaced, never followed |
| Waiting | 10 seconds each for resolution/connect/TLS; response-header default 60 seconds |
| Body | at most 8 MiB and five minutes; a cut is an error, not silent truncation |

The deployed proxy sets `EGRESS_HEADER_TIMEOUT=180s`. A positive value up to
ten minutes is accepted; invalid values refuse startup. This buys patience,
not a broader destination set. A buffered body cut becomes 502; a stream whose
status was already sent ends and records the failure.

Hosted hosts are vetted at boot and request time. `ca_file` replaces system
trust for its configured host and is read at boot; unreadable trust material
refuses configuration. Documentation-only address ranges remain usable in
isolated synthetic tests, not guaranteed publicly reachable.

## The egress sentence

The network allowance is **TCP 443 to non-excluded public addresses**, not
“only GitHub”; a port rule alone does not enforce TLS. The application client
enforces HTTPS and configured hostnames. A compromised pod can bypass that
client and use its network allowance directly. Core NetworkPolicy supplies no
hostname selector or output-content filter. See [egress](egress.md).

## Changing a legacy upstream

Hosted/keyed model entries require review of the committed table and Secret
mounts in [proxy.yaml](../k8s/plane/proxy.yaml). Operator model overlays refuse
`credential_file`, `credential_header`, `internet`, `ca_file`, `extra_headers`
and `prices`; allowing custody overrides could redirect readable credentials
to a chosen host. Do not work around those refusals in generated YAML.

The model table still uses non-secret headers, including the migration route's
functional `X-Orka-Tools: disabled`; this is not retained gateway policy.
See [Responses translation](migrate.md#responses-translation-and-refusals).
Reapply reviewed configuration and restart the proxy, which reads its table
at boot.

## Evidence retained

[Dialer tests](../plane/internal/egress/egress_test.go) cover refusals/rebinding;
[body tests](../plane/internal/egress/bounded_body_test.go) cover bounds. Retired
MCP synthetic fixtures are not current verification. These tests do not prove
that a live provider accepts today's credential or returns usable model usage.
