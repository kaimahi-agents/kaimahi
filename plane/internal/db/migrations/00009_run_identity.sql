-- +goose Up
-- Identity on the call: WHO an agent acted for (the first gap the
-- agentdesktop positioning pass found). Until now the only
-- human anywhere in the plane's data was `decided_by` on an approval —
-- the APPROVER, never the requester — so the ledger could say
-- "ap-agent spent $4.10" and never "on whose behalf". Additive
-- throughout, and every new column carries an EXPLICIT value: an empty
-- string that could mean "nobody" or "we lost it" would be worse than
-- no column at all.
--
-- The vocabulary reuses the `decided_by` shape (migration 00006) —
-- free text prefixed by the path that vouched for it — rather than
-- inventing a second one:
--
--   'slack:<user id>'  a person, vouched for by the Slack signature the
--                      inbound bridge verified (the same claim the
--                      approver list is checked against). The Slack user
--                      id and NOTHING else: no name, no email, no
--                      profile — these tables are in every pg_dump.
--   'none'             the plane can say there is no person: no run was
--                      open for this credential when the call was
--                      authenticated (an operator-driven turn), or the
--                      run that was open came from a source that names
--                      nobody (a signed webhook). A complete answer,
--                      not a gap.
--   'unknown'          the plane CANNOT say: more than one run was open
--                      for this credential at once, or the attribution
--                      read failed. Attribution was lost; it is not a
--                      claim that nobody was there.
--   'legacy'           the row predates attribution. Backfill only.

-- A run is one agent turn the plane triggered and held open: the
-- inbound bridge opens it before the A2A call and closes it when the
-- call returns, so every governed call the agent makes in between is
-- inside the window. That window is the ONLY correlation available —
-- kagent's agent pod authenticates to the proxy and the gateway with
-- its credential and nothing else, and the prime directive says we do
-- not fork kagent to add a header it would have to be trusted not to
-- forge. What the plane vouches for is therefore what the plane itself
-- observed at the door.
--
-- expires_at bounds a run a crashed replica never closed, the same
-- discipline a spend reservation has: past it the run stops counting,
-- so one lost close cannot poison every later call for that credential.
CREATE TABLE agent_run (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The GOVERNED credential the run spends under — the agent's, not
    -- the hook's. Plain text, no FK, like ledger_entry: the trail must
    -- outlive a deleted credential.
    credential_name text NOT NULL,
    -- A run always knows which of the two it is; 'unknown' and 'legacy'
    -- are resolutions, never a run's own claim.
    acted_for       text NOT NULL CHECK (acted_for = 'none' OR acted_for ~ '^slack:[A-Z0-9]{1,64}$'),
    -- How the run started, e.g. 'inbound:slack-events'.
    source          text NOT NULL,
    delivery_id     text NOT NULL DEFAULT '',
    -- The inbound_audit admission row this run answers, when there is
    -- one; NULL for a run with no inbound event behind it.
    event_id        uuid,
    started_at      timestamptz NOT NULL DEFAULT now(),
    ended_at        timestamptz,
    expires_at      timestamptz NOT NULL
);

CREATE INDEX agent_run_open ON agent_run (credential_name, expires_at) WHERE ended_at IS NULL;
CREATE INDEX agent_run_credential_started ON agent_run (credential_name, started_at);

-- The two enforcement trails gain the resolution, and a link to the run
-- it came from. run_id is NULLABLE and means exactly one thing: no run
-- was resolved for this row. It is NOT implied by acted_for: a run whose
-- source names nobody is attributed 'none' and still carries its run id,
-- because a run with no person is still a run. run_id is provenance,
-- never the answer, so nothing has to read it to learn who acted —
-- acted_for states that in words ('none', 'unknown' or 'legacy').
ALTER TABLE ledger_entry ADD COLUMN acted_for text NOT NULL DEFAULT 'legacy';
ALTER TABLE ledger_entry ADD COLUMN run_id uuid;
ALTER TABLE tool_audit ADD COLUMN acted_for text NOT NULL DEFAULT 'legacy';
ALTER TABLE tool_audit ADD COLUMN run_id uuid;

-- The inbound trail records the identity at the door, where it is first
-- known — including on rows that never became a run (a denial, an
-- ignored event, a Slack approval command).
ALTER TABLE inbound_audit ADD COLUMN acted_for text NOT NULL DEFAULT 'legacy';

-- ADD COLUMN ... DEFAULT 'legacy' backfilled every existing row as what
-- it is: written before attribution existed. Changing the default now
-- CLOSES that class, exactly as migration 00008 closed the NULL-digest
-- class: no row written from here on can be 'legacy', and a writer that
-- forgets to say who acted gets 'unknown' — "we cannot say" — never a
-- false claim that nobody was there.
ALTER TABLE ledger_entry  ALTER COLUMN acted_for SET DEFAULT 'unknown';
ALTER TABLE tool_audit    ALTER COLUMN acted_for SET DEFAULT 'unknown';
ALTER TABLE inbound_audit ALTER COLUMN acted_for SET DEFAULT 'unknown';

ALTER TABLE ledger_entry ADD CONSTRAINT ledger_entry_acted_for_check
    CHECK (acted_for IN ('none', 'unknown', 'legacy') OR acted_for ~ '^slack:[A-Z0-9]{1,64}$');
ALTER TABLE tool_audit ADD CONSTRAINT tool_audit_acted_for_check
    CHECK (acted_for IN ('none', 'unknown', 'legacy') OR acted_for ~ '^slack:[A-Z0-9]{1,64}$');
ALTER TABLE inbound_audit ADD CONSTRAINT inbound_audit_acted_for_check
    CHECK (acted_for IN ('none', 'unknown', 'legacy') OR acted_for ~ '^slack:[A-Z0-9]{1,64}$');

-- +goose Down
ALTER TABLE inbound_audit DROP CONSTRAINT inbound_audit_acted_for_check;
ALTER TABLE tool_audit DROP CONSTRAINT tool_audit_acted_for_check;
ALTER TABLE ledger_entry DROP CONSTRAINT ledger_entry_acted_for_check;
ALTER TABLE inbound_audit DROP COLUMN acted_for;
ALTER TABLE tool_audit DROP COLUMN run_id;
ALTER TABLE tool_audit DROP COLUMN acted_for;
ALTER TABLE ledger_entry DROP COLUMN run_id;
ALTER TABLE ledger_entry DROP COLUMN acted_for;
DROP TABLE agent_run;
