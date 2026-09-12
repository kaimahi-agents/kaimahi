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

// An EMPTY allowlist is an answer, not an error: it means nothing is
// callable without a live grant. It must also marshal as `[]`, never
// `null` — the plane would read a null as "no change".
func TestParseToolList(t *testing.T) {
	empty, err := ParseToolList("-")
	if err != nil {
		t.Fatalf("the empty allowlist was refused: %v", err)
	}
	if empty == nil {
		t.Fatal("the empty allowlist is nil, which marshals as null, not []")
	}
	body, _ := json.Marshal(map[string]any{"tools": empty})
	if string(body) != `{"tools":[]}` {
		t.Errorf("empty allowlist marshalled as %s", body)
	}

	got, err := ParseToolList("k8s_get_resources,k8s_get_events")
	if err != nil || strings.Join(got, ",") != "k8s_get_resources,k8s_get_events" {
		t.Errorf("ParseToolList = %v, %v", got, err)
	}
	// The ORDER given is the order sent. The plane sorts on read-back; kmx
	// does not sort on write, or a caller could not tell the two apart.
	for _, bad := range []string{"a,,b", "a b", `a","b`, "a/b", "a,"} {
		if _, err := ParseToolList(bad); err == nil {
			t.Errorf("ParseToolList(%q) was accepted", bad)
		}
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
		w.Write([]byte(`{"id": "g-1", "credential": "hello-tools", "kind": "tool",
			"subject": "k8s_get_events", "expires_at": "2026-09-03T10:00:00Z", "max_uses": 1}`))
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
	want := "Granted: hello-tools tool/k8s_get_events — expires 2026-09-03T10:00:00Z, 1 use(s) (grant g-1)"
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

// A tool request carries the CALL it is about; a budget request cannot.
func TestRequestArgumentsAreToolOnly(t *testing.T) {
	var body map[string]any
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"deduped": true}`))
	}))
	deduped, err := c.Request("hello-tools", "tool", "k8s_get_events",
		map[string]any{"namespace": "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !deduped {
		t.Error("deduped was not read off the reply")
	}
	if args, _ := body["arguments"].(map[string]any); args["namespace"] != "default" {
		t.Errorf("the call's arguments did not travel: %v", body)
	}

	if _, err := c.Request("hello-world", "budget", "tokens", map[string]any{"x": 1}); err == nil {
		t.Error("a budget request accepted arguments")
	}
	for _, kind := range []string{"inbound", "nonsense"} {
		body = nil
		if _, err := c.Request("hello-world", kind, "tokens", nil); err == nil {
			t.Errorf("unsupported request kind %q was accepted", kind)
		}
		if body != nil {
			t.Errorf("unsupported request kind %q reached the API: %v", kind, body)
		}
	}
	body = nil
	if _, err := c.Request("hello-world", "budget", "tokens", nil); err != nil {
		t.Fatal(err)
	}
	if body["kind"] != "budget" || body["subject"] != "tokens" || body["credential"] != "hello-world" {
		t.Errorf("budget request did not travel: %v", body)
	}
	// Omitting the arguments must OMIT the key, not send null: on a tool
	// request the absent key means the ARGUMENT-LESS call, never "any call".
	body = nil
	if _, err := c.Request("hello-tools", "tool", "k8s_get_events", nil); err != nil {
		t.Fatal(err)
	}
	if _, present := body["arguments"]; present {
		t.Error("an omitted call was sent as an explicit key")
	}
}

// The allowlist reads back SORTED, and an empty one is an answer.
func TestToolAllowlistView(t *testing.T) {
	reply := `{"credential": "hello-tools", "tools": ["k8s_get_events", "k8s_get_resources"]}`
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(reply))
	}))
	var out bytes.Buffer
	if err := c.ToolAllowlist(&out, "hello-tools"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "hello-tools: k8s_get_events, k8s_get_resources\n" {
		t.Errorf("allowlist read back as %q", got)
	}

	reply = `{"credential": "hello-tools", "tools": []}`
	out.Reset()
	if err := c.ToolAllowlist(&out, "hello-tools"); err != nil {
		t.Fatalf("an empty allowlist was reported as a failure: %v", err)
	}
	if got := out.String(); got != "hello-tools: (empty — nothing callable)\n" {
		t.Errorf("the empty allowlist read back as %q", got)
	}
	// A null list is the same answer, not a crash.
	reply = `{"credential": "hello-tools", "tools": null}`
	out.Reset()
	if err := c.ToolAllowlist(&out, "hello-tools"); err != nil {
		t.Fatalf("a null allowlist was reported as a failure: %v", err)
	}
	if got := out.String(); got != "hello-tools: (empty — nothing callable)\n" {
		t.Errorf("the null allowlist read back as %q", got)
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
	if err := c.SetToolAllowlist("hello-tools", []string{"k8s_get_events"}); err == nil {
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
