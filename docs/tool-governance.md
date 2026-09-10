# Legacy reference: MCP gateway and argument policy

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The gateway code remains, so
> this page retains its protocol and policy contract. It is not a reason
> to install a parallel governance platform. Start at the
> [documentation index](README.md) for current onboarding.

## Scope and custody

The gateway is the proxy process's TLS listener on 8081, exposed internally
as `kaimahi-mcp-gateway`. Its route is `/upstream/<name>/mcp` (optional
trailing slash), where `name` must exist in the
[tool upstream table](../k8s/plane/upstreams.yaml).

The caller presents a plane-issued opaque credential. The gateway removes
caller credential-slot headers and injects an upstream credential only
from plane-side custody where configured. A missing custody file refuses
the forward. Agents do not need the real upstream token on this path.
Direct connections are outside this policy; [egress](egress.md) describes
where NetworkPolicy actually prevents them.

The [handler](../plane/internal/gateway/gateway.go) is stateless: each
message is authenticated and checked, even without an earlier handshake.
Unknown credentials return 401, expired credentials 403, unreadable
credential storage 503; an unknown upstream is refused before dialing.

## Declaring what arguments mean

Each tool can declare `policy_fields` in the table:

| Declaration | Digest binds | Consequence |
|---|---|---|
| `["invoice_id", "amount_cents", "payee_id"]` | those present fields | changes to undeclared fields do not need a different grant |
| `[]` | tool/binding mode, no arguments | a grant permits any arguments: weakest setting |
| no declaration | whole canonical argument object | extra or changed fields can invalidate a retry's grant |

A declaration with no `policy_fields` key is invalid. Field names are
top-level `[A-Za-z0-9_-]{1,64}`; nested paths are not addressable.
Declarations are global by tool name: conflicting declarations on different
upstreams are refused. Allowlists are also **credential/tool-name scoped,
not upstream scoped**. Adding a same-named tool on another upstream can
make it callable under an existing allowlist.

[canon.go](../plane/internal/gateway/canon.go) rejects duplicate JSON keys
at any depth, excessive depth/node count, non-object arguments and batches.
Policy evaluation, digest, audit summary and forwarded bytes derive from
one parsed canonical tree. Object key ordering and whitespace do not change
the binding; integer `48000` and decimal `48000.0` are distinct values.

[digest.go](../plane/internal/gateway/digest.go) binds the tool, mode and
selected arguments. The human summary is restricted to declared fields,
scalar rendering, 64 bytes per rendered value and 240 bytes overall.
Nested values are labeled as objects/lists, not expanded. It is neither a
complete payload nor an output-redaction mechanism. Choose fields deliberately.

## Enforcement, all fail-closed

1. An in-bound standing constraint admits a call without consuming a grant.
2. Otherwise a static allowlist admits only when no constraint exists for
   that credential/tool. A constraint is a bound, not another way in.
3. Otherwise a matching live call-bound grant admits and consumes one use
   before forwarding. Old NULL-digest grants are a closed verb-level class,
   consumed after exact matches; no new NULL-digest tool grant is issued.
4. Otherwise the call is denied and an [approval request](approvals.md)
   is filed. A filing failure never changes the denial into admission.

Supported MCP methods are `initialize`, `notifications/initialized`,
`tools/list`, `tools/call`, and locally answered `ping`. Other methods
are denied. POST carries messages, DELETE ends a session; GET is not a
server-event stream. The request body limit is 4 MiB.

`initialize` capabilities are narrowed to tools only, with no
`listChanged` promise. Its projection buffers at most 1 MiB; `tools/list`
projection at most 8 MiB. Oversized/unparseable successful projections are
refused rather than truncated. Projected replies are JSON; otherwise clients
must tolerate upstream JSON or SSE framing, including error responses.
Client-advertised sampling, roots and elicitation are not made functional:
server-to-client requests and client response messages are unsupported.

`tools/list` exposes the allowlist plus live-granted and constrained tools.
That is visibility of potentially permitted calls, **not permission for all
arguments**. kagent further intersects discovery with its own `toolNames`.
Discovery can be stale; admission is always checked on the actual call.

Every tool-call outcome and attributable denial is appended to `tool_audit`.
Allowed rows are written after the response, so audit recording is not
atomic with an external action. A failed write trips that replica's gateway
to 503 until a later write succeeds. Pre-auth failures have no credential
attribution. Read status and detail: `allowed` alone proves neither upstream
success nor completed side effects. Caller/actor limits are in [identity](identity.md).

## Existing operator commands

```sh
kmx tools allowlist hello-tools
kmx tools allow k8s_get_resources --credential hello-tools
kmx audit tool hello-tools
```

`tools allow` replaces the gateway allowlist, not the agent's selection;
`-` empties it but does not remove standing constraints or live grants.
`kmx tools govern` orders credential, allowlist, seam acceptance and agent
patching. [Bring-your-own seams](govern-your-agent.md) retains scaffold and
overlay caveats; [tools](tools.md) describes the direct kagent example.

## Configuration and upstream narrowing

The table and constraints load at boot; apply changes and roll the proxy.
Operator overlays cannot replace committed entries or set custody/hosted
fields (`credential_file`, `credential_header`, `internet`, `ca_file`,
`extra_headers`). Invalid config refuses the new process, not an existing
replica's loaded table. See [overlay.go](../plane/internal/config/overlay.go).

Committed `extra_headers` can request upstream tool narrowing. Enforcement
of those headers belongs to the upstream server; verify its actual surface,
not only its listing or documentation. Config rejects a header that replaces
the credential slot, and custody injection happens last. Hosted dialing
limits are in [hosted upstreams](hosted-upstreams.md).

Argument policy governs inputs only. Tool results pass through without
filtering or redaction. Gateway tests in
[plane/internal/gateway](../plane/internal/gateway/) remain; this reference
retires the duplicate demos, not their runtime or regression coverage.
