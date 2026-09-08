-- +goose Up
-- Who called: the two enforcement trails learn to tell a client the
-- plane deployed from one it did not.
--
-- The gap this closes. Every governed row named the CREDENTIAL and, since
-- migration 00009, who the call was acted for — and nothing else. Nothing
-- in a row distinguished an agent pod from a shell script holding the same
-- token: not the client name in the MCP handshake, which the gateway
-- relays without reading, not the user agent, not the source address. A
-- reader meeting `acted_for = 'none'` on a call the plane never triggered
-- had no way to see that the word was being stretched.
--
-- What is recorded is deliberately TWO facts, kept apart, because they are
-- worth different amounts:
--
--   caller_claim  what the caller SAYS it is. Self-reported, unverified,
--                 and attacker-controlled: any client can send any string.
--                 The 'ua:' prefix is part of the value so the column can
--                 never be misread as something the plane checked.
--   caller_addr   what the plane OBSERVED at its own socket: the peer
--                 address of the connection the request arrived on. Not a
--                 claim by the thing being governed, which is the same
--                 principle acted_for already runs on.
--
-- Neither is an input to any decision. Nothing here admits, denies or
-- grants anything; the seams' fail-closed rules are untouched.
--
--   'ua:<text>'    the caller's own name, bounded and reduced to one
--                  printable line at the write (store/audittext.go)
--   'none'         the caller offered no identification at all
--   'unrecorded'   the writer resolved none. The default from here on: a
--                  writer that forgets says "no record", never a claim
--   'legacy'       the row was written before the caller was recorded.
--                  Backfill only; this migration closes the class
--
-- caller_addr uses the same three non-value words, with 'unknown' in
-- place of 'none' — an address is never OFFERED, so the only two ways to
-- lack one are not having recorded it and not having been able to read
-- it. None of the four can collide with a real value: an address is an
-- address, and every caller-supplied name carries the 'ua:' prefix.
--
-- Existing rows get 'legacy' and are otherwise untouched. They cannot
-- know their caller and must not be made to look as though they were
-- recorded as empty — the same distinction acted_for draws, applied to a
-- new column.
ALTER TABLE tool_audit    ADD COLUMN caller_claim text NOT NULL DEFAULT 'legacy';
ALTER TABLE tool_audit    ADD COLUMN caller_addr  text NOT NULL DEFAULT 'legacy';
ALTER TABLE ledger_entry  ADD COLUMN caller_claim text NOT NULL DEFAULT 'legacy';
ALTER TABLE ledger_entry  ADD COLUMN caller_addr  text NOT NULL DEFAULT 'legacy';

ALTER TABLE tool_audit   ALTER COLUMN caller_claim SET DEFAULT 'unrecorded';
ALTER TABLE tool_audit   ALTER COLUMN caller_addr  SET DEFAULT 'unrecorded';
ALTER TABLE ledger_entry ALTER COLUMN caller_claim SET DEFAULT 'unrecorded';
ALTER TABLE ledger_entry ALTER COLUMN caller_addr  SET DEFAULT 'unrecorded';

-- The length bounds are enforced here as well as in code. These tables
-- are in every pg_dump (`make backup`), the value is attacker-controlled,
-- and a bound that lives only in the writer is a bound one future writer
-- forgets. 160 leaves room for a real user agent; an address is at most
-- an IPv6 literal with a zone.
ALTER TABLE tool_audit ADD CONSTRAINT tool_audit_caller_claim_check
    CHECK (caller_claim IN ('none', 'unrecorded', 'legacy') OR
           (caller_claim LIKE 'ua:%' AND length(caller_claim) <= 160));
ALTER TABLE tool_audit ADD CONSTRAINT tool_audit_caller_addr_check
    CHECK (caller_addr IN ('unknown', 'unrecorded', 'legacy') OR length(caller_addr) <= 64);
ALTER TABLE ledger_entry ADD CONSTRAINT ledger_entry_caller_claim_check
    CHECK (caller_claim IN ('none', 'unrecorded', 'legacy') OR
           (caller_claim LIKE 'ua:%' AND length(caller_claim) <= 160));
ALTER TABLE ledger_entry ADD CONSTRAINT ledger_entry_caller_addr_check
    CHECK (caller_addr IN ('unknown', 'unrecorded', 'legacy') OR length(caller_addr) <= 64);

-- +goose Down
ALTER TABLE ledger_entry DROP CONSTRAINT ledger_entry_caller_addr_check;
ALTER TABLE ledger_entry DROP CONSTRAINT ledger_entry_caller_claim_check;
ALTER TABLE tool_audit DROP CONSTRAINT tool_audit_caller_addr_check;
ALTER TABLE tool_audit DROP CONSTRAINT tool_audit_caller_claim_check;
ALTER TABLE ledger_entry DROP COLUMN caller_addr;
ALTER TABLE ledger_entry DROP COLUMN caller_claim;
ALTER TABLE tool_audit DROP COLUMN caller_addr;
ALTER TABLE tool_audit DROP COLUMN caller_claim;
