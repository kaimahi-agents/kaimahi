package app

import (
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"io"
)

func newEvaluationJudge(config evaluationJudgeConfig, selected []bundleEvaluationCase, out io.Writer) (agentruntime.EvaluationJudge, error) {
	cases := make([]agentruntime.EvaluationCase, len(selected))
	for i, c := range selected {
		cases[i] = c.Case
	}
	return newEvaluationCaseJudge(config, cases, out)
}
