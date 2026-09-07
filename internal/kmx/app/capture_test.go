package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/seam"
)

// The one path on which kmx accepts credential material, tested for what does
// NOT happen: the value is never in an argument list, never in a file, never
// echoed, and it is not read at all unless a terminal is on the other end.
//
// Nothing here is a real credential and nothing reaches a real upstream. The
// token shape is assembled rather than written so a fixture can never be
// mistaken for a leak, and the upstream is a local server.

const captureToken = "github" + "_pat_" + "AAAAAAAAAAAAAAAAAAAAAA"

// A kubectl that records every argument list and every byte of stdin it was
// given. Both are what the test then searches for the credential.
const fakeCaptureKubectl = `#!/bin/sh
{ printf '%s\n' "$*"; } >> "$KMX_TEST_ARGS"
case "$*" in
  *"get secret"*)
    if [ -f "$KMX_TEST_SECRET_EXISTS" ]; then cat "$KMX_TEST_SECRET_EXISTS"; exit 0; fi
    echo 'Error from server (NotFound): secrets "x" not found' >&2; exit 1 ;;
  *"apply -f -"*)
    cat >> "$KMX_TEST_STDIN"; exit 0 ;;
  *"get deploy/kaimahi-proxy"*)
    [ -n "$KMX_TEST_NO_PROXY" ] && exit 1
    echo 'deployment.apps/kaimahi-proxy'; exit 0 ;;
esac
exit 0
`

type captureFixture struct {
	app      *App
	out      *bytes.Buffer
	errOut   *bytes.Buffer
	dir      string
	args     string
	stdin    string
	upstream *httptest.Server
}

func newCaptureFixture(t *testing.T, handler http.HandlerFunc) *captureFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeCaptureKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Github-Authentication-Token-Expiration", "2026-12-01 09:00:00 +0000")
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "owner/name"})
		}
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	f := &captureFixture{
		out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dir: dir,
		args: filepath.Join(dir, "args"), stdin: filepath.Join(dir, "stdin"),
		upstream: srv,
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", f.args)
	t.Setenv("KMX_TEST_STDIN", f.stdin)
	t.Setenv("KMX_TEST_SECRET_EXISTS", filepath.Join(dir, "absent"))

	r := run.Default()
	r.Stdout, r.Stderr = f.out, f.errOut
	env := seam.DefaultEnv()
	env.GitHubAPI, env.ADOBase = srv.URL, srv.URL
	f.app = &App{
		Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
			ContextSource: config.SourceKubeCtx},
		Run: r, Out: f.out, Err: f.errOut,
		// A test supplies the value the terminal would have supplied. It is
		// the ONLY way to reach this path without a TTY, and it is
		// unexported: no flag, environment variable or file can set it.
		readSecret: func() ([]byte, error) { return []byte(captureToken), nil },
		seamEnv:    &env,
		guarded:    true,
	}
	return f
}

func (f *captureFixture) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// Everything the operator, the machine and the cluster can see afterwards,
// searched for the credential itself.
func (f *captureFixture) assertTokenIsNowhereButTheSecret(t *testing.T) {
	t.Helper()
	// The process table: kubectl's arguments, every invocation.
	if args := f.read(t, f.args); strings.Contains(args, captureToken) {
		t.Errorf("the credential reached a command line:\n%s", args)
	}
	// kmx's own output, both streams. The commands kmx echoes go to stderr.
	if strings.Contains(f.out.String(), captureToken) || strings.Contains(f.errOut.String(), captureToken) {
		t.Errorf("the credential was printed:\nstdout: %s\nstderr: %s", f.out, f.errOut)
	}
	// Anything left on disk. The scripts this replaces had to write a 0600
	// file, because `kubectl create secret --from-file` needs a path.
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "stdin" || e.IsDir() {
			continue
		}
		if strings.Contains(f.read(t, filepath.Join(f.dir, e.Name())), captureToken) {
			t.Errorf("the credential was left in a file: %s", e.Name())
		}
	}
	// And where it IS: base64 in the Secret document, on kubectl's stdin.
	body := f.read(t, f.stdin)
	if !strings.Contains(body, base64.StdEncoding.EncodeToString([]byte(captureToken))) {
		t.Errorf("the credential did not reach the Secret:\n%s", body)
	}
	// Not in plaintext even there.
	if strings.Contains(body, captureToken) {
		t.Errorf("the credential was written in plaintext into the manifest:\n%s", body)
	}
}

func TestACapturedCredentialGoesToTheSecretAndNowhereElse(t *testing.T) {
	f := newCaptureFixture(t, nil)
	if err := f.app.CaptureCredential(CaptureOptions{Seam: "github-release", Subject: "owner/name"}); err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	f.assertTokenIsNowhereButTheSecret(t)

	body := f.read(t, f.stdin)
	if !strings.Contains(body, "name: kaimahi-release-pat") || !strings.Contains(body, "namespace: kaimahi") {
		t.Errorf("the Secret is not the one the gateway reads:\n%s", body)
	}
	// The operator is told what was established and what was not; the
	// unproven half is not optional, because a silence reads as a guarantee.
	stderr := f.errOut.String()
	for _, want := range []string{"PROVEN:", "DEADLINE:", "NOT PROVEN:", "Secret kaimahi/kaimahi-release-pat stored."} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the report is missing %q:\n%s", want, stderr)
		}
	}
	// Capturing a credential for an upstream on the internet is also the
	// moment the gateway needs its way out, and the moment the proxy has to
	// re-read an optional mount.
	args := f.read(t, f.args)
	if !strings.Contains(args, "rollout restart deploy/kaimahi-proxy") {
		t.Errorf("the proxy was not rolled, so it keeps serving without the credential:\n%s", args)
	}
	if !strings.Contains(f.read(t, f.stdin), "kaimahi-proxy-egress-hosted") {
		t.Error("the hosted-egress allowance was not applied, so the credential cannot be used")
	}
}

// A refusal by the upstream stores nothing. This is the whole reason the
// prompt is not simply a prompt: a capture that stored an unvetted value
// would be faster than the make target at getting a broken credential into a
// cluster.
func TestARefusedCredentialIsNotStored(t *testing.T) {
	f := newCaptureFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	err := f.app.CaptureCredential(CaptureOptions{Seam: "github", Subject: "owner/name"})
	if err == nil {
		t.Fatal("a token the upstream refused was accepted")
	}
	if !strings.Contains(err.Error(), "REFUSING") {
		t.Errorf("the refusal does not say so: %v", err)
	}
	if body := f.read(t, f.stdin); strings.Contains(body, "kind: Secret") {
		t.Errorf("a Secret was written anyway:\n%s", body)
	}
	f.assertTokenIsNowhereButTheSecretIsAbsent(t)
}

func (f *captureFixture) assertTokenIsNowhereButTheSecretIsAbsent(t *testing.T) {
	t.Helper()
	if args := f.read(t, f.args); strings.Contains(args, captureToken) {
		t.Errorf("the credential reached a command line:\n%s", args)
	}
	if strings.Contains(f.errOut.String()+f.out.String(), captureToken) {
		t.Error("the credential was printed")
	}
	if strings.Contains(f.read(t, f.stdin), captureToken) {
		t.Error("the credential was piped to kubectl despite the refusal")
	}
}

// Terminal only. There is no flag, environment variable or file that takes
// the value, and a stdin that is not a terminal is refused rather than read —
// before any cluster is touched.
func TestAPipedOrRedirectedStdinIsRefusedBeforeAnythingHappens(t *testing.T) {
	f := newCaptureFixture(t, nil)
	// A regular file is exactly what a shell redirect (or a pipe) hands to a
	// process, and it is not a terminal.
	path := filepath.Join(f.dir, "piped")
	if err := os.WriteFile(path, []byte(captureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	piped, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer piped.Close()

	f.app.readSecret = nil // the real path: a terminal or nothing
	f.app.Stdin = piped
	f.app.guarded = false
	err = f.app.CaptureCredential(CaptureOptions{Seam: "github-release", Subject: "owner/name"})
	if err == nil {
		t.Fatal("a redirected stdin was accepted")
	}
	if !strings.Contains(err.Error(), "from a terminal") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	// Nothing was read, and nothing was reached: not the cluster, not the
	// upstream. A refusal that had already asked the API server would have
	// left a trail here.
	if args := f.read(t, f.args); args != "" {
		t.Errorf("the cluster was touched before the terminal check:\n%s", args)
	}
	if strings.Contains(f.errOut.String(), captureToken) {
		t.Error("the credential in the redirected file was read and printed")
	}
}

// An upstream nobody has heard of, and a subject that is not one, are both
// refused before a credential is read: a typo costs a retype, not a token.
func TestAnUnknownUpstreamOrSubjectIsRefusedBeforeReading(t *testing.T) {
	for _, tc := range []struct{ seam, subject, says string }{
		{"gitlab", "owner/name", "no upstream named"},
		{"github-release", "owner", "not a repository"},
		{"ado", "an organization", "not a organization"},
	} {
		f := newCaptureFixture(t, nil)
		read := false
		f.app.readSecret = func() ([]byte, error) { read = true; return []byte(captureToken), nil }
		err := f.app.CaptureCredential(CaptureOptions{Seam: tc.seam, Subject: tc.subject})
		if err == nil {
			t.Fatalf("%s %q was accepted", tc.seam, tc.subject)
		}
		if !strings.Contains(err.Error(), tc.says) {
			t.Errorf("the refusal does not say which check failed: %v", err)
		}
		if read {
			t.Errorf("%s %q asked for a credential before checking itself", tc.seam, tc.subject)
		}
	}
}

// A credential that is already stored is not overwritten by accident. The
// destructive direction is the one that has to be said out loud.
func TestAStoredCredentialIsNotReplacedSilently(t *testing.T) {
	f := newCaptureFixture(t, nil)
	existing := filepath.Join(f.dir, "existing")
	if err := os.WriteFile(existing, []byte("2026-09-01T10:00:00Z"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_TEST_SECRET_EXISTS", existing)

	read := false
	f.app.readSecret = func() ([]byte, error) { read = true; return []byte(captureToken), nil }
	err := f.app.CaptureCredential(CaptureOptions{Seam: "ado", Subject: "an-organization"})
	if err == nil {
		t.Fatal("an existing credential was replaced without being asked about")
	}
	if !strings.Contains(err.Error(), "--replace") || !strings.Contains(err.Error(), "2026-09-01") {
		t.Errorf("the refusal does not say what is there or how to replace it: %v", err)
	}
	if read {
		t.Error("a credential was read before finding out one was already stored")
	}

	// And with --replace it goes through, saying what it is about to do.
	f2 := newCaptureFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	})
	t.Setenv("KMX_TEST_SECRET_EXISTS", existing)
	f2.app.readSecret = func() ([]byte, error) { return adoTestToken(t), nil }
	if err := f2.app.CaptureCredential(CaptureOptions{Seam: "ado", Subject: "an-organization", Replace: true}); err != nil {
		t.Fatalf("--replace was refused: %v", err)
	}
	if !strings.Contains(f2.errOut.String(), "will be overwritten") {
		t.Errorf("the overwrite was not announced:\n%s", f2.errOut)
	}
	if !strings.Contains(f2.read(t, f2.stdin), "name: kaimahi-ado-token") {
		t.Error("the replacement was not written")
	}
}

// A read of the cluster that FAILS is not a "no". An unreachable API server
// or an RBAC denial read as "there is nothing there" would overwrite a
// working credential while reporting a first capture.
func TestAnUnreadableClusterIsNotAnAbsentCredential(t *testing.T) {
	f := newCaptureFixture(t, nil)
	// A kubectl that fails for a reason that is not NotFound.
	if err := os.WriteFile(filepath.Join(f.dir, "kubectl"),
		[]byte("#!/bin/sh\necho 'error: You must be logged in to the server (Unauthorized)' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	read := false
	f.app.readSecret = func() ([]byte, error) { read = true; return []byte(captureToken), nil }
	err := f.app.CaptureCredential(CaptureOptions{Seam: "github", Subject: "owner/name"})
	if err == nil {
		t.Fatal("an unreadable cluster was treated as an empty one")
	}
	if !strings.Contains(err.Error(), "cannot tell whether") {
		t.Errorf("the refusal does not say what is unknown: %v", err)
	}
	if read {
		t.Error("a credential was read despite not knowing what is in the cluster")
	}
}

// A capture on a cluster with no plane yet stores the credential and says
// plainly that there is nothing to restart, rather than failing on a rollout
// of a Deployment that does not exist.
func TestACaptureBeforeThePlaneIsDeployedSaysSo(t *testing.T) {
	f := newCaptureFixture(t, nil)
	t.Setenv("KMX_TEST_NO_PROXY", "1")
	if err := f.app.CaptureCredential(CaptureOptions{Seam: "github", Subject: "owner/name"}); err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	if !strings.Contains(f.errOut.String(), "not deployed here yet") {
		t.Errorf("the missing plane was not mentioned:\n%s", f.errOut)
	}
	if strings.Contains(f.read(t, f.args), "rollout restart") {
		t.Error("a Deployment that does not exist was restarted")
	}
}

func adoTestToken(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"aud": "https://mcp.dev.azure.com",
		"exp": 4102444800, // 2100-01-01, so the fixture never goes stale
	})
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return []byte(header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln")
}

// The Secret document is rendered here rather than by `kubectl create secret`,
// which is what keeps the value out of the process table. It has to be a
// document the API server accepts.
func TestTheRenderedSecretIsWellFormedAndBase64(t *testing.T) {
	body := credentialSecretManifest("kaimahi-release-pat", "kaimahi", "token", []byte("a value"))
	want := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: kaimahi-release-pat\n" +
		"  namespace: kaimahi\ntype: Opaque\ndata:\n  token: " +
		base64.StdEncoding.EncodeToString([]byte("a value")) + "\n"
	if string(body) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", body, want)
	}
	// A value with a newline or a quote in it cannot break out of the
	// document, because it is never in the document.
	body = credentialSecretManifest("n", "ns", "k", []byte("line\n\"quoted\": value"))
	if strings.Contains(string(body), "quoted") {
		t.Errorf("the value was not encoded:\n%s", body)
	}
}

// The buffers kmx owns are cleared once the write is done. Not a guarantee —
// the runtime may have copied a slice while growing it — but the copy this
// code is responsible for does not outlive the command.
func TestTheBuffersAreCleared(t *testing.T) {
	b := []byte(captureToken)
	zeroBytes(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("byte %d survived: %q", i, c)
		}
	}
	if got := string(trimSpaceBytes([]byte("  " + captureToken + " \r\n"))); got != captureToken {
		t.Errorf("a pasted value was not trimmed: %q", got)
	}
}
