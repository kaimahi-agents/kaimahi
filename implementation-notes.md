# W45 — the tool seam works for a client we did not write

Closes items 3, 4 and 5 of `docs/reviews/2026-09-08-foreign-app-sundae-funday.md`.
This lane changes what the seam ACCEPTS, never what it permits.

## Confirmed facts (evidence)

- `initialize` and `notifications/initialized` share one case and take the
  streaming `forward` path (`plane/internal/gateway/gateway.go:356`); only
  `tools/list` gets the buffered projection path (`:378`, `forwardProjected`
  `:664`). Successful lifecycle relays are not audited and the test pins that
  (`gateway_test.go:241`).
- `lastSSEData` returns the LAST event's data (`gateway.go:892`), not the frame
  whose JSON-RPC id matches the request. Fine for a listing, not for a
  handshake that may be followed by another frame.
- `copyResponseHeaders` copies the upstream `Content-Type`, including
  `text/event-stream`; only `writeRPC` overwrites it (`gateway.go:256`).
- Both data seams serve TLS (`plane/cmd/kaimahi-proxy/main.go:307`). The
  report's nginx snippet (§4.2) does `proxy_pass http://…:8081` and could not
  work as printed. The serving certificate carries `kaimahi-mcp-gateway.kaimahi`
  (`internal/kmx/seamcert/seamcert.go:110`).
- The plane CA is published ONLY into `kagent`
  (`internal/kmx/app/certificate.go:195`). An adopter namespace gets nothing to
  verify the seam against.
- A missing CRD is NOT a NotFound and the repo asserts that deliberately
  (`internal/kmx/app/notfound_test.go:26`). Detection needs a positive probe.
- `GenerateUpstream` renders four documents from a literal slice and cannot emit
  a subset (`internal/kmx/scaffold/upstream_yaml.go:64`). The four-document
  count is pinned by `upstream_counts_test.go` and stated in `docs/kmx.md`.
- `GovernTools` touches kagent in six places (`internal/kmx/app/tools.go`:
  baseline `:143`, CA publish `:162`, seam apply/preflight `:168`, verdict wait
  `:193`, agent patch `:212`, `waitSwitched` `:215`, `waitAgentReady` `:218`).
  Its credential Secret defaults to namespace `kagent`.

## Decisions

- **Buffer bound for `initialize`: 1 MiB** (`maxInitializeResp`), an eighth of
  the `tools/list` ceiling. A handshake carries capabilities, `serverInfo` and
  `instructions`; a megabyte is far beyond any of those and still bounded.
  **At the limit the response is REFUSED with a 502, never truncated** — the
  client sees an error, not a handshake with fields quietly missing.
- **The projected `initialize` picks the SSE frame whose id matches the
  request**, and fails closed when there is none. Stricter than the
  `tools/list` path, which takes the last frame.
- **The credential path for a header-less client is the nginx shim, scaffolded
  by `kmx tools sidecar`** — not a query parameter. A token in a URL reaches:
  the ingress/load-balancer access log, the client library's own request log,
  every intermediate proxy's log, `kubectl logs` on anything that logs a request
  line, and shell history. None is a place a bearer token may land.
- **kmx skips the CRD rather than refusing.** The written artifact keeps all
  four documents; the APPLY is what skips the `RemoteMCPServer` when the CRD is
  absent. `kmx tools govern` preflights the namespace BEFORE minting a token,
  because the token is shown once and the report lost one to this.

## Deviations / discoveries

(filled in as the lane runs)
