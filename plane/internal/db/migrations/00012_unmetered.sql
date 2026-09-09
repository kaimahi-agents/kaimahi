-- +goose Up
-- One more cost_source: 'unmetered'.
--
-- The other four say why a cost is what it is — free by classification,
-- priced from a configured row, unpriced on a metered upstream, or
-- denied before the call was made. This one says something different and
-- worse: the call HAPPENED, and the token counts on this row are not its
-- counts. The plane could not read them.
--
-- It exists because the alternative was a row that looked ordinary. A
-- Responses-API upstream read by a chat-completions reader reported
-- thirteen input and sixteen output tokens and was ledgered `0 in /
-- 0 out, free` — indistinguishable, in the ledger and in every sum built
-- from it, from a call that genuinely cost nothing. A budget over that
-- upstream could never be exhausted.
--
-- The proxy writes this in two cases and refuses the call in the first:
-- a buffered success whose usage envelope it could not read (the answer
-- is never handed over; the row carries status 502), and a STREAMED
-- success whose bytes had already left (the row carries the upstream's
-- own status). Never priced — pricing a count that was never read would
-- invent the cost.
--
-- Widening a CHECK constraint accepts every row that was legal before,
-- so existing data needs no rewrite and this migration cannot fail on
-- it.
ALTER TABLE ledger_entry DROP CONSTRAINT ledger_entry_cost_source_check;
ALTER TABLE ledger_entry ADD CONSTRAINT ledger_entry_cost_source_check
    CHECK (cost_source IN ('free', 'priced', 'unpriced', 'denied', 'unmetered'));

-- +goose Down
-- Narrowing again would reject rows this version legitimately wrote, so
-- the down migration removes those rows' claim rather than the rows:
-- 'unpriced' is the nearest true statement about a row whose tokens were
-- never read on an upstream whose cost could not be computed.
UPDATE ledger_entry SET cost_source = 'unpriced' WHERE cost_source = 'unmetered';
ALTER TABLE ledger_entry DROP CONSTRAINT ledger_entry_cost_source_check;
ALTER TABLE ledger_entry ADD CONSTRAINT ledger_entry_cost_source_check
    CHECK (cost_source IN ('free', 'priced', 'unpriced', 'denied'));
