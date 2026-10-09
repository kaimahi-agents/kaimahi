package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBuildEmitsArchiveThroughBuildxExporter(t *testing.T) {
	var exported agentImage
	exporter := ociExporterFunc(func(_ context.Context, image agentImage, dst io.Writer) error {
		exported = image
		_, err := io.WriteString(dst, "oci archive")
		return err
	})
	builder := New(Options{
		ModelBaseURL:   "https://models.example/v1",
		ModelAPIKeyEnv: "AZURE_OPENAI_API_KEY",
		exporter:       exporter,
	})
	var output bytes.Buffer
	result, err := builder.Build(t.Context(), minimalPlan(), &output)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if output.String() != "oci archive" || result.MediaType != OCIArchiveMediaType {
		t.Fatalf("unexpected result: output=%q result=%+v", output.String(), result)
	}
	if exported.Name != "writer" ||
		exported.AdapterRef != "registry.example/harness@"+testDigest ||
		exported.Platform != "linux/amd64" || exported.SourceEpoch != 1 {
		t.Fatalf("exported image = %+v", exported)
	}
	config := decodeAgentkitFile(t, exported.AgentkitFile)
	if config.Metadata.Name != "writer" ||
		config.Instructions != "Write clearly.\n" ||
		config.Model.BaseURL != "https://models.example/v1" ||
		config.Model.APIKeyEnv != "AZURE_OPENAI_API_KEY" {
		t.Fatalf("AgentKit config = %+v", config)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "not AgentSuite-conformant") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestAgentkitFileMapsResolvedPlan(t *testing.T) {
	plan := minimalPlan()
	builder := New(Options{
		ModelBaseURL:   "https://example.openai.azure.com/openai/v1/",
		ModelAPIKeyEnv: "AZURE_OPENAI_API_KEY",
	})
	data, err := builder.agentkitFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("#syntax="+agentKitFrontend+"\n")) {
		t.Fatalf("AgentKit file header = %q", data)
	}
	config := decodeAgentkitFile(t, data)
	if config.APIVersion != "v1alpha1" || config.Kind != "Agent" ||
		config.Metadata.Name != plan.Agent.ID || config.Runtime != agentKitRuntime {
		t.Fatalf("identity/runtime = %+v", config)
	}
	if config.Model.Provider != plan.Agent.Model.Protocol ||
		config.Model.Name != plan.Agent.Model.Model ||
		config.Model.BaseURL != "https://example.openai.azure.com/openai/v1/" ||
		config.Model.APIKeyEnv != "AZURE_OPENAI_API_KEY" {
		t.Fatalf("model = %+v", config.Model)
	}
	if config.Instructions != string(plan.Instructions) || !config.Expose.OpenAI {
		t.Fatalf("instructions/expose = %+v", config)
	}
}

func TestBuildRejectsUnsupportedAgentSuiteFeatures(t *testing.T) {
	plan := minimalPlan()
	plan.Agent.Invokes = []agentsuite.AgentInvoke{{Agent: "reviewer", MaxConcurrent: 1, MaxDepth: 1}}
	plan.ToolProviders = []agentsuite.SelectedToolProvider{{ID: "search"}}
	err := buildError(New(Options{ModelBaseURL: "https://models.example/v1"}), plan)
	for _, want := range []string{"invocation edges", "ToolProviders"} {
		if !strings.Contains(err, want) {
			t.Fatalf("Build() error = %q, want %q", err, want)
		}
	}
}

func TestBuildRejectsInvalidPlanMappings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*agentsuite.SandboxPlan)
		want   string
	}{
		{
			name: "unsupported protocol",
			mutate: func(plan *agentsuite.SandboxPlan) {
				plan.Agent.Model.Protocol = "anthropic"
			},
			want: "does not support model protocol",
		},
		{
			name: "endpoint environment",
			mutate: func(plan *agentsuite.SandboxPlan) {
				plan.Agent.Model.EndpointEnv = "MODEL_ENDPOINT"
			},
			want: "model.endpointEnv",
		},
		{
			name: "secret references",
			mutate: func(plan *agentsuite.SandboxPlan) {
				plan.Agent.Model.SecretRefs = []string{"model-key"}
			},
			want: "model.secretRefs",
		},
		{
			name: "missing runtime base",
			mutate: func(plan *agentsuite.SandboxPlan) {
				plan.RuntimeBase.ImageRef = ""
			},
			want: "runtime-base image reference is required",
		},
		{
			name: "unpinned harness",
			mutate: func(plan *agentsuite.SandboxPlan) {
				plan.Harness.ImageRef = "registry.example/harness:latest"
			},
			want: "digest-addressed",
		},
		{
			name: "unsupported platform",
			mutate: func(plan *agentsuite.SandboxPlan) {
				plan.Composition.Platform.Architecture = "s390x"
			},
			want: "does not support platform linux/s390x",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := minimalPlan()
			test.mutate(&plan)
			err := buildError(New(Options{ModelBaseURL: "https://models.example/v1"}), plan)
			if !strings.Contains(err, test.want) {
				t.Fatalf("Build() error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestBuildRejectsUnsafeModelURLs(t *testing.T) {
	for _, value := range []string{
		"https://models.example/v1?api-key=secret",
		"https://models.example/v1?",
		"******models.example/v1",
		"https://:443/v1",
		"https://models.example/v1#fragment",
	} {
		t.Run(value, func(t *testing.T) {
			err := buildError(New(Options{ModelBaseURL: value}), minimalPlan())
			if !strings.Contains(err, "--model-base-url") {
				t.Fatalf("Build() error = %q", err)
			}
		})
	}
}

func TestAgentkitFilePreservesModelAPIKeyEnvironmentName(t *testing.T) {
	builder := New(Options{
		ModelBaseURL:   "https://models.example/v1",
		ModelAPIKeyEnv: "sk_model_api_key",
	})
	data, err := builder.agentkitFile(minimalPlan())
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeAgentkitFile(t, data).Model.APIKeyEnv; got != "sk_model_api_key" {
		t.Fatalf("model apiKeyEnv = %q", got)
	}
}

func TestBuildRejectsInvalidInputsAndExporterFailure(t *testing.T) {
	var nilBuilder *Builder
	if _, err := nilBuilder.Build(t.Context(), minimalPlan(), io.Discard); err == nil ||
		!strings.Contains(err.Error(), "builder is required") {
		t.Fatalf("nil Build() error = %v", err)
	}
	builder := New(Options{ModelBaseURL: "https://models.example/v1"})
	if _, err := builder.Build(t.Context(), minimalPlan(), nil); err == nil ||
		!strings.Contains(err.Error(), "output is required") {
		t.Fatalf("nil output Build() error = %v", err)
	}
	builder = New(Options{
		ModelBaseURL: "https://models.example/v1",
		exporter: ociExporterFunc(func(context.Context, agentImage, io.Writer) error {
			return errors.New("export failed")
		}),
	})
	if _, err := builder.Build(t.Context(), minimalPlan(), io.Discard); err == nil ||
		!strings.Contains(err.Error(), "build experimental AgentKit image: export failed") {
		t.Fatalf("export Build() error = %v", err)
	}
}

func TestValidateDigestReferenceRejectsInvalidReferences(t *testing.T) {
	for _, value := range []string{
		"registry.example/harness:latest",
		"registry.example/harness@sha256:" + strings.Repeat("g", 64),
	} {
		if err := validateDigestReference("harness", value); err == nil ||
			!strings.Contains(err.Error(), "digest-addressed") {
			t.Fatalf("validateDigestReference(%q) error = %v", value, err)
		}
	}
}

func TestBuildRequiresModelURL(t *testing.T) {
	err := buildError(New(Options{}), minimalPlan())
	if !strings.Contains(err, "--model-base-url") {
		t.Fatalf("Build() error = %q", err)
	}
}

func decodeAgentkitFile(t *testing.T, data []byte) agentConfig {
	t.Helper()
	_, body, ok := bytes.Cut(data, []byte{'\n'})
	if !ok {
		t.Fatalf("AgentKit file has no body: %q", data)
	}
	var config agentConfig
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatalf("decode AgentKit file: %v\n%s", err, data)
	}
	return config
}

func buildError(builder *Builder, plan agentsuite.SandboxPlan) string {
	_, err := builder.Build(context.Background(), plan, io.Discard)
	if err == nil {
		return ""
	}
	return err.Error()
}

func minimalPlan() agentsuite.SandboxPlan {
	platform := agentsuite.Platform{OS: "linux", Architecture: "amd64"}
	return agentsuite.SandboxPlan{
		Agent: agentsuite.Agent{
			ID: "writer",
			Model: agentsuite.ModelRequirement{
				Protocol: "openai-compatible",
				Model:    "example-model",
			},
		},
		Instructions: []byte("Write clearly.\n"),
		Composition: agentsuite.Composition{
			Agent: "writer", Platform: platform, BuildProfile: "default",
		},
		BuildProfile: agentsuite.BuildProfile{SourceEpoch: 1},
		RuntimeBase: agentsuite.PlatformImage{
			Platform: platform, ImageRef: "registry.example/runtime@" + testDigest,
		},
		Harness: agentsuite.PlatformImage{
			Platform: platform, ImageRef: "registry.example/harness@" + testDigest,
		},
	}
}

type ociExporterFunc func(context.Context, agentImage, io.Writer) error

func (f ociExporterFunc) ExportOCI(ctx context.Context, image agentImage, dst io.Writer) error {
	return f(ctx, image, dst)
}
