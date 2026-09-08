// Package store is the plane's Postgres layer: credentials (hashes only),
// budget caps, and the append-only spend ledger. Rewritten for the kagent
// architecture — tomte-old's store carried the replaced control plane
// (tenants, runs, workflows); only its spend-ledger pattern survives here.
package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrExists   = errors.New("store: already exists")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Credential is a Kaimahi-issued identity: the opaque token the governed
// preset carries, known here only by its sha256. Caps are monthly (UTC);
// nil means no cap of that kind.
type Credential struct {
	Name      string
	CapCents  *int64
	CapTokens *int64
	// ExpiresAt is when the credential stops authenticating anywhere.
	// NULL (nil) is a LEGACY credential — issued before expiry existed,
	// and still valid: a closed class, because no new credential can be
	// minted without one (migration 00010).
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// Expired reports whether the credential is past its deadline at now. A
// legacy credential (no expiry) never is.
func (c Credential) Expired(now time.Time) bool {
	return c.ExpiresAt != nil && !now.Before(*c.ExpiresAt)
}

// ExpiredPrefix begins every refusal an expired credential earns. The
// seams' metric classifiers key on it, so it is a constant, not a
// spelling each seam repeats.
const ExpiredPrefix = "expired credential "

// ExpiredMessage is what an operator reads when a credential's time is
// up: what is wrong, WHEN it went wrong, and the command that fixes it.
// A credential that expires silently at 3am is an outage nobody
// diagnoses, so the refusal has to do the diagnosis.
func ExpiredMessage(c Credential) string {
	when := "an unknown time"
	if c.ExpiresAt != nil {
		when = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return ExpiredPrefix + strconv.Quote(c.Name) + ": it expired at " + when +
		"; renew it with 'make credential-renew NAME=" + c.Name + " TTL=720h', " +
		"or re-issue the credential and re-point its Secret"
}

// ExpiringSoon reports whether a credential is inside its warning
// window — what the operator sees BEFORE the outage, in the credentials
// view, in the logs and in the expiry gauge.
func (c Credential) ExpiringSoon(now time.Time, window time.Duration) bool {
	return c.ExpiresAt != nil && !c.Expired(now) && c.ExpiresAt.Sub(now) <= window
}

// ExpiryWarning is the window an operator is warned in. Long enough that
// a weekly rhythm catches it.
const ExpiryWarning = 7 * 24 * time.Hour

// LedgerEntry is one append-only spend row. CostSource records why the
// cost is what it is ('free', 'priced', 'unpriced', 'denied') — a zero
// cost always carries its explanation (no blanket $0).
type LedgerEntry struct {
	CredentialName string `json:"credential"`
	Upstream       string `json:"upstream"`
	Model          string `json:"model"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	CostCents      int64  `json:"cost_cents"`
	CostSource     string `json:"cost_source"`
	Status         int    `json:"status"`
	// ActedFor names WHO the call was made for, from the closed
	// vocabulary in identity.go ('slack:<user id>', 'none', 'unknown',
	// 'legacy'). Never empty: an empty column that could mean "nobody"
	// or "we lost it" is worse than no column. RunID is the run the call
	// fell inside, empty when there was none — provenance, never the
	// answer.
	ActedFor string `json:"acted_for"`
	RunID    string `json:"run_id,omitempty"`
	// CallerClaim/CallerAddr say WHO CALLED: the client's own word for
	// itself and the address the plane observed (caller.go). Legibility,
	// never enforcement — a reader meeting 'none' can see whether the
	// call came through a door the plane opened.
	CallerClaim string    `json:"caller_claim"`
	CallerAddr  string    `json:"caller_addr"`
	CreatedAt   time.Time `json:"created_at"`
}

// CreateCredential mints a credential row. expiresAt is REQUIRED: the
// admin surface applies a default TTL when the caller names none, so
// the legacy no-expiry class only ever shrinks (migration 00010).
func (s *Store) CreateCredential(ctx context.Context, name string, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO credential (name, token_hash, expires_at) VALUES ($1, $2, $3)`, name, tokenHash, expiresAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return ErrExists
	}
	return err
}

// CredentialByTokenHash resolves an inbound bearer to its credential.
// ErrNotFound means an unknown token — the caller answers 401.
func (s *Store) CredentialByTokenHash(ctx context.Context, tokenHash []byte) (Credential, error) {
	var c Credential
	err := s.pool.QueryRow(ctx,
		`SELECT name, cap_cents, cap_tokens, expires_at, created_at FROM credential WHERE token_hash = $1`,
		tokenHash).Scan(&c.Name, &c.CapCents, &c.CapTokens, &c.ExpiresAt, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	return c, err
}

// SetBudget replaces both caps for the named credential (nil clears a cap).
func (s *Store) SetBudget(ctx context.Context, name string, capCents, capTokens *int64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE credential SET cap_cents = $2, cap_tokens = $3 WHERE name = $1`,
		name, capCents, capTokens)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordLedger appends one spend row and, in the same transaction,
// consumes the reservation the call was admitted under (the row
// replaces the hold; empty when the call held nothing — a denial, or a
// credential with no caps). The ledger stays append-only: the one
// delete here is of a reservation, never of a row. A reservation that
// already expired and was swept is simply gone; the row still lands.
func (s *Store) RecordLedger(ctx context.Context, e LedgerEntry, reservationID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`INSERT INTO ledger_entry (credential_name, upstream, model, input_tokens, output_tokens, cost_cents, cost_source, status, acted_for, run_id, caller_claim, caller_addr)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		e.CredentialName, auditText(e.Upstream), auditText(e.Model), e.InputTokens, e.OutputTokens, e.CostCents, e.CostSource, e.Status,
		actedFor(e.ActedFor), nullableUUID(e.RunID),
		callerClaimFor(e.CallerClaim), callerAddrFor(e.CallerAddr)); err != nil {
		return err
	}
	if reservationID != "" {
		if _, err := tx.Exec(ctx,
			`DELETE FROM spend_reservation WHERE id = $1`, reservationID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ToolAuditEntry is one append-only tool-governance row: what the MCP
// gateway decided about one inbound method. Decision says whose status
// the row carries: 'allowed' rows record the upstream's HTTP status,
// 'denied' rows the gateway's own (like the spend ledger's denied rows).
type ToolAuditEntry struct {
	CredentialName string `json:"credential"`
	Upstream       string `json:"upstream"`
	Method         string `json:"method"`
	Tool           string `json:"tool"`
	Decision       string `json:"decision"`
	Status         int    `json:"status"`
	Detail         string `json:"detail"`
	// ArgDigest/ArgSummary identify the CALL: the digest an
	// approval is welded to, and the transaction line built from the
	// tool's declared policy fields. Present on tools/call rows —
	// denied and allowed alike, so the approved call and the call that
	// ran are provably the same one — and empty on other methods.
	// Arbitrary arguments are never recorded: this table is in every
	// pg_dump (`make backup`).
	ArgDigest  string `json:"arg_digest,omitempty"`
	ArgSummary string `json:"arg_summary,omitempty"`
	// ActedFor/RunID: who the tool call was made for, and the run it
	// fell inside. Same closed vocabulary as the ledger (identity.go).
	ActedFor string `json:"acted_for"`
	RunID    string `json:"run_id,omitempty"`
	// CallerClaim/CallerAddr say WHO CALLED: the client's own word for
	// itself and the address the plane observed (caller.go). Legibility,
	// never enforcement — a reader meeting 'none' can see whether the
	// call came through a door the plane opened.
	CallerClaim string    `json:"caller_claim"`
	CallerAddr  string    `json:"caller_addr"`
	CreatedAt   time.Time `json:"created_at"`
}

// SetToolAllowlist replaces the credential's whole allowlist (empty
// clears it — nothing callable). Transactional so a reader never sees a
// half-replaced set.
func (s *Store) SetToolAllowlist(ctx context.Context, credentialName string, tools []string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM credential WHERE name = $1)`, credentialName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM tool_allowlist WHERE credential_name = $1`, credentialName); err != nil {
		return err
	}
	for _, tool := range tools {
		if _, err := tx.Exec(ctx,
			`INSERT INTO tool_allowlist (credential_name, tool) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			credentialName, tool); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ToolAllowlist returns the credential's allowed tools, sorted. An empty
// result is meaningful: nothing callable.
func (s *Store) ToolAllowlist(ctx context.Context, credentialName string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tool FROM tool_allowlist WHERE credential_name = $1 ORDER BY tool`, credentialName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CredentialsAllowlisting returns, for each of the given tool names, the
// credentials that already allowlist it — ordered, so a message built
// from it is stable.
//
// The allowlist is per-CREDENTIAL, not per-(credential, upstream)
// — a documented property of the gateway, and the reason onboarding a
// new upstream that offers an already-allowlisted tool NAME makes that
// tool callable on the new server by every credential that already had
// it. That is not a bug to fix here (scoping the allowlist by upstream
// is a design decision, not a change to make in passing), but it is a
// fact an operator must
// be told at the moment they onboard, because otherwise nothing says it.
func (s *Store) CredentialsAllowlisting(ctx context.Context, tools []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(tools) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT tool, credential_name FROM tool_allowlist
		  WHERE tool = ANY($1) ORDER BY tool, credential_name`, tools)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tool, cred string
		if err := rows.Scan(&tool, &cred); err != nil {
			return nil, err
		}
		out[tool] = append(out[tool], cred)
	}
	return out, rows.Err()
}

// RecordToolAudit appends one tool-audit row. Append-only by
// construction, like the spend ledger.
func (s *Store) RecordToolAudit(ctx context.Context, e ToolAuditEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tool_audit (credential_name, upstream, method, tool, decision, status, detail, arg_digest, arg_summary, acted_for, run_id, caller_claim, caller_addr)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		e.CredentialName, auditText(e.Upstream), auditText(e.Method), auditText(e.Tool), e.Decision, e.Status, auditText(e.Detail),
		e.ArgDigest, auditText(e.ArgSummary),
		actedFor(e.ActedFor), nullableUUID(e.RunID),
		callerClaimFor(e.CallerClaim), callerAddrFor(e.CallerAddr))
	return err
}

// ToolAudit returns the newest audit rows for one credential (all
// credentials when name is empty), newest first.
func (s *Store) ToolAudit(ctx context.Context, credentialName string, limit int) ([]ToolAuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT credential_name, upstream, method, tool, decision, status, detail, arg_digest, arg_summary,
		        acted_for, COALESCE(run_id::text, ''), caller_claim, caller_addr, created_at
		 FROM tool_audit
		 WHERE ($1 = '' OR credential_name = $1)
		 ORDER BY created_at DESC LIMIT $2`,
		credentialName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolAuditEntry
	for rows.Next() {
		var e ToolAuditEntry
		if err := rows.Scan(&e.CredentialName, &e.Upstream, &e.Method, &e.Tool,
			&e.Decision, &e.Status, &e.Detail, &e.ArgDigest, &e.ArgSummary,
			&e.ActedFor, &e.RunID, &e.CallerClaim, &e.CallerAddr, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MonthUsage sums the ledger for one credential since monthStart. Denied
// rows carry zero usage so including them is harmless; billed usage counts
// whether or not the surrounding request succeeded (spend is recorded
// before failures are honored — standing guidance).
func (s *Store) MonthUsage(ctx context.Context, credentialName string, monthStart time.Time) (cents, tokens int64, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(cost_cents), 0), COALESCE(SUM(input_tokens + output_tokens), 0)
		 FROM ledger_entry WHERE credential_name = $1 AND created_at >= $2`,
		credentialName, monthStart).Scan(&cents, &tokens)
	return cents, tokens, err
}

// Ledger returns the newest entries for one credential (all credentials
// when name is empty), newest first.
func (s *Store) Ledger(ctx context.Context, credentialName string, limit int) ([]LedgerEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT credential_name, upstream, model, input_tokens, output_tokens, cost_cents, cost_source, status,
		        acted_for, COALESCE(run_id::text, ''), caller_claim, caller_addr, created_at
		 FROM ledger_entry
		 WHERE ($1 = '' OR credential_name = $1)
		 ORDER BY created_at DESC LIMIT $2`,
		credentialName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.CredentialName, &e.Upstream, &e.Model, &e.InputTokens,
			&e.OutputTokens, &e.CostCents, &e.CostSource, &e.Status,
			&e.ActedFor, &e.RunID, &e.CallerClaim, &e.CallerAddr, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RenewCredential extends (or shortens) the deadline on an existing
// credential WITHOUT touching its token: the material never moves, so
// no Secret has to be rewritten and nothing has to travel. Rotating
// the material is still what it always was — issue a
// fresh credential and re-point the Secret at it.
func (s *Store) RenewCredential(ctx context.Context, name string, expiresAt time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE credential SET expires_at = $2 WHERE name = $1`, name, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCredentials returns every governed credential, soonest deadline
// first so the one about to strand an agent is at the top; legacy
// credentials (no expiry) sort last. Names, caps and deadlines only —
// the token hash never leaves the database.
func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, cap_cents, cap_tokens, expires_at, created_at FROM credential
		 ORDER BY expires_at ASC NULLS LAST, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.Name, &c.CapCents, &c.CapTokens, &c.ExpiresAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
