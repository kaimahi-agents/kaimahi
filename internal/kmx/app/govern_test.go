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

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/run"
)

// A fake kubectl on PATH, so `kmx govern` can be driven end to end without a
// cluster: every argument list and everything piped into it is recorded.
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
  *"get secret kaimahi-governed-token"*) printf '%s' "$KMX_TEST_BOUND"; exit 0 ;;
  *port-forward*)
    # A real kubectl announces the bind before anything may be sent through
    # it; kmx waits for exactly this line, so the fake has to print it.
    printf 'Forwarding from 127.0.0.1:%s -> 9091\n' "$KMX_TEST_ADMIN_PORT"
    exec sleep 30 ;;
  *"get agent hello-world"*)
    if [ -n "$KMX_TEST_AGENT_ERR" ]; then
      printf '%s\n' "$KMX_TEST_AGENT_ERR" >&2
      exit 1
    fi
    printf 'agent.kagent.dev/hello-world\n'; exit 0 ;;
  *"apply -f -"*) cat >> "$KMX_TEST_STDIN"; exit 0 ;;
esac
exit 0
`

type governFixture struct {
	app     *App
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	argsLog string
	stdin   string
}

func newGovernFixture(t *testing.T, agentErr string, issue http.HandlerFunc) *governFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake kubectl is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(fakeKubectl), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		issue(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	f := &governFixture{
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

func (f *governFixture) args() string {
	b, _ := os.ReadFile(f.argsLog)
	return string(b)
}

func (f *governFixture) piped() string {
	b, _ := os.ReadFile(f.stdin)
	return string(b)
}

func issued(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": token})
	}
}

func governOptions() GovernOptions {
	return GovernOptions{
		Agent:           config.DefaultAgent,
		Preset:          config.GovernedModelConfig,
		Secret:          config.GovernedSecret,
		SecretNamespace: config.DefaultNamespace,
	}
}

func TestInteractiveGovernanceResourcesAreAgentSpecific(t *testing.T) {
	secret := governedResourceName("kmx-token", "hello-tools")
	preset := governedResourceName("kmx-governed-ollama", "hello-tools")
	credential := governedResourceName("kmx-model", "hello-tools")
	if secret != "kmx-token-hello-tools" || preset != "kmx-governed-ollama-hello-tools" {
		t.Fatalf("unexpected resource names: secret=%q preset=%q", secret, preset)
	}
	if credential != "kmx-model-hello-tools" {
		t.Fatalf("unexpected credential name: %q", credential)
	}
	raw, err := interactiveModelManifest(preset, secret, "qwen2.5:3b", true, "hello-tools")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(raw)
	for _, want := range []string{`"name":"` + preset + `"`, `"apiKeySecret":"` + secret + `"`, `"model":"qwen2.5:3b"`, "kaimahi-proxy.kaimahi.svc.cluster.local", `"kaimahi.dev/chat-agent":"hello-tools"`} {
		if !strings.Contains(manifest, want) {
			t.Errorf("agent-specific ModelConfig lacks %q:\n%s", want, manifest)
		}
	}
	if strings.Contains(manifest, config.GovernedSecret) {
		t.Fatalf("agent-specific ModelConfig reused singleton Secret:\n%s", manifest)
	}
}

func TestInteractiveModelManifestCannotBeInjectedByModelName(t *testing.T) {
	raw, err := interactiveModelManifest("preset", "secret", "qwen\n---\nkind: Secret", true, "agent")
	if err != nil {
		t.Fatal(err)
	}
	var resource map[string]any
	if err := json.Unmarshal(raw, &resource); err != nil {
		t.Fatalf("generated manifest is not one JSON document: %v\n%s", err, raw)
	}
	if strings.Contains(string(raw), "\n---\n") {
		t.Fatalf("model name escaped into a YAML document boundary:\n%s", raw)
	}
}

func TestInteractiveDirectModelManifestPreservesModelWithoutASecret(t *testing.T) {
	raw, err := interactiveModelManifest("kmx-direct-ollama-agent", "", "custom:latest", false, "agent")
	if err != nil {
		t.Fatal(err)
	}
	var resource struct {
		Spec map[string]any `json:"spec"`
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		t.Fatal(err)
	}
	if resource.Spec["model"] != "custom:latest" || resource.Spec["provider"] != "Ollama" {
		t.Fatalf("direct manifest changed model/provider: %s", raw)
	}
	if _, exists := resource.Spec["apiKeySecret"]; exists {
		t.Fatalf("direct manifest retained governed Secret: %s", raw)
	}
}

func TestGovernedResourceNamesRemainValidKubernetesNames(t *testing.T) {
	name := governedResourceName("kmx-governed-ollama", strings.Repeat("a", 63))
	if len(name) > 63 || !agentNameRE.MatchString(name) {
		t.Fatalf("invalid generated resource name %q", name)
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
		t.Fatalf("ordinary govern behavior changed to %q", got)
	}
}

// The rule `make govern` states in a comment and enforces with a grep, here
// enforced by construction: ONLY a genuine NotFound may skip the switch.
// Every other failure — an unreachable API server, an expired credential, an
// RBAC denial, a wrong context — must abort. Collapsing them prints a
// reassuring NOTE, exits 0, and leaves the agent on an UNGOVERNED preset,
// spending outside the plane.
func TestGovernRefusesToSkipTheSwitchOnAnAmbiguousRead(t *testing.T) {
	for _, ambiguous := range []string{
		"The connection to the server 127.0.0.1:6443 was refused - did you specify the right host or port?",
		`Error from server (Forbidden): agents.kagent.dev is forbidden: User "x" cannot get resource "agents"`,
		`error: the server doesn't have a resource type "agent"`,
		"error: You must be logged in to the server (Unauthorized)",
	} {
		t.Run(ambiguous[:20], func(t *testing.T) {
			f := newGovernFixture(t, ambiguous, issued("kmh_"+strings.Repeat("a", 64)))
			err := f.app.Govern("hello-world", governOptions())
			if err == nil {
				t.Fatal("govern succeeded without switching the agent")
			}
			if !strings.Contains(err.Error(), "refusing to leave it ungoverned") {
				t.Errorf("wrong refusal: %v", err)
			}
			if strings.Contains(f.errOut.String(), "does not exist yet") {
				t.Errorf("an unreadable agent was reported as absent:\n%s", f.errOut.String())
			}
		})
	}
}

// A genuine NotFound is the ordering where governance is stood up before the
// agents exist. It proceeds and still applies the presets — and it must be
// honest about what happens next: `kmx up` creates hello-world on the
// KEYLESS preset, so an agent created after this runs ungoverned until
// govern is run again. A NOTE promising otherwise would be the same
// "reassuring message, exit 0, agent spending outside the plane" the
// NotFound discrimination above exists to prevent.
func TestGovernNotesAGenuinelyAbsentAgentAndStillAppliesThePresets(t *testing.T) {
	f := newGovernFixture(t,
		`Error from server (NotFound): agents.kagent.dev "hello-world" not found`,
		issued("kmh_"+strings.Repeat("b", 64)))
	if err := f.app.Govern("hello-world", governOptions()); err != nil {
		t.Fatalf("govern: %v", err)
	}
	note := f.errOut.String()
	if !strings.Contains(note, "does not exist yet") {
		t.Errorf("the absent agent was not reported:\n%s", note)
	}
	if !strings.Contains(note, config.KeylessModelConfig) || !strings.Contains(note, "kmx govern hello-world") {
		t.Errorf("the NOTE promises governance the runtime will not deliver:\n%s", note)
	}
	piped := f.piped()
	for _, want := range []string{"governed-ollama", "governed-copilot"} {
		if !strings.Contains(piped, want) {
			t.Errorf("preset %s was not applied:\n%s", want, piped)
		}
	}
}

// Custody, proven rather than commented: the issued token reaches the
// cluster through kubectl's STDIN and appears in no argument, no environment
// listing, no file kmx wrote, and nothing printed. This is the property the
// shell script needed a 0600 file and a dry-run pipe to approximate.
func TestTheIssuedTokenTravelsOnlyThroughThePipe(t *testing.T) {
	token := "kmh_" + strings.Repeat("c", 64)
	f := newGovernFixture(t,
		`Error from server (NotFound): agents.kagent.dev "hello-world" not found`,
		issued(token))
	if err := f.app.Govern("hello-world", governOptions()); err != nil {
		t.Fatalf("govern: %v", err)
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

// A credential name is interpolated into JSON and a query string. The
// script's check_name is kept because the plane validating again is a second
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
	f := newGovernFixture(t, "", conflict)
	// The fake kubectl answers the annotation read with an empty string,
	// which is the "exists in the plane, Secret missing or unlabeled" case:
	// the token cannot be recovered, so the operator is told exactly how to
	// clear the row rather than being handed a half-governed agent.
	err := f.app.Govern("hello-world", governOptions())
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
// `kmx govern demo` while kaimahi-governed-token holds hello-world's token
// would otherwise mint demo's credential, overwrite the Secret, and destroy
// the only copy of hello-world's token — the plane keeps only its hash, so
// hello-world would stay live and permanently unusable. Refusing before the
// POST also means no orphan credential row is left behind.
func TestASecondCredentialWillNotOverwriteAnotherOnesToken(t *testing.T) {
	t.Setenv("KMX_TEST_BOUND", "hello-world")
	issuedAnyway := false
	f := newGovernFixture(t, "", func(w http.ResponseWriter, r *http.Request) {
		issuedAnyway = true
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"token": "kmh_" + strings.Repeat("d", 64)})
	})
	err := f.app.Govern("demo", governOptions())
	if err == nil {
		t.Fatal("govern demo overwrote the Secret holding hello-world's token")
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
func TestGoverningTheSameCredentialAgainIsFine(t *testing.T) {
	t.Setenv("KMX_TEST_BOUND", "hello-world")
	f := newGovernFixture(t, `Error from server (NotFound): agents.kagent.dev "hello-world" not found`,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"credential exists"}`))
		})
	if err := f.app.Govern("hello-world", governOptions()); err != nil {
		t.Fatalf("re-governing the same credential failed: %v", err)
	}
	if !strings.Contains(f.errOut.String(), "keeping both") {
		t.Errorf("the already-issued case was not reconciled:\n%s", f.errOut.String())
	}
}
