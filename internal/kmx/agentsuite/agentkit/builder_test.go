package agentkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestBuildEmitsArchiveThroughExporter(t *testing.T) {
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
	result, err := builder.Build(context.Background(), minimalPlan(), &output)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if output.String() != "oci archive" || result.MediaType != OCIArchiveMediaType {
		t.Fatalf("unexpected result: output=%q result=%+v", output.String(), result)
	}
	if exported.AdapterRef != "registry.example/harness@"+testDigest ||
		exported.OS != "linux" || exported.Architecture != "amd64" {
		t.Fatalf("exported image = %+v", exported)
	}
	if exported.Agent.Metadata.Name != "writer" ||
		exported.Agent.Instructions != "Write clearly.\n" ||
		exported.Agent.Model.BaseURL != "https://models.example/v1" ||
		exported.Agent.Model.APIKeyEnv != "AZURE_OPENAI_API_KEY" {
		t.Fatalf("effective AgentKit agent = %+v", exported.Agent)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "not AgentSuite-conformant") {
		t.Fatalf("warnings = %v", result.Warnings)
	}
}

func TestAgentConfigMapsResolvedPlan(t *testing.T) {
	plan := minimalPlan()
	builder := New(Options{
		ModelBaseURL:   "https://example.openai.azure.com/openai/v1/",
		ModelAPIKeyEnv: "AZURE_OPENAI_API_KEY",
		Runtime:        "pydantic-ai",
	})

	cfg := builder.agentConfig(plan)

	if cfg.APIVersion != "v1alpha1" || cfg.Kind != "Agent" {
		t.Fatalf("type metadata = apiVersion %q kind %q", cfg.APIVersion, cfg.Kind)
	}
	if cfg.Metadata.Name != plan.Agent.ID || cfg.Runtime != "pydantic-ai" {
		t.Fatalf("identity/runtime = metadata %+v runtime %q", cfg.Metadata, cfg.Runtime)
	}
	if cfg.Model.Provider != plan.Agent.Model.Protocol ||
		cfg.Model.Name != plan.Agent.Model.Model ||
		cfg.Model.BaseURL != "https://example.openai.azure.com/openai/v1/" ||
		cfg.Model.APIKeyEnv != "AZURE_OPENAI_API_KEY" {
		t.Fatalf("model = %+v", cfg.Model)
	}
	if cfg.Instructions.Inline != string(plan.Instructions) || cfg.Instructions.File != "" {
		t.Fatalf("instructions = %+v", cfg.Instructions)
	}
	if !cfg.Expose.OpenAI || cfg.Expose.Port != 0 {
		t.Fatalf("expose = %+v", cfg.Expose)
	}
	if len(cfg.Tools) != 0 || len(cfg.BrokeredTools) != 0 || len(cfg.Env) != 0 || len(cfg.Context.Providers) != 0 {
		t.Fatalf("unexpected unsupported AgentKit fields: %+v", cfg)
	}
}

func TestBuildRejectsUnsupportedAgentSuiteFeatures(t *testing.T) {
	plan := minimalPlan()
	plan.Agent.Invokes = []agentsuite.AgentInvoke{{Agent: "reviewer", MaxConcurrent: 1, MaxDepth: 1}}
	plan.ToolProviders = []agentsuite.SelectedToolProvider{{ID: "search"}}
	builder := New(Options{
		ModelBaseURL: "https://models.example/v1",
	})
	err := buildError(builder, plan)
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
			builder := New(Options{ModelBaseURL: "https://models.example/v1"})
			err := buildError(builder, plan)
			if !strings.Contains(err, test.want) {
				t.Fatalf("Build() error = %q, want %q", err, test.want)
			}
		})
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
	tests := []struct {
		value string
		want  string
	}{
		{"registry.example/harness:latest", "digest-addressed"},
		{"registry.example/harness@sha256:" + strings.Repeat("g", 64), "digest-addressed"},
	}
	for _, test := range tests {
		if err := validateDigestReference("harness", test.value); err == nil ||
			!strings.Contains(err.Error(), test.want) {
			t.Fatalf("validateDigestReference(%q) error = %v", test.value, err)
		}
	}
}

func TestNewUsesBuildkitHost(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "tcp://buildkit.example:1234")
	builder := New(Options{})
	if builder.options.BuildkitAddress != "tcp://buildkit.example:1234" {
		t.Fatalf("BuildkitAddress = %q", builder.options.BuildkitAddress)
	}
}

func TestNewUsesManagedBuildkitByDefault(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", "")
	builder := New(Options{})
	exporter, ok := builder.options.exporter.(buildkitExporter)
	if !ok {
		t.Fatalf("exporter = %T", builder.options.exporter)
	}
	if exporter.address != managedBuildkitAddress || exporter.manager == nil {
		t.Fatalf("managed exporter = %+v", exporter)
	}
}

func TestNewManagesConfiguredKMXBuildkitAddress(t *testing.T) {
	t.Setenv("BUILDKIT_HOST", managedBuildkitAddress)
	builder := New(Options{})
	exporter, ok := builder.options.exporter.(buildkitExporter)
	if !ok {
		t.Fatalf("exporter = %T", builder.options.exporter)
	}
	if exporter.address != managedBuildkitAddress || exporter.manager == nil {
		t.Fatalf("managed exporter = %+v", exporter)
	}
}

func TestBuildRequiresModelURL(t *testing.T) {
	builder := New(Options{})
	err := buildError(builder, minimalPlan())
	if !strings.Contains(err, "--model-base-url") {
		t.Fatalf("Build() error = %q", err)
	}
}

func TestBuildRejectsInvalidModelAPIKeyEnvironmentName(t *testing.T) {
	builder := New(Options{
		ModelBaseURL:   "https://example.openai.azure.com/openai/v1/",
		ModelAPIKeyEnv: "not-valid",
	})
	err := buildError(builder, minimalPlan())
	if !strings.Contains(err, "apiKeyEnv") {
		t.Fatalf("Build() error = %q", err)
	}
}

func TestBuildRejectsUnknownAgentKitRuntime(t *testing.T) {
	builder := New(Options{
		Runtime:      "unknown",
		ModelBaseURL: "https://models.example/v1",
	})
	err := buildError(builder, minimalPlan())
	if !strings.Contains(err, "runtime \"unknown\" is not supported") {
		t.Fatalf("Build() error = %q", err)
	}
}

func minimalPlan() agentsuite.SandboxPlan {
	platform := agentsuite.Platform{OS: "linux", Architecture: "amd64"}
	return agentsuite.SandboxPlan{
		Agent: agentsuite.Agent{
			ID:           "writer",
			Instructions: agentsuite.FileRef{Path: "instructions/writer.md", Digest: testDigest},
			Model:        agentsuite.ModelRequirement{Protocol: "openai-compatible", Model: "example-model"},
		},
		Instructions: []byte("Write clearly.\n"),
		Composition: agentsuite.Composition{
			Agent: "writer", Platform: platform, BuildProfile: "default",
		},
		RuntimeBase: agentsuite.PlatformImage{
			Platform: platform, ImageRef: "registry.example/runtime@" + testDigest,
		},
		Harness: agentsuite.PlatformImage{
			Platform: platform, ImageRef: "registry.example/harness@" + testDigest,
		},
	}
}

func buildError(builder *Builder, plan agentsuite.SandboxPlan) string {
	var output bytes.Buffer
	_, err := builder.Build(context.Background(), plan, &output)
	if err == nil {
		return ""
	}
	return err.Error()
}

type ociExporterFunc func(context.Context, agentImage, io.Writer) error

func (fn ociExporterFunc) ExportOCI(ctx context.Context, image agentImage, dst io.Writer) error {
	return fn(ctx, image, dst)
}
