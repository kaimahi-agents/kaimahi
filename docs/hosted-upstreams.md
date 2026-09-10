# Legacy reference: hosted upstream custody and dialing

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. This reference remains for
> operators of the existing proxy/gateway and their credential diagnostics,
> not as an alternative platform onboarding guide. Start at the
> [documentation index](README.md) for the current direction.

## Existing paths

The [committed table](../k8s/plane/upstreams.yaml) marks internet upstreams
with `internet: true`: the Copilot model endpoint and the `github`,
`github-release` and `ado` MCP entries. The model proxy and MCP gateway
share one [hardened client](../plane/internal/egress/egress.go). An unmarked
entry must have an in-cluster hostname; omission is not an internet bypass.

The table names an endpoint and custody file, never token material.
Both a usable credential and a network allowance are needed for a keyed
hosted forward. The [hosted allowance](../k8s/egress-hosted.yaml) is separate
from the base plane deployment; the
[Copilot allowance](../k8s/egress-copilot.yaml) selects the same proxy pod
and opens the same public TCP port. Removing one does not revoke reach
while the other remains. NetworkPolicy rules are additive.

## Custody

Existing native capture commands are:

```sh
kmx credential capture github owner/name
kmx credential capture github-release owner/name
kmx credential capture ado <organization>
```

Use `--replace` for intentional replacement. Capture requires a terminal
with echo disabled; token values are not accepted as flags, environment
variables, files or piped stdin. Validation contacts the actual upstream
before storage. Kubernetes Secrets are rendered in memory rather than
written as manifests on disk. See [seam](../internal/kmx/seam/) and
[command definitions](../cmd/kmx/commands.go).

| Entry | Intended credential | What capture cannot prove |
|---|---|---|
| `github` | fine-grained, read-only token for the named repository | exact permissions or single-repository scope |
| `github-release` | fine-grained token with required write permissions | deletion is not separately excluded by Contents-write |
| `ado` | expiring Entra access token accepted by the hosted MCP server | a future refresh or lasting validity |

GitHub validation establishes that the token can read the named repository,
not that it can read only that repository. Do not infer confinement from
capture success. Gateway policy and reviewed server narrowing remain needed.
The hosted ADO seam is not the local stdio/PAT server; use its actual schema.
Organization names belong in runtime parameters, not committed identifiers.

Custody files are read per request, after Kubernetes has projected changes.
The plane does not renew an upstream access token itself. The release driver
can refresh ADO using the operator's session; stale credentials can appear
as missing discovered tools. See [release agent](release-agent.md).

Deleting custody Secrets stops future reads after projection; it does not
revoke an external token at its issuer or recall an in-flight request.
Close unused egress allowances and revoke tokens at the issuer as part of
retirement. Existing cleanup wiring remains in the [Makefile](../Makefile).

## The dialer's refusals

| Boundary | Existing behavior |
|---|---|
| Scheme/port/host | HTTPS on 443 to configured hosted hosts only |
| Resolution | every returned address checked; one prohibited answer refuses the call |
| Private reach | private, link-local, loopback, carrier-NAT, multicast, reserved and metadata ranges rejected, including embedded IPv4 forms |
| Rebinding | connects to the checked address; resolves again for each call, without connection reuse |
| Redirects | surfaced, never followed; gateway reports failure |
| Waiting | 10 seconds each for resolution/connect/TLS; response-header default 60 seconds |
| Body | at most 8 MiB and five minutes; a cut is an error, not silent truncation |

The deployed proxy sets `EGRESS_HEADER_TIMEOUT=180s`. A positive value up
to ten minutes is accepted; invalid values refuse startup. Changing this
buys patience, not a broader destination set. A buffered body cut becomes
502; a stream whose status was already sent ends and records the failure.

Hosted hosts are vetted at boot and at request time. `ca_file` replaces
system trust for its configured host and is read at boot; unreadable trust
material refuses configuration. Documentation-only address ranges are not
blocked by the dialer's private-address list, enabling isolated synthetic
tests; this is not a promise that those addresses are reachable publicly.

## The egress sentence

The network allowance is **TCP 443 to non-excluded public addresses**, not
“only GitHub” or “only Slack”, and a port rule alone does not enforce TLS.
The application client enforces HTTPS and configured hostnames. A compromised
pod can bypass that client and use its network allowance directly.
Core NetworkPolicy supplies no hostname selector or output-content filter.
See [egress](egress.md) for scope, IPv6 and CNI caveats.

## Changing a legacy upstream

Hosted/keyed entries require review of the committed table and Secret mounts
in [proxy.yaml](../k8s/plane/proxy.yaml). Operator overlays refuse
`credential_file`, `credential_header`, `internet`, `ca_file` and
`extra_headers`; allowing them would let an overlay redirect readable
credential material to a chosen host. Model overlays additionally refuse
prices. Do not work around that refusal in generated YAML.

Non-secret `extra_headers` may narrow the server's tool surface. A header
that replaces the credential slot is refused, and credentials are injected
last. Actual narrowing depends on the server honoring the header on calls,
not just discovery; inspect live behavior before relying on it.
Reapply configuration and restart the proxy, which reads its table at boot.

## Evidence retained

[Dialer tests](../plane/internal/egress/egress_test.go) cover refusals and
rebinding; [body tests](../plane/internal/egress/bounded_body_test.go) cover
limits. The [synthetic upstream](../scripts/ci/synthetic-upstream.sh) and
[MCP echo server](../scripts/ci/mcp-echo-server.py) test the network path
without real provider credentials. They do not prove a live GitHub/ADO
service accepts today's token or still offers yesterday's tool schema.
Historical live transcripts and duplicate setup tutorials are retired.
