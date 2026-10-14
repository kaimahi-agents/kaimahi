package agentsuite

import "testing"

func TestInferenceCapabilitiesMatchAndRefuse(t *testing.T) {
	r := InferenceRequirements{API: "openai-chat-completions-v1", ContextTokens: 8192, OutputTokens: 1024, Streaming: true}
	if err := r.Match(r); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*InferenceRequirements){func(c *InferenceRequirements) { c.ContextTokens = 4096 }, func(c *InferenceRequirements) { c.OutputTokens = 512 }, func(c *InferenceRequirements) { c.Streaming = false }, func(c *InferenceRequirements) { c.API = "responses" }} {
		c := r
		change(&c)
		if r.Match(c) == nil {
			t.Fatal("incompatible inference accepted")
		}
	}
}
