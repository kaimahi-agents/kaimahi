-- +goose Up
-- Credentials that expire (the second gap the agentdesktop positioning
-- pass found). A governed token was bounded by allowlist, budget and
-- standing constraint — but never by TIME. It lived until someone
-- deleted its row, which is the one thing nobody does.
--
-- NULLABLE, and the NULL class is CLOSED by this migration in the same
-- way migration 00008 closed the NULL-digest class: NULL means a
-- LEGACY credential, issued before expiry existed, and it still works
-- — the conservative reading, because silently expiring every token in
-- a running cluster at migration time is an outage, not a control. No
-- new credential can be minted without one: the admin surface applies
-- a default TTL when the caller names none and refuses an explicit
-- "never", so the NULL class only ever shrinks.
--
-- Expiry is enforced at every seam that AUTHENTICATES a credential
-- (the LLM proxy, the MCP gateway, the inbound door), fails closed,
-- and is audited exactly like every other refusal — never filtered out
-- of the lookup, which would answer "unknown token" and send an
-- operator hunting the wrong problem.
ALTER TABLE credential ADD COLUMN expires_at timestamptz;

-- Renewal extends the deadline on the SAME token: the material never
-- moves, so nothing has to travel and no Secret has to be rewritten.
-- Rotating the material is still what it always was — issue a fresh
-- credential and re-point the Secret.
CREATE INDEX credential_expires ON credential (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP INDEX credential_expires;
ALTER TABLE credential DROP COLUMN expires_at;
