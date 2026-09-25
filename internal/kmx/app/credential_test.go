package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seamcert"
)

// A fake kubectl on PATH, so credential issue can be driven end to end
// without a cluster: every argument list and everything piped into it is
// recorded.
const fakeKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"config view"*)
    cat <<'JSON'
{"clusters":[{"name":"kind-kaimahi-p1","cluster":{"server":"https://127.0.0.1:6443"}}],
 "contexts":[{"name":"kind-kaimahi-p1","context":{"cluster":"kind-kaimahi-p1"}}]}
JSON
    exit 0 ;;
  *"get secret kaimahi-admin"*) printf '%s' "$KMX_TEST_ADMIN_B64"; exit 0 ;;
  *"get secret kaimahi-governed-token"*)
    if [ "$KMX_TEST_SECRET_EXISTS" != true ]; then
      printf 'Error from server (NotFound): secrets "kaimahi-governed-token" not found\n' >&2
      exit 1
    fi
    printf '{"metadata":{"annotations":{"kaimahi.dev/credential":"%s"}}}' "$KMX_TEST_BOUND"
    exit 0 ;;
  *"get secret kaimahi-plane-seam-tls"*) printf '%s' "$KMX_TEST_SEAM_TLS"; exit 0 ;;
  *"get secret "*)
    printf 'Error from server (NotFound): secret not found\n' >&2
    exit 1 ;;
  *"get modelconfig custom"*)
    if [ -n "$KMX_TEST_MODEL_ERR" ]; then printf '%s\n' "$KMX_TEST_MODEL_ERR" >&2; exit 1; fi
    printf '%s' "$KMX_TEST_MODEL"; exit 0 ;;
  *port-forward*)
    # A real kubectl announces the bind before anything may be sent through
    # it; kmx waits for exactly this line, so the fake has to print it.
    printf 'Forwarding from 127.0.0.1:%s -> 9091\n' "$KMX_TEST_ADMIN_PORT"
    exec sleep 30 ;;
  *"get agents.kagent.dev hello-world"*)
    if [ -n "$KMX_TEST_AGENT_ERR" ]; then
      printf '%s\n' "$KMX_TEST_AGENT_ERR" >&2
      exit 1
    fi
    printf 'agent.kagent.dev/hello-world\n'; exit 0 ;;
  *"get remotemcpserver"*)
    if [ -n "$KMX_TEST_TOOL_SERVER" ]; then printf '%s' "$KMX_TEST_TOOL_SERVER"; exit 0; fi
    [ -z "$KMX_TEST_SEAM" ] && exit 0
    printf '%s' "$KMX_TEST_SEAM"; exit 0 ;;
  *"get crd remotemcpservers.kagent.dev"*)
    case "$KMX_TEST_NO_KAGENT" in
      1) printf 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "remotemcpservers.kagent.dev" not found\n' >&2; exit 1 ;;
      *) printf 'customresourcedefinition.apiextensions.k8s.io/remotemcpservers.kagent.dev\n'; exit 0 ;;
    esac ;;
  *"get namespace"*)
    for missing in $KMX_TEST_NO_NAMESPACES; do
      case "$*" in
        *"get namespace $missing "*)
          printf 'Error from server (NotFound): namespaces "%s" not found\n' "$missing" >&2
          exit 1 ;;
      esac
    done
    printf 'namespace/x\n'; exit 0 ;;
  *"apply -f -"*) cat >> "$KMX_TEST_STDIN"; exit 0 ;;
esac
exit 0
`

type credentialFixture struct {
	app     *App
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	argsLog string
	stdin   string
}

func newCredentialFixture(t *testing.T, agentErr string, issue http.HandlerFunc) *credentialFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(planePreamble(admin.Speaks, issue))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	f := &credentialFixture{
		out:     &bytes.Buffer{},
		errOut:  &bytes.Buffer{},
		argsLog: filepath.Join(dir, "args"),
		stdin:   filepath.Join(dir, "stdin"),
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", f.argsLog)
	t.Setenv("KMX_TEST_STDIN", f.stdin)
	t.Setenv("KMX_TEST_AGENT_ERR", agentErr)
	t.Setenv("KMX_TEST_ADMIN_B64", base64.StdEncoding.EncodeToString([]byte("admin-bearer")))
	t.Setenv("KMX_TEST_ADMIN_PORT", u.Port())
	t.Setenv("KMX_TEST_BOUND", os.Getenv("KMX_TEST_BOUND"))
	if os.Getenv("KMX_TEST_BOUND") != "" {
		t.Setenv("KMX_TEST_SECRET_EXISTS", "true")
	} else {
		t.Setenv("KMX_TEST_SECRET_EXISTS", os.Getenv("KMX_TEST_SECRET_EXISTS"))
	}
	t.Setenv("KMX_TEST_SEAM_TLS", seamTLSSecret(t))

	cfg := &config.Config{
		KindCluster: "kaimahi-p1",
		KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx,
		AdminPort:  u.Port(),
		Credential: "hello-world",
	}
	r := run.Default()
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{Cfg: cfg, Run: r, Out: f.out, Err: f.errOut}
	return f
}

func (f *credentialFixture) args() string {
	b, _ := os.ReadFile(f.argsLog)
	return string(b)
}

func (f *credentialFixture) piped() string {
	b, _ := os.ReadFile(f.stdin)
	return string(b)
}

func issued(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	}
}

func TestInteractiveCredentialReplacesOnlyPrevalidatedOwnedSecret(t *testing.T) {
	if got := credentialSecretVerb(true, false); got != "create" {
		t.Fatalf("absent interactive Secret uses %q", got)
	}
	if got := credentialSecretVerb(true, true); got != "apply" {
		t.Fatalf("existing owned interactive Secret uses %q", got)
	}
	if got := credentialSecretVerb(false, false); got != "apply" {
		t.Fatalf("non-interactive issue behavior changed to %q", got)
	}
}

// Custody, proven rather than commented: the issued token reaches the
// cluster through kubectl's STDIN and appears in no argument, no environment
// listing, no file kmx wrote, and nothing printed. This is the property the
// shell script needed a 0600 file and a dry-run pipe to approximate.
func TestTheIssuedTokenTravelsOnlyThroughThePipe(t *testing.T) {
	token := "kmh_" + strings.Repeat("c", 64)
	f := newCredentialFixture(t, "", issued(token))
	if err := f.app.IssueCredentialToSecret("hello-world", config.GovernedSecret, config.DefaultNamespace, nil); err != nil {
		t.Fatalf("issue: %v", err)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(token))
	if !strings.Contains(f.piped(), "api-key: "+encoded) {
		t.Fatalf("the token was not piped into kubectl:\n%s", f.piped())
	}
	if !strings.Contains(f.piped(), `kaimahi.dev/credential: "hello-world"`) {
		t.Errorf("the Secret is not bound to its credential:\n%s", f.piped())
	}
	for name, body := range map[string]string{
		"kubectl arguments": f.args(),
		"stdout":            f.out.String(),
		"stderr":            f.errOut.String(),
	} {
		if strings.Contains(body, token) || strings.Contains(body, encoded) {
			t.Errorf("the token appears in %s:\n%s", name, body)
		}
	}
	// ...and the admin bearer, which gates issuing every credential, is not
	// in an argument list either.
	if strings.Contains(f.args(), "admin-bearer") {
		t.Errorf("the admin token appears in kubectl's arguments:\n%s", f.args())
	}
}

func TestCredentialIssueStoresTokenWithGovernSafetyWithoutLoggingIt(t *testing.T) {
	token := "kmh_" + strings.Repeat("e", 64)
	var request map[string]any
	f := newCredentialFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode issue request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	})
	ttl := int64(3600)
	if err := f.app.IssueCredentialToSecret("batch-agent", config.GovernedSecret, "jobs", &ttl); err != nil {
		t.Fatalf("issue to Secret: %v", err)
	}
	if request["name"] != "batch-agent" || request["ttl_seconds"] != float64(3600) {
		t.Fatalf("issue request=%v", request)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(token))
	for _, want := range []string{
		"namespace: jobs",
		`kaimahi.dev/credential: "batch-agent"`,
		"api-key: " + encoded,
	} {
		if !strings.Contains(f.piped(), want) {
			t.Errorf("Secret manifest lacks %q:\n%s", want, f.piped())
		}
	}
	if strings.Contains(f.piped(), "kaimahi.dev/chat-agent") {
		t.Errorf("generic Secret was spuriously bound to an agent:\n%s", f.piped())
	}
	if !strings.Contains(f.args(), "-n jobs apply -f -") {
		t.Errorf("Secret was not applied in the requested namespace:\n%s", f.args())
	}
	for label, output := range map[string]string{"stdout": f.out.String(), "stderr": f.errOut.String(), "arguments": f.args()} {
		if strings.Contains(output, token) || strings.Contains(output, encoded) {
			t.Errorf("token leaked through %s:\n%s", label, output)
		}
	}
}

func TestCredentialIssueChecksSecretBindingBeforePost(t *testing.T) {
	t.Setenv("KMX_TEST_BOUND", "other-agent")
	posted := false
	f := newCredentialFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.WriteHeader(http.StatusCreated)
	})
	err := f.app.IssueCredentialToSecret("batch-agent", config.GovernedSecret, config.DefaultNamespace, nil)
	if err == nil || !strings.Contains(err.Error(), `not "batch-agent"`) {
		t.Fatalf("wrong binding error: %v", err)
	}
	if posted {
		t.Fatal("credential was issued before the conflicting Secret binding was checked")
	}
}

func TestCredentialIssueRefusesUnboundExistingSecretBeforePost(t *testing.T) {
	t.Setenv("KMX_TEST_SECRET_EXISTS", "true")
	posted := false
	f := newCredentialFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.WriteHeader(http.StatusCreated)
	})
	err := f.app.IssueCredentialToSecret("batch-agent", config.GovernedSecret, config.DefaultNamespace, nil)
	if err == nil || !strings.Contains(err.Error(), "without a kaimahi.dev/credential binding") {
		t.Fatalf("wrong unbound Secret error: %v", err)
	}
	if posted {
		t.Fatal("credential was issued before the existing Secret identity was validated")
	}
}

func TestCredentialIssueReconcilesConflictOnlyWithMatchingSecret(t *testing.T) {
	t.Setenv("KMX_TEST_BOUND", "batch-agent")
	f := newCredentialFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"credential exists"}`))
	})
	if err := f.app.IssueCredentialToSecret("batch-agent", config.GovernedSecret, config.DefaultNamespace, nil); err != nil {
		t.Fatalf("reconcile matching credential: %v", err)
	}
	if !strings.Contains(f.errOut.String(), "keeping both") {
		t.Errorf("matching identity was not reported as reconciled:\n%s", f.errOut.String())
	}
	if strings.Contains(f.piped(), "kind: Secret") {
		t.Errorf("409 reconciliation unexpectedly rotated the Secret:\n%s", f.piped())
	}
}

func TestCredentialIssueNeverPrintsUnexpectedResponseBody(t *testing.T) {
	token := "kmh_" + strings.Repeat("f", 64)
	f := newCredentialFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`{"token":"` + token + `"}`))
	})
	err := f.app.IssueCredentialToSecret("batch-agent", config.GovernedSecret, config.DefaultNamespace, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("unexpected issue error: %v", err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(f.errOut.String(), token) || strings.Contains(f.out.String(), token) {
		t.Fatal("unexpected credential response body leaked its token")
	}
}

// A credential name is interpolated into JSON and a query string. The
// The command's name check is kept because the plane validating again is a second
// line, not the first.
func TestCredentialNamesAreValidated(t *testing.T) {
	for _, bad := range []string{"", "Hello", "hello world", "hello/../x", `hello"`, "hello.world"} {
		if err := validCredentialName(bad); err == nil {
			t.Errorf("credential name %q was accepted", bad)
		}
	}
	for _, good := range []string{"hello-world", "kaimahi-plane", "a1"} {
		if err := validCredentialName(good); err != nil {
			t.Errorf("credential name %q was refused: %v", good, err)
		}
	}
}

// An already-issued credential whose Secret is bound elsewhere must refuse.
// Reusing that Secret would hand one credential's token to another
// credential's agent, and the ledger would attribute the spend to the wrong
// one — silently, because both tokens are opaque.
func TestAnAlreadyIssuedCredentialIsReconciledNotOverwritten(t *testing.T) {
	conflict := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"credential exists"}`))
	}
	f := newCredentialFixture(t, "", conflict)
	// The fake kubectl answers the annotation read with an empty string,
	// which is the "exists in the plane, Secret missing or unlabeled" case:
	// the token cannot be recovered, so the operator is told exactly how to
	// clear the row rather than being handed a half-governed agent.
	err := f.app.IssueCredentialToSecret("hello-world", config.GovernedSecret, config.DefaultNamespace, nil)
	if err == nil {
		t.Fatal("a 409 with no bound Secret was accepted")
	}
	if !strings.Contains(err.Error(), "shown exactly once") ||
		!strings.Contains(err.Error(), "DELETE FROM credential") {
		t.Errorf("the refusal does not tell the operator how to recover: %v", err)
	}
}

// Two credentials, one Secret: the second must be refused BEFORE it is
// issued.
//
// Issuing `demo` while kaimahi-governed-token holds hello-world's token
// would otherwise mint demo's credential, overwrite the Secret, and destroy
// the only copy of hello-world's token — the plane keeps only its hash, so
// hello-world would stay live and permanently unusable. Refusing before the
// POST also means no orphan credential row is left behind.
func TestASecondCredentialWillNotOverwriteAnotherOnesToken(t *testing.T) {
	t.Setenv("KMX_TEST_BOUND", "hello-world")
	issuedAnyway := false
	f := newCredentialFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		issuedAnyway = true
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": "kmh_" + strings.Repeat("d", 64)})
	})
	err := f.app.IssueCredentialToSecret("demo", config.GovernedSecret, config.DefaultNamespace, nil)
	if err == nil {
		t.Fatal("issuing demo overwrote the Secret holding hello-world's token")
	}
	if !strings.Contains(err.Error(), `not "demo"`) || !strings.Contains(err.Error(), "--secret") {
		t.Errorf("the refusal does not name the conflict and the way out: %v", err)
	}
	if issuedAnyway {
		t.Error("the credential was minted before the conflict was detected, leaving an orphan row")
	}
	if strings.Contains(f.piped(), "kind: Secret") {
		t.Errorf("a Secret was applied anyway:\n%s", f.piped())
	}
}

// The same Secret, the same credential, re-run: that is idempotence, not a
// conflict.
func TestIssuingTheSameCredentialAgainIsFine(t *testing.T) {
	t.Setenv("KMX_TEST_BOUND", "hello-world")
	f := newCredentialFixture(t, "",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"credential exists"}`))
		})
	if err := f.app.IssueCredentialToSecret("hello-world", config.GovernedSecret, config.DefaultNamespace, nil); err != nil {
		t.Fatalf("re-issuing the same credential failed: %v", err)
	}
	if !strings.Contains(f.errOut.String(), "keeping both") {
		t.Errorf("the already-issued case was not reconciled:\n%s", f.errOut.String())
	}
}

// seamTLSSecret is the plane's seam-certificate Secret as kubectl prints it.
//
// `kmx migrate` reads it to republish the authority into a workload's
// namespace before it points anything at a seam, so a fixture without one
// stands in for a plane that has not been deployed.
func seamTLSSecret(t *testing.T) string {
	t.Helper()
	authority, err := seamcert.MintAuthority(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	serving, err := authority.Sign(seamcert.SeamNames(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"data": map[string]string{
		"tls.crt": base64.StdEncoding.EncodeToString(serving.CertPEM),
		"tls.key": base64.StdEncoding.EncodeToString(serving.KeyPEM),
		"ca.crt":  base64.StdEncoding.EncodeToString(authority.CertPEM),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// Pointing a workload at a seam it cannot verify is not a partial success. A
// plane with no certificate has to stop the whole operation, and say which
// command produces one — the alternative is a workload switched onto a
// governed route whose Secret is absent, which reads as a broken seam rather
// than as a missing step.
func TestMigrateRefusesWhenThePlaneHasNoSeamCertificate(t *testing.T) {
	f := newCredentialFixture(t, "", issued("kmh_"+strings.Repeat("a", 64)))
	t.Setenv("KMX_TEST_SEAM_TLS", `{"data":{}}`)
	err := f.app.publishPlaneAuthority(config.DefaultNamespace)
	if err == nil {
		t.Fatal("an authority was published from a plane with no seam certificate")
	}
	if !strings.Contains(err.Error(), "no ca.crt") && !strings.Contains(err.Error(), "carries no ca.crt") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}
	if !strings.Contains(err.Error(), "kmx plane") {
		t.Errorf("the refusal does not name the command that fixes it: %v", err)
	}
}
