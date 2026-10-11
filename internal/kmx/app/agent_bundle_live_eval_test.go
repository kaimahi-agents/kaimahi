package app

import (
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestLiveEvalLoopBundle(t *testing.T) {
	bundle := "testdata/live-eval-loop"
	portable, _, _, err := readSessionsEvaluationSource(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if portable.Spec.Model.Name != "qwen-3.5-2b" {
		t.Fatalf("fixture model does not match the AIKit CI provider: %q", portable.Spec.Model.Name)
	}
	cases, _, err := loadBundleEvaluationCases(bundle)
	if err != nil {
		t.Fatal(err)
	}
	answers := map[string]string{
		"capital-france": "The capital of France is Paris.",
		"arithmetic":     "2 + 2 = 4.",
	}
	if len(cases) != len(answers) {
		t.Fatalf("got %d cases, want %d", len(cases), len(answers))
	}
	for _, c := range cases {
		answer, ok := answers[c.Case.ID]
		if !ok {
			t.Fatalf("unexpected case %q", c.Case.ID)
		}
		if _, missing := agentruntime.MatchExpectations(answer, c.Case.ExpectContains); len(missing) != 0 {
			t.Errorf("case %s rejects a correct paraphrased answer", c.Case.ID)
		}
		if _, missing := agentruntime.MatchExpectations("I do not know.", c.Case.ExpectContains); len(missing) == 0 {
			t.Errorf("case %s accepts a non-answer", c.Case.ID)
		}
	}
}
