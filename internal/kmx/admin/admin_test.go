package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeKube records every kubectl kmx would run, and stands in for the
// port-forward with a process that simply stays alive.
type fakeKube struct {
	token string
	calls [][]string
	// forward is the shell command the fake kubectl runs in place of
	// `port-forward`. A real one announces the bind on stdout and stays up.
	forward string
}

func (f *fakeKube) Capture(args ...string) (string, error) {
	f.calls = append(f.calls, args)
	return base64.StdEncoding.EncodeToString([]byte(f.token)), nil
}

func (f *fakeKube) Command(args ...string) *exec.Cmd {
	f.calls = append(f.calls, args)
	// A stand-in for `kubectl port-forward`. The test server is already
	// listening on the port, so once this announces the bind the health
	// check succeeds exactly as it would through a real forward.
	return exec.Command("sh", "-c", f.forward)
}

// forwarding is what a healthy `kubectl port-forward` does: announce the
// bind, then stay up until it is killed.
func forwarding(port string) string {
	return "echo 'Forwarding from 127.0.0.1:" + port + " -> 9091'; exec sleep 60"
}

// freePort asks the kernel for a loopback port nothing is listening on, so
// the "the forward never came up" path can be exercised for real.
// impatient shortens the readiness wait, so a test that exercises a timeout
// does not spend the real one on it.
func impatient(t *testing.T) {
	t.Helper()
	attempts, interval := pollAttempts, pollInterval
	pollAttempts, pollInterval = 3, time.Millisecond
	t.Cleanup(func() { pollAttempts, pollInterval = attempts, interval })
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func serve(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return srv, u.Port()
}

func open(t *testing.T, handler http.HandlerFunc) (*Client, *fakeKube) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell to stand in for the port-forward")
	}
	_, port := serve(t, handler)
	k := &fakeKube{token: "s3cret-admin-token", forward: forwarding(port)}
	c, err := Open(k, port, io.Discard)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(c.Close)
	return c, k
}

func health(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

// The custody rule (docs/COORDINATION.md security guidance): a token travels
// through pipes and process memory, never argv, the environment, a file or a
// log. The shell had to spill the admin bearer into a 0600 file for curl;
// this asserts Go does not put it anywhere at all.
func TestTokensNeverLeaveTheProcess(t *testing.T) {
	var gotAuth string
	c, k := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"entries": []}`))
	}))

	var log bytes.Buffer
	if err := c.Ledger(&log, ""); err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	if gotAuth != "Bearer s3cret-admin-token" {
		t.Fatalf("the admin call did not carry the bearer: %q", gotAuth)
	}

	// Not in any kubectl argument list...
	for _, call := range k.calls {
		for _, arg := range call {
			if strings.Contains(arg, "s3cret-admin-token") {
				t.Errorf("the admin token appeared in a command argument: %v", call)
			}
		}
	}
	// ...and not in anything printed.
	if strings.Contains(log.String(), "s3cret-admin-token") {
		t.Errorf("the admin token was printed: %s", log.String())
	}

	// The Secret is read with jsonpath into this process, and the forward
	// is to the POD's admin port — cluster credentials before the bearer.
	joined := ""
	for _, call := range k.calls {
		joined += strings.Join(call, " ") + "\n"
	}
	for _, want := range []string{
		"get secret kaimahi-admin",
		"port-forward --address 127.0.0.1 deploy/kaimahi-proxy",
		":9091",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("kubectl calls do not include %q:\n%s", want, joined)
		}
	}
}

// A forward that never binds must fail loudly — and the interesting case is
// not "nothing answers", it is "something answers".
//
// The port is the default for BOTH implementations, so a `make ledger` (or a
// second kmx) against another cluster can be holding it. kubectl then dies
// with "address already in use" while a perfectly healthy plane — somebody
// else's — answers /healthz. Probing health first would accept that, and
// `kmx govern` would issue the credential in THAT plane while the Secret and
// the preset switch landed in this one. Waiting for kubectl's own
// "Forwarding from" line is what makes the socket provably ours.
func TestOpenRefusesAForwardThatIsNotOurs(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell")
	}
	// A healthy plane is listening on the port; our forward dies at once.
	_, port := serve(t, health(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was sent through somebody else's forward")
	}))
	k := &fakeKube{
		token:   "t",
		forward: "echo 'Unable to listen on port " + port + ": address already in use' >&2; exit 1",
	}
	c, err := Open(k, port, io.Discard)
	if err == nil {
		c.Close()
		t.Fatal("Open accepted a port held by another cluster's forward")
	}
	if !strings.Contains(err.Error(), "never came up") {
		t.Errorf("unexpected failure: %v", err)
	}
	// The refusal quotes kubectl, so the operator can see WHY.
	if !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("the refusal drops kubectl's own explanation: %v", err)
	}
}

// The forward is ours but the plane behind it is not answering: a different
// failure, and it must say so rather than blame the port.
func TestOpenSaysWhenThePlaneItselfIsSilent(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell")
	}
	impatient(t)
	port := freePort(t)
	k := &fakeKube{token: "t", forward: forwarding(port)}
	c, err := Open(k, port, io.Discard)
	if err == nil {
		c.Close()
		t.Fatal("Open succeeded with no admin API behind the forward")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("unexpected failure: %v", err)
	}
}

// An empty kaimahi-admin Secret must not be read as an empty bearer that
// then fails one call later with a confusing 401.
func TestOpenRefusesAnEmptyAdminSecret(t *testing.T) {
	k := &fakeKube{token: "  ", forward: "true"}
	if _, err := Open(k, "19099", io.Discard); err == nil ||
		!strings.Contains(err.Error(), "missing or empty") {
		t.Fatalf("empty admin Secret: got %v", err)
	}
}

// The read views are what an operator and CI both look at. `kmx ledger` and
// `make ledger` must print the same table: the widths, the headers and the
// month-to-date line are load-bearing, and ci.yml greps them column by
// column.
func TestLedgerRendersTheSameTableTheScriptDoes(t *testing.T) {
	body := `{"entries": [
	  {"created_at": "2026-09-03T01:37:36.123456Z", "credential": "hello-world",
	   "upstream": "ollama", "model": "qwen2.5:3b", "input_tokens": 41,
	   "output_tokens": 17, "cost_cents": 0, "cost_source": "free", "status": 200},
	  {"created_at": "2026-09-03T01:38:00Z", "credential": "hello-world",
	   "upstream": "ollama", "model": "a-very-long-model-name-that-must-be-cut",
	   "input_tokens": 0, "output_tokens": 0, "cost_cents": 0,
	   "cost_source": "denied", "status": 429}],
	 "month_cents": 0, "month_tokens": 58}`
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin/ledger") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Errorf("limit=%q, want 50", got)
		}
		w.Write([]byte(body))
	}))

	var out bytes.Buffer
	if err := c.Ledger(&out, "hello-world"); err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	got := out.String()

	// The exact patterns ci.yml asserts on `make ledger`'s output.
	for _, pattern := range []string{
		`hello-world +ollama +qwen2\.5:3b +[0-9]+ +[0-9]+ +0 +free +200`,
		`denied +429`,
		`month to date: 0 cents, 58 tokens`,
	} {
		if !regexp.MustCompile(pattern).MatchString(got) {
			t.Errorf("ledger output does not match /%s/:\n%s", pattern, got)
		}
	}
	if !strings.HasPrefix(got, "created (UTC)       credential   upstream  model  ") {
		t.Errorf("header columns changed:\n%q", strings.SplitN(got, "\n", 2)[0])
	}
	// Timestamps to the second, models to 16 characters — the script's
	// [:19] and [:16] slices.
	if !strings.Contains(got, "2026-09-03T01:37:36 ") {
		t.Errorf("timestamp not cut to the second:\n%s", got)
	}
	if !strings.Contains(got, "a-very-long-mode ") {
		t.Errorf("long model name not cut to 16 characters:\n%s", got)
	}
	// A token count must never be rendered in scientific notation.
	if strings.Contains(got, "e+") {
		t.Errorf("a number was rendered as a float:\n%s", got)
	}
}

func TestGrantsAndAuditsRenderLikeTheScript(t *testing.T) {
	replies := map[string]string{
		"/admin/grants": `{"grants": [{"id": "00000000-0000-4000-8000-000000000001",
		  "credential": "hello-tools", "kind": "tool", "subject": "k8s_get_events",
		  "live": true, "expires_at": "2026-09-03T02:00:00.5Z", "uses": 0, "max_uses": 1,
		  "amount": null, "created_at": "2026-09-03T01:50:00Z", "decided_by": null,
		  "arg_digest": "8f84e4e9f653abc0000000000000000000000000000000000000000000000000"}]}`,
		"/admin/tool-audit": `{"entries": [{"created_at": "2026-09-03T01:51:00Z",
		  "credential": "hello-tools", "upstream": "kagent-tools", "method": "tools/call",
		  "tool": "k8s_get_resources", "decision": "allowed", "status": 200, "detail": "",
		  "arg_digest": "77245d044835abc0000000000000000000000000000000000000000000000000",
		  "arg_summary": "k8s_get_resources: namespace default"}]}`,
		"/admin/approval-audit": `{"entries": [{"created_at": "2026-09-03T01:52:00Z",
		  "credential": "hello-world", "kind": "budget", "subject": "tokens",
		  "action": "approved", "decided_by": null, "bounds": "1 use(s)",
		  "arg_summary": ""}]}`,
	}
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		body, ok := replies[r.URL.Path]
		if !ok {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))

	var grants bytes.Buffer
	if err := c.Grants(&grants, ""); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	for _, want := range []string{
		"live", "expires (UTC)", "decided by", "binds",
		"hello-tools", "k8s_get_events",
		// A tool grant admits ONE call, and the table says which.
		"call 8f84e4e9f653",
		" yes   ",              // liveness is a word, not a JSON bool
		"0/1",                  // uses/max_uses
		"2026-09-03T02:00:00 ", // expiry cut to the second, like every other timestamp
	} {
		if !strings.Contains(grants.String(), want) {
			t.Errorf("grants output lacks %q:\n%s", want, grants.String())
		}
	}
	// A null optional prints as "-", never as "<nil>" or "null".
	if strings.Contains(grants.String(), "<nil>") || strings.Contains(grants.String(), "null") {
		t.Errorf("a null rendered literally:\n%s", grants.String())
	}

	var tools bytes.Buffer
	if err := c.ToolAudit(&tools, ""); err != nil {
		t.Fatalf("ToolAudit: %v", err)
	}
	// The audited call travels with the row: the summary a human reads and
	// the digest prefix that ties a denial, its approval and the admitted
	// call together.
	if !strings.Contains(tools.String(), "k8s_get_resources: namespace default [77245d044835]") {
		t.Errorf("tool audit does not name the call:\n%s", tools.String())
	}
	if !regexp.MustCompile(`hello-tools +kagent-tools +tools/call +k8s_get_resources +allowed +200`).
		MatchString(tools.String()) {
		t.Errorf("tool audit does not match the pattern ci.yml greps:\n%s", tools.String())
	}

	var approvals bytes.Buffer
	if err := c.ApprovalAudit(&approvals, ""); err != nil {
		t.Fatalf("ApprovalAudit: %v", err)
	}
	if !regexp.MustCompile(`hello-world +budget +tokens +approved`).MatchString(approvals.String()) {
		t.Errorf("approval audit does not match the pattern ci.yml greps:\n%s", approvals.String())
	}
}

// An empty grants list says so in words. "no grants" is what `make grants`
// prints, and a bare header would read as a fetch that failed.
func TestEmptyGrantsSaysSo(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"grants": null}`))
	}))
	var out bytes.Buffer
	if err := c.Grants(&out, ""); err != nil {
		t.Fatalf("Grants: %v", err)
	}
	if strings.TrimSpace(out.String()) != "no grants" {
		t.Errorf("empty grants printed %q", out.String())
	}
}

// Fail closed on a read: anything but 200 is an error carrying the plane's
// own words, not an empty table that reads as "nothing has happened yet".
func TestAReadThatIsNot200IsAnError(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "database unavailable"}`))
	}))
	var out bytes.Buffer
	err := c.Ledger(&out, "")
	if err == nil {
		t.Fatal("a 500 was rendered as an empty ledger")
	}
	if !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "database unavailable") {
		t.Errorf("error loses the plane's reply: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a failed read printed a table anyway:\n%s", out.String())
	}
}

// An authenticated call must not follow a redirect: Go strips Authorization
// across hosts, but a same-host redirect would carry the admin bearer
// somewhere it was not addressed to.
func TestAuthenticatedCallsDoNotFollowRedirects(t *testing.T) {
	hits := 0
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/admin/ledger" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		t.Errorf("the client followed a redirect to %s", r.URL.Path)
	}))
	var out bytes.Buffer
	if err := c.Ledger(&out, ""); err == nil {
		t.Fatal("a 302 was accepted as a ledger")
	}
	if hits != 1 {
		t.Errorf("%d requests made; the redirect was followed", hits)
	}
}

// The credential issue call is a POST with the name in the body, and the
// token comes back in the reply — it is shown exactly once and never again.
func TestIssueSendsTheNameAndReadsTheToken(t *testing.T) {
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "hello-world" {
			t.Errorf("issue body %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token": "kmh_` + strings.Repeat("a", 64) + `"}`))
	}))
	status, body, err := c.Do(http.MethodPost, "/admin/credentials", map[string]string{"name": "hello-world"})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("issue: status %d err %v", status, err)
	}
	doc, err := decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(str(doc["token"]), "kmh_") {
		t.Errorf("token not read back: %v", doc)
	}
}

// Nothing this package does writes a file. The shell script had to
// (curl -H @file, kubectl --from-file); a regression to that pattern would
// put a bearer on disk.
func TestNoFilesAreWritten(t *testing.T) {
	dir := t.TempDir()
	before, _ := filepath.Glob(filepath.Join(dir, "*"))
	t.Setenv("TMPDIR", dir)
	c, _ := open(t, health(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"entries": []}`))
	}))
	if err := c.Ledger(io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(after) != len(before) {
		t.Errorf("files appeared in the temporary directory: %v", after)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth-header")); err == nil {
		t.Error("an auth-header file was written")
	}
}
