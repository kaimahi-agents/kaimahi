package app

import agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"

func suiteAssertionEvaluatorVersion(kind string) string {
	if kind == "llmJudge" {
		return suitePassingJudgeVersion
	}
	return suitePassingEvaluatorVersion
}

func suitePassingAssertionDefinition(a agentruntime.EvaluationAssertion) suitePassingAssertion {
	row := suitePassingAssertion{ID: a.ID, Type: a.Type, DefinitionDigest: agentruntime.EvaluationAssertionDigest(a), EvaluatorVersion: suiteAssertionEvaluatorVersion(a.Type), Verdict: "pass"}
	if a.Type == "llmJudge" {
		row.Judge = &agentruntime.EvaluationJudgeEvidence{Model: a.JudgeModel, ModelVersion: a.JudgeVersion, Provenance: "user-declared", EvaluatorDigest: agentruntime.EvaluationJudgeEvaluatorDigest(), MinScore: *a.MinScore}
	}
	return row
}

func suitePassingAssertionResult(a agentruntime.EvaluationAssertionResult) suitePassingAssertion {
	row := suitePassingAssertion{ID: a.ID, Type: a.Type, DefinitionDigest: a.DefinitionDigest, EvaluatorVersion: suiteAssertionEvaluatorVersion(a.Type), Verdict: string(a.Verdict)}
	if a.Judge != nil {
		evidence := *a.Judge
		evidence.Scores = append([]int(nil), a.Judge.Scores...)
		row.Judge = &evidence
	}
	return row
}
