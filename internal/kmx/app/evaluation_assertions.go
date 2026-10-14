package app

import (
	"context"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func evaluateCaseWithJudge(ctx context.Context, c agentruntime.EvaluationCase, evidence agentruntime.EvaluationEvidence, judge agentruntime.EvaluationJudge) []agentruntime.EvaluationAssertionResult {
	if judge != nil {
		return judge(ctx, c, evidence)
	}
	return agentruntime.EvaluateAssertions(c, evidence)
}
