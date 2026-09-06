-- +goose Up
-- Approval routing via Slack, and WHO decided. Until now "who
-- approved" was implied — the admin bearer was the only writer the admin
-- port admits — and recorded nowhere. A decision typed in Slack is made
-- by a person the plane can name (the mentioning user's id, verified by
-- Slack's signature and checked against the hook's approver list), so
-- the decision rows now carry it.
--
-- decided_by is free text, prefixed by the path that vouched for it:
--   'admin'          the admin bearer (make approve / make deny)
--   'slack:<userid>' a Slack user, via an app_mention command
-- Backward-compatible: the columns default so no writer has to change,
-- and every decision already on record was made by the admin bearer, so
-- it is backfilled as such rather than left blank as if unknown. A
-- pending request and a 'requested' audit row have no decider ('').
ALTER TABLE approval_request ADD COLUMN decided_by text NOT NULL DEFAULT '';
UPDATE approval_request SET decided_by = 'admin' WHERE status IN ('approved', 'denied');

ALTER TABLE permit_grant ADD COLUMN decided_by text NOT NULL DEFAULT 'admin';

ALTER TABLE approval_audit ADD COLUMN decided_by text NOT NULL DEFAULT '';
UPDATE approval_audit SET decided_by = 'admin' WHERE action IN ('approved', 'denied');

-- A Slack command (approve/deny) is a new kind of inbound decision: not
-- a denial, not an admission (no agent runs, no grant use is burned), and
-- not merely ignored (the plane acted on it). Recorded as 'command' with
-- the outcome in detail, so the inbound trail shows every decision taken
-- from Slack next to the mentions that triggered agents.
ALTER TABLE inbound_audit DROP CONSTRAINT inbound_audit_decision_check;
ALTER TABLE inbound_audit ADD CONSTRAINT inbound_audit_decision_check
    CHECK (decision IN ('denied', 'admitted', 'completed', 'failed', 'challenge', 'ignored', 'command'));

-- +goose Down
DELETE FROM inbound_audit WHERE decision = 'command';
ALTER TABLE inbound_audit DROP CONSTRAINT inbound_audit_decision_check;
ALTER TABLE inbound_audit ADD CONSTRAINT inbound_audit_decision_check
    CHECK (decision IN ('denied', 'admitted', 'completed', 'failed', 'challenge', 'ignored'));
ALTER TABLE approval_audit DROP COLUMN decided_by;
ALTER TABLE permit_grant DROP COLUMN decided_by;
ALTER TABLE approval_request DROP COLUMN decided_by;
