package agentsuite

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSandboxPlanSelectsMinimalComposition(t *testing.T) {
	plan, err := ResolveSandboxPlan(filepath.Join("testdata", "minimal"), BuildSelection{})
	if err != nil {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
	if plan.Agent.ID != "writer" || plan.Composition.Platform.String() != "linux/amd64" {
		t.Fatalf("unexpected plan selection: agent=%q platform=%s", plan.Agent.ID, plan.Composition.Platform)
	}
	if plan.RuntimeBase.ImageRef == "" || plan.Harness.ImageRef == "" {
		t.Fatalf("plan did not resolve build images: runtime=%q harness=%q", plan.RuntimeBase.ImageRef, plan.Harness.ImageRef)
	}
	if got := string(plan.Instructions); got != "Write clearly.\n" {
		t.Fatalf("instructions = %q", got)
	}
	if plan.CompositionDigest == "" || plan.SuiteManifestHash == "" {
		t.Fatalf("plan identities are incomplete: %+v", plan)
	}
}

func TestResolveSandboxPlanSelectsBuildableIncidentAnalystExample(t *testing.T) {
	plan, err := ResolveSandboxPlan(filepath.Join("testdata", "incident-analyst"), BuildSelection{})
	if err != nil {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
	if plan.Agent.ID != "incident-analyst" || plan.Agent.Model.Model != "gpt-5-mini" {
		t.Fatalf("unexpected agent: %+v", plan.Agent)
	}
	if got := plan.RuntimeBase.ImageRef; !strings.HasPrefix(got, "docker.io/library/python@sha256:") {
		t.Fatalf("runtime base = %q", got)
	}
	if got := plan.Harness.ImageRef; !strings.HasPrefix(got, "ghcr.io/orka-agents/agentkit/serve-pydantic-ai@sha256:") {
		t.Fatalf("harness = %q", got)
	}
	if !strings.Contains(string(plan.Instructions), "incident response analyst") {
		t.Fatalf("unexpected instructions: %q", plan.Instructions)
	}
}

func TestResolveSandboxPlanRequiresAgentForMultiAgentSuite(t *testing.T) {
	_, err := ResolveSandboxPlan(filepath.Join("testdata", "coordinator-workers"), BuildSelection{})
	if err == nil || !strings.Contains(err.Error(), "--agent is required") {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
}

func TestResolveSandboxPlanRejectsUnknownPlatform(t *testing.T) {
	_, err := ResolveSandboxPlan(filepath.Join("testdata", "minimal"), BuildSelection{
		Agent:    "writer",
		Platform: "linux/arm64",
	})
	if err == nil || !strings.Contains(err.Error(), "no composition") {
		t.Fatalf("ResolveSandboxPlan() error = %v", err)
	}
}

func TestResolveSelectedToolProvidersMarksEveryDeclaredRoot(t *testing.T) {
	platform := Platform{OS: "linux", Architecture: "amd64"}
	dependencyVariant := ToolProviderVariant{
		Platform: platform, VariantDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	rootVariant := ToolProviderVariant{
		Platform:      platform,
		VariantDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Dependencies: []ToolProviderDependency{{
			ID: "shared", Version: "1.0.0", VariantDigest: dependencyVariant.VariantDigest,
		}},
	}
	v := &validator{
		toolProviders: map[string]ToolProvider{
			"root@1.0.0":   {ID: "root", Version: "1.0.0", Variants: []ToolProviderVariant{rootVariant}},
			"shared@1.0.0": {ID: "shared", Version: "1.0.0", Variants: []ToolProviderVariant{dependencyVariant}},
		},
		toolProviderDigests: map[string]string{
			"root@1.0.0":   "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"shared@1.0.0": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		},
	}
	selected, err := resolveSelectedToolProviders(v, Composition{
		Platform: platform,
		ToolProviders: []ResolvedToolProvider{
			{ID: "root", Version: "1.0.0"},
			{ID: "shared", Version: "1.0.0"},
		},
	})
	if err != nil {
		t.Fatalf("resolveSelectedToolProviders() error = %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("selected providers = %+v", selected)
	}
	for _, provider := range selected {
		if !provider.Root {
			t.Fatalf("declared provider %s@%s is not marked as a root", provider.ID, provider.Version)
		}
	}
}
