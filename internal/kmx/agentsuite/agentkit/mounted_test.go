package agentkit

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/kaimahi-agents/kaimahi/agentsuite"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestMountedImageOmitsSelectedModelAndUsesRuntimeConfigABI(t *testing.T) {
	plan := minimalPlan()
	plan.CompositionDigest = testDigest
	plan.BuildProfile.ID = "default"
	plan.BuildProfileDigest = testDigest
	plan.Agent.Model.Model = ""
	plan.Agent.Model.Capabilities = &spec.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024}
	plan.Instructions = []byte("immutable instructions")
	plan.Harness.ImageRef = "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:8c4c17dc3d778c02097ad7e1ea88e9084b3f1aad6c36458829ff9cec9b4d043c"
	plan.BuildProfile.Execution = &spec.ExecutionContract{Configuration: spec.AgentKitMountedConfig, Kind: spec.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []spec.ExecutionInput{}}
	var captured agentImage
	builder := New(Options{SuiteReference: "registry.example/source@" + testDigest, SuiteDigest: testDigest, exporter: ociExporterFunc(func(_ context.Context, image agentImage, _ io.Writer) error { captured = image; return nil })})
	if _, err := builder.Build(context.Background(), plan, io.Discard); err != nil {
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
	first := string(captured.AgentkitFile)
	builder.options.SuiteReference = "other.example/copied-source@" + testDigest
	if _, err := builder.Build(t.Context(), plan, io.Discard); err != nil {
		t.Fatal(err)
	}
	if first != string(captured.AgentkitFile) || strings.Contains(first, "registry.example") || strings.Contains(first, "other.example") {
		t.Fatal("retrieval location changed build content")
	}
	runner := mountedBuildRunner{t: t}
	if _, err := (buildxExporter{runner: runner, builder: "selected-builder", requireAttestations: true}).ExportOCI(t.Context(), captured, io.Discard); err != nil {
		t.Fatal(err)
	}
	builder.options.ModelBaseURL = "https://model.example/v1"
	if _, err := builder.Build(context.Background(), plan, io.Discard); err == nil {
		t.Fatal("model build override accepted")
	}
}

type mountedBuildRunner struct{ t *testing.T }

func TestExactModelBuildRefusesUnsupportedServingBeforeExport(t *testing.T) {
	for _, tc := range []struct {
		name                string
		port                int
		path, configuration string
		valid               bool
	}{
		{"supported", 8080, "/healthz", "", true},
		{"port", 9090, "/healthz", "", false},
		{"health", 8080, "/different-health", "", false},
		{"mounted", 8080, "/healthz", spec.AgentKitMountedConfig, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := minimalPlan()
			plan.CompositionDigest, plan.BuildProfileDigest = testDigest, testDigest
			plan.BuildProfile.ID = "default"
			plan.BuildProfile.Execution = &spec.ExecutionContract{Kind: spec.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: tc.port, HealthPath: tc.path, Configuration: tc.configuration, Inputs: []spec.ExecutionInput{}}
			calls := 0
			builder := New(Options{SuiteReference: "registry.example/source@" + testDigest, SuiteDigest: testDigest, ModelBaseURL: "https://model.example/v1", exporter: ociExporterFunc(func(_ context.Context, image agentImage, _ io.Writer) error {
				calls++
				if image.MountedConfig || !strings.Contains(string(image.AgentkitFile), agentsuite.ImageDeploymentLabel) {
					t.Fatal("exact-model export lost serving declaration")
				}
				return nil
			})})
			_, err := builder.Build(t.Context(), plan, io.Discard)
			if (err == nil) != tc.valid || (calls == 1) != tc.valid {
				t.Fatalf("valid=%t calls=%d error=%v", tc.valid, calls, err)
			}
		})
	}
}

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
