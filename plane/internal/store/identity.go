package store

// Identity on the call: the agent_run table and the one read that turns
// it into an answer. The retired inbound bridge was the only run writer.
// Existing run windows are still resolved during an upgrade, and ledger
// and tool-audit rows keep their historical attribution and back-references.
//
// That window is the only correlation the plane can SUBSTANTIATE. The
// agent pod authenticates to the model proxy with its
// credential and nothing else; a header the agent set would be a claim
// by the thing being governed, and forking kagent to add a trusted one
// is exactly what the prime directive exists to stop. So the plane
// vouches for what the plane itself observed at its own door, and says
// plainly when it cannot.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The closed vocabulary of an attribution. Everything that is not a
// 'slack:<user id>' is one of these three, and each says a different
// thing on purpose.
const (
	// ActedForNone: the plane can say there is no person. Either no run
	// was open for this credential (an operator-driven turn), or the run
	// that was open came from a source that names nobody (a signed
	// webhook). A complete answer, not a gap.
	ActedForNone = "none"
	// ActedForUnknown: the plane CANNOT say. More than one run was open
	// for this credential at once, or the attribution read failed.
	// Attribution was lost — never a claim that nobody was there.
	ActedForUnknown = "unknown"
	// ActedForLegacy: written before attribution existed. Backfill only;
	// migration 00009 closed the class.
	ActedForLegacy = "legacy"
)

// Attribution is what a seam stamps on the rows it writes. RunID is
// provenance: the run this call fell inside, carried whenever ONE run
// resolved — including a run that names nobody, because a run with no
// person is still a run. It is empty when no run resolved at all: no run
// was open ('none'), or attribution was lost ('unknown'). ActedFor is
// always the answer on its own, so nothing has to read an absent id to
// learn who acted.
type Attribution struct {
	ActedFor string
	RunID    string
}

// Nobody is what a seam stamps when there was no person: no run open, so
// nothing and nobody to carry.
//
// It is named for what the value ASSERTS. The name it replaced —
// "Unattributed" — read as "nobody attributed this", which is the state
// the plane cannot express at all, while the value beneath it claims
// "there is no person". A name that argues against the distinction its
// own constants draw is part of how an overclaim stays invisible.
var Nobody = Attribution{ActedFor: ActedForNone}

// Lost is what a seam stamps when it cannot resolve one.
var Lost = Attribution{ActedFor: ActedForUnknown}

// Run is one open agent turn.
type Run struct {
	ID             string     `json:"id"`
	CredentialName string     `json:"credential"`
	ActedFor       string     `json:"acted_for"`
	Source         string     `json:"source"`
	DeliveryID     string     `json:"delivery_id"`
	EventID        *string    `json:"event_id,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
}

// ActorFor resolves who a call for this credential is being made for,
// right now. Three outcomes, and the caller never has to guess which:
//
//	no open run   → 'none'    (nobody triggered this through the plane)
//	one open run  → its actor ('slack:<id>', or 'none' for a source that
//	                names nobody), plus the run id as provenance
//	two or more   → 'unknown' (the plane will not guess which person a
//	                call belongs to when two runs overlap)
//
// A store error is 'unknown' too, and is the caller's to log: it is a
// lost attribution, not an assertion that nobody was there. Attribution
// is not enforcement, so a failure here never admits or denies anything
// — the seams' own fail-closed rules are untouched.
func (s *Store) ActorFor(ctx context.Context, credential string) (Attribution, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, acted_for FROM agent_run
		 WHERE credential_name = $1 AND ended_at IS NULL AND expires_at > now()
		 ORDER BY started_at LIMIT 2`, credential)
	if err != nil {
		return Lost, err
	}
	defer rows.Close()
	var found []Attribution
	for rows.Next() {
		var a Attribution
		if err := rows.Scan(&a.RunID, &a.ActedFor); err != nil {
			return Lost, err
		}
		found = append(found, a)
	}
	if err := rows.Err(); err != nil {
		return Lost, err
	}
	switch len(found) {
	case 0:
		return Nobody, nil
	case 1:
		return found[0], nil
	default:
		return Lost, nil
	}
}

// RunByID reads one run — a ledger or audit row's back-reference to the
// delivery that caused it.
//
// Kept although no product path calls it yet. Its caller is the Postgres
// test that proves the run row round-trips: a ledger entry carries a run id,
// and that id resolves to the source and delivery it came from. That is an
// assertion about persisted data, and the alternative to keeping this
// accessor is a second copy of the SELECT inside the test, where nothing
// would hold it to the schema the product writes.
func (s *Store) RunByID(ctx context.Context, id string) (Run, error) {
	var r Run
	err := s.pool.QueryRow(ctx,
		`SELECT id, credential_name, acted_for, source, delivery_id, event_id, started_at, ended_at
		 FROM agent_run WHERE id = $1`, id).
		Scan(&r.ID, &r.CredentialName, &r.ActedFor, &r.Source, &r.DeliveryID, &r.EventID, &r.StartedAt, &r.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	return r, err
}

// actedFor normalises what a seam handed us. Empty is never written as
// a claim: a writer that forgot to resolve an actor gets 'unknown' —
// "we cannot say" — which is what the column default says too.
func actedFor(v string) string {
	if v == "" {
		return ActedForUnknown
	}
	return v
}

// nullableUUID turns an empty run id into a SQL NULL: no run, rather
// than a run that does not exist.
func nullableUUID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}
