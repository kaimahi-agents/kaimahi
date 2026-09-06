-- +goose Up
-- Approvals are time-boxed permits. A denied action files a
-- pending approval request; a human approval mints a BOUNDED grant that
-- compiles onto the existing enforcement rows (tool allowlist checks,
-- budget caps) at decision time. Fail-closed inheritances from the
-- ported permit discipline (tomte-old permit.go): deny-all is the
-- ABSENCE of a grant, and a grant allowing everything forever is an
-- error — at least one bound is REQUIRED (the table CHECK enforces it).

CREATE TABLE approval_request (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Bound to a REAL credential: a request (and thus a grant) for a
    -- name that doesn't exist yet would lie dormant and activate the
    -- moment someone mints that name. CASCADE: a deleted credential's
    -- requests die with it (audit rows below carry the history).
    credential_name text NOT NULL REFERENCES credential (name) ON DELETE CASCADE,
    -- tool: subject is the tool name; budget: subject is which cap was
    -- exceeded ('tokens' or 'cents').
    kind            text NOT NULL CHECK (kind IN ('tool', 'budget')),
    subject         text NOT NULL,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied')),
    detail          text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    decided_at      timestamptz
);

-- Dedupe: a retry loop must not spam the queue — at most one PENDING
-- request per (credential, kind, subject); auto-filing inserts with
-- ON CONFLICT DO NOTHING against this index.
CREATE UNIQUE INDEX approval_request_pending_uniq
    ON approval_request (credential_name, kind, subject)
    WHERE status = 'pending';

-- "grant" is a reserved word in SQL; permit_grant echoes the ported
-- package's name.
CREATE TABLE permit_grant (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      uuid NOT NULL REFERENCES approval_request (id) ON DELETE CASCADE,
    credential_name text NOT NULL REFERENCES credential (name) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('tool', 'budget')),
    subject         text NOT NULL,
    -- Bounds. NULL means unbounded in that dimension, but never in both:
    expires_at      timestamptz,
    max_uses        integer CHECK (max_uses > 0),
    uses            integer NOT NULL DEFAULT 0 CHECK (uses >= 0),
    CHECK (expires_at IS NOT NULL OR max_uses IS NOT NULL),
    -- Budget grants raise the effective cap by amount (tokens or cents,
    -- per subject) while live; tool grants carry none.
    amount          bigint CHECK (amount > 0),
    CHECK ((kind = 'budget') = (amount IS NOT NULL)),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX permit_grant_lookup ON permit_grant (credential_name, kind, subject, created_at);

-- The approvals' own append-only trail: who decided is the admin bearer
-- (the only writer the admin port admits); what/when/bounds/outcome are
-- recorded per action. Enforcement audit (ledger, tool_audit) is never
-- suppressed by approval state — this trail is additional.
CREATE TABLE approval_audit (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      uuid NOT NULL,
    credential_name text NOT NULL,
    kind            text NOT NULL,
    subject         text NOT NULL,
    action          text NOT NULL CHECK (action IN ('requested', 'approved', 'denied')),
    -- Human-readable bounds on 'approved'
    -- ("expires=2026-09-01T13:51:08Z uses=1 amount=500"); empty otherwise.
    bounds          text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX approval_audit_credential_created ON approval_audit (credential_name, created_at);

-- +goose Down
DROP TABLE approval_audit;
DROP TABLE permit_grant;
DROP TABLE approval_request;
