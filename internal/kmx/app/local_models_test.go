package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
)

type staticLocalDetector struct {
	models []localModel
	calls  int
}

func (d *staticLocalDetector) Detect(context.Context) ([]localModel, error) {
	d.calls++
	return d.models, nil
}

func TestOllamaDetectorListsOnlyInstalledModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"},{"name":"llama3.2:3b"},{"name":"bad\nname"}]}`))
	}))
	defer server.Close()
	d := ollamaDetector{client: server.Client(), endpoint: server.URL}
	models, err := d.Detect(context.Background())
	if err != nil || len(models) != 2 || models[0].Provider != "ollama" || models[0].Model != "qwen3:8b" || models[0].Endpoint != "" {
		t.Fatalf("models = %#v, err = %v", models, err)
	}
}

func TestSingleDetectedModelDefaultsToBundledButCanBeSelected(t *testing.T) {
	model := []localModel{{Provider: "ollama", Model: "qwen3:8b"}}
	for name, tc := range map[string]struct {
		input        string
		wantSelected bool
	}{
		"default bundled": {"\n", false},
		"select host":     {"y\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			stdin, err := os.CreateTemp(t.TempDir(), "input")
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if _, err := stdin.WriteString(tc.input); err != nil {
				t.Fatal(err)
			}
			if _, err := stdin.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			a := &App{Stdin: stdin, Err: &out}
			selected, err := a.promptLocalModel(model)
			if err != nil {
				t.Fatal(err)
			}
			if (selected != nil) != tc.wantSelected {
				t.Fatalf("selected=%#v, output=%q", selected, out.String())
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Fatalf("prompt does not communicate bundled default: %q", out.String())
			}
		})
	}
}

func TestMultipleDetectedModelsDefaultToFirstAndOfferBundledFallback(t *testing.T) {
	stdin, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if _, err := stdin.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &App{Stdin: stdin, Err: &out}
	models := []localModel{{Provider: "ollama", Model: "first"}, {Provider: "ollama", Model: "second"}}
	selected, err := a.promptLocalModel(models)
	if err != nil {
		t.Fatal(err)
	}
	if selected != nil {
		t.Fatalf("selected=%#v", selected)
	}
	if !strings.Contains(out.String(), "1. Install KMX's bundled model") || !strings.Contains(out.String(), "2. Try ollama/first") || !strings.Contains(out.String(), "Select a model [1]") {
		t.Fatalf("missing default or fallback: %q", out.String())
	}
}

func TestNoDetectedModelsMeansNoSelection(t *testing.T) {
	d := &staticLocalDetector{}
	selected := false
	a := &App{Cfg: &config.Config{Model: config.DefaultModel}, Err: &bytes.Buffer{}, localModels: &localModelEnvironment{
		Detectors: []localModelDetector{d}, Interactive: func() bool { return true },
		Select: func([]localModel) (*localModel, error) { selected = true; return nil, nil },
	}}
	if err := a.maybeSelectLocalModel(true); err != nil {
		t.Fatal(err)
	}
	if selected || a.selectedLocalModel != nil {
		t.Fatal("selection was offered without a detected model")
	}
}

func TestDetectedModelCanBeSelectedOnce(t *testing.T) {
	want := localModel{Provider: "ollama", Model: "qwen3:8b", Endpoint: "http://host:11434"}
	d := &staticLocalDetector{models: []localModel{want}}
	a := &App{Cfg: &config.Config{Model: config.DefaultModel}, Err: &bytes.Buffer{}, localModels: &localModelEnvironment{
		Detectors: []localModelDetector{d}, Interactive: func() bool { return true },
		Verify: func(selected *localModel) error { selected.Endpoint = want.Endpoint; return nil },
		Select: func(models []localModel) (*localModel, error) { return &models[0], nil },
	}}
	if err := a.maybeSelectLocalModel(true); err != nil {
		t.Fatal(err)
	}
	if err := a.maybeSelectLocalModel(true); err != nil {
		t.Fatal(err)
	}
	a.verifySelectedLocalModel()
	if d.calls != 1 || a.selectedLocalModel == nil || a.Cfg.Model != want.Model || !a.localModelVerified {
		t.Fatalf("calls=%d selected=%#v model=%q", d.calls, a.selectedLocalModel, a.Cfg.Model)
	}
}

func TestUnreachableSelectedModelFallsBackToBundled(t *testing.T) {
	want := localModel{Provider: "ollama", Model: "qwen3:8b"}
	a := &App{Cfg: &config.Config{Model: config.DefaultModel}, Err: &bytes.Buffer{}, selectedLocalModel: &want,
		localModels: &localModelEnvironment{Verify: func(*localModel) error { return context.DeadlineExceeded }}}
	a.verifySelectedLocalModel()
	if a.selectedLocalModel != nil || a.Cfg.Model != config.DefaultModel {
		t.Fatalf("selection=%#v model=%q", a.selectedLocalModel, a.Cfg.Model)
	}
	if !strings.Contains(a.Err.(*bytes.Buffer).String(), "installing KMX's bundled model instead") {
		t.Fatalf("fallback was not explained: %s", a.Err.(*bytes.Buffer).String())
	}
}

func TestOllamaTagsContainRequiresSelectedInstalledModel(t *testing.T) {
	body := []byte(`{"models":[{"name":"qwen3:8b"}]}`)
	if !ollamaTagsContain(body, "qwen3:8b") || ollamaTagsContain(body, "missing") || ollamaTagsContain([]byte("bad"), "qwen3:8b") {
		t.Fatal("tag membership was not exact")
	}
}

func TestKindOllamaEndpointsIncludeLinuxBridgeFallback(t *testing.T) {
	docker := ollamaKindEndpoints("docker", "172.18.0.1")
	if len(docker) != 2 || docker[0] != "http://host.docker.internal:11434" || docker[1] != "http://172.18.0.1:11434" {
		t.Fatalf("docker endpoints=%q", docker)
	}
	podman := ollamaKindEndpoints("podman", "")
	if len(podman) != 1 || podman[0] != "http://host.containers.internal:11434" {
		t.Fatalf("podman endpoints=%q", podman)
	}
}

func TestRedirectedOutputDisablesLocalModelPrompt(t *testing.T) {
	stdin, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	a := &App{Stdin: stdin, Out: stdout, Err: os.Stderr}
	if a.localModelInteractive() {
		t.Fatal("redirected stdout was treated as interactive")
	}
}

func TestLiveHostModelConfigIsPreservedAcrossAgentStep(t *testing.T) {
	model, err := parseLiveKeylessLocalModel([]byte(`{"spec":{"provider":"Ollama","model":"qwen3:8b","ollama":{"host":"http://172.18.0.1:11434"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || model.Model != "qwen3:8b" || model.Endpoint != "http://172.18.0.1:11434" {
		t.Fatalf("model=%#v", model)
	}
	a := &App{Cfg: &config.Config{Model: config.DefaultModel}, selectedLocalModel: model}
	body, err := a.renderModelManifest("hello-world.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "model: qwen3:8b") || !strings.Contains(string(body), model.Endpoint) {
		t.Fatalf("live route was not preserved:\n%s", body)
	}
}

func TestHostModelFollowupsCarryOrkaRouteAndOmitIncompatibleGovernance(t *testing.T) {
	a := &App{Cfg: &config.Config{KindCluster: "demo", KubeContext: "kind-demo", ContainerEngine: "docker", Credential: "hello-world"},
		selectedLocalModel: &localModel{Provider: "ollama", Model: "qwen3:8b", Endpoint: "http://172.18.0.1:11434"}}
	next := a.quickstartFollowups("hello-world")
	if len(next) != 3 {
		t.Fatalf("host followups include incompatible governance: %q", next)
	}
	if !strings.Contains(next[1], "--model qwen3:8b") || !strings.Contains(next[1], "--model-url http://172.18.0.1:11434/v1") {
		t.Fatalf("Orka followup lost verified route: %q", next[1])
	}
	for _, command := range next {
		if strings.Contains(command, " plane") || strings.Contains(command, " govern") {
			t.Fatalf("incompatible followup offered: %q", command)
		}
	}
}

func TestNoninteractiveAndExplicitModelSkipDetection(t *testing.T) {
	for name, tc := range map[string]struct {
		allow    bool
		explicit bool
	}{"json": {false, false}, "explicit": {true, true}} {
		t.Run(name, func(t *testing.T) {
			d := &staticLocalDetector{models: []localModel{{Provider: "ollama", Model: "qwen3:8b"}}}
			a := &App{Cfg: &config.Config{Model: config.DefaultModel, ModelExplicit: tc.explicit}, Err: &bytes.Buffer{}, localModels: &localModelEnvironment{
				Detectors: []localModelDetector{d}, Interactive: func() bool { return true },
			}}
			if err := a.maybeSelectLocalModel(tc.allow); err != nil {
				t.Fatal(err)
			}
			if d.calls != 0 {
				t.Fatalf("detector called %d times", d.calls)
			}
		})
	}
}

func TestSelectedModelRendersBothRuntimeInputs(t *testing.T) {
	a := &App{Cfg: &config.Config{Model: config.DefaultModel}, selectedLocalModel: &localModel{
		Provider: "ollama", Model: "qwen3:8b", Endpoint: "http://host.docker.internal:11434",
	}}
	for _, name := range []string{"hello-world.yaml", "kagent-values.yaml"} {
		body, err := a.renderModelManifest(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "model: qwen3:8b") || !strings.Contains(text, "http://host.docker.internal:11434") || strings.Contains(text, "model: qwen2.5:3b") {
			t.Errorf("%s was not rendered with the selection:\n%s", name, text)
		}
	}
}
