package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/admin"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// --- the URL, taken apart the way the plane takes it apart ---

func TestTheServiceAndPortComeOutOfTheURL(t *testing.T) {
	for _, tc := range []struct {
		url, svc, ns string
		port         int
	}{
		{"http://acme-warehouse.acme:8090/mcp", "acme-warehouse", "acme", 8090},
		{"http://acme-warehouse.acme.svc.cluster.local:8090/mcp", "acme-warehouse", "acme", 8090},
		{"http://acme-warehouse.acme.svc:8090/mcp", "acme-warehouse", "acme", 8090},
		// A bare Service name resolves in the CALLER's namespace, and the
		// caller is the proxy — so it is the plane's namespace, stated.
		{"http://warehouse:8090/mcp", "warehouse", "kaimahi", 8090},
		{"http://warehouse/mcp", "warehouse", "kaimahi", 80},
	} {
		svc, ns, port, err := parseUpstreamURL(tc.url)
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if svc != tc.svc || ns != tc.ns || port != tc.port {
			t.Fatalf("%s -> %s/%s:%d, want %s/%s:%d", tc.url, ns, svc, port, tc.ns, tc.svc, tc.port)
		}
	}
}

func TestAURLThatIsNotAnInClusterServiceIsRefused(t *testing.T) {
	for _, bad := range []string{
		"", "acme-warehouse.acme:8090/mcp", "http://acme-warehouse.acme:8090",
		"http://user:pass@acme.acme:80/mcp", "ftp://acme.acme:80/mcp",
	} {
		if _, _, _, err := parseUpstreamURL(bad); err == nil {
			t.Fatalf("--url %q was accepted", bad)
		}
	}
	// A hosted server is a different path with a different threat model
	// (the hardened dialer, an opt-in 443 allowance), and the refusal
	// has to say so rather than look like a typo.
	_, _, _, err := parseUpstreamURL("https://mcp.example.com/mcp")
	if err == nil {
		t.Fatal("an internet URL was accepted by the in-cluster path")
	}
	_, _, _, err = parseUpstreamURL("http://mcp.example.com.extra.bits/mcp")
	if err == nil || !strings.Contains(err.Error(), "hosted-upstreams.md") {
		t.Fatalf("the refusal must name the hosted path: %v", err)
	}
}

// --- the Service, read from the cluster because a name cannot say it ---

// fakeAddKubectl answers the reads `kmx tools add` makes. KMX_TEST_SVC is
// the Service JSON; KMX_TEST_OVERLAY is the overlay ConfigMap's data (or
// "notfound" / "boom" to exercise the two failure directions).
// KMX_TEST_NO_KAGENT=1 is a cluster where the kagent CRDs were never
// installed; "boom" is one that could not be asked.
const fakeAddKubectl = `#!/bin/sh
printf '%s\n' "$*" >> "$KMX_TEST_ARGS"
case "$*" in
  *"apply -f -"*|*"apply --dry-run=server -f -"*)
    [ -n "$KMX_TEST_STDIN" ] && cat >> "$KMX_TEST_STDIN"
    exit 0 ;;
  *"get crd remotemcpservers.kagent.dev"*)
    case "$KMX_TEST_NO_KAGENT" in
      1)    printf 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "remotemcpservers.kagent.dev" not found\n' >&2; exit 1 ;;
      boom) printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
      *)    printf 'customresourcedefinition.apiextensions.k8s.io/remotemcpservers.kagent.dev\n'; exit 0 ;;
    esac ;;
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
  *"get remotemcpserver"*)
    case "$KMX_TEST_NO_SERVER" in
      1)    printf 'Error from server (NotFound): remotemcpservers.kagent.dev "x" not found\n' >&2; exit 1 ;;
      boom) printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
      *)    printf 'remotemcpserver.kagent.dev/x\n'; exit 0 ;;
    esac ;;
  *"get service"*)
    if [ "$KMX_TEST_SVC" = "notfound" ]; then
      printf 'Error from server (NotFound): services "acme-warehouse" not found\n' >&2
      exit 1
    fi
    printf '%s' "$KMX_TEST_SVC"; exit 0 ;;
  *"get configmap kaimahi-upstreams-extra"*)
    case "$KMX_TEST_OVERLAY" in
      notfound) printf 'Error from server (NotFound): configmaps "kaimahi-upstreams-extra" not found\n' >&2; exit 1 ;;
      boom)     printf 'Unable to connect to the server: dial tcp: i/o timeout\n' >&2; exit 1 ;;
      *)
        if [ "$KMX_TEST_RV" = "none" ]; then
          printf '{"metadata":{},"data":%s}' "$KMX_TEST_OVERLAY"
        else
          printf '{"metadata":{"resourceVersion":"%s"},"data":%s}' "$KMX_TEST_RV" "$KMX_TEST_OVERLAY"
        fi
        exit 0 ;;
    esac ;;
esac
exit 0
`

const warehouseService = `{"spec":{"selector":{"app":"acme-warehouse"},
  "ports":[{"port":8090,"targetPort":9090,"protocol":"TCP"}]}}`

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
			_, _ = w.Write([]byte(`{"ok":true,"tool_upstreams":["kagent-tools","warehouse"],"declared":{"stock_get":["sku"]}}`))
		}
	}
	srv := httptest.NewServer(planePreamble(admin.Speaks, validate))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	f := &addFixture{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, dir: dir,
		argsLog: filepath.Join(dir, "args")}
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
	f.app = &App{Cfg: &config.Config{
		KindCluster: "kaimahi-p1", KubeContext: "kind-kaimahi-p1", ContextSource: config.SourceKubeCtx, AdminPort: u.Port(),
	}, Run: r, Out: f.out, Err: f.errOut}
	return f
}

func addOpts(dir string) AddUpstreamOptions {
	return AddUpstreamOptions{
		Name:  "warehouse",
		URL:   "http://acme-warehouse.acme:8090/mcp",
		Tools: []string{"stock_get:sku", "stock_adjust:sku,delta"},
		Out:   filepath.Join(dir, "warehouse.yaml"),
	}
}

func TestTheNetworkPolicyUsesTheContainerPortNotTheServicePort(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	// The Service publishes 8090 and the pods listen on 9090. A policy
	// written against 8090 blocks every call while reading as correct,
	// because policy is evaluated on the post-NAT pod address.
	if !strings.Contains(string(doc), "port: 9090") {
		t.Fatalf("the pair does not use the container port:\n%s", doc)
	}
	if strings.Contains(string(doc), "port: 8090") {
		t.Fatalf("the SERVICE port reached a NetworkPolicy:\n%s", doc)
	}
	// The Service's own port belongs in the gateway's table, where the
	// gateway dials it through the Service.
	if !strings.Contains(string(doc), "acme-warehouse.acme:8090/mcp") {
		t.Fatalf("the overlay lost the server's URL:\n%s", doc)
	}
}

func TestANamedTargetPortIsNotGuessed(t *testing.T) {
	f := newAddFixture(t, `{"spec":{"selector":{"app":"acme-warehouse"},
	  "ports":[{"port":8090,"targetPort":"mcp","protocol":"TCP"}]}}`, "notfound", nil)
	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil || !strings.Contains(err.Error(), "--pod-port") {
		t.Fatalf("a named target port must be refused with the fix named, got: %v", err)
	}
}

func TestAnUnsetTargetPortDefaultsToTheServicePort(t *testing.T) {
	f := newAddFixture(t, `{"spec":{"selector":{"app":"acme-warehouse"},"ports":[{"port":8090}]}}`, "notfound", nil)
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	doc, _ := os.ReadFile(opt.Out)
	if !strings.Contains(string(doc), "port: 8090") {
		t.Fatalf("want the Service port when targetPort is unset:\n%s", doc)
	}
}

func TestGoverningAgainstASeamThatWasNeverAppliedFailsFast(t *testing.T) {
	// `kmx tools add --no-apply` writes the manifest and stops, so the
	// RemoteMCPServer may simply not be there. Without this the next
	// step waits five minutes on an object that does not exist and
	// reports a timeout, which says nothing about the cause.
	f := newAddFixture(t, warehouseService, "notfound", nil)
	t.Setenv("KMX_TEST_NO_SERVER", "1")
	err := f.app.preflightToolServer("kaimahi-warehouse")
	if err == nil || !strings.Contains(err.Error(), "no RemoteMCPServer") {
		t.Fatalf("want an early refusal naming the missing seam, got: %v", err)
	}
	if !strings.Contains(err.Error(), "apply the manifest first") {
		t.Fatalf("the refusal must name the fix: %v", err)
	}
	// An ambiguous read is NOT absence: an unreachable API server or an
	// RBAC denial must not be reported as "you forgot to apply it".
	t.Setenv("KMX_TEST_NO_SERVER", "boom")
	err = f.app.preflightToolServer("kaimahi-warehouse")
	if err == nil || strings.Contains(err.Error(), "apply the manifest first") {
		t.Fatalf("an ambiguous read was reported as absence: %v", err)
	}
}

func TestASelectorThatMatchesMoreThanTheServerSaysSoBeforeTheGuardAsks(t *testing.T) {
	// Found in review: the ingress policy is pinned to the Service's
	// selector, and a shared selector (`tier: backend`) would cut every
	// matching workload off from all callers but the proxy — and, under
	// the default posture, give them zero egress. kmx does not refuse it
	// (sharing can be legitimate) but it must not happen silently.
	t.Setenv("KMX_TEST_PODS", "warehouse-1 billing-2 reports-3")
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	notes := f.errOut.String()
	for _, want := range []string{"matches 3 pods, not one", "billing-2", "reach nothing at all"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("the blast radius was not named (%q missing):\n%s", want, notes)
		}
	}
}

func TestOnePodIsNamedRatherThanWarnedAbout(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.errOut.String(), "governs pod acme-warehouse-5f7c") {
		t.Fatalf("the ordinary case should name the pod, not warn:\n%s", f.errOut)
	}
}

func TestAServiceWithNoSelectorIsRefusedRatherThanSelectingEveryPod(t *testing.T) {
	f := newAddFixture(t, `{"spec":{"ports":[{"port":8090,"targetPort":9090}]}}`, "notfound", nil)
	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil || !strings.Contains(err.Error(), "no selector") {
		t.Fatalf("want a refusal for a selector-less Service, got: %v", err)
	}
}

func TestAMissingServiceSaysWhyItIsNeeded(t *testing.T) {
	f := newAddFixture(t, "notfound", "notfound", nil)
	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil || !strings.Contains(err.Error(), "no Service") {
		t.Fatalf("want a refusal naming the Service, got: %v", err)
	}
}

// --- the overlay, which must never be silently emptied ---

func TestAnUnreadableOverlayIsNotTreatedAsAnEmptyOne(t *testing.T) {
	// A ConfigMap apply REPLACES data. If an unreachable API server, an
	// RBAC denial or the wrong context read as "nothing is onboarded",
	// this command would emit a ConfigMap that un-onboards every server
	// somebody else added.
	f := newAddFixture(t, warehouseService, "boom", nil)
	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil || !strings.Contains(err.Error(), "reading the overlay") {
		t.Fatalf("want a hard failure on an ambiguous overlay read, got: %v", err)
	}
}

func TestAnExistingOverlayIsCarriedWholeIntoTheEmittedConfigMap(t *testing.T) {
	f := newAddFixture(t, warehouseService,
		`{"depot.json":"{\"tool_upstreams\": {\"depot\": {\"url\": \"http://d.acme:80/mcp\"}}}"}`, nil)
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	doc, _ := os.ReadFile(opt.Out)
	for _, want := range []string{`"depot.json"`, `"warehouse.json"`, "depot"} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("the emitted overlay dropped %q:\n%s", want, doc)
		}
	}
	// And the apply is CONDITIONAL on the version it was read at. This
	// manifest is meant to be applied later; without the precondition,
	// applying a file scaffolded on Monday would prune a fragment added
	// on Tuesday and leave the upstream it constrained running unbounded.
	if !strings.Contains(string(doc), `resourceVersion: "4711"`) {
		t.Fatalf("the emitted overlay carries no apply precondition:\n%s", doc)
	}
}

func TestAnOverlayWithNoVersionToStateIsRefused(t *testing.T) {
	// An unconditional apply of a whole-map snapshot is exactly the
	// behaviour the precondition exists to remove, so an object that
	// cannot supply one is refused rather than applied without it.
	t.Setenv("KMX_TEST_RV", "none")
	f := newAddFixture(t, warehouseService, `{}`, nil)
	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil || !strings.Contains(err.Error(), "no resourceVersion") {
		t.Fatalf("want a refusal rather than an unconditional apply, got: %v", err)
	}
}

func TestOnboardingTheSameNameTwiceIsRefused(t *testing.T) {
	f := newAddFixture(t, warehouseService,
		`{"warehouse.json":"{\"tool_upstreams\": {\"warehouse\": {\"url\": \"http://w:80/mcp\"}}}"}`, nil)
	err := f.app.AddUpstream(addOpts(f.dir))
	if err == nil || !strings.Contains(err.Error(), "already in the overlay") {
		t.Fatalf("want a refusal, got: %v", err)
	}
}

// --- validation happens BEFORE anything is written or applied ---

func TestAnUpstreamThePlaneRefusesIsNeverWrittenAndNeverApplied(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"ok":false,"error":"config: tool upstream \"warehouse\": policy_fields is required"}`))
	})
	opt := addOpts(f.dir)
	err := f.app.AddUpstream(opt)
	if err == nil || !strings.Contains(err.Error(), "policy_fields is required") {
		t.Fatalf("the plane's own message must come back verbatim, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing has been applied") {
		t.Fatalf("the refusal must say the cluster is unchanged: %v", err)
	}
	if _, statErr := os.Stat(opt.Out); statErr == nil {
		t.Fatal("a refused upstream left a manifest behind")
	}
	if strings.Contains(readFile(t, f.argsLog), "apply") {
		t.Fatal("a refused upstream reached kubectl apply")
	}
}

func TestWhatThePlaneUnderstoodIsEchoedBack(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	// "Verify it worked from the audit rather than from hope" starts
	// here: the plane says what it will bind, in its own words.
	if !strings.Contains(f.errOut.String(), "an approval binds sku") {
		t.Fatalf("the plane's reading of the declaration was not shown:\n%s", f.errOut)
	}
}

// --- generate-don't-mutate, and no credential in any form ---

func TestOutDashPrintsAndMutatesNothing(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.Out = "-"
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.out.String(), "kind: RemoteMCPServer") {
		t.Fatalf("--out - printed nothing useful:\n%s", f.out)
	}
	args := readFile(t, f.argsLog)
	for _, forbidden := range []string{"apply", "rollout", "create"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("--out - ran %q:\n%s", forbidden, args)
		}
	}
}

func TestTheSecretIsNamedAndNeverValued(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.Out = "-"
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	doc := f.out.String()
	if !strings.Contains(doc, `name: "kaimahi-warehouse-token"`) || !strings.Contains(doc, "key: api-key") {
		t.Fatalf("the seam does not reference the Secret by name:\n%s", doc)
	}
	// Nothing that could be a token value: the generator refuses key
	// shapes over its own output, and there is no flag that takes one.
	// `valueFrom:` is the safe form and is expected; a bare `value:` in
	// a headersFrom block would be an inline credential.
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "value:") || strings.Contains(trimmed, "kmh_") {
			t.Fatalf("the scaffold emitted something that could be a credential: %q", line)
		}
	}
}

func TestAToolNamedWithNoDeclarationStopsTheCommand(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	opt := addOpts(f.dir)
	opt.Tools = []string{"stock_adjust"}
	err := f.app.AddUpstream(opt)
	if err == nil || !strings.Contains(err.Error(), "WEAKEST") {
		t.Fatalf("want the policy_fields guidance, got: %v", err)
	}
	if _, statErr := os.Stat(opt.Out); statErr == nil {
		t.Fatal("the command wrote a manifest before the declaration was settled")
	}
}

func TestTheCommittedFourCannotBeRedefined(t *testing.T) {
	f := newAddFixture(t, warehouseService, "notfound", nil)
	for _, name := range []string{"erp", "slack", "github", "kagent-tools"} {
		opt := addOpts(f.dir)
		opt.Name = name
		if err := f.app.AddUpstream(opt); err == nil {
			t.Fatalf("%q was accepted as an overlay name", name)
		}
	}
}

func TestTheSubmittedOverlayIsTheWholeOverlayNotJustTheNewEntry(t *testing.T) {
	var got map[string]any
	f := newAddFixture(t, warehouseService,
		`{"depot.json":"{\"tool_upstreams\": {\"depot\": {\"url\": \"http://d.acme:80/mcp\"}}}"}`,
		func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"tool_upstreams":["depot","warehouse"]}`))
		})
	opt := addOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddUpstream(opt); err != nil {
		t.Fatal(err)
	}
	frags, _ := got["fragments"].(map[string]any)
	if len(frags) != 2 || frags["depot.json"] == nil || frags["warehouse.json"] == nil {
		t.Fatalf("validation must cover the overlay as it will exist, got %v", frags)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}
