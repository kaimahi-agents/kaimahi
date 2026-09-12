// Package proxy is the governance plane's egress gateway for LLM traffic:
// the budget meter's enforcement point and the only place real upstream
// credentials are attached to outbound requests. It mounts at kagent's
// ModelConfig baseUrl seam — the governed preset points openAI.baseUrl
// here and carries only a Kaimahi-issued opaque token.
//
// Adapted from tomte-old's proxy package. Ported patterns: one upstream
// base and exactly one allowed (method, path) per upstream as the whole
// blast radius; client auth slots stripped before the real credential is
// injected; fail-closed ordering (authenticate, authorize route, meter,
// then forward); ledger writes on a cancel-free context so a client
// disconnect cannot drop the record of a billed call.
package proxy

import (
	"context"
	"net/http"
	"time"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/egress"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// Store is what the proxy needs from Postgres. *store.Store satisfies it.
type Store interface {
	CredentialByTokenHash(ctx context.Context, tokenHash []byte) (store.Credential, error)
	// RecordLedger appends the row and consumes the call's reservation
	// (empty when the call held nothing).
	RecordLedger(ctx context.Context, e store.LedgerEntry, reservationID string) error
	CreateCredential(ctx context.Context, name string, tokenHash []byte, expiresAt time.Time) error
	// Credential expiry (admin surface): renew extends the deadline on
	// the same token; the list is what an operator reads to see one
	// coming.
	RenewCredential(ctx context.Context, name string, expiresAt time.Time) error
	ListCredentials(ctx context.Context) ([]store.Credential, error)
	SetBudget(ctx context.Context, name string, capCents, capTokens *int64) error
	Ledger(ctx context.Context, credentialName string, limit int) ([]store.LedgerEntry, error)
	MonthUsage(ctx context.Context, credentialName string, monthStart time.Time) (cents, tokens int64, err error)
	// Tool governance (admin surface; the gateway's own data path
	// uses the narrower gateway.Store).
	SetToolAllowlist(ctx context.Context, credentialName string, tools []string) error
	ToolAllowlist(ctx context.Context, credentialName string) ([]string, error)
	ToolAudit(ctx context.Context, credentialName string, limit int) ([]store.ToolAuditEntry, error)
	// Which credentials already allowlist a tool NAME, so onboarding
	// an upstream that offers one can say so instead of claiming nothing
	// can call it yet.
	CredentialsAllowlisting(ctx context.Context, tools []string) (map[string][]string, error)
	// Approvals: deny-and-pend filing (data path) and the decision
	// surface (admin).
	FileApprovalRequest(ctx context.Context, f store.Filing) (filed bool, err error)
	PendingApprovals(ctx context.Context) ([]store.ApprovalRequest, error)
	ApproveRequest(ctx context.Context, id string, expiresAt *time.Time, maxUses *int32, amount *int64, decidedBy string) (store.Grant, error)
	DenyApprovalRequest(ctx context.Context, id string, decidedBy string) error
	Grants(ctx context.Context, credential string, limit int) ([]store.Grant, []bool, error)
	ApprovalAudit(ctx context.Context, credential string, limit int) ([]store.ApprovalAuditEntry, error)
	// Identity on the call: who the run this call falls inside is being
	// made for. Resolution only — never enforcement.
	ActorFor(ctx context.Context, credential string) (store.Attribution, error)
}

// Meter admits or denies a request under the credential's budget caps,
// exactly: an admitted call under a cap holds a reservation until
// its ledger write. *meter.Meter satisfies it.
type Meter interface {
	Reserve(ctx context.Context, cred store.Credential, priced bool) (meter.Reservation, error)
}

type Deps struct {
	Store  Store
	Meter  Meter
	Config config.Config
	// ConfigBase is the COMMITTED table this replica booted from,
	// before any overlay was merged in. The admin surface
	// validates a candidate overlay against it, so a validation is
	// always "would this overlay load over the committed table"
	// and never "would it load over whatever is already overlaid",
	// which would collide an overlay with itself on a second run.
	ConfigBase []byte
	// Client makes IN-CLUSTER upstream calls. Nil gets a default that
	// REFUSES redirects (a keyed call must never follow one — standing
	// guidance) and bounds a call at 5 minutes.
	Client *http.Client
	// InternetClient makes every call to an upstream marked
	// `internet: true` — Copilot: the ONE hardened client main builds
	// (internal/egress) and shares with the MCP gateway. Nil means no
	// hosted upstream can be reached — such a call fails closed (502,
	// ledgered) rather than falling back to the plain client.
	InternetClient *http.Client
}

func (d Deps) clientFor(up config.Upstream) (*http.Client, error) {
	if up.Internet {
		if d.InternetClient == nil {
			return nil, egress.ErrNoClient
		}
		return d.InternetClient, nil
	}
	if d.Client != nil {
		return d.Client, nil
	}
	return &http.Client{
		Timeout: 5 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // surface the 3xx; never follow it with a credential
		},
	}, nil
}
