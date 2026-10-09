package agentsuite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCapabilitySourceRoundTripAndSelection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hello")
	requirements := InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024}
	if err := CreateHTTPSource(root, CreateRequest{Name: "hello", Instructions: "exact instructions\n", Inference: requirements}); err != nil {
		t.Fatal(err)
	}
	plan, err := ResolveSandboxPlan(root, BuildSelection{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Agent.Model.Model != "" || plan.Agent.Model.Capabilities == nil || *plan.Agent.Model.Capabilities != requirements || string(plan.Instructions) != "exact instructions\n" {
		t.Fatalf("wrong projection: %+v", plan.Agent)
	}
	if err := CreateHTTPSource(root, CreateRequest{Name: "hello", Instructions: "overwrite", Inference: requirements}); err == nil {
		t.Fatal("existing source overwritten")
	}
	data, err := os.ReadFile(filepath.Join(root, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	validateSchemaJSON(t, compileReferenceSchema(t, "agent.schema.json"), string(data), true)
}

func TestInferenceRequirementsRejectInsufficientCapabilities(t *testing.T) {
	required := InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024, Streaming: true}
	if err := required.Match(required); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*InferenceRequirements){
		func(r *InferenceRequirements) { r.ContextTokens = 4096 },
		func(r *InferenceRequirements) { r.OutputTokens = 512 },
		func(r *InferenceRequirements) { r.Streaming = false },
		func(r *InferenceRequirements) { r.API = "responses" },
	} {
		candidate := required
		change(&candidate)
		if required.Match(candidate) == nil {
			t.Fatalf("accepted %+v", candidate)
		}
	}
}
