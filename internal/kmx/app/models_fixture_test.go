package app

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// Shared model-onboarding and migration fixture. Reads distinguish absence
// from failure and model a version change between generation and apply.
const fakeAddKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"apply -f -"*|*"apply --dry-run=server -f -"*)
    [ -n "$KMX_TEST_STDIN" ] && cat >> "$KMX_TEST_STDIN"
    exit 0 ;;
  *"config view"*)
    cat <<'JSON'
{"clusters":[{"name":"kind-kaimahi-p1","cluster":{"server":"https://127.0.0.1:6443"}}],
 "contexts":[{"name":"kind-kaimahi-p1","context":{"cluster":"kind-kaimahi-p1"}}]}
JSON
    exit 0 ;;
  *"get secret kaimahi-admin"*) printf '%s' "$KMX_TEST_ADMIN_B64"; exit 0 ;;
  *"get secret kaimahi-plane-seam-tls"*) printf '%s' "$KMX_TEST_SEAM_TLS"; exit 0 ;;
  *port-forward*)
    printf 'Forwarding from 127.0.0.1:%s -> 9091\n' "$KMX_TEST_ADMIN_PORT"
    exec sleep 30 ;;
  *"get pods"*) printf '%s' "$KMX_TEST_PODS"; exit 0 ;;
  *"get service"*)
    if [ "$KMX_TEST_SVC" = "notfound" ]; then
      printf 'Error from server (NotFound): services "acme-warehouse" not found\n' >&2
      exit 1
    fi
    printf '%s' "$KMX_TEST_SVC"; exit 0 ;;
  *"get configmap kaimahi-upstreams-extra"*)
    case "$KMX_TEST_OVERLAY" in
      notfound) printf 'Error from server (NotFound): configmaps "kaimahi-upstreams-extra" not found\n' >&2; exit 1 ;;
      boom) printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
      *)
        if [ "$KMX_TEST_RV" = "none" ]; then
          printf '{"metadata":{},"data":%s}' "$KMX_TEST_OVERLAY"
          exit 0
        fi
        rv="$KMX_TEST_RV"
        if [ -n "$KMX_TEST_RV_SECOND" ] && [ -f "$KMX_TEST_ARGS.overlay-read" ]; then
          rv="$KMX_TEST_RV_SECOND"
        fi
        : >> "$KMX_TEST_ARGS.overlay-read"
        printf '{"metadata":{"resourceVersion":"%s"},"data":%s}' "$rv" "$KMX_TEST_OVERLAY"
        exit 0 ;;
    esac ;;
esac
exit 0
`

type addFixture struct {
	app     *App
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	dir     string
	argsLog string
}

func newAddFixture(t *testing.T, svc, overlay string, validate http.HandlerFunc) *addFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeAddKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	if validate == nil {
		validate = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true,"upstreams":["ollama","house"]}`))
		}
	}
	srv := httptest.NewServer(planePreamble(admin.Speaks, validate))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := &addFixture{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dir: dir, argsLog: filepath.Join(dir, "args")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", f.argsLog)
	t.Setenv("KMX_TEST_SVC", svc)
	if os.Getenv("KMX_TEST_PODS") == "" {
		t.Setenv("KMX_TEST_PODS", "acme-warehouse-5f7c")
	}
	t.Setenv("KMX_TEST_OVERLAY", overlay)
	if os.Getenv("KMX_TEST_RV") != "none" {
		t.Setenv("KMX_TEST_RV", "4711")
	}
	t.Setenv("KMX_TEST_ADMIN_B64", base64.StdEncoding.EncodeToString([]byte("admin-bearer")))
	t.Setenv("KMX_TEST_ADMIN_PORT", u.Port())
	r := run.Default()
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{Cfg: &config.Config{KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx, AdminPort: u.Port()}, Run: r, Out: f.out, Err: f.errOut}
	return f
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}
