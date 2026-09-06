-- +goose Up
-- Tool governance: the per-credential tool allowlist the MCP gateway
-- enforces (and projects into tools/list), and the append-only tool-call
-- audit trail. A separate table from ledger_entry on purpose: spend rows
-- carry cost semantics (cost_source, token CHECKs) that tool actions do
-- not have; the append-only, fail-closed machinery is shared in code.

-- No row for a credential = nothing callable (fail closed). The gateway
-- reads the whole set per request; allowlists are demo-scale by design.
CREATE TABLE tool_allowlist (
    credential_name text NOT NULL,
    tool            text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_name, tool)
);

CREATE TABLE tool_audit (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    credential_name text NOT NULL,
    upstream        text NOT NULL,
    method          text NOT NULL,
    -- The tool named by a tools/call; empty for other audited methods.
    tool            text NOT NULL DEFAULT '',
    -- allowed: relayed upstream (status = upstream HTTP status);
    -- denied: refused by the gateway (status = the refusal's semantic
    -- status: 403 for policy denials — including those answered on the
    -- wire as JSON-RPC errors over HTTP 200 — 503/400 for the rest).
    decision        text NOT NULL CHECK (decision IN ('allowed', 'denied')),
    status          integer NOT NULL,
    -- Human-readable refusal reason on denials; empty otherwise.
    detail          text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tool_audit_credential_created ON tool_audit (credential_name, created_at);

-- +goose Down
DROP TABLE tool_audit;
DROP TABLE tool_allowlist;
