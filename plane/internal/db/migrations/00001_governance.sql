-- +goose Up
-- The governance floor: Kaimahi-issued credentials (the opaque tokens the
-- governed ModelConfig presets carry — only their sha256 is stored), the
-- per-credential monthly budget caps, and the append-only spend ledger.
-- Schema pattern follows tomte-old's spend_entry (13_spend_ledger.sql):
-- token counts are recorded even when cost is zero, so the ledger is
-- honest about usage on free/unpriced upstreams; cost_source says WHY a
-- cost is zero (no blanket $0 by inference — standing guidance).

CREATE TABLE credential (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL UNIQUE,
    token_hash  bytea NOT NULL UNIQUE, -- sha256 of the issued token; plaintext never stored
    -- Monthly caps (calendar month, UTC). NULL = no cap of that kind.
    cap_cents   bigint CHECK (cap_cents >= 0),
    cap_tokens  bigint CHECK (cap_tokens >= 0),
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entry (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    credential_name text NOT NULL,
    upstream        text NOT NULL,
    model           text NOT NULL,
    input_tokens    bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens   bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cost_cents      bigint NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    -- free: upstream explicitly classified $0; priced: a configured price
    -- row was applied; unpriced: metered upstream with no price row for
    -- this model (tokens still counted); denied: request never forwarded.
    cost_source     text NOT NULL CHECK (cost_source IN ('free', 'priced', 'unpriced', 'denied')),
    status          integer NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ledger_entry_credential_created ON ledger_entry (credential_name, created_at);

-- +goose Down
DROP TABLE ledger_entry;
DROP TABLE credential;
