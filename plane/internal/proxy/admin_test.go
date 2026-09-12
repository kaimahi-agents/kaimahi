package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/meter"
	"github.com/kaimahi-agents/kaimahi/plane/internal/proxy"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

func adminMux(t *testing.T, f *fakeStore) (http.Handler, string) {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "admin-token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("admin-secret\n"), 0o600))
	d := proxy.Deps{Store: f, Meter: &meter.Meter{Store: f}, Config: config.Config{}}
	return proxy.NewAdminMux(d, tokenFile), "admin-secret"
}

func adminDo(mux http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestAdminRequiresToken(t *testing.T) {
	mux, _ := adminMux(t, newFakeStore())
	require.Equal(t, 401, adminDo(mux, "POST", "/admin/credentials", "", `{"name": "a"}`).Code)
	require.Equal(t, 401, adminDo(mux, "POST", "/admin/credentials", "wrong", `{"name": "a"}`).Code)
}

func TestAdminFailsClosedWithoutTokenFile(t *testing.T) {
	d := proxy.Deps{Store: newFakeStore(), Meter: &meter.Meter{Store: newFakeStore()}}
	mux := proxy.NewAdminMux(d, "/nonexistent/token")
	require.Equal(t, 503, adminDo(mux, "POST", "/admin/credentials", "any", `{"name": "a"}`).Code)
}

func TestIssueCredentialRoundTrip(t *testing.T) {
	f := newFakeStore()
	mux, tok := adminMux(t, f)
	w := adminDo(mux, "POST", "/admin/credentials", tok, `{"name": "hello-world"}`)
	require.Equal(t, 201, w.Code)
	var resp struct{ Name, Token string }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "hello-world", resp.Name)
	require.True(t, strings.HasPrefix(resp.Token, "kmh_"))
	require.Len(t, resp.Token, 4+64)

	// The issued token authenticates on the data plane.
	dataMux := proxy.NewDataMux(proxy.Deps{Store: f, Meter: &meter.Meter{Store: f},
		Config: config.Config{Upstreams: map[string]config.Upstream{}}})
	require.Equal(t, 403, doChat(t, dataMux, resp.Token, "/upstream/x/y", chatBody).Code,
		"known token reaches authorization (403 unknown upstream), not 401")

	// Duplicate name conflicts.
	require.Equal(t, 409, adminDo(mux, "POST", "/admin/credentials", tok, `{"name": "hello-world"}`).Code)
}

func TestIssueCredentialRejectsBadNames(t *testing.T) {
	mux, tok := adminMux(t, newFakeStore())
	for _, body := range []string{`{}`, `{"name": "UPPER"}`, `{"name": "has space"}`, `not json`} {
		require.Equal(t, 400, adminDo(mux, "POST", "/admin/credentials", tok, body).Code, body)
	}
}

func TestSetBudgetAndLedger(t *testing.T) {
	f := newFakeStore()
	f.addToken("tok", store.Credential{Name: "hello"})
	f.ledger = append(f.ledger, store.LedgerEntry{CredentialName: "hello", Upstream: "ollama",
		Model: "m", InputTokens: 1, OutputTokens: 2, CostSource: "free", Status: 200})
	mux, tok := adminMux(t, f)

	require.Equal(t, 204, adminDo(mux, "PUT", "/admin/budgets", tok,
		`{"credential": "hello", "cap_tokens": 5}`).Code)
	require.Equal(t, 404, adminDo(mux, "PUT", "/admin/budgets", tok,
		`{"credential": "nope", "cap_tokens": 5}`).Code)
	require.Equal(t, 400, adminDo(mux, "PUT", "/admin/budgets", tok,
		`{"credential": "hello", "cap_tokens": -1}`).Code)

	w := adminDo(mux, "GET", "/admin/ledger?credential=hello", tok, "")
	require.Equal(t, 200, w.Code)
	var resp struct {
		Entries     []store.LedgerEntry `json:"entries"`
		MonthCents  *int64              `json:"month_cents"`
		MonthTokens *int64              `json:"month_tokens"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Entries, 1)
	require.NotNil(t, resp.MonthCents)
	require.NotNil(t, resp.MonthTokens)
}

func TestApprovalLifecycle(t *testing.T) {
	f := newFakeStore()
	f.addToken("kmh_a", store.Credential{Name: "hello-model"})
	mux, tok := adminMux(t, f)

	// A request must bind to a REAL credential (the FK): unknown 404s.
	require.Equal(t, 404, adminDo(mux, "POST", "/admin/requests", tok,
		`{"credential": "ghost", "kind": "budget", "subject": "tokens"}`).Code)

	// Explicit filing (`make request`), deduped on refile.
	w := adminDo(mux, "POST", "/admin/requests", tok,
		`{"credential": "hello-model", "kind": "budget", "subject": "tokens"}`)
	require.Equal(t, 201, w.Code)
	require.Contains(t, w.Body.String(), `"filed":true`)
	w = adminDo(mux, "POST", "/admin/requests", tok,
		`{"credential": "hello-model", "kind": "budget", "subject": "tokens"}`)
	require.Equal(t, 201, w.Code)
	require.Contains(t, w.Body.String(), `"deduped":true`)

	// The queue lists it.
	w = adminDo(mux, "GET", "/admin/approvals", tok, "")
	require.Equal(t, 200, w.Code)
	var list struct{ Pending []store.ApprovalRequest }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Pending, 1)
	id := list.Pending[0].ID

	// Bound validation, ported permit discipline: an unbounded grant is
	// refused; a budget grant without an amount is refused.
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/approvals/"+id+"/approve", tok, `{}`).Code)
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/approvals/"+id+"/approve", tok,
		`{"max_uses": 1}`).Code)
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/approvals/"+id+"/approve", tok,
		`{"max_uses": 1, "unknown_field": true}`).Code, "unknown fields rejected")
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/approvals/not-a-uuid/approve", tok,
		`{"max_uses": 1}`).Code)

	// A bounded approval mints the grant; deciding again conflicts.
	w = adminDo(mux, "POST", "/admin/approvals/"+id+"/approve", tok, `{"max_uses": 1, "ttl_seconds": 60, "amount": 1000}`)
	require.Equal(t, 201, w.Code)
	require.Equal(t, 409, adminDo(mux, "POST", "/admin/approvals/"+id+"/approve", tok, `{"max_uses": 1}`).Code)
	require.Equal(t, 409, adminDo(mux, "POST", "/admin/approvals/"+id+"/deny", tok, "").Code)
	require.Equal(t, 404, adminDo(mux, "POST",
		"/admin/approvals/00000000-0000-0000-0000-000000000099/deny", tok, "").Code)

	// Grants and the approvals' own audit trail list the history.
	w = adminDo(mux, "GET", "/admin/grants?credential=hello-model", tok, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "tokens")
	w = adminDo(mux, "GET", "/admin/approval-audit?credential=hello-model", tok, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"requested"`)
	require.Contains(t, w.Body.String(), `"approved"`)

	// A budget request requires an amount to approve.
	w = adminDo(mux, "POST", "/admin/requests", tok,
		`{"credential": "hello-model", "kind": "budget", "subject": "cents"}`)
	require.Equal(t, 201, w.Code)
	w = adminDo(mux, "GET", "/admin/approvals", tok, "")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &list))
	require.Len(t, list.Pending, 1)
	bid := list.Pending[0].ID
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/approvals/"+bid+"/approve", tok,
		`{"max_uses": 1}`).Code, "budget approvals require an amount")
	require.Equal(t, 201, adminDo(mux, "POST", "/admin/approvals/"+bid+"/approve", tok,
		`{"max_uses": 1, "amount": 1000}`).Code)
}

func TestApprovalRequestValidation(t *testing.T) {
	mux, tok := adminMux(t, newFakeStore())
	for _, body := range []string{
		`{"credential": "x", "kind": "nope", "subject": "a"}`,
		`{"credential": "UPPER", "kind": "tool", "subject": "a"}`,
		`{"credential": "x", "kind": "budget", "subject": "gold"}`,
		`{"credential": "x", "kind": "tool", "subject": "bad subject"}`,
		`{"credential": "x", "kind": "tool", "subject": "a"} trailing`,
		`not json`,
	} {
		require.Equal(t, 400, adminDo(mux, "POST", "/admin/requests", tok, body).Code, body)
	}
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/approvals", "", "").Code)
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/grants", "", "").Code)
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/approval-audit", "", "").Code)
}

// Issuing, renewing and listing the deadline: the operator surface for
// credentials that expire. The token is minted once and never shown
// again; only names, caps and deadlines are readable afterwards.
func TestIssuedCredentialsCarryADeadlineAndCanBeRenewed(t *testing.T) {
	f := newFakeStore()
	mux, tok := adminMux(t, f)

	// No ttl named: the default applies. There is no way to ask for a
	// credential that never expires — that class is closed.
	w := adminDo(mux, "POST", "/admin/credentials", tok, `{"name": "hello-world"}`)
	require.Equal(t, 201, w.Code)
	var issued struct {
		Name      string `json:"name"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &issued))
	require.NotEmpty(t, issued.ExpiresAt, "an issued credential says when it dies")
	first, err := time.Parse(time.RFC3339, issued.ExpiresAt)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(30*24*time.Hour), first, time.Minute)

	// A typo cannot mint something dead on arrival, or alive for a decade.
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/credentials", tok, `{"name": "b", "ttl_seconds": 1}`).Code)
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/credentials", tok, `{"name": "b", "ttl_seconds": 99999999}`).Code)
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/credentials", tok, `{"name": "b", "ttl_seconds": 0}`).Code)

	// The list is what an operator reads to see an expiry coming.
	w = adminDo(mux, "GET", "/admin/credentials", tok, "")
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), issued.Token, "the token is shown exactly once, at issue")
	var listed struct {
		Credentials []struct {
			Name         string     `json:"credential"`
			ExpiresAt    *time.Time `json:"expires_at"`
			Expired      bool
			ExpiringSoon bool `json:"expiring_soon"`
		}
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &listed))
	require.Len(t, listed.Credentials, 1)
	require.Equal(t, "hello-world", listed.Credentials[0].Name)
	require.False(t, listed.Credentials[0].Expired)
	require.False(t, listed.Credentials[0].ExpiringSoon)

	// Renewing moves the deadline without minting anything: no material
	// travels, so no Secret has to be rewritten.
	w = adminDo(mux, "POST", "/admin/credentials/hello-world/renew", tok, `{"ttl_seconds": 3600}`)
	require.Equal(t, 200, w.Code)
	require.NotContains(t, w.Body.String(), "kmh_")
	var renewed struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &renewed))
	again, err := time.Parse(time.RFC3339, renewed.ExpiresAt)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Hour), again, time.Minute)

	require.Equal(t, 404, adminDo(mux, "POST", "/admin/credentials/nope/renew", tok, `{"ttl_seconds": 3600}`).Code)
	require.Equal(t, 400, adminDo(mux, "POST", "/admin/credentials/hello-world/renew", tok, `{"ttl_seconds": 5}`).Code)
	require.Equal(t, 401, adminDo(mux, "POST", "/admin/credentials/hello-world/renew", "", `{}`).Code)
	require.Equal(t, 401, adminDo(mux, "GET", "/admin/credentials", "", "").Code)
}
