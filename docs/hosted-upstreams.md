# Hosted upstreams: the gateway reaches an MCP server on the internet

Until now every tool server behind the MCP gateway ran inside the
cluster, and the docs said why: there was no hardened dialer and no
SSRF protection, so an internet-facing upstream must not slip in. This
page is that path, built. The demo upstream is GitHub's hosted MCP
server: a governed agent reads what is open on a repository through the
gateway, allowlisted and audited, with the GitHub token in plane custody
and never in the agent's hands.

The same change hardens the one internet upstream the plane already
had. The LLM proxy's Copilot path and the gateway's hosted tool
upstreams now go through **one** dialer, and the committed table says
so for both (`internet: true`), so nothing about either is implicit.

## What it is

```
  agent pod (kagent)                          plane pod (kaimahi)
  ┌────────────────────┐   kmh_ token   ┌──────────────────────────┐
  │ hello-github       │ ─────────────▶ │ MCP gateway :8081        │
  │ (governed-copilot) │                │  authenticate, allowlist, │
  └────────────────────┘                │  audit; then for an       │
                                        │  `internet: true` upstream│
                                        │  the HARDENED DIALER:     │
                                        │  https · 443 · host pinned│
                                        │  resolve → check EVERY    │
                                        │  address → connect to the │
                                        │  checked address · no     │
                                        │  redirects · bounded ·    │
                                        │  capped · GitHub token    │
                                        │  injected from custody    │
                                        └────────────┬─────────────┘
                     NetworkPolicy: 443 to public only (opt-in)   │
                                                                  ▼
                                             api.githubcopilot.com/mcp/
```

Three things are new, and each is a separate opt-in:

- **The table entry.** `tool_upstreams.github` in
  [`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml) names
  GitHub's endpoint, carries `internet: true`, and names (never carries)
  a plane-side Secret in `credential_file`. It is committed, so the
  plane always knows the entry and vets its host at boot; without the
  Secret and the allowance below, a call to it fails closed.
- **The credential.** `kmx credential capture github owner/name` (or
  `make github-secret GITHUB_REPO=owner/name`, which is the same command)
  reads a fine-grained, read-only, one-repository token AT A PROMPT,
  proves it can read that repository, and stores it as
  `kaimahi/kaimahi-github-pat`. The gateway injects it per request.
  `make github-revoke` deletes it.
- **The allowance.** [`k8s/egress-hosted.yaml`](../k8s/egress-hosted.yaml)
  lets the proxy pod (the gateway lives in it) open TCP 443 to public
  addresses. `make github-secret` applies it; `make github-revoke` removes
  it; on kind and in CI it is never applied by default. The capture
  applies it for you, because a stored credential the gateway cannot
  reach the internet with is a credential that cannot be used.

Then `make govern-github` issues the agent's credential with a read-only
allowlist and puts `hello-github` behind the seam, and
`make github-ask GITHUB_REPO=owner/name` asks it what is open.

## Custody

The GitHub token is the plane's, exactly like the Copilot token:

- Captured at a prompt by `kmx credential capture github owner/name`
  (`internal/kmx/seam`), with the terminal's echo off. The value is read
  from a terminal or not at all — a pipe, a redirect, a flag, an
  environment variable and a file are all refused, so the token cannot
  arrive from a shell history or a CI log. It never reaches argv, a
  file, a log or a manifest on disk: the Secret is rendered in memory and
  piped to `kubectl apply -f -`. Nothing is stored unless GitHub answers
  a well-formed positive for the named repository.
- Only a **fine-grained** token is accepted (`github_pat_` prefix; a
  classic PAT or an OAuth token is refused). Scope it to one repository
  with Issues: Read and Pull requests: Read. The capture proves the token
  reads that one repository; GitHub does not expose a fine-grained
  token's permissions, so read-only is your choice at creation, and the
  gateway allowlist, which never names a write tool, is the layer the
  plane enforces.
- Mounted into the proxy pod only, at the path the table names, read per
  request (rotation needs no restart), and added to the log redactor at
  boot.
- The agent's Secret (`kagent/kaimahi-github-token`) holds only its
  `kmh_` token; CI asserts the shape, as it does for Slack.

**Why not the Copilot token.** GitHub's hosted server accepts any GitHub
bearer token, and the Copilot device-flow login does authenticate to it
(verified 2026-09-02). But the plane only ever holds the *exchanged*
Copilot token, which expires in about thirty minutes, and the OAuth
token behind it is scoped `read:user`, which reads public repositories
only. A tool credential that dies every half hour and cannot be pinned
to one repository is the wrong shape; a fine-grained token is the right
one.

## The dialer's refusals

[`plane/internal/egress`](../plane/internal/egress/egress.go) is the one
hardened client, built once in `main` and injected into both the LLM
proxy and the MCP gateway (a test asserts they hold the same client).
Every upstream marked `internet: true` goes through it; every other
upstream keeps the plain in-cluster dial. What it refuses, all
fail-closed:

| Refused | How |
|---|---|
| Anything but `https` on port 443 | At config load, and again in the client before any network contact |
| A host the table does not name | The client knows only the hosts of the marked entries |
| A host that resolves to a private, link-local, loopback, carrier-NAT, multicast, reserved or cloud-metadata address (`169.254.169.254` first of all), in IPv4, IPv6, IPv4-mapped or NAT64 form | Every resolved address is checked; one bad answer refuses the call. A private answer at boot refuses the config loudly (the pod does not start); a private answer later refuses that call, audited |
| DNS rebinding: a record that changes after the check | The connection goes to the address that was checked, never back through the name, and every call resolves afresh (no connection reuse) |
| Redirects | Surfaced, never followed; the gateway answers 502 and audits it |
| A silent upstream | 10 s each to resolve, to connect and to handshake; **180 s to start answering**, which is what the committed plane runs. That last bound is the one operator-adjustable number in this table: the dialer's own default is 60 s, `k8s/plane/proxy.yaml` sets `EGRESS_HEADER_TIMEOUT: 180s`, and any value up to 10 minutes is accepted — outside that the proxy refuses at boot. It is adjustable because a long reasoning completion legitimately exceeds a minute to first token and surfaced as a 502 naming http2 rather than the cause. It buys patience, never reach: every other refusal here is fixed |
| A stalled or oversized body | Cut at 5 minutes or 8 MiB; the read fails with a named error rather than truncating silently. A buffered body becomes a 502 on both seams and the gateway's audit row says so; a streamed (SSE) body has already carried its status, so the stream ends and the row notes the cut |

The documentation ranges (`192.0.2.0/24`, `198.51.100.0/24`,
`203.0.113.0/24`, `2001:db8::/32`) are deliberately not refused: they
are neither private nor routable, which is what lets CI stand a
synthetic upstream on one without loosening the list for it.

A `ca_file` on an entry replaces the system roots for that one host
(how CI's stand-in presents a throwaway certificate). It is read at
boot; an unreadable one refuses the config. Nothing committed uses one.

What the dialer does not do: it does not inspect what a tool server
returns beyond size and time, and it does not make an IP/port rule into
a hostname rule. The next section is about that.

## The egress sentence

The allowance is TCP 443 to any public host; the upstream table pins the
host, the dialer refuses private addresses. Not "only
api.githubcopilot.com": NetworkPolicy has no hostname selector, and
[egress.md](egress.md) says the same about the Slack pod and the Copilot
allowance. What bounds the destination is the layer above the network
(the gateway forwards to exactly the URL the committed table names,
refuses redirects, and dials only through the hardened client) and the
layer below it (the private ranges are excluded from the allowance, so
it cannot be turned back into the cluster).

The gateway and the LLM proxy are one pod, so the Copilot allowance and
this one select the same pod and open the same port. A proxy with
Copilot enabled already has this reach. The second file exists so that a
hosted *tool* upstream is an opt-in of its own, with its own inverse, and
CI asserts the two files are the exact same shape and that neither ever
lands in `k8s/plane/`.

## Proven how

CI is keyless (no GitHub token exists there, ever), so the hosted path
is proven against a **synthetic** upstream: a tiny https MCP echo server
([`scripts/ci/mcp-echo-server.py`](../scripts/ci/mcp-echo-server.py))
in a sidecar container on kind's docker network, holding `203.0.113.10`
as a secondary address, routed from the node with the destination
unchanged, so the NetworkPolicy is evaluated on that public-looking
address exactly as it would be for GitHub. The cluster resolves two
names to it through a CoreDNS hosts entry, and the gateway trusts its
throwaway certificate through `ca_file` only
([`scripts/ci/synthetic-upstream.sh`](../scripts/ci/synthetic-upstream.sh)).
Every PR then asserts, in order:

1. Every hosted host is vetted at boot (the stand-in and its rebind
   name through their `ca_file`, GitHub through the system roots); a
   table entry whose host resolves private is refused at load, loudly,
   while the serving replicas stay up.
2. The credential's allowlist holds one read tool, the write tool is
   absent, the agent-side Secret is `kmh_` only.
3. With the allowance applied, a governed call is answered by the
   stand-in and audited `allowed 200`.
4. The write tool is denied (403, a request filed); a redirecting
   stand-in is refused and audited.
5. The record for the second name is rebound to the sidecar's private
   docker address: the dialer refuses before any byte leaves, and the
   audit row carries the reason (`… resolves to 172.x.x.x (private (RFC
   1918))`).
6. The allowance is removed: the dial fails closed (`unreachable`),
   audited.

The unit tests cover the refusal table (private, loopback, link-local,
metadata, carrier-NAT, multicast, reserved, IPv6, IPv4-mapped, NAT64),
the rebinding case with a scripted resolver, scheme and port
enforcement, the size cap, the body lifetime, the header timeout, the
`ca_file` trust boundary, and the config-load refusals.

GitHub itself was verified once, by hand, on a kind cluster with the
worker's own read-only token, then the token was deleted; the transcript
is in the PR that shipped this page.

## From zero

```sh
make up && make plane
make plane-copilot-secret                      # the demo agent thinks on governed Copilot
make govern                                    # the governed presets
make github-secret GITHUB_REPO=owner/name      # paste the fine-grained token; applies the allowance
make govern-github                             # credential, read-only allowlist, seam, agent
make github-ask GITHUB_REPO=owner/name         # "what is open on …?"
make github-audit                              # allowed 200 rows for list_issues / list_pull_requests
```

Ask it to create an issue and the call is denied and a request is filed
(`make approvals`); an approval would mint a bounded grant for
`issue_write` exactly as in [approvals.md](approvals.md), and a granted
write would still stop at GitHub, because the token is read-only. When
done: `make github-down` removes the agent and the seam;
`make github-revoke` deletes the token and closes the allowance.

## How to add another hosted server

1. Add a `tool_upstreams` entry **to the committed table**
   ([`k8s/plane/upstreams.yaml`](../k8s/plane/upstreams.yaml)): an `https`
   URL on 443, `internet: true`, and a `credential_file` naming a mounted
   Secret path if the server is keyed. This cannot be done from an
   operator overlay — the plane refuses `internet`, `ca_file`,
   `credential_file`, `credential_header` and `extra_headers` in an
   overlay fragment, so a hosted or keyed upstream is a reviewed change to
   this repository by design. Add `extra_headers` if the server lets a
   caller narrow what it offers — do that before relying on the allowlist
   alone. Add the Secret mount to
   [`k8s/plane/proxy.yaml`](../k8s/plane/proxy.yaml) as an optional
   volume, and a row in `internal/kmx/seam`'s table — the seam's name,
   the Secret it stores, the subject it is scoped to, and a check that
   vets the token the way that server can be vetted. A new upstream is a
   row, not a new command.
2. If the server's certificate is not from a public CA, put the PEM in
   the `kaimahi-upstream-ca` ConfigMap and name it in `ca_file`.
3. Apply the allowance (`make egress-hosted`) when the server is
   configured, and remove it (`make egress-hosted-off`) when it is not.
4. Issue a credential per upstream with a read-only allowlist; leave the
   write tools to approvals.
5. Roll the proxy: the table is a `subPath` mount and is read at boot,
   and the boot log will say `hosted upstream vetted` with the addresses
   and the trust anchor, or refuse the entry and say why.

The release agent removed two of the three things this section used to say
were out of scope. There are now three hosted seams, all on the same dialer
and the same opt-in allowance:

| Entry | Server | Credential |
|---|---|---|
| `github` | GitHub's hosted MCP server | a fine-grained, **read-only**, one-repository token |
| `github-release` | the same server, narrowed by header | a fine-grained, one-repository token that can **write** |
| `ado` | Microsoft's hosted Azure DevOps MCP server | a Microsoft **Entra access token** — that server accepts nothing else |

So an OAuth-authenticated hosted server exists (`ado`, and its token lives
about an hour), and write tools on GitHub exist — each write denied by
default, filed naming the artifact, and approved call by call. See
[release-agent.md](release-agent.md).

A hosted entry may also carry `extra_headers`: committed-table only (an
overlay is refused), non-secret headers set on every forwarded call. That is how `github-release` and
`ado` narrow what their servers are willing to OFFER
(`X-MCP-Toolsets`, `X-MCP-Tools`, `X-MCP-Exclude-Tools`), before
discovery and therefore before the allowlist ever runs — a tool excluded
this way is not in `tools/list`, is not projected, and cannot be reached
even by an approval. A header naming the credential slot is refused at
load, and the credential is injected last regardless, so a committed
header can never displace one held in custody.

Still not in scope, and said so: hostname-level egress (a CNI with FQDN
policies or an egress gateway). The consolidated status of every
governed and ungoverned surface is in
[README.md](README.md#what-is-governed-today-and-what-is-not).
