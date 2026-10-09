package imagelift

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestModelRebindChangesConfigAndPlanButPreservesImageAndResourceUIDs(t *testing.T) {
	env, record, old := example(t)
	requirements := agentsuite.InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024}
	record.Inference = &requirements
	record.Execution.Configuration = agentsuite.AgentKitMountedConfig
	record.Execution.Inputs = []agentsuite.ExecutionInput{}
	env.Inputs = map[string]InputBinding{}
	env.Inference = &InferenceBinding{Model: "model-a", Endpoint: "http://model.agents.svc/v1", Capabilities: requirements, Evidence: "operator-declared"}
	env.Instructions = "immutable source instructions"
	first, err := Render(old.Image, record, env)
	if err != nil {
		t.Fatal(err)
	}
	cluster := newFake()
	receipt, err := Deploy(context.Background(), cluster, first, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env.Inference.Model = "model-b"
	second, err := Render(old.Image, record, env)
	if err != nil {
		t.Fatal(err)
	}
	if first.Image != second.Image || first.Digest == second.Digest {
		t.Fatal("model rebind identity is incorrect")
	}
	newReceipt, err := Deploy(context.Background(), cluster, second, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range receipt.Resources {
		if r.UID != newReceipt.Resources[i].UID {
			t.Fatal("resource identity changed")
		}
	}
	var abi struct {
		Model        struct{ Name string }
		Instructions string
	}
	if err := json.Unmarshal([]byte(second.Objects[0]["data"].(Object)["agent.json"].(string)), &abi); err != nil {
		t.Fatal(err)
	}
	if abi.Model.Name != "model-b" || abi.Instructions != env.Instructions {
		t.Fatal("runtime ABI differs")
	}
	env.Inference.Capabilities.ContextTokens = 4096
	if _, err := Render(old.Image, record, env); err == nil {
		t.Fatal("insufficient capabilities accepted")
	}
}
