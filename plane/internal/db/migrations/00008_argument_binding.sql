-- +goose Up
-- Argument binding: an approval binds to the exact CALL,
-- not to the verb. Additive throughout — every column defaults, so no
-- writer has to change and `make backup` / `make restore` round-trip
-- across the boundary.
--
-- arg_digest is the sha256 (hex) of a call's canonical policy fields
-- (plane/internal/gateway/digest.go); arg_summary is the human-readable
-- transaction line built from the tool's DECLARED policy fields only —
-- never arbitrary arguments, because these tables are in every pg_dump.

-- What was denied, in the approver's words and in the digest they are
-- deciding about.
ALTER TABLE approval_request ADD COLUMN arg_digest text NOT NULL DEFAULT '';
ALTER TABLE approval_request ADD COLUMN arg_summary text NOT NULL DEFAULT '';

-- The dedup key gains the digest: two attempts to pay DIFFERENT amounts
-- are two requests, which is the defect argument binding fixes. Genuine
-- repeats of the same call still collapse into one. arg_digest is NOT
-- NULL, so (unlike a nullable column) every row participates in the
-- uniqueness rather than silently escaping it.
DROP INDEX approval_request_pending_uniq;
CREATE UNIQUE INDEX approval_request_pending_uniq
    ON approval_request (credential_name, kind, subject, arg_digest)
    WHERE status = 'pending';

-- The grant is welded to the digest. NULLABLE on purpose, and the NULL
-- class is CLOSED by this migration: rows that predate it are the only
-- ones a NULL can describe, because the store refuses from here on to
-- mint a tool grant for a request that carries no digest. A NULL digest
-- is therefore honoured verb-level (those grants were already bounded by
-- expiry and use count when a human approved them, and no new one can be
-- created); every grant minted after this point admits one call only.
ALTER TABLE permit_grant ADD COLUMN arg_digest text;

-- The approvals' own trail records what was approved, not just which
-- verb.
ALTER TABLE approval_audit ADD COLUMN arg_digest text NOT NULL DEFAULT '';
ALTER TABLE approval_audit ADD COLUMN arg_summary text NOT NULL DEFAULT '';

-- The enforcement trail records the digest and summary of the call on
-- BOTH the denial and the admitted call, so the approved call and the
-- call that ran are provably the same one.
ALTER TABLE tool_audit ADD COLUMN arg_digest text NOT NULL DEFAULT '';
ALTER TABLE tool_audit ADD COLUMN arg_summary text NOT NULL DEFAULT '';

-- +goose Down
-- The Up direction allows several pending requests that differ only by
-- arg_digest — the point of the lane. Recreating the verb-level index
-- over them fails on a unique violation, which reads as a broken
-- migration rather than as what it is, so say it plainly first.
-- +goose StatementBegin
DO $$
DECLARE n integer;
BEGIN
    SELECT count(*) INTO n FROM (
        SELECT 1 FROM approval_request WHERE status = 'pending'
        GROUP BY credential_name, kind, subject HAVING count(*) > 1
    ) AS collisions;
    IF n > 0 THEN
        RAISE EXCEPTION 'cannot roll back argument binding: % (credential, kind, subject) group(s) hold pending requests that differ only by the call they name; approve or deny them first', n;
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE tool_audit DROP COLUMN arg_summary;
ALTER TABLE tool_audit DROP COLUMN arg_digest;
ALTER TABLE approval_audit DROP COLUMN arg_summary;
ALTER TABLE approval_audit DROP COLUMN arg_digest;
ALTER TABLE permit_grant DROP COLUMN arg_digest;
DROP INDEX approval_request_pending_uniq;
CREATE UNIQUE INDEX approval_request_pending_uniq
    ON approval_request (credential_name, kind, subject)
    WHERE status = 'pending';
ALTER TABLE approval_request DROP COLUMN arg_summary;
ALTER TABLE approval_request DROP COLUMN arg_digest;
