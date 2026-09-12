package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
)

// The validation in front of every mutation exists because these values are
// interpolated into JSON bodies and
// URL paths — and because a typo should fail before a port-forward is
// opened, not after.

func TestParseCap(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want *int64
		bad  bool
	}{
		{in: "-", want: nil},
		{in: "", want: nil},
		{in: "0", want: ptr(0)},
		{in: "100", want: ptr(100)},
		{in: "1000000", want: ptr(1000000)},
		{in: "-1", bad: true},
		{in: "+1", bad: true},
		{in: "1.5", bad: true},
		{in: "1e6", bad: true},
		{in: "abc", bad: true},
		{in: "9223372036854775807", want: ptr(math.MaxInt64)},
		{in: "9223372036854775808", bad: true},
		// A cap is interpolated into a JSON body; a value that smuggles
		// structure must not reach it.
		{in: "1, \"cap_tokens\": 9", bad: true},
	} {
		got, err := ParseCap("cap", tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseCap(%q) was accepted", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCap(%q): %v", tc.in, err)
			continue
		}
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("ParseCap(%q) = %v, want %v", tc.in, deref(got), deref(tc.want))
		}
	}
}

func TestParseTTL(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want *int64
		bad  bool
	}{
		{in: "-", want: nil},
		{in: "", want: nil},
		{in: "90", want: ptr(90)},
		{in: "90s", want: ptr(90)},
		{in: "5m", want: ptr(300)},
		{in: "2h", want: ptr(7200)},
		{in: "1d", want: ptr(86400)},
		{in: "10m", want: ptr(600)},
		{in: "5min", bad: true},
		{in: "m", bad: true},
		{in: "-5m", bad: true},
		{in: "5w", bad: true},
		{in: "5 m", bad: true},
		{in: "0", bad: true},
		{in: "0d", bad: true},
		{in: "+1", bad: true},
		{in: "9223372036854775807", want: ptr(math.MaxInt64)},
		{in: "153722867280912930m", want: ptr(9223372036854775800)},
		{in: "153722867280912931m", bad: true},
		{in: "9223372036854775807d", bad: true},
		{in: "9223372036854775808", bad: true},
	} {
		got, err := ParseTTL(tc.in)
		if tc.bad {
			if err == nil {
				t.Errorf("ParseTTL(%q) was accepted", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTTL(%q): %v", tc.in, err)
			continue
		}
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("ParseTTL(%q) = %v, want %v", tc.in, deref(got), deref(tc.want))
		}
	}
}

func TestIdentityIssueValidatesThenDiscardsTheBearer(t *testing.T) {
	var body map[string]any
	token := "kmh_" + strings.Repeat("a", 64)
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/admin/credentials" {
			t.Errorf("request was %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	}))

	created, err := c.IssueIdentityCredential("inbound-demo", ptr(300))
	if err != nil || !created {
		t.Fatalf("IssueIdentityCredential: created=%v err=%v", created, err)
	}
	if body["name"] != "inbound-demo" || body["ttl_seconds"] != json.Number("300") && body["ttl_seconds"] != float64(300) {
		t.Errorf("issue body = %#v", body)
	}
}

func TestIdentityIssueTreatsConflictAsIdempotentSuccess(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"credential already exists"}`, http.StatusConflict)
	}))
	created, err := c.IssueIdentityCredential("inbound-demo", nil)
	if err != nil || created {
		t.Fatalf("conflict: created=%v err=%v", created, err)
	}
}

func TestIdentityIssueRefusesAnInvalidOneTimeBearer(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": "kmh_" + "not-a-real-token"})
	}))
	if _, err := c.IssueIdentityCredential("inbound-demo", nil); err == nil || !strings.Contains(err.Error(), "invalid Kaimahi token") {
		t.Fatalf("invalid token error = %v", err)
	}
}

func TestIdentityIssueNeverQuotesABearerFromAnErrorResponse(t *testing.T) {
	token := "kmh_" + strings.Repeat("b", 64)
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	}))
	_, err := c.IssueIdentityCredential("inbound-demo", nil)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "kmh_") {
		t.Fatalf("error exposed bearer: %v", err)
	}
}

func TestValidRequestID(t *testing.T) {
	if err := ValidRequestID("00000000-0000-4000-8000-000000000001"); err != nil {
		t.Errorf("a UUID was refused: %v", err)
	}
	// Deliberately low-entropy fixtures: scripts/check-no-azure-ids.sh
	// scans this public tree for GUIDs, and a random-looking one in a test
	// is indistinguishable from a leaked subscription id.
	for _, bad := range []string{"", "abc", "00000000000040008000000000000001",
		"00000000-0000-4000-8000-00000000000A", "../../admin/credentials",
		"00000000-0000-4000-8000-000000000001/deny"} {
		if err := ValidRequestID(bad); err == nil {
			t.Errorf("ValidRequestID(%q) was accepted", bad)
		}
	}
}

// An approval with no bounds is refused BEFORE the port-forward, with the
// plane's own sentence. The plane refuses it too — this is the check that
// stops an operator paying for a forward to learn it.
func TestApproveRefusesAnUnboundedGrant(t *testing.T) {
	if err := CheckBounds(nil, nil); err == nil {
		t.Fatal("an unbounded approval was accepted")
	} else if !strings.Contains(err.Error(), "an unbounded grant is a config change") {
		t.Errorf("the refusal does not carry the plane's wording: %v", err)
	}
	if err := CheckBounds(ptr(60), nil); err != nil {
		t.Errorf("a TTL alone was refused: %v", err)
	}
	if err := CheckBounds(nil, ptr(1)); err != nil {
		t.Errorf("a use count alone was refused: %v", err)
	}

	// An AMOUNT alone is not a bound — it caps what may be SPENT, not how
	// long or how often the grant lives — and it never reaches the wire.
	called := false
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusCreated)
	}))
	if _, err := c.Approve("00000000-0000-4000-8000-000000000001", nil, nil, ptr(1000)); err == nil {
		t.Error("an amount-only approval was sent")
	}
	if called {
		t.Error("the unbounded approval reached the admin API")
	}
}

func TestApprovalAndCredentialNumericBounds(t *testing.T) {
	for _, tc := range []struct {
		what string
		max  int64
	}{{"uses", 1_000_000}, {"amount", 1_000_000_000_000}} {
		for _, n := range []int64{-1, 0, tc.max + 1, math.MaxInt64} {
			if _, err := ParseCap(tc.what, fmt.Sprint(n)); err == nil {
				t.Errorf("%s=%d accepted", tc.what, n)
			}
		}
		for _, n := range []int64{1, tc.max} {
			if _, err := ParseCap(tc.what, fmt.Sprint(n)); err != nil {
				t.Errorf("%s=%d refused: %v", tc.what, n, err)
			}
		}
	}
	for _, ttl := range []int64{-1, 0, 2592001, math.MaxInt64} {
		if err := CheckBounds(ptr(ttl), ptr(1)); err == nil {
			t.Errorf("approval ttl=%d accepted", ttl)
		}
	}
	for _, ttl := range []int64{-1, 0, 59, 31536001, math.MaxInt64} {
		if err := CheckCredentialTTL(ptr(ttl)); err == nil {
			t.Errorf("credential ttl=%d accepted", ttl)
		}
	}
	for _, ttl := range []int64{60, 31536000} {
		if err := CheckCredentialTTL(ptr(ttl)); err != nil {
			t.Errorf("credential ttl=%d refused: %v", ttl, err)
		}
	}
}

// Only the bounds that were SET are sent, and the reply has a stable operator
// rendering.
func TestApproveSendsOnlyTheBoundsGiven(t *testing.T) {
	var body map[string]any
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id": "g-1", "credential": "hello-world", "kind": "budget",
			"subject": "tokens", "expires_at": "2026-09-03T10:00:00Z", "max_uses": 1}`))
	}))
	grant, err := c.Approve("00000000-0000-4000-8000-000000000001", ptr(600), ptr(1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["amount"]; ok {
		t.Error("an unset amount was sent as a key")
	}
	if body["ttl_seconds"] == nil {
		t.Error("ttl_seconds was not sent")
	}
	want := "Granted: hello-world budget/tokens — expires 2026-09-03T10:00:00Z, 1 use(s) (grant g-1)"
	if got := GrantSummary(grant); got != want {
		t.Errorf("GrantSummary =\n  %s\nwant\n  %s", got, want)
	}
}

// A mutation that does not get its well-formed positive fails, quoting what
// the plane said. Fail closed: "it returned something" is not "it worked".
func TestMutationsRefuseTheWrongStatus(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cap_cents must be a non-negative integer", http.StatusBadRequest)
	}))
	err := c.SetBudget("hello-world", ptr(100), nil)
	if err == nil {
		t.Fatal("a 400 was accepted as a budget set")
	}
	if !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "cap_cents") {
		t.Errorf("the failure does not quote the plane: %v", err)
	}
}

func TestUnsupportedRequestKindsNeverReachTheAPI(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) { t.Errorf("unsupported request reached %s", r.URL.Path) }))
	for _, kind := range []string{"tool", "inbound", "nonsense"} {
		if _, err := c.Request("hello-world", kind, "tokens"); err == nil {
			t.Errorf("kind %q accepted", kind)
		}
	}
}

// The pending table is AWK'd by CI — `$1` is the id an approval is issued
// against — so its columns are a contract, and the CALL column is what a
// human is actually approving.
func TestApprovalsTable(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pending": [{"id": "00000000-0000-4000-8000-000000000001",
			"created_at": "2026-09-03T09:15:00.123456Z", "credential": "hello-tools",
			"kind": "tool", "subject": "k8s_get_events", "detail": "denied by allowlist",
			"arg_summary": "k8s_get_events: namespace default"}]}`))
	}))
	var out bytes.Buffer
	if err := c.Approvals(&out); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.Split(out.String(), "\n")[1])
	if fields[0] != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("the id is not the first column: %q", fields)
	}
	if fields[2] != "hello-tools" || fields[3] != "tool" || fields[4] != "k8s_get_events" {
		t.Errorf("the AWK'd columns moved: %q", fields)
	}
	if !strings.Contains(out.String(), "k8s_get_events: namespace default") {
		t.Error("the CALL is missing — an approver cannot see the transaction")
	}
	// The timestamp is cut to the second, as every other table cuts it.
	if fields[1] != "2026-09-03T09:15:00" {
		t.Errorf("the timestamp is %q", fields[1])
	}
}

func TestApprovalsEmpty(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"pending": null}`))
	}))
	var out bytes.Buffer
	if err := c.Approvals(&out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "no pending approval requests\n" {
		t.Errorf("empty approvals printed %q", out.String())
	}
}

// Every mutation carries the admin bearer and none of them follows a
// redirect — the custody rule the reads already hold.
func TestMutationsCarryTheBearerAndRefuseRedirects(t *testing.T) {
	var auth string
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/elsewhere" {
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := c.SetBudget("hello-world", ptr(100), nil); err == nil {
		t.Error("the mutation followed a redirect and called it a success")
	}
	if auth != "Bearer s3cret-admin-token" {
		t.Errorf("the bearer did not travel: %q", auth)
	}
}

func ptr(n int64) *int64 { return &n }

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
