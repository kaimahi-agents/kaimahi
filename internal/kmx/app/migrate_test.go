package app

// `kmx migrate` against a fake cluster.
//
// The fake kubectl below is its own rather than a generalisation of the
// others in this package, and deliberately: each of these fakes answers
// the exact reads one command makes, and a shared one drifts into
// answering reads nobody asked for.

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

const fakeMigrateKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"apply -f -"*)
    [ -n "$KMX_TEST_STDIN" ] && cat >> "$KMX_TEST_STDIN"
    exit 0 ;;
  *"config view"*)
    cat <<'JSON'
{"clusters":[{"name":"kind-kaimahi-p1","cluster":{"server":"https://127.0.0.1:6443"}}],
 "contexts":[{"name":"kind-kaimahi-p1","context":{"cluster":"kind-kaimahi-p1"}}]}
JSON
    exit 0 ;;
  *"get namespace"*) printf 'namespace/x\n'; exit 0 ;;
  *"get deployment"*) printf '%s' "$KMX_TEST_DEPLOYMENT"; exit 0 ;;
  *"get configmap override-config"*) printf '%s' "$KMX_TEST_CONFIGMAP2"; exit 0 ;;
  *"get configmap"*) printf '%s' "$KMX_TEST_CONFIGMAP"; exit 0 ;;
  *"get provider"*)
    case "$KMX_TEST_PROVIDER" in
      notfound) printf 'Error from server (NotFound): providers.core.orka.ai "local" not found\n' >&2; exit 1 ;;
      *) printf '%s' "$KMX_TEST_PROVIDER"; exit 0 ;;
    esac ;;
  *"create token"*)
    printf '{"status":{"token":"%s","expirationTimestamp":"2026-10-09T00:00:00Z"}}' "$KMX_TEST_SA_TOKEN"
    exit 0 ;;
  *"get secret kaimahi-admin"*) printf '%s' "$KMX_TEST_ADMIN_B64"; exit 0 ;;
  *"get secret kaimahi-plane-seam-tls"*) printf '%s' "$KMX_TEST_SEAM_TLS"; exit 0 ;;
  *"get secret"*)
    printf 'Error from server (NotFound): secrets "kaimahi-concierge-token" not found\n' >&2; exit 1 ;;
  *port-forward*)
    printf 'Forwarding from 127.0.0.1:%s -> 9091\n' "$KMX_TEST_ADMIN_PORT"
    exec sleep 30 ;;
esac
exit 0
`

// One container, reading its model configuration the way a Helm chart
// usually arranges it: envFrom a ConfigMap.
const concierge = `{"spec":{"template":{"spec":{"containers":[
  {"name":"concierge","envFrom":[{"configMapRef":{"name":"app-config"}}]}]}}}}`

const conciergeConfig = `{"OPENAI_BASE_URL":"http://orka-api.orka-system:8080/openai/v1",
  "OPENAI_CHAT_MODEL":"local/qwen2.5:3b"}`

type migrateFixture struct {
	app     *App
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	dir     string
	argsLog string
}

func newMigrateFixture(t *testing.T) *migrateFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(fakeMigrateKubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(planePreamble(admin.Speaks, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"concierge","token":"kmh_fake","expires_at":"2026-10-09T00:00:00Z"}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := &migrateFixture{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dir: dir,
		argsLog: filepath.Join(dir, "args")}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KMX_TEST_ARGS", f.argsLog)
	t.Setenv("KMX_TEST_STDIN", filepath.Join(dir, "stdin"))
	t.Setenv("KMX_TEST_DEPLOYMENT", concierge)
	t.Setenv("KMX_TEST_CONFIGMAP", conciergeConfig)
	t.Setenv("KMX_TEST_PROVIDER", "true")
	t.Setenv("KMX_TEST_SA_TOKEN", "eyJhbGciOiJSUzI1NiJ9.fake.signature")
	t.Setenv("KMX_TEST_SEAM_TLS", seamTLSSecret(t))
	t.Setenv("KMX_TEST_ADMIN_B64", base64.StdEncoding.EncodeToString([]byte("admin-bearer")))
	t.Setenv("KMX_TEST_ADMIN_PORT", u.Port())
	r := run.Default()
	r.Stdout, r.Stderr = f.out, f.errOut
	f.app = &App{Cfg: &config.Config{
		KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1",
		ContextSource: config.SourceKubeCtx, AdminPort: u.Port(),
	}, Run: r, Out: f.out, Err: f.errOut}
	return f
}

func migrateOpts(dir string) MigrateOptions {
	return MigrateOptions{
		Deployment: "concierge", Namespace: "demo", Model: "local/qwen2.5:3b",
		Out: filepath.Join(dir, "concierge.yaml"),
	}
}

// The division of labour, which is the design: kmx applies the objects it
// owns and leaves the adopter's Deployment alone.
func TestAMigrationAppliesWhatItOwnsAndNotYourDeployment(t *testing.T) {
	f := newMigrateFixture(t)
	opt := migrateOpts(f.dir)
	if err := f.app.Migrate(opt); err != nil {
		t.Fatal(err)
	}
	patchPath := strings.TrimSuffix(opt.Out, ".yaml") + "-patch.yaml"
	if _, err := os.Stat(patchPath); err != nil {
		t.Fatalf("the Deployment patch was not written: %v", err)
	}
	args := readFile(t, f.argsLog)
	if !strings.Contains(args, "apply -f "+opt.Out) {
		t.Fatalf("the identity and the seam allowance were not applied:\n%s", args)
	}
	if strings.Contains(args, "patch deployment") || strings.Contains(args, patchPath) {
		t.Fatalf("kmx patched a workload it does not own:\n%s", args)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "patch deployment concierge") {
		t.Fatalf("the operator is not given the command for their half:\n%s", notes)
	}
	// What it read before it wrote anything, reported back.
	if !strings.Contains(notes, "from ConfigMap app-config") {
		t.Fatalf("the migration does not report where the application reads its base URL:\n%s", notes)
	}
}

// The two tokens both travel by pipe. Neither ever becomes an argument,
// which is the difference between a credential in a Secret and one in
// every shell history and process listing on the node.
func TestNeitherTokenIsEverAnArgument(t *testing.T) {
	f := newMigrateFixture(t)
	if err := f.app.Migrate(migrateOpts(f.dir)); err != nil {
		t.Fatal(err)
	}
	args := readFile(t, f.argsLog)
	for _, secret := range []string{"eyJhbGciOiJSUzI1NiJ9.fake.signature", "kmh_fake"} {
		if strings.Contains(args, secret) {
			t.Fatalf("a token reached the command line:\n%s", args)
		}
	}
	written := readFile(t, filepath.Join(f.dir, "stdin"))
	for _, want := range []string{"kaimahi-orka-token", "kaimahi-concierge-token", "kaimahi-plane-ca"} {
		if !strings.Contains(written, want) {
			t.Fatalf("Secret %q was never written:\n%s", want, written)
		}
	}
}

// What the migration reports having read is the operator's whole basis
// for believing the right container was found, so it has to resolve the
// environment the way Kubernetes does: every envFrom source in order,
// then the container's own env, last one winning.
func TestTheReportedWiringResolvesTheWayKubernetesDoes(t *testing.T) {
	f := newMigrateFixture(t)
	t.Setenv("KMX_TEST_DEPLOYMENT", `{"spec":{"template":{"spec":{"containers":[
	  {"name":"concierge",
	   "envFrom":[{"configMapRef":{"name":"app-config"}},{"configMapRef":{"name":"override-config"}}],
	   "env":[{"name":"OPENAI_CHAT_MODEL","valueFrom":{"configMapKeyRef":{"name":"x","key":"y"}}}]}]}}}}`)
	t.Setenv("KMX_TEST_CONFIGMAP", `{"OPENAI_BASE_URL":"http://first.invalid/v1"}`)
	t.Setenv("KMX_TEST_CONFIGMAP2", `{"OPENAI_BASE_URL":"http://second.invalid/v1"}`)
	if err := f.app.Migrate(migrateOpts(f.dir)); err != nil {
		t.Fatal(err)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "http://second.invalid/v1 (from ConfigMap override-config)") {
		t.Fatalf("the later envFrom source did not win, as it does in Kubernetes:\n%s", notes)
	}
	// A variable set from a reference is set, not empty.
	if strings.Contains(notes, "OPENAI_CHAT_MODEL = (empty)") {
		t.Fatalf("a variable set from a reference was reported as empty:\n%s", notes)
	}
	if !strings.Contains(notes, "(from a reference kmx did not read)") {
		t.Fatalf("a variable set from a reference is not reported as such:\n%s", notes)
	}
}

// An envFrom source may carry a prefix, and the variable the container
// receives is then prefix + key. Looking up the variable name as if it
// were the key finds nothing, and a migration that reports "this
// application does not read a base URL" about one that does is a refusal
// nobody can act on.
func TestAPrefixedEnvFromSourceIsResolvedByItsKey(t *testing.T) {
	f := newMigrateFixture(t)
	t.Setenv("KMX_TEST_DEPLOYMENT", `{"spec":{"template":{"spec":{"containers":[
	  {"name":"concierge","envFrom":[{"prefix":"OPENAI_","configMapRef":{"name":"app-config"}}]}]}}}}`)
	t.Setenv("KMX_TEST_CONFIGMAP", `{"BASE_URL":"http://orka-api.orka-system:8080/openai/v1","CHAT_MODEL":"local/qwen2.5:3b"}`)
	if err := f.app.Migrate(migrateOpts(f.dir)); err != nil {
		t.Fatalf("a prefixed source was not resolved: %v", err)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "OPENAI_BASE_URL = http://orka-api.orka-system:8080/openai/v1") {
		t.Fatalf("the prefixed key was not resolved to the variable the container receives:\n%s", notes)
	}
}

// Running it twice is the documented way to replace an expiring token, so
// the second run has to get past its own output rather than refusing to
// overwrite it.
func TestAMigrationCanBeRunAgain(t *testing.T) {
	f := newMigrateFixture(t)
	opt := migrateOpts(f.dir)
	if err := f.app.Migrate(opt); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Migrate(opt); err != nil {
		t.Fatalf("a second run was refused: %v", err)
	}
	notes := f.errOut.String()
	if !strings.Contains(notes, "Unchanged "+opt.Out) {
		t.Fatalf("the second run does not report the manifest as unchanged:\n%s", notes)
	}
	// A file the operator has edited is still refused, even here.
	if err := os.WriteFile(opt.Out, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Migrate(opt); err == nil {
		t.Fatal("an edited manifest was overwritten")
	}
}

// --no-apply writes and stops, the same contract every other scaffolding
// command in kmx has.
func TestAMigrationWithNoApplyMutatesNothing(t *testing.T) {
	f := newMigrateFixture(t)
	opt := migrateOpts(f.dir)
	opt.NoApply = true
	if err := f.app.Migrate(opt); err != nil {
		t.Fatal(err)
	}
	args := readFile(t, f.argsLog)
	for _, forbidden := range []string{"apply", "patch", "create token", "rollout"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("--no-apply ran %q:\n%s", forbidden, args)
		}
	}
}

// Each of these refuses before anything is written. The last two are the
// expensive ones: a patch that sets a variable the application never
// reads, or names a model the endpoint cannot resolve, both report
// success and leave an application that does not work.
func TestAMigrationRefusesBeforeItWritesAnything(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *MigrateOptions)
		says  string
	}{
		{"no namespace", func(_ *testing.T, o *MigrateOptions) { o.Namespace = "" }, "--namespace is required"},
		{"no model", func(_ *testing.T, o *MigrateOptions) { o.Model = "" }, "--model is required"},
		{"an upstream the committed table does not carry",
			func(_ *testing.T, o *MigrateOptions) { o.Upstream = "somewhere-else" }, "committed model upstreams"},
		{"a container that does not read a base URL", func(t *testing.T, _ *MigrateOptions) {
			t.Setenv("KMX_TEST_CONFIGMAP", `{"LOG_LEVEL":"INFO"}`)
		}, "does not read OPENAI_BASE_URL"},
		{"several containers and none named", func(t *testing.T, _ *MigrateOptions) {
			t.Setenv("KMX_TEST_DEPLOYMENT", `{"spec":{"template":{"spec":{"containers":[
			  {"name":"concierge"},{"name":"sidecar"}]}}}}`)
		}, "name the one that talks to a model"},
		{"a named container that is not there", func(_ *testing.T, o *MigrateOptions) {
			o.Container = "nope"
		}, "has no container"},
		{"an endpoint with no Provider for the model", func(t *testing.T, _ *MigrateOptions) {
			t.Setenv("KMX_TEST_PROVIDER", "notfound")
		}, "has no Provider"},
		{"a Provider that is not ready", func(t *testing.T, _ *MigrateOptions) {
			t.Setenv("KMX_TEST_PROVIDER", "false")
		}, "is not ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMigrateFixture(t)
			opt := migrateOpts(f.dir)
			tc.setup(t, &opt)
			err := f.app.Migrate(opt)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("the refusal does not say %q: %v", tc.says, err)
			}
			if _, statErr := os.Stat(opt.Out); statErr == nil {
				t.Fatalf("%s was refused but a manifest was written anyway", tc.name)
			}
			if args, readErr := os.ReadFile(f.argsLog); readErr == nil {
				if strings.Contains(string(args), "apply") || strings.Contains(string(args), "create token") {
					t.Fatalf("%s was refused after touching the cluster:\n%s", tc.name, args)
				}
			}
		})
	}
}
