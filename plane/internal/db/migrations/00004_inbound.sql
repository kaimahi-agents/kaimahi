-- +goose Up
-- Inbound connectors: the first INGRESS surface in the plane. An
-- external event (a webhook) may trigger a kagent agent through the
-- plane — authenticated, replay-protected, rate-limited, approved,
-- budget-gated, and audited. Two changes:
--
-- 1. A third grant kind, 'inbound'. Triggering an agent from outside is
--    consequential, so it is an APPROVABLE action like any other: an
--    event on a hook with no live grant is denied and files a pending
--    request (subject = hook name); a human approves it BOUNDED (uses
--    and/or TTL), and each admitted event consumes one use. That reuses
--    the permit machinery unchanged and gives the operator the one lever
--    an ingress needs most — an event-count bound — without a new table.
--    Widening a CHECK is the whole schema cost; a grant of this kind
--    carries no amount, exactly like a tool grant.
ALTER TABLE approval_request DROP CONSTRAINT approval_request_kind_check;
ALTER TABLE approval_request ADD CONSTRAINT approval_request_kind_check
    CHECK (kind IN ('tool', 'budget', 'inbound'));
ALTER TABLE permit_grant DROP CONSTRAINT permit_grant_kind_check;
ALTER TABLE permit_grant ADD CONSTRAINT permit_grant_kind_check
    CHECK (kind IN ('tool', 'budget', 'inbound'));

-- 2. The inbound audit trail. Append-only like ledger_entry and
--    tool_audit: no delete path, and the only UPDATE is inside the
--    admitting transaction (the grant id is written onto the row before
--    it commits; nothing touches a committed row). Every decision about
--    an attributable event is a row, and an admitted event gets a SECOND
--    row when its invocation finishes ('completed' or 'failed') — the
--    outcome is appended, never patched onto the admission.
--
--    The admission row doubles as the REPLAY GUARD: (hook, delivery_id)
--    is unique among admitted rows, so an event the plane already
--    honoured cannot be honoured twice, and the admission insert is the
--    same transaction that consumes the grant use — no use is burned on
--    a replay, and no event is admitted without a recorded row (an event
--    that cannot be recorded must not be honoured). Denied deliveries
--    stay retryable on purpose: a source that retries after its event
--    was denied (no grant yet, budget out) is delivering, not replaying.
CREATE TABLE inbound_audit (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    hook            text NOT NULL,
    -- The hook's credential (config-bound; the identity charged and
    -- granted). Plain text, no FK, like ledger_entry — audit rows must
    -- outlive a deleted credential.
    credential_name text NOT NULL,
    -- The source's delivery/event id (a Kaimahi delivery header, or a
    -- Slack event_id); empty on rows refused before one could be read.
    delivery_id     text NOT NULL DEFAULT '',
    -- denied: refused by the plane (status = the refusal's HTTP status);
    -- admitted: authenticated, granted, queued (status 202);
    -- completed / failed: the invocation's outcome (status = the A2A
    -- endpoint's HTTP status, 0 when unreachable);
    -- challenge: a source's URL-verification handshake answered without
    -- triggering anything.
    decision        text NOT NULL CHECK (decision IN ('denied', 'admitted', 'completed', 'failed', 'challenge')),
    status          integer NOT NULL,
    -- Human-readable reason on denials/failures, the grant used on
    -- admissions, the task id on outcomes; empty otherwise.
    detail          text NOT NULL DEFAULT '',
    -- The agent the hook targets, as "namespace/name".
    agent           text NOT NULL DEFAULT '',
    -- Token usage the agent runtime REPORTED for this invocation
    -- (kagent_usage_metadata), on outcome rows — a 'failed' task can
    -- have spent tokens too, and says so. Spend attribution
    -- only: the spend itself is ledgered by the proxy under the agent's
    -- governed credential; these counts are never priced here.
    input_tokens    bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens   bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX inbound_audit_admitted_delivery
    ON inbound_audit (hook, delivery_id)
    WHERE decision = 'admitted';

CREATE INDEX inbound_audit_hook_created ON inbound_audit (hook, created_at);

-- +goose Down
DROP TABLE inbound_audit;
ALTER TABLE permit_grant DROP CONSTRAINT permit_grant_kind_check;
ALTER TABLE permit_grant ADD CONSTRAINT permit_grant_kind_check
    CHECK (kind IN ('tool', 'budget'));
ALTER TABLE approval_request DROP CONSTRAINT approval_request_kind_check;
ALTER TABLE approval_request ADD CONSTRAINT approval_request_kind_check
    CHECK (kind IN ('tool', 'budget'));
