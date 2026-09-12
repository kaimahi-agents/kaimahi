package admin

import (
	"encoding/json"
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

func TestCredentialNumericBounds(t *testing.T) {
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

// Renewal sends only an explicitly supplied lifetime and reads the new expiry.
func TestRenewSendsOnlyTheLifetimeGiven(t *testing.T) {
	for _, ttl := range []*int64{nil, ptr(600)} {
		var body map[string]any
		c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/admin/credentials/hello-world/renew" {
				t.Errorf("unexpected renewal: %s %s", r.Method, r.URL.Path)
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			w.Write([]byte(`{"expires_at":"2026-09-03T10:00:00Z"}`))
		}))
		expires, err := c.RenewCredential("hello-world", ttl)
		if err != nil || expires != "2026-09-03T10:00:00Z" {
			t.Fatalf("renewal expiry = %q, %v", expires, err)
		}
		if ttl == nil && len(body) != 0 || ttl != nil && (len(body) != 1 || body["ttl_seconds"] != float64(600)) {
			t.Errorf("renewal body = %#v", body)
		}
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
