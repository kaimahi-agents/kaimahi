package runtime

import "testing"

func TestEvaluationMinPassesRequiresRuns(t *testing.T) {
	_, err := ParseEvaluationCase([]byte("id: repeated\ninput: question\nexpectContains: [answer]\nminPasses: 1\n"))
	if err == nil || err.Error() != "evaluation minPasses requires runs" {
		t.Fatalf("missing runs did not report dependent-field refusal: %v", err)
	}
}

func TestEvaluationRepeatedCaseFields(t *testing.T) {
	base := "id: repeated\ninput: question\nexpectContains: [answer]\n"
	for _, tc := range []struct {
		policy       string
		runs, passes int
	}{{"", 1, 1}, {"runs: 3\n", 3, 3}, {"runs: 3\nminPasses: 2\n", 3, 2}, {"runs: 1\nminPasses: 1\n", 1, 1}, {"runs: 100\nminPasses: 1\n", 100, 1}} {
		c, err := ParseEvaluationCase([]byte(base + tc.policy))
		if err != nil {
			t.Fatalf("valid policy %q: %v", tc.policy, err)
		}
		if runs, passes := c.RunPolicy(); runs != tc.runs || passes != tc.passes {
			t.Errorf("policy %q: %d/%d", tc.policy, passes, runs)
		}
	}
	for _, policy := range []string{"minPasses: 1", "minpasses: 1", "Runs: 1", "runs: 1\nminPasses: -1", "runs: 1\nminPasses: 1.5", "runs: 1\nminPasses: true", "runs: 0", "runs: -1", "runs: 101", "runs: null", "runs: '3'", "runs: 1.5", "runs: true", "minPasses: 0", "minPasses: 2", "runs: 3\nminPasses: 4", "runs: 3\nminPasses: null", "runs: 3\nminPasses: '2'"} {
		if _, err := ParseEvaluationCase([]byte(base + policy + "\n")); err == nil {
			t.Errorf("accepted invalid policy %q", policy)
		}
	}
	if _, err := ParseSessionReferenceAssertions([]byte("id: reference\nexpectContains: [answer]\nruns: 2\n")); err == nil {
		t.Fatal("reference extraction accepted execution policy")
	}
}
