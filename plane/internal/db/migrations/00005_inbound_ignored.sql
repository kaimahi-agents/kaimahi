-- +goose Up
-- The Slack Events loop. A subscribed event that is well-formed but
-- deliberately NOT a trigger (anything but a human's app_mention — the
-- bot's own reply landing in the channel, first of all) is acknowledged
-- to Slack with a 2xx so it is not retried, and recorded here as
-- 'ignored' so the trail shows it arrived and what the plane declined to
-- do with it. It is neither a denial (nothing was refused) nor an
-- admission (nothing ran, no grant use burned). Widening the CHECK is
-- the whole schema cost.
ALTER TABLE inbound_audit DROP CONSTRAINT inbound_audit_decision_check;
ALTER TABLE inbound_audit ADD CONSTRAINT inbound_audit_decision_check
    CHECK (decision IN ('denied', 'admitted', 'completed', 'failed', 'challenge', 'ignored'));

-- +goose Down
DELETE FROM inbound_audit WHERE decision = 'ignored';
ALTER TABLE inbound_audit DROP CONSTRAINT inbound_audit_decision_check;
ALTER TABLE inbound_audit ADD CONSTRAINT inbound_audit_decision_check
    CHECK (decision IN ('denied', 'admitted', 'completed', 'failed', 'challenge'));
