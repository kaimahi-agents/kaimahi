package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	suitecore "github.com/agent-primitives/suite/core"
	kmxsuite "github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestMountedImageOmitsSelectedModelAndUsesRuntimeConfigABI(t *testing.T) {
	plan := minimalPlan()
	plan.CompositionDigest = testDigest
	plan.BuildProfile.ID = "default"
	plan.BuildProfileDigest = testDigest
	plan.Agent.Model.Model = ""
	plan.Agent.Model.Capabilities = &suitecore.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024}
	plan.Instructions = []byte("immutable instructions")
	plan.Harness.ImageRef = "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:8c4c17dc3d778c02097ad7e1ea88e9084b3f1aad6c36458829ff9cec9b4d043c"
	plan.BuildProfile.Execution = &suitecore.ExecutionContract{Configuration: suitecore.AgentKitMountedConfig, Kind: suitecore.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []suitecore.ExecutionInput{}}
	plan.BuildProfile.Execution.Inputs = []suitecore.ExecutionInput{{Name: "listen", Environment: "AGENTKIT_BIND"}, {Name: "agent-auth", Environment: "AGENTKIT_AUTH_TOKEN", Secret: true}}
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
	_, labelLine, ok := strings.Cut(string(captured.AgentkitFile), "LABEL "+kmxsuite.ImageDeploymentLabel+"=")
	if !ok {
		t.Fatal("missing declaration")
	}
	label, err := strconv.Unquote(strings.TrimSpace(labelLine))
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := kmxsuite.DecodeImageDeployment([]byte(label))
	if err != nil {
		t.Fatal(err)
	}
	if len(declaration.Execution.Inputs) != 2 || !strings.Contains(string(captured.AgentkitFile), "AGENTKIT_BIND=127.0.0.1") {
		t.Fatal("mounted serving inputs/config not represented together")
	}
	first := string(captured.AgentkitFile)
	badPlatform := plan
	badPlatform.Composition.Platform.Architecture = "arm64"
	refusingBuilder := New(builder.options)
	refusingBuilder.options.exporter = ociExporterFunc(func(context.Context, agentImage, io.Writer) error {
		t.Fatal("amd64-only mounted harness reached arm64 exporter")
		return nil
	})
	if _, err := refusingBuilder.Build(t.Context(), badPlatform, io.Discard); err == nil || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("unqualified mounted platform accepted: %v", err)
	}
	for _, inputs := range [][]suitecore.ExecutionInput{nil, {{Name: "listen", Environment: "AGENTKIT_BIND"}}, {{Name: "auth", Environment: "AGENTKIT_AUTH_TOKEN", Secret: true}}} {
		bad := plan
		execution := *plan.BuildProfile.Execution
		execution.Inputs = inputs
		bad.BuildProfile.Execution = &execution
		if _, err := builder.Build(t.Context(), bad, io.Discard); err == nil {
			t.Fatal("incomplete mounted serving inputs accepted")
		}
	}
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

func TestAgentKitCredentialDeclarationMatchesGeneratedConfig(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprint(declared), func(t *testing.T) {
			plan := minimalPlan()
			plan.CompositionDigest = testDigest
			plan.BuildProfileDigest = testDigest
			plan.BuildProfile.ID = "default"
			plan.BuildProfile.Execution = &suitecore.ExecutionContract{Kind: suitecore.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: 8080, HealthPath: "/healthz", Inputs: []suitecore.ExecutionInput{}}
			if declared {
				plan.BuildProfile.Execution.Inputs = []suitecore.ExecutionInput{{Name: "model-key", Environment: "MODEL_API_KEY", Secret: true}}
			}
			calls := 0
			b := New(Options{SuiteReference: "registry.example/suite@" + testDigest, SuiteDigest: testDigest, ModelBaseURL: "https://model.example/v1", ModelAPIKeyEnv: "MODEL_API_KEY", exporter: ociExporterFunc(func(_ context.Context, img agentImage, _ io.Writer) error {
				calls++
				_, body, _ := strings.Cut(string(img.AgentkitFile), "\n")
				var config agentConfig
				if err := json.Unmarshal([]byte(body), &config); err != nil {
					t.Fatal(err)
				}
				r, err := kmxsuite.DecodeImageDeployment([]byte(config.Metadata.Labels[kmxsuite.ImageDeploymentLabel]))
				if err != nil {
					t.Fatal(err)
				}
				if config.Model.APIKeyEnv != "MODEL_API_KEY" || len(r.Execution.Inputs) != 1 || r.Execution.Inputs[0].Environment != config.Model.APIKeyEnv || !r.Execution.Inputs[0].Secret {
					t.Fatal("config/declaration disagreement")
				}
				return nil
			})})
			_, err := b.Build(t.Context(), plan, io.Discard)
			if (err == nil) != declared || calls != btoi(declared) {
				t.Fatalf("declared=%t calls=%d error=%v", declared, calls, err)
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
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
		{"mounted", 8080, "/healthz", suitecore.AgentKitMountedConfig, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := minimalPlan()
			plan.CompositionDigest, plan.BuildProfileDigest = testDigest, testDigest
			plan.BuildProfile.ID = "default"
			plan.BuildProfile.Execution = &suitecore.ExecutionContract{Kind: suitecore.ExecutionHTTPV1, Protocol: "openai-chat-v1", Port: tc.port, HealthPath: tc.path, Configuration: tc.configuration, Inputs: []suitecore.ExecutionInput{}}
			calls := 0
			builder := New(Options{SuiteReference: "registry.example/source@" + testDigest, SuiteDigest: testDigest, ModelBaseURL: "https://model.example/v1", exporter: ociExporterFunc(func(_ context.Context, image agentImage, _ io.Writer) error {
				calls++
				if image.MountedConfig || !strings.Contains(string(image.AgentkitFile), kmxsuite.ImageDeploymentLabel) {
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
	if err != nil || !strings.Contains(string(data), kmxsuite.AgentKitConfigPath) {
		r.t.Fatalf("configuration ABI recipe: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "agentkitfile.yaml")); !os.IsNotExist(err) {
		r.t.Fatal("mounted build also supplied legacy config")
	}
	return nil
}
