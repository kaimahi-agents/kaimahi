package localagent

import (
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestAuthoredRequirementsCannotBeWeakened(t *testing.T) {
	pair := &agentfile.Pair{Agent: &agentfile.Agent{Frontmatter: "name: sample\nmodelRequirements:\n  contextTokens: 32768\n  outputTokens: 2048\n  reasoning: true\n"}}
	r, err := ModelRequirements(pair, modelaccess.Requirements{ContextTokens: 4096, OutputTokens: 4096})
	if err != nil || r.ContextTokens != 32768 || r.OutputTokens != 4096 || !r.Reasoning {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{"[]", "{contextToken: 8192}", "{contextTokens: -1}", "{reasoning: true, reasoning: false}"} {
		pair.Agent.Frontmatter = "modelRequirements: " + bad
		if _, err := ModelRequirements(pair, modelaccess.Requirements{}); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestRetainedCapabilitiesRejectDuplicateFields(t *testing.T) {
	var record Instance
	if err := strictLocalAgentJSON([]byte(`{"capabilities":{"reasoning":false,"reasoning":true}}`), &record); err == nil {
		t.Fatal("ambiguous retained capabilities accepted")
	}
}
