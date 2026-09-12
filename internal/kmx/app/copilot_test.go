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
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

const (
	testCopilotOAuth = "oauth_" + "not-a-real-token"
	testCopilotToken = "copilot_" + "short-lived-not-real"
)

func TestTheRenderedSecretIsWellFormedAndBase64(t *testing.T) {
	body := credentialSecretManifest("kaimahi-copilot-token", "kaimahi", "api-key", []byte("a value"))
	want := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: kaimahi-copilot-token\n  namespace: kaimahi\ntype: Opaque\ndata:\n  api-key: YSB2YWx1ZQ==\n"
	if string(body) != want {
		t.Fatalf("rendered: %s", body)
	}
	body = credentialSecretManifest("n", "ns", "k", []byte("line\n\"quoted\": value"))
	if strings.Contains(string(body), "quoted") {
		t.Fatalf("value was not encoded: %s", body)
	}
}

func TestTheBuffersAreCleared(t *testing.T) {
	b := []byte(testCopilotToken)
	zeroBytes(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("byte %d survived: %q", i, c)
		}
	}
	if got := string(trimSpaceBytes([]byte("  " + testCopilotToken + " \r\n"))); got != testCopilotToken {
		t.Fatalf("token was not trimmed: %q", got)
	}
}

func TestCopilotCredentialUsesDeviceLoginAndStoresOnlyTheExchange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	argsFile, stdinFile := filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	kubectl := `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"get deploy/kaimahi-proxy"*) echo 'Error from server (NotFound): deployments.apps "kaimahi-proxy" not found' >&2; exit 1 ;;
  *"apply -f -"*) cat >> "$KMX_TEST_STDIN" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(kubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", argsFile)
	t.Setenv("KMX_TEST_STDIN", stdinFile)

	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "device-secret", "user_code": "ABCD-EFGH", "verification_uri": "https://github.example/device", "interval": 1})
		case "/access":
			polls++
			if r.FormValue("device_code") != "device-secret" || r.FormValue("client_id") != copilotClientID {
				t.Errorf("device poll omitted its form values")
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": testCopilotOAuth})
		case "/exchange":
			if got := r.Header.Get("Authorization"); got != "token "+testCopilotOAuth {
				t.Errorf("exchange authorization=%q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": testCopilotToken})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out, errOut bytes.Buffer
	r := run.Default()
	r.Stdout, r.Stderr = &out, &errOut
	stdinPath := filepath.Join(dir, "unread-stdin")
	if err := os.WriteFile(stdinPath, []byte("must remain unread"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	cache := filepath.Join(dir, "config", "copilot-oauth-token")
	a := &App{
		Cfg: &config.Config{KubeContext: "kind-kaimahi-p1"}, Run: r, Out: &out, Err: &errOut, Stdin: stdin, guarded: true,
		copilotEnv: &copilotEnvironment{deviceURL: srv.URL + "/device", accessURL: srv.URL + "/access", exchangeURL: srv.URL + "/exchange", tokenFile: cache, client: srv.Client(), sleep: func(time.Duration) {}},
	}
	if err := a.CaptureCopilotCredential(); err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	if polls != 1 {
		t.Fatalf("device polls=%d, want 1", polls)
	}
	if at, err := stdin.Seek(0, 1); err != nil || at != 0 {
		t.Fatalf("stdin was consumed: offset=%d err=%v", at, err)
	}
	info, err := os.Stat(cache)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("OAuth cache mode=%v, want 0600", info.Mode().Perm())
	}
	cached, _ := os.ReadFile(cache)
	if string(cached) != testCopilotOAuth {
		t.Fatalf("OAuth cache does not contain the device-login token")
	}
	manifest, _ := os.ReadFile(stdinFile)
	text := string(manifest)
	if !strings.Contains(text, "name: "+copilotSecretName) || !strings.Contains(text, base64.StdEncoding.EncodeToString([]byte(testCopilotToken))) {
		t.Fatalf("plane Secret was not stored:\n%s", text)
	}
	if strings.Contains(text, testCopilotOAuth) || strings.Contains(text, testCopilotToken) {
		t.Fatal("credential was written in plaintext to kubectl stdin")
	}
	args, _ := os.ReadFile(argsFile)
	visible := string(args) + out.String() + errOut.String()
	if strings.Contains(visible, testCopilotOAuth) || strings.Contains(visible, testCopilotToken) {
		t.Fatal("credential reached argv or output")
	}
	if !strings.Contains(text, "kaimahi-proxy-egress-copilot") {
		t.Fatal("Copilot egress was not applied")
	}
}

func TestCopilotExchangeRefusesRedirects(t *testing.T) {
	targetReached := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetReached = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	env, err := defaultCopilotEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	env.exchangeURL = redirect.URL
	if _, err := copilotExchange(env, []byte(testCopilotOAuth)); err == nil {
		t.Fatal("credential-bearing redirect was followed")
	}
	if targetReached {
		t.Fatal("redirect target received the credential-bearing request")
	}
}
