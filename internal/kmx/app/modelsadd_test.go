package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
)

// The endpoint's URL, taken apart into the two halves the table stores
// separately. The split is the thing an operator would otherwise guess,
// and guessing it wrong produces an upstream that loads cleanly and
// refuses every call with "path not allowed".
func TestTheBaseAndThePathComeOutOfOneURL(t *testing.T) {
	for _, tc := range []struct {
		url, base, path, svc, ns string
		port                     int
	}{
		{"http://vllm.demo:8000/v1/responses", "http://vllm.demo:8000", "v1/responses", "vllm", "demo", 8000},
		{"http://vllm.demo.svc.cluster.local:8000/v1/chat/completions",
			"http://vllm.demo.svc.cluster.local:8000", "v1/chat/completions", "vllm", "demo", 8000},
		// A bare Service name resolves in the CALLER's namespace, and the
		// caller is the proxy — so it is the plane's namespace, stated.
		{"http://vllm/v1/responses", "http://vllm", "v1/responses", "vllm", "kaimahi", 80},
	} {
		base, path, svc, ns, port, err := scaffold.ParseModelURL(tc.url)
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if base != tc.base || path != tc.path || svc != tc.svc || ns != tc.ns || port != tc.port {
			t.Fatalf("%s -> base %s path %s %s/%s:%d, want base %s path %s %s/%s:%d",
				tc.url, base, path, ns, svc, port, tc.base, tc.path, tc.ns, tc.svc, tc.port)
		}
	}
}

func TestAModelURLWithoutAPathIsRefused(t *testing.T) {
	// The table allows exactly ONE forwarded path per upstream. A URL
	// with no path leaves it to be invented, and the message has to say
	// which half is missing rather than "invalid URL".
	_, _, _, _, _, err := scaffold.ParseModelURL("http://vllm.demo:8000")
	if err == nil || !strings.Contains(err.Error(), "ONE forwarded path") {
		t.Fatalf("a URL with no path must be refused with the reason named: %v", err)
	}
	for _, bad := range []string{
		"", "vllm.demo:8000/v1/responses", "http://user:pass@vllm.demo:8000/v1/responses",
		"http://vllm.demo:8000/v1/responses?key=x",
	} {
		if _, _, _, _, _, err := scaffold.ParseModelURL(bad); err == nil {
			t.Fatalf("--url %q was accepted", bad)
		}
	}
	// A hosted endpoint holds a real API key and is a reviewed entry,
	// not an overlay one. The refusal must say so rather than look like
	// a typo.
	_, _, _, _, _, err = scaffold.ParseModelURL("http://api.example.com.extra.bits/v1/responses")
	if err == nil || !strings.Contains(err.Error(), "hosted-upstreams.md") {
		t.Fatalf("the refusal must name the hosted path: %v", err)
	}
}

// The protocol is resolved, never guessed: a path that names one
// answers, a path that names none demands the flag, and a flag that
// contradicts its own path is refused rather than resolved.
func TestTheProtocolIsResolvedOrRefusedNeverGuessed(t *testing.T) {
	for _, tc := range []struct{ path, declared, want string }{
		{"v1/responses", "", "responses"},
		{"v1/chat/completions", "", "chat_completions"},
		{"openai/deployments/gpt/chat/completions", "", "chat_completions"},
		{"api/generate", "responses", "responses"},
		{"v1/responses", "responses", "responses"},
	} {
		got, err := resolveProtocol(tc.path, tc.declared)
		if err != nil || got != tc.want {
			t.Fatalf("path %q + --protocol %q -> %q, %v; want %q", tc.path, tc.declared, got, err, tc.want)
		}
	}
	if _, err := resolveProtocol("api/generate", ""); err == nil ||
		!strings.Contains(err.Error(), "--protocol") {
		t.Fatalf("an unnameable path must demand the flag: %v", err)
	}
	if _, err := resolveProtocol("v1/responses", "chat_completions"); err == nil ||
		!strings.Contains(err.Error(), "refused rather than resolved") {
		t.Fatalf("a contradiction must be refused, not resolved: %v", err)
	}
	if _, err := resolveProtocol("api/generate", "grpc"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
}

// The classification is the operator's and has no default. A $0 by
// inference is a budget nothing can exhaust.
func TestAModelUpstreamWithNoClassificationIsRefused(t *testing.T) {
	f := newModelFixture(t, vllmService, "notfound", nil)
	opt := modelOpts(f.dir)
	opt.Classification = ""
	err := f.app.AddModel(opt)
	if err == nil || !strings.Contains(err.Error(), "--classification is required") {
		t.Fatalf("want a refusal naming the flag, got: %v", err)
	}
	if _, statErr := os.Stat(opt.Out); statErr == nil {
		t.Fatal("a refused command wrote a manifest")
	}
}

func TestACommittedModelUpstreamMayNotBeRedefined(t *testing.T) {
	f := newModelFixture(t, vllmService, "notfound", nil)
	opt := modelOpts(f.dir)
	opt.Name = "ollama"
	if err := f.app.AddModel(opt); err == nil || !strings.Contains(err.Error(), "committed model upstreams") {
		t.Fatalf("want a refusal naming the committed table, got: %v", err)
	}
}

// The whole artifact, read the way an operator reads it: three
// documents, the protocol stated explicitly in the fragment, the
// NetworkPolicy pair on the CONTAINER port, and no credential anywhere.
func TestTheScaffoldedModelManifestIsThreeReviewableDocuments(t *testing.T) {
	f := newModelFixture(t, vllmService, "notfound", nil)
	opt := modelOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddModel(opt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, want := range []string{
		`"protocol": "responses"`,
		`"path": "v1/responses"`,
		`"classification": "free"`,
		`"base_url": "http://vllm.demo:8000"`,
		"kind: ConfigMap",
		"name: kaimahi-model-house-egress",
		"name: kaimahi-model-house-ingress",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("the manifest is missing %q:\n%s", want, doc)
		}
	}
	// The Service publishes 8000 and the pods listen on 9000. A policy
	// written against the Service port blocks every call while reading
	// as correct, because policy is evaluated on the post-NAT address.
	if !strings.Contains(doc, "port: 9000") || strings.Contains(doc, "port: 8000\n") {
		t.Fatalf("the pair does not use the container port:\n%s", doc)
	}
	// There is no fourth document: a ModelConfig is a kagent CRD, and a
	// foreign runtime has no kagent. The base URL is printed instead.
	if strings.Contains(doc, "kind: ModelConfig") {
		t.Fatalf("a kagent custom resource reached a manifest a non-kagent adopter must apply:\n%s", doc)
	}
	if !strings.Contains(f.out.String()+f.errOut.String(), scaffold.SeamBaseURL("house")) {
		t.Fatalf("the seam's base URL was never printed:\n%s%s", f.out.String(), f.errOut.String())
	}
	// And what an operator coming from `kmx tools add` would otherwise
	// assume: there is no allowlist on this seam.
	if !strings.Contains(f.out.String()+f.errOut.String(), "no allowlist") {
		t.Fatalf("the absence of an allowlist was never stated:\n%s%s", f.out.String(), f.errOut.String())
	}
}

// The overlay is emitted WHOLE. A map missing a key somebody else's
// onboarding put there would be pruned by `kubectl apply`, silently
// un-onboarding their upstream — and the two seams share one ConfigMap,
// so a model onboarding can prune a tool one.
func TestOnboardingAModelCarriesEveryExistingFragmentIncludingToolOnes(t *testing.T) {
	f := newModelFixture(t, vllmService,
		`{"warehouse.json":"{\"tool_upstreams\":{\"warehouse\":{\"url\":\"http://w.acme:80/mcp\"}}}"}`, nil)
	opt := modelOpts(f.dir)
	opt.NoApply = true
	if err := f.app.AddModel(opt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(opt.Out)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	if !strings.Contains(doc, "warehouse.json") {
		t.Fatalf("an existing tool fragment was dropped from the emitted overlay:\n%s", doc)
	}
	if !strings.Contains(doc, "model-house.json") {
		t.Fatalf("the new model fragment is not in the emitted overlay:\n%s", doc)
	}
	// The read version travels with it as an apply precondition.
	if !strings.Contains(doc, `resourceVersion: "4711"`) {
		t.Fatalf("the overlay's resourceVersion is not the apply precondition:\n%s", doc)
	}
}

// Nothing is written when the plane refuses the table.
func TestAPlaneRefusalLeavesNothingWritten(t *testing.T) {
	f := newModelFixture(t, vllmService, "notfound", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"ok":false,"error":"config: upstream \"house\": path names no protocol"}`))
	})
	opt := modelOpts(f.dir)
	err := f.app.AddModel(opt)
	if err == nil || !strings.Contains(err.Error(), "nothing has been applied") {
		t.Fatalf("want the plane's refusal, got: %v", err)
	}
	if _, statErr := os.Stat(opt.Out); statErr == nil {
		t.Fatal("a refused table left a manifest behind")
	}
}

// --- fixture -------------------------------------------------------------

const vllmService = `{"spec":{"selector":{"app":"vllm"},
  "ports":[{"port":8000,"targetPort":9000,"protocol":"TCP"}]}}`

func newModelFixture(t *testing.T, svc, overlay string, validate http.HandlerFunc) *addFixture {
	t.Helper()
	t.Setenv("KMX_TEST_PODS", "vllm-0")
	if validate == nil {
		validate = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"upstreams":["house (responses)","ollama (chat_completions)"]}`))
		}
	}
	return newAddFixture(t, svc, overlay, validate)
}

func modelOpts(dir string) AddModelOptions {
	return AddModelOptions{
		Name:           "house",
		URL:            "http://vllm.demo:8000/v1/responses",
		Classification: "free",
		Out:            filepath.Join(dir, "model-house.yaml"),
	}
}
