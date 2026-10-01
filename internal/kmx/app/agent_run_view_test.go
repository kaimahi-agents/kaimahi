package app

import (
	"testing"
)

func TestOrkaRunHelpersReadCurrentPolicyNotSpecText(t *testing.T) {
	raw := []byte(`{"spec":{"coordination":{"allowedAgents":[{"name":"receiving"},{"name":"purchasing"}]},"systemPrompt":"PRIVATE PROMPT"}}`)
	names, err := orkaRunHelpers(raw)
	if err != nil || len(names) != 2 || names[0] != "receiving" || names[1] != "purchasing" {
		t.Fatalf("helpers=%v err=%v", names, err)
	}
}
