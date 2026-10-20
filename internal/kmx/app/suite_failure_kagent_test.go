package app

import (
	"context"
	"errors"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"os/exec"
	"testing"
	"time"
)

func TestSuiteFailureKagentDiagnosticChannel(t *testing.T) {
	for _, stage := range []string{"send", "malformed", "attribution"} {
		t.Run(stage, func(t *testing.T) {
			a, _, _, _ := suiteKagentFixture(t, "")
			a.captureCommand = func(ctx context.Context, cmd *exec.Cmd) error {
				if stage == "send" {
					return errors.New("private subprocess stderr")
				}
				if stage == "malformed" {
					_, e := cmd.Stdout.Write([]byte("private malformed body"))
					return e
				}
				_, e := cmd.Stdout.Write(evaluationTaskJSON("rpc-fixture", "message-fixture", "completed", "private answer"))
				return e
			}
			bounded, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			ctx, notes := newSuiteFailureContext(bounded)
			calls := 0
			observe := func() (kagentEvaluationObservation, error) {
				calls++
				if stage == "attribution" && calls == 2 {
					return kagentEvaluationObservation{}, errors.New("private drift")
				}
				return kagentEvaluationObservation{Service: kagentControllerService{Name: "controller", Namespace: "system", UID: "uid"}}, nil
			}
			c := agentruntime.EvaluationCase{ID: "case", Input: "input"}
			execution := kagentEvaluationExecution{RPCID: "rpc-fixture", RequestMessageID: "message-fixture"}
			op := &kagentEvaluationOperation{Requests: map[string]kagentEvaluationExecution{"case": execution}, Tasks: map[string]bool{}, Contexts: map[string]bool{}}
			result, answer, err := a.executeKagentEvaluationCase(ctx, agentruntime.AgentRef{Name: "agent", Namespace: "agents"}, c, execution, op, observe, nil)
			want := map[string]string{"send": "kagent-send-unavailable", "malformed": "kagent-response-malformed", "attribution": "attribution-unverified"}[stage]
			if notes.class != want || !result.Stop || answer != "" || err != nil {
				t.Fatalf("class=%s stop=%t answer-present=%t err=%v", notes.class, result.Stop, answer != "", err)
			}
		})
	}
}
