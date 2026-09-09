package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
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
    if [ -n "$KMX_TEST_NO_PROXY" ]; then
      echo 'Error from server (NotFound): deployments.apps "kaimahi-proxy" not found' >&2; exit 1
    fi
    if [ -n "$KMX_TEST_PROXY_ERR" ]; then printf '%s\n' "$KMX_TEST_PROXY_ERR" >&2; exit 1; fi
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
	// Every place a stray copy of the credential could land is pointed at the
	// one directory the assertions read. Without this the "nothing on disk"
	// scan looks only at the fixture's own scratch directory, which the
	// capture path has no reason to write to — so it would pass however
	// freely kmx spilled a token into a temporary file.
	t.Setenv("TMPDIR", dir)
	t.Setenv("KMX_HOME", dir)
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
	// file, because `kubectl create secret --from-file` needs a path. The
	// fixture points TMPDIR and KMX_HOME here, so this is where such a file
	// would land; if it stops doing so, this scan stops meaning anything.
	if os.TempDir() != f.dir || os.Getenv("KMX_HOME") != f.dir {
		t.Fatalf("temporary files no longer land in the scanned directory (TMPDIR=%s KMX_HOME=%s, scanning %s)", os.TempDir(), os.Getenv("KMX_HOME"), f.dir)
	}
	found := 0
	if err := filepath.WalkDir(f.dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || path == f.stdin {
			return err
		}
		found++
		if strings.Contains(f.read(t, path), captureToken) {
			rel, _ := filepath.Rel(f.dir, path)
			t.Errorf("the credential was left in a file: %s", rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("no files were scanned at all, so nothing on disk was actually checked")
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
// the value, and a session that is not interactive is refused rather than
// read — before any cluster is touched.
//
// What can be established without a real terminal is the refusal and its
// cost: nothing read, nothing asked, nothing stored. Which half of the fence
// stops a given session cannot be told apart here, because a process running
// under `go test` has no terminal on either stream; see the note on the
// stdin-not-a-terminal clause below.
func TestANonInteractiveSessionIsRefusedBeforeAnythingHappens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin func(t *testing.T, dir string) *os.File
	}{
		// A regular file is exactly what a shell redirect (or a pipe) hands
		// to a process, and it is not a terminal.
		{"a redirected stdin", func(t *testing.T, dir string) *os.File {
			path := filepath.Join(dir, "piped")
			if err := os.WriteFile(path, []byte(captureToken+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			piped, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { piped.Close() })
			return piped
		}},
		// A closed stdin: the shape a daemon or a detached CI step has.
		{"no stdin at all", func(*testing.T, string) *os.File { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCaptureFixture(t, nil)
			f.app.readSecret = nil // the real path: a terminal or nothing
			f.app.Stdin = tc.stdin(t, f.dir)
			f.app.guarded = false

			err := f.app.CaptureCredential(CaptureOptions{Seam: "github-release", Subject: "owner/name"})
			if err == nil {
				t.Fatal("a non-interactive session was accepted")
			}
			if !strings.Contains(err.Error(), "from a terminal") {
				t.Errorf("the refusal does not say what is wrong: %v", err)
			}
			// Nothing was read, and nothing was reached: not the cluster, not
			// the upstream. A refusal that had already asked the API server
			// would have left a trail here.
			if args := f.read(t, f.args); args != "" {
				t.Errorf("the cluster was touched before the terminal check:\n%s", args)
			}
			if body := f.read(t, f.stdin); body != "" {
				t.Errorf("a Secret was written despite the refusal:\n%s", body)
			}
			if strings.Contains(f.errOut.String()+f.out.String(), captureToken) {
				t.Error("the credential on the refused stream was read and printed")
			}
		})
	}

	// The redirected-stdin case above also proves that a token sitting on a
	// redirected stdin is left unconsumed, which is a stronger statement than
	// "an error came back".
	f := newCaptureFixture(t, nil)
	path := filepath.Join(f.dir, "unread")
	if err := os.WriteFile(path, []byte(captureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	piped, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer piped.Close()
	f.app.readSecret, f.app.Stdin, f.app.guarded = nil, piped, false
	if err := f.app.CaptureCredential(CaptureOptions{Seam: "github-release", Subject: "owner/name"}); err == nil {
		t.Fatal("a redirected stdin was accepted")
	}
	at, err := piped.Seek(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if at != 0 {
		t.Errorf("the refused stdin was read anyway: the offset moved to %d", at)
	}
}

// The fence refuses when EITHER stream is not a terminal, and Go stops at the
// first clause that is true. Under `go test` neither stream is a terminal, so
// every case above is refused by the stderr clause and the
// stdin-is-not-a-terminal clause is never reached. Telling the two apart needs
// a real terminal on stderr, which means allocating a pty — Linux-only code
// behind a build tag, since the ioctls that open one do not exist on macOS,
// where contributors are told to run these tests. That is recorded here rather
// than papered over with an assertion that cannot distinguish them.

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

func TestCaptureDoesNotReportAnUnreadableDeploymentAsAbsent(t *testing.T) {
	for _, failure := range []string{
		"Error from server (Forbidden): deployments.apps is forbidden",
		"error: You must be logged in to the server (Unauthorized)",
		"Unable to connect to the server: connection refused",
		"error: context was not found",
		`error: context "remote" not found`,
		"exec credential plugin failed: NotFound",
	} {
		t.Run(failure, func(t *testing.T) {
			f := newCaptureFixture(t, nil)
			t.Setenv("KMX_TEST_PROXY_ERR", failure)
			err := f.app.CaptureCredential(CaptureOptions{Seam: "github", Subject: "owner/name"})
			if err == nil || !strings.Contains(err.Error(), "credential is stored") || !strings.Contains(err.Error(), failure) {
				t.Fatalf("unexpected capture result: %v", err)
			}
			if strings.Contains(f.errOut.String(), "not deployed here yet") || strings.Contains(f.read(t, f.args), "rollout restart") {
				t.Fatalf("failed deployment read was treated as absence: %s", f.errOut)
			}
		})
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
