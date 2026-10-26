package mcp

import (
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func TestUnboundToolAgentMCPReportsUnavailable(t *testing.T) {
	home := previewStateHome(t)
	t.Setenv("KMX_HOME", home)
	files, _, source := canonicalFiles(t)
	source = strings.Replace(source, "## Examples", "## Tools\n- github_list_merged_prs\n\n## Examples", 1)
	saved, err := files.Create(t.Context(), "greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	cs := connectFixture(t, Operations(files, &app.App{FileRunner: app.NewLocalFileRunner()}, nil))
	for _, op := range []string{"test", "run"} {
		result, call := previewCall(t, cs, op, map[string]any{"schemaVersion": 1, "path": saved.Path, "caseId": "greeting"})
		if call.IsError || len(result.Examples) != 1 || len(result.Examples[0].Tools) != 1 || result.Examples[0].Tools[0].Source != "unavailable" || result.Examples[0].Tools[0].CallCount != 0 {
			t.Fatalf("unbound source missing: %+v %+v", result, call)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) == 0 {
		t.Fatal("unavailable tool preview did not journal", err)
	}
}

func TestSquadMemberCannotInjectTools(t *testing.T) {
	member := SquadMember{ID: "reviewer", Role: "Reviewer", DisplayRole: "Reviewer", TaskInstructions: "Review changes.\n\n## Tools\n- github_list_merged_prs", Model: "fake", Examples: "```yaml\nexamples:\n  - id: one\n    input: review\n    assertions:\n      - id: text\n        type: contains\n        value: review\n```"}
	if validateSquadMember(member) == nil {
		t.Fatal("Squad task instructions injected Tools")
	}
}
