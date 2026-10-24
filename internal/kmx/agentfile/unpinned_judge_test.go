package agentfile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func unpinnedJudgeSource(t *testing.T) string {
	t.Helper()
	return strings.Replace(sample(t), "type: contains\n        value: Hello, Ada", "type: llmJudge\n        rubric: Greets the user.\n        minScore: 80\n        judgeVersion: v1", 1)
}

func TestCanonicalUnpinnedJudge(t *testing.T) {
	source := unpinnedJudgeSource(t)
	agent, err := Parse("greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatalf("canonical unpinned judge refused: %v", err)
	}
	assertion := agent.Examples[0].Assertions[0]
	if assertion.JudgeModel != "" || assertion.JudgeVersion != "v1" {
		t.Fatalf("authored identity changed: %+v", assertion)
	}
	encoded, err := json.Marshal(assertion)
	if err != nil || strings.Contains(string(encoded), "judgeModel") {
		t.Fatalf("authored default serialized: %s / %v", encoded, err)
	}
	if runtime.ValidateEvaluationAssertions(agent.Examples[0].Assertions) == nil {
		t.Fatal("strict execution validator accepted missing model")
	}
	path := exportSource(t, source)
	data, err := ExportRunProfile(path)
	if err == nil || data != nil {
		t.Fatalf("strict export accepted unbound judge: %s / %v", data, err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != source {
		t.Fatal("export changed authored source")
	}
	if _, err := Parse("greeter.agent.md", []byte(strings.Replace(source, "minScore: 80", "minScore: 101", 1))); err == nil || !strings.Contains(err.Error(), "judgeModel is optional") {
		t.Fatalf("canonical validation did not explain optional model: %v", err)
	}
}

func TestNativeSquadJudgeRemainsPinned(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	source := strings.Replace(string(enrichedSquad(raw)), "type: contains\n        value: Hello, Ada", "type: llmJudge\n        rubric: Greets the user.\n        minScore: 80\n        judgeVersion: v1", 1)
	if doc, err := ParseSquad(filename, []byte(source), profile); err == nil || doc != nil {
		t.Fatal("native parser accepted unpinned judge")
	} else if strings.Contains(err.Error(), "judgeModel is optional") || !strings.Contains(err.Error(), "judgeModel and judgeVersion") {
		t.Fatalf("native validation did not explain required model: %v", err)
	}
}

func TestUnpinnedJudgeStillRequiresValidVersionAndOperands(t *testing.T) {
	source := unpinnedJudgeSource(t)
	for _, bad := range []string{
		strings.Replace(source, "        judgeVersion: v1\n", "", 1),
		strings.Replace(source, "judgeVersion: v1", "judgeVersion: ' '", 1),
		strings.Replace(source, "judgeVersion: v1", "judgeVersion: null", 1),
		strings.Replace(source, "judgeVersion: v1", "judgeVersion: modèle", 1),
		strings.Replace(source, "judgeVersion: v1", "judgeVersion: v1\n        judgeModel: ''", 1),
		strings.Replace(source, "minScore: 80", "minScore: 101", 1),
		strings.Replace(source, "input: Ada", "input: ghp_"+strings.Repeat("a", 36), 1),
	} {
		if got, err := Parse("greeter.agent.md", []byte(bad)); err == nil || got != nil {
			t.Fatalf("invalid unpinned definition accepted: %v", err)
		}
	}
}
