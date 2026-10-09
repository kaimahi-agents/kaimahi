package agentkit

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestMountedImageOmitsSelectedModelAndUsesRuntimeConfigABI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sample")
	if err := agentsuite.CreateHTTPSource(root, agentsuite.CreateRequest{Name: "sample", Instructions: "immutable instructions", Inference: agentsuite.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024}}); err != nil {
		t.Fatal(err)
	}
	plan, err := agentsuite.ResolveSandboxPlan(root, agentsuite.BuildSelection{})
	if err != nil {
		t.Fatal(err)
	}
	var captured agentImage
	builder := New(Options{SuiteReference: "registry.example/source@" + testDigest, SuiteDigest: testDigest, exporter: ociExporterFunc(func(_ context.Context, image agentImage, _ io.Writer) error { captured = image; return nil })})
	if _, err := builder.Build(context.Background(), *plan, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !captured.MountedConfig || strings.Contains(string(captured.AgentkitFile), "baseURL") {
		t.Fatal("model selection entered build")
	}
	if !strings.Contains(string(captured.AgentkitFile), `CMD ["--config","/run/agentsuite/agent.json","--protocol","openai"]`) || strings.Contains(string(captured.AgentkitFile), "/agent/agent.yaml") || strings.Contains(string(captured.AgentkitFile), "RUN ") {
		t.Fatal("unexpected build recipe")
	}
	if string(captured.Instructions) != "immutable instructions" {
		t.Fatal("instructions changed")
	}
	runner := mountedBuildRunner{t: t}
	if _, err := (buildxExporter{runner: runner, builder: "selected-builder", requireAttestations: true}).ExportOCI(t.Context(), captured, io.Discard); err != nil {
		t.Fatal(err)
	}
	builder.options.ModelBaseURL = "https://model.example/v1"
	if _, err := builder.Build(context.Background(), *plan, io.Discard); err == nil {
		t.Fatal("model build override accepted")
	}
}

type mountedBuildRunner struct{ t *testing.T }

func (r mountedBuildRunner) Run(_ context.Context, _ io.Writer, _ io.Writer, name string, args ...string) error {
	if len(args) == 2 && args[1] == "version" {
		return nil
	}
	command := strings.Join(args, " ")
	for _, want := range []string{"--builder selected-builder", "--sbom=true", "--provenance=mode=max"} {
		if !strings.Contains(command, want) {
			r.t.Fatalf("missing %s: %s", want, command)
		}
	}
	root := args[len(args)-1]
	data, err := os.ReadFile(filepath.Join(root, "instructions.txt"))
	if err != nil || string(data) != "immutable instructions" {
		r.t.Fatalf("build input: %q %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil || !strings.Contains(string(data), agentsuite.AgentKitConfigPath) {
		r.t.Fatalf("configuration ABI recipe: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "agentkitfile.yaml")); !os.IsNotExist(err) {
		r.t.Fatal("mounted build also supplied legacy config")
	}
	return nil
}
