package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type kagentEvaluationObservation struct {
	Service kagentControllerService
}

type kagentEvaluationOperation struct {
	Requests        map[string]kagentEvaluationExecution
	Tasks, Contexts map[string]bool
	RequireHistory  bool
}

func kagentUnknownEvaluation(ctx context.Context, c agentruntime.EvaluationCase, execution kagentEvaluationExecution, judge agentruntime.EvaluationJudge) agentruntime.EvaluationReceipt {
	return agentruntime.EvaluationReceipt{CaseID: c.ID, Verdict: agentruntime.EvaluationUnknown, Detail: evaluationExecutionUnavailable, Execution: &execution, Assertions: evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{ToolSupported: false}, judge)}
}

func (a *App) executeKagentEvaluationCase(ctx context.Context, ref agentruntime.AgentRef, c agentruntime.EvaluationCase, execution kagentEvaluationExecution, op *kagentEvaluationOperation, observe func() (kagentEvaluationObservation, error), judge agentruntime.EvaluationJudge) (agentruntime.EvaluationReceipt, string, error) {
	result := kagentUnknownEvaluation(ctx, c, execution, judge)
	observed, err := observe()
	if err != nil {
		return agentruntime.EvaluationReceipt{}, "", err
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": execution.RPCID, "method": "message/send", "params": map[string]any{"message": map[string]any{"kind": "message", "role": "user", "messageId": execution.RequestMessageID, "parts": []any{map[string]any{"kind": "text", "text": c.Input}}}, "configuration": map[string]any{"blocking": true}}})
	if err != nil {
		return agentruntime.EvaluationReceipt{}, "", fmt.Errorf("Kagent evaluation request unavailable")
	}
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return agentruntime.EvaluationReceipt{}, "", fmt.Errorf("Kagent evaluation deadline exhausted before send")
	}
	path := kagentServiceProxyPath(observed.Service, "/api/a2a/"+url.PathEscape(ref.Namespace)+"/"+url.PathEscape(ref.Name))
	raw, sendErr := a.kagentCaptureWithTimeout(ctx, remaining, remaining.String(), body, "create", "--raw", path, "-f", "-")
	answer := ""
	var parseErr error
	if sendErr == nil {
		execution, answer, parseErr = parseKagentEvaluationResponse(raw, execution, c.Input)
		if op.RequireHistory && !suiteKagentResponseHistory(raw) {
			parseErr = fmt.Errorf("Kagent suite requires attributable history")
		}
		if execution.TaskID != "" && (op.Tasks[execution.TaskID] || op.Contexts[execution.ContextID]) {
			execution = op.Requests[c.ID]
			parseErr = fmt.Errorf("Kagent evaluation returned reused native identity")
		}
		if execution.TaskID != "" {
			op.Tasks[execution.TaskID], op.Contexts[execution.ContextID] = true, true
		}
	}
	result.Execution = &execution
	// Attribution takes precedence over both a native failure and an answer.
	if _, err = observe(); err != nil {
		result.Stop = true
		return result, answer, nil
	}
	if sendErr != nil || parseErr != nil {
		result.Stop = true
		return result, answer, nil
	}
	switch execution.State {
	case "failed", "canceled", "rejected":
		result.Verdict = agentruntime.EvaluationFail
		result.Detail = evaluationExecutionFailed
	case "completed":
		result.AnswerSHA256 = agentruntime.EvaluationAnswerDigest(answer)
		result.Detail = ""
		result.Assertions = evaluateCaseWithJudge(ctx, c, agentruntime.EvaluationEvidence{Answer: answer, AnswerAvailable: true, ToolSupported: false}, judge)
		result.Verdict = agentruntime.EvaluationAssertionsVerdict(result.Assertions)
	default:
		result.Stop = true
	}
	return result, answer, nil
}
