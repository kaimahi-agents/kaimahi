package app

import (
	"io"
	"net/http"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// LocalPreviewJudge builds the native llmJudge evaluator for a local preview
// route. It is the same two-sample, strict-parse, threshold-side judge used by
// workspace test; only the route is supplied by local preview. transport, when
// non-nil, replaces the network (the offline fake judge or a test transport).
// Judge rationale is discarded: it is diagnostic content, never printed by
// default.
func LocalPreviewJudge(baseURL, keyEnv string, transport http.RoundTripper, cases []agentruntime.EvaluationCase) (agentruntime.EvaluationJudge, error) {
	return newEvaluationCaseJudge(evaluationJudgeConfig{BaseURL: baseURL, KeyEnv: keyEnv, transport: transport}, cases, io.Discard)
}
