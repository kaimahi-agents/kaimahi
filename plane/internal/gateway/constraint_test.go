package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaimahi-agents/kaimahi/plane/internal/config"
	"github.com/kaimahi-agents/kaimahi/plane/internal/store"
)

// policyFor builds the gateway's argument-policy surface the way main
// does: from the committed config, never inferred.
func policyFor(t *testing.T, tools, constraints string) config.PolicySet {
	t.Helper()
	raw := `{
	  "upstreams": {"ollama": {"base_url": "http://ollama.ollama:11434", "path": "v1/chat/completions", "classification": "free"}},
	  "tool_upstreams": {"kagent-tools": {"url": "http://kagent-tools.kagent:8084/mcp", "tools": ` + tools + `}}`
	if constraints != "" {
		raw += `, "standing_constraints": ` + constraints
	}
	raw += `}`
	cfg, err := config.Parse([]byte(raw))
	require.NoError(t, err)
	return cfg.Policy()
}

const payDecl = `{"payment_schedule": {"policy_fields": ["invoice_id", "amount_cents", "payee_id"]}}`

func constrainedGateway(t *testing.T, fs *fakeStore, up *httptest.Server, constraints string) http.Handler {
	t.Helper()
	return NewMux(Deps{
		Store:     fs,
		Upstreams: map[string]config.ToolUpstream{"kagent-tools": {URL: up.URL + "/mcp"}},
		Policy:    policyFor(t, payDecl, constraints),
	})
}

func okUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`))
	}))
	t.Cleanup(up.Close)
	return up
}

func call(t *testing.T, h http.Handler, args string) *httptest.ResponseRecorder {
	t.Helper()
	return post(h, goodToken, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"payment_schedule","arguments":`+args+`}}`))
}

const under10k = `{"ap-agent": {"payment_schedule": [{"field": "amount_cents", "op": "lte", "value": 1000000}]}}`

// The accounts-payable case: routine calls proceed with no human at
// all, and the audit still records exactly what ran. The boundary is
// the test — at, just under, and just over.
func TestStandingConstraintAdmitsWithoutAnApproval(t *testing.T) {
	for _, tc := range []struct {
		name   string
		amount string
		admit  bool
	}{
		{"just under", "999999", true},
		{"at the bound", "1000000", true},
		{"a cent over", "1000001", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{credential: store.Credential{Name: "ap-agent"}}
			h := constrainedGateway(t, fs, okUpstream(t), under10k)
			rec := call(t, h, `{"invoice_id":"INV-1","amount_cents":`+tc.amount+`,"payee_id":"MER-4471"}`)

			require.Len(t, fs.audits, 1)
			row := fs.audits[0]
			assert.NotEmpty(t, row.ArgDigest)
			assert.Equal(t, "payment_schedule: invoice_id INV-1, amount_cents "+tc.amount+", payee_id MER-4471", row.ArgSummary)
			if tc.admit {
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, "allowed", row.Decision)
				assert.Equal(t, "within standing constraint", row.Detail)
				assert.Empty(t, fs.filed, "a call inside the bounds asks nobody")
				return
			}
			// Outside: denied in-protocol, and a request is filed for the
			// call actually attempted.
			assert.Equal(t, http.StatusOK, rec.Code) // JSON-RPC error over 200
			assert.Contains(t, rec.Body.String(), "outside the standing constraint")
			assert.Contains(t, rec.Body.String(), "amount_cents lte 1000000")
			assert.Equal(t, "denied", row.Decision)
			require.Len(t, fs.filings, 1)
			assert.Equal(t, row.ArgDigest, fs.filings[0].ArgDigest)
			assert.Equal(t, row.ArgSummary, fs.filings[0].ArgSummary)
		})
	}
}

// "…and never otherwise": where a constraint exists it BINDS, so the
// static allowlist no longer admits that tool for that credential.
func TestStandingConstraintBindsEvenAnAllowlistedTool(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "ap-agent"}, allow: []string{"payment_schedule"}}
	h := constrainedGateway(t, fs, okUpstream(t), under10k)

	rec := call(t, h, `{"invoice_id":"INV-1","amount_cents":4800000,"payee_id":"MER-4471"}`)
	assert.Contains(t, rec.Body.String(), "outside the standing constraint")
	assert.Equal(t, "denied", fs.audits[0].Decision)
	require.Len(t, fs.filings, 1)

	// Inside the bound, the same allowlisted tool proceeds.
	rec = call(t, h, `{"invoice_id":"INV-1","amount_cents":900000,"payee_id":"MER-4471"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "allowed", fs.audits[1].Decision)
}

// A tool a credential is constrained on is callable right now for some
// arguments, so discovery shows it — the same rule live grants follow.
func TestConstrainedToolsJoinTheProjection(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[` +
			`{"name":"payment_schedule"},{"name":"dispute_open"}]}}`))
	}))
	defer up.Close()
	fs := &fakeStore{credential: store.Credential{Name: "ap-agent"}}
	h := constrainedGateway(t, fs, up, under10k)

	rec := post(h, goodToken, rpc(t, "tools/list", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "payment_schedule")
	assert.NotContains(t, rec.Body.String(), "dispute_open")
}

// The defect argument binding fixes: two attempts to pay DIFFERENT
// amounts must file TWO requests. Genuine repeats still dedupe (in the
// store's index, proven against Postgres in store_pg_test.go).
func TestTwoDifferentCallsFileTwoRequests(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "ap-agent"}}
	h := constrainedGateway(t, fs, okUpstream(t), "")

	call(t, h, `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"MER-4471"}`)
	call(t, h, `{"invoice_id":"INV-1","amount_cents":4800000,"payee_id":"MER-4471"}`)
	require.Len(t, fs.filings, 2)
	assert.NotEqual(t, fs.filings[0].ArgDigest, fs.filings[1].ArgDigest)
	assert.Contains(t, fs.filings[0].ArgSummary, "amount_cents 3255000")
	assert.Contains(t, fs.filings[1].ArgSummary, "amount_cents 4800000")

	// Same call twice: the same digest, so the store's dedup key collapses
	// them into one pending request.
	call(t, h, `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"MER-4471"}`)
	require.Len(t, fs.filings, 3)
	assert.Equal(t, fs.filings[0].ArgDigest, fs.filings[2].ArgDigest)
}

// A grant is welded to ONE call: a manipulated agent cannot spend the
// approval for "pay MER-4471" on "pay EVIL-1".
func TestAGrantAdmitsOnlyTheApprovedCall(t *testing.T) {
	approvedArgs := `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"MER-4471"}`
	fs := &fakeStore{credential: store.Credential{Name: "ap-agent"},
		toolGrants: map[string]int{"payment_schedule": 1}}
	h := constrainedGateway(t, fs, okUpstream(t), "")
	fs.grantDigest = Bind("payment_schedule", argsOf(t, approvedArgs),
		[]string{"invoice_id", "amount_cents", "payee_id"}, true).Digest

	// A different payee: denied, and it files its OWN request rather than
	// riding the grant — which is still unspent.
	rec := call(t, h, `{"invoice_id":"INV-1","amount_cents":3255000,"payee_id":"EVIL-1"}`)
	assert.Contains(t, rec.Body.String(), "not permitted")
	require.Len(t, fs.filings, 1)
	assert.Contains(t, fs.filings[0].ArgSummary, "EVIL-1")
	assert.Equal(t, 1, fs.toolGrants["payment_schedule"], "a mismatch consumes nothing")

	// The approved call itself is admitted, consuming the use.
	rec = call(t, h, approvedArgs)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, body, "error")
	assert.Equal(t, 0, fs.toolGrants["payment_schedule"])
	assert.Equal(t, "granted grant-1", fs.audits[1].Detail)
	assert.Equal(t, fs.grantDigest, fs.audits[1].ArgDigest,
		"the admitted row carries the digest the grant was welded to")
}

// Arguments are policy inputs, so a call whose arguments are not an
// object is refused rather than forwarded unexamined.
func TestNonObjectArgumentsAreRefused(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "ap-agent"}, allow: []string{"payment_schedule"}}
	h := constrainedGateway(t, fs, okUpstream(t), "")
	rec := call(t, h, `[1,2]`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "must be a JSON object")
}

// An undeclared tool still binds — to the whole canonical argument
// object — and its summary carries no argument values at all.
func TestUndeclaredToolBindsTheWholeCallAndNamesNothing(t *testing.T) {
	fs := &fakeStore{credential: store.Credential{Name: "hello-tools"}}
	h := NewMux(Deps{Store: fs, Upstreams: map[string]config.ToolUpstream{"kagent-tools": {URL: okUpstream(t).URL + "/mcp"}}})
	rec := post(h, goodToken, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"k8s_get_events","arguments":{"namespace":"default","account":"secret-value"}}}`))
	assert.Contains(t, rec.Body.String(), "not permitted")
	require.Len(t, fs.filings, 1)
	assert.NotContains(t, fs.filings[0].ArgSummary, "secret-value")
	assert.NotContains(t, fs.filings[0].ArgSummary, "namespace")
	assert.NotEmpty(t, fs.filings[0].ArgDigest)
}
