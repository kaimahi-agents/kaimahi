# Foreign-runtime seam — retired investigation

> **Legacy Kaimahi plane, not an Orka guarantee.** Orka is the platform;
> Kaimahi helps people get agents onto it. The horizontal governance-plane
> investigation and transcript are retired; protocol code remains. This
> page survives because CLI diagnostics and existing integrations point
> here. Start at the [documentation index](README.md) and [migration](migrate.md).

A migrated application keeps its **owner-managed Deployment**. Configuring
its model traffic does not transfer runtime ownership or govern its tools.
The seam bridge is transitional; shrinking it to nothing is success, not
an obligation to preserve a second platform.

## What a client still needs

- A plane-issued opaque token in `Authorization` (the `Bearer ` prefix
  is accepted), and an upstream name in the configured table.
- Tool URL: `https://kaimahi-mcp-gateway.kaimahi:8081/upstream/<name>/mcp`.
  Model URL: `https://kaimahi-proxy.kaimahi:8080/upstream/<name>/<client-path>`;
  use the entry's allowed client path, not a guessed protocol suffix.
- The plane CA: Secret `kaimahi-plane-ca`, key `ca.crt`, published into the
  credential Secret's namespace by `kmx tools govern`. Give that certificate
  to the HTTP client; **do not disable TLS verification**.
- Network access. The base [policy](../k8s/plane/network-policy.yaml)
  admits namespace `kagent` on the two data ports, not arbitrary namespaces.
  Additional ingress must select the proxy in `kaimahi` and the intended
  source namespace; client egress must also permit it. A NetworkPolicy in
  the client's namespace cannot grant ingress to a different namespace's pod.

A valid token from a blocked network position times out without an audit
row. Measure from the client, with an allowed control; server-side default
zero egress can otherwise masquerade as a seam failure. Host-networked
clients require separate policy analysis rather than a namespace assumption.

## Protocol and scaffolding limits

The gateway is stateless and checks each call, even without discovery or a
handshake. It supports MCP tools, not the complete MCP surface; see
[tool governance](tool-governance.md#enforcement-all-fail-closed) for methods,
projection, framing and request bounds. Do not advertise client sampling,
roots or elicitation: server-to-client requests are not supported here.

`kmx tools add` retains all four documents in its output but skips applying
the RemoteMCPServer if its CRD is absent. `kmx tools govern` then issues
the credential, applies policy and publishes the CA without patching an Agent.
An API/RBAC error is not treated as absence of kagent. For URL-only clients,
`kmx tools sidecar` generates a loopback credential proxy and an owner-applied
Deployment patch; see [sidecar.go](../internal/kmx/scaffold/sidecar.go).
Do not put bearer tokens in query strings or public manifests.

## What this does not establish

**There is no public model/MCP seam ingress.** Services are ClusterIP;
the certificate names in-cluster services and loopback, not an external
hostname. The [Slack edge](inbound.md) exposes neither data seam.
The inbound bridge invokes kagent A2A only, not an arbitrary runtime.
Model calls through the proxy are metered separately from turn invocation.

**`acted_for=none` is not evidence that no human was involved.** Without
an open inbound run the store writes `none`, including for independently
triggered applications. Caller headers are self-claims, and socket addresses
are not human identity. Read [identity](identity.md) before interpreting
these rows. Historical no-kagent experiments did not prove a general runtime
support contract, and this documentation retirement changes no runtime code.
