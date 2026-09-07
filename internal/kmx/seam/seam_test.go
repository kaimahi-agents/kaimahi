package seam

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Nothing here is a real credential and nothing here reaches a real upstream.
// The token shapes are ASSEMBLED rather than written, so a fixture in this
// file can never be mistaken for a leak by a scanner or by a person reading a
// diff, and every check runs against a local server that answers the way the
// real one does.
func fineGrainedToken() []byte {
	return []byte("github" + "_pat_" + strings.Repeat("A", 22))
}

// jwt builds an access-token-shaped string around the claims given. The
// signature is not checked by anything here — the server is the authority on
// whether a token is real, which is exactly why the local checks are limited
// to the shape and the deadline.
func jwt(t *testing.T, claims map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	if !strings.HasPrefix(header, "eyJ") {
		t.Fatalf("the fixture header does not look like a JWT: %s", header)
	}
	return []byte(header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln")
}

func liveADOClaims() map[string]any {
	return map[string]any{
		"aud": "https://mcp.dev.azure.com",
		"exp": time.Now().Add(55 * time.Minute).Unix(),
	}
}

// testEnv points the checks at a local server standing in for the upstream.
func testEnv(t *testing.T, handler http.HandlerFunc) Env {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	env := DefaultEnv()
	env.GitHubAPI = srv.URL
	env.ADOBase = srv.URL
	return env
}

func githubOK(repo string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Github-Authentication-Token-Expiration", "2026-12-01 09:00:00 +0000")
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": repo})
	}
}

func adoOK() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`))
	}
}

// --- the table itself ------------------------------------------------------

// Every seam is complete. A row missing its Secret or its key stores a
// credential where the gateway does not read it, which fails much later and
// looks like a bad token.
func TestEverySeamIsComplete(t *testing.T) {
	for _, s := range All() {
		if s.Name == "" || s.Secret == "" || s.Key == "" || s.Prompt == "" ||
			s.Guidance == "" || s.SubjectName == "" || s.SubjectHint == "" || s.Summary == "" {
			t.Errorf("seam %q is missing a field: %+v", s.Name, s)
		}
		if s.subject == nil || s.validate == nil {
			t.Errorf("seam %q has no subject rule or no check", s.Name)
		}
	}
	if _, err := Lookup("nope"); err == nil {
		t.Fatal("an unknown upstream was accepted")
	} else if !strings.Contains(err.Error(), "github-release") {
		t.Errorf("the refusal does not say what exists: %v", err)
	}
}

// The subject is checked BEFORE a credential is read, so a typo costs a
// retype and not a token. It is also interpolated into a URL, so the rule is
// anchored.
func TestTheSubjectIsCheckedBeforeAnythingIsRead(t *testing.T) {
	github, err := Lookup("github-release")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "  ", "owner", "owner/name/extra", "owner/na me",
		"../../etc", "owner/name?x=1", "https://github.com/owner/name",
	} {
		if err := github.CheckSubject(bad); err == nil {
			t.Errorf("repository %q was accepted", bad)
		}
	}
	if err := github.CheckSubject("kaimahi-agents/kaimahi"); err != nil {
		t.Errorf("a real repository was refused: %v", err)
	}

	ado, err := Lookup("ado")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "with space", "with/slash", "-leading", strings.Repeat("x", 64)} {
		if err := ado.CheckSubject(bad); err == nil {
			t.Errorf("organization %q was accepted", bad)
		}
	}
	if err := ado.CheckSubject("an-organization"); err != nil {
		t.Errorf("a plausible organization was refused: %v", err)
	}
}

// --- GitHub ----------------------------------------------------------------

func TestAGitHubTokenThatIsNotFineGrainedIsRefusedWithoutACall(t *testing.T) {
	called := false
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "owner/name"})
	})
	s, _ := Lookup("github-release")
	for _, bad := range [][]byte{
		[]byte("gh" + "p_" + strings.Repeat("A", 36)), // a classic token
		[]byte("gh" + "o_" + strings.Repeat("A", 36)), // an OAuth token
		[]byte("not-a-token"),
		[]byte(""),
		append(fineGrainedToken(), " trailing"...),
	} {
		_, err := s.Validate(env, "owner/name", bad)
		if err == nil {
			t.Fatalf("%q was accepted", bad)
		}
		if !strings.Contains(err.Error(), "fine-grained") {
			t.Errorf("the refusal does not say which check failed: %v", err)
		}
	}
	if called {
		t.Error("a token that cannot be right was still sent to GitHub")
	}
}

func TestAGitHubTokenThatCannotReadTheRepositoryIsRefused(t *testing.T) {
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	s, _ := Lookup("github")
	_, err := s.Validate(env, "owner/name", fineGrainedToken())
	if err == nil {
		t.Fatal("a token that cannot read the repository was accepted")
	}
	if !strings.Contains(err.Error(), "read owner/name") {
		t.Errorf("the refusal does not name the repository: %v", err)
	}
	// The message is what the operator has to act on, so it says what to
	// check — and never an HTTP status, which is not a thing anyone can fix.
	for _, forbidden := range []string{"404", "HTTP", "status"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("the refusal shows the operator %q: %v", forbidden, err)
		}
	}
}

// A classic token that somehow reaches the API call is still refused, on the
// scopes GitHub announces for it. Belt and braces on purpose: the prefix says
// what a token calls itself, the header says what it is.
func TestAGitHubTokenAnnouncingScopesIsRefused(t *testing.T) {
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-OAuth-Scopes", "repo, workflow")
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "owner/name"})
	})
	s, _ := Lookup("github-release")
	_, err := s.Validate(env, "owner/name", fineGrainedToken())
	if err == nil {
		t.Fatal("a token carrying OAuth scopes was accepted")
	}
	if !strings.Contains(err.Error(), "classic token") {
		t.Errorf("the refusal does not say which check failed: %v", err)
	}
}

// The answer has to be about the repository that was asked for. A proxy or a
// redirect that answered about something else must not read as a pass.
func TestGitHubAnsweringAboutAnotherRepositoryIsRefused(t *testing.T) {
	env := testEnv(t, githubOK("someone/else"))
	s, _ := Lookup("github-release")
	if _, err := s.Validate(env, "owner/name", fineGrainedToken()); err == nil {
		t.Fatal("an answer about another repository was accepted")
	}
	env = testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>welcome to the filtering proxy</html>"))
	})
	if _, err := s.Validate(env, "owner/name", fineGrainedToken()); err == nil {
		t.Fatal("an HTML page answered with 200 was accepted")
	}
}

func TestAGoodGitHubTokenIsVettedAndItsLimitsAreSaid(t *testing.T) {
	var auth, accept string
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		auth, accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		if r.URL.Path != "/repos/owner/name" {
			t.Errorf("asked for %s", r.URL.Path)
		}
		githubOK("owner/name")(w, r)
	})
	s, _ := Lookup("github-release")
	report, err := s.Validate(env, "owner/name", fineGrainedToken())
	if err != nil {
		t.Fatalf("a good token was refused: %v", err)
	}
	if auth != "Bearer "+string(fineGrainedToken()) {
		t.Errorf("the token did not travel in the Authorization header: %q", auth)
	}
	if accept != "application/vnd.github+json" {
		t.Errorf("Accept was %q", accept)
	}
	if len(report.Proven) != 1 || !strings.Contains(report.Proven[0], "owner/name") {
		t.Errorf("proven: %v", report.Proven)
	}
	if !strings.Contains(report.Deadline, "2026-12-01") {
		t.Errorf("the deadline was not reported: %q", report.Deadline)
	}
	// The half that is not a footnote: what this path CANNOT establish, said
	// plainly. A silence here would read as a guarantee.
	if len(report.Unproven) != 1 || !strings.Contains(report.Unproven[0], "one\n  repository") {
		t.Errorf("the single-repository limit is not reported: %v", report.Unproven)
	}
}

// An absent expiry header means "this answer did not carry one", not "this
// token never expires" — the distinction a check here once got wrong, by
// refusing valid tokens on a silence.
func TestAnAbsentGitHubExpiryIsReportedNotRefused(t *testing.T) {
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "owner/name"})
	})
	s, _ := Lookup("github")
	report, err := s.Validate(env, "owner/name", fineGrainedToken())
	if err != nil {
		t.Fatalf("a token whose answer carried no expiry was refused: %v", err)
	}
	if !strings.Contains(report.Deadline, "NOT REPORTED") {
		t.Errorf("the missing deadline was not said out loud: %q", report.Deadline)
	}
}

// A keyed call must not follow a redirect: Go strips Authorization across
// hosts but not every header, and the destination of a redirect is a host
// nobody vetted.
func TestAKeyedCallDoesNotFollowARedirect(t *testing.T) {
	elsewhere := httptest.NewServer(githubOK("owner/name"))
	t.Cleanup(elsewhere.Close)
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/repos/owner/name", http.StatusMovedPermanently)
	})
	s, _ := Lookup("github")
	_, err := s.Validate(env, "owner/name", fineGrainedToken())
	if err == nil {
		t.Fatal("the check followed a redirect and accepted the answer")
	}
	var target *url.Error
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("the refusal does not name the reason: %v (%T)", err, target)
	}
}

// --- Azure DevOps ----------------------------------------------------------

func TestAnAzureDevOpsCredentialThatIsNotAnAccessTokenIsRefusedWithoutACall(t *testing.T) {
	called := false
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) { called = true; adoOK()(w, r) })
	s, _ := Lookup("ado")
	for _, bad := range [][]byte{
		[]byte(strings.Repeat("a", 52)), // a personal access token's shape
		[]byte("eyJ.only.two"),
		[]byte(""),
	} {
		if _, err := s.Validate(env, "an-organization", bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if called {
		t.Error("something that is not an access token was still sent to the server")
	}
}

func TestAnAzureDevOpsTokenWithNoExpiryIsRefused(t *testing.T) {
	env := testEnv(t, adoOK())
	s, _ := Lookup("ado")
	for _, claims := range []map[string]any{
		{"aud": "https://mcp.dev.azure.com"},
		{"aud": "https://mcp.dev.azure.com", "exp": "soon"},
		{"aud": "https://mcp.dev.azure.com", "exp": 0},
	} {
		_, err := s.Validate(env, "an-organization", jwt(t, claims))
		if err == nil {
			t.Fatalf("a token with claims %v was accepted", claims)
		}
		if !strings.Contains(err.Error(), "no expiry claim") {
			t.Errorf("the refusal does not say which check failed: %v", err)
		}
	}
}

func TestAnExpiredAzureDevOpsTokenIsRefused(t *testing.T) {
	env := testEnv(t, adoOK())
	s, _ := Lookup("ado")
	claims := liveADOClaims()
	claims["exp"] = time.Now().Add(-20 * time.Minute).Unix()
	_, err := s.Validate(env, "an-organization", jwt(t, claims))
	if err == nil {
		t.Fatal("an expired token was accepted")
	}
	if !strings.Contains(err.Error(), "expired 20 minutes ago") {
		t.Errorf("the refusal does not say how stale it is: %v", err)
	}
	if !strings.Contains(err.Error(), "get-access-token") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

// The audience is REPORTED, never enforced: Entra writes it two ways for the
// same request, and refusing on it refused tokens minted by the command this
// seam recommends. The server below is the authority instead.
func TestAnUnexpectedAudienceIsReportedAndTheServerDecides(t *testing.T) {
	env := testEnv(t, adoOK())
	s, _ := Lookup("ado")
	claims := liveADOClaims()
	claims["aud"] = "an-application-identifier"
	report, err := s.Validate(env, "an-organization", jwt(t, claims))
	if err != nil {
		t.Fatalf("a token the SERVER accepted was refused on its audience: %v", err)
	}
	if len(report.Unproven) == 0 || !strings.Contains(report.Unproven[0], "audience") {
		t.Errorf("the audience limit was not reported: %v", report.Unproven)
	}
	// And the other direction: the right-looking audience is not a pass on
	// its own — the server still gets to say no.
	env = testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	})
	if _, err := s.Validate(env, "an-organization", jwt(t, liveADOClaims())); err == nil {
		t.Fatal("a token the server rejected was accepted because its audience looked right")
	}
}

func TestTheAzureDevOpsServerHasTheLastWord(t *testing.T) {
	s, _ := Lookup("ado")
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		says    string
	}{
		{"the token is not accepted", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusUnauthorized)
		}, "does not accept this token"},
		{"the organization does not exist", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no", http.StatusNotFound)
		}, `no organization named "an-organization"`},
		{"a filtering proxy answers with a page", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>blocked</html>"))
		}, "not a protocol message"},
		{"the handshake itself fails", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"nope"}}`))
		}, "refused the opening handshake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Validate(testEnv(t, tc.handler), "an-organization", jwt(t, liveADOClaims()))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say which check failed: %v", err)
			}
		})
	}
}

// The hosted endpoint may answer as a server-sent-event stream. The result is
// in the last data: frame, and a check that could not read it would refuse
// every working token.
func TestAServerSentEventAnswerIsUnderstood(t *testing.T) {
	var body, auth string
	env := testEnv(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		b := make([]byte, 4096)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n")
	})
	s, _ := Lookup("ado")
	token := jwt(t, liveADOClaims())
	report, err := s.Validate(env, "an-organization", token)
	if err != nil {
		t.Fatalf("a streamed answer was not understood: %v", err)
	}
	if auth != "Bearer "+string(token) {
		t.Errorf("the token did not travel in the Authorization header: %q", auth)
	}
	if !strings.Contains(body, `"method":"initialize"`) {
		t.Errorf("the check did not open the protocol: %q", body)
	}
	if len(report.Proven) != 2 {
		t.Errorf("proven: %v", report.Proven)
	}
	if !strings.Contains(report.Deadline, "minutes") {
		t.Errorf("the deadline was not reported: %q", report.Deadline)
	}
}
