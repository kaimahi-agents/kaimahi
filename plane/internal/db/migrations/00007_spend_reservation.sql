-- +goose Up
-- Exact budgets under concurrency. Until now the meter READ
-- month-to-date spend from the ledger, decided, forwarded, and only then
-- wrote the row —
-- so N concurrent calls (across N replicas, or one) could each see
-- headroom for one call and all be admitted. This table closes the gap:
-- an admitted call leaves a reservation BEFORE it is forwarded, written
-- in the same transaction that took the decision under a lock on the
-- credential row, and the ledger write that records the call's real
-- usage deletes it. Committed spend = ledger rows + open reservations,
-- and the lock makes check-and-reserve serial per credential, so
-- concurrent admission is exactly what serial admission would be.
--
-- A hold is the LEAST an admitted call can spend (one token; one cent
-- when the model is priced), never an estimate: it bounds ADMISSIONS
-- (no more calls are admitted concurrently than the cap has room for),
-- not overshoot — every call admitted with headroom for its hold may
-- still finish above the cap by its own usage (the LLM proxy's soft-stop).
-- expires_at bounds a reservation a crashed replica never
-- consumed: it stops counting when it expires (longer than any call the
-- proxy allows), and the next admission for that credential sweeps it.
CREATE TABLE spend_reservation (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    credential_name text NOT NULL REFERENCES credential (name) ON DELETE CASCADE,
    hold_cents      bigint NOT NULL DEFAULT 0 CHECK (hold_cents >= 0),
    hold_tokens     bigint NOT NULL DEFAULT 0 CHECK (hold_tokens >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL
);

CREATE INDEX spend_reservation_credential ON spend_reservation (credential_name, created_at);

-- +goose Down
DROP TABLE spend_reservation;
