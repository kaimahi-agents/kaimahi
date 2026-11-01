package copilotd

import (
	"context"
	"errors"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/microsoft/agent-host-protocol/clients/go/ahptypes"
	"testing"
)

func TestRunnerUnsupportedBeforeAuth(t *testing.T) {
	for _, req := range []agentpreview.Request{
		{Examples: []agentfile.Example{{Mocks: &agentfile.Mocks{}}}},
		{Examples: []agentfile.Example{{Assertions: []runtime.EvaluationAssertion{{Type: "llmJudge"}}}}},
	} {
		touched := false
		r := NewRunner(Options{Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
			touched = true
			return nil, errors.New("auth")
		}})
		out, err := r.Run(t.Context(), req)
		if err == nil || touched || !out.NoJournal {
			t.Fatalf("%v %v %+v", err, touched, out)
		}
	}
}
func TestRunnerCancelledBeforeEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	touched := false
	r := NewRunner(Options{Access: func(context.Context, func(ahptypes.ActionEnvelope) error) (copilot.HostAccess, error) {
		touched = true
		return nil, errors.New("auth")
	}})
	_, err := r.Run(ctx, agentpreview.Request{})
	if !errors.Is(err, context.Canceled) || touched {
		t.Fatalf("%v %v", err, touched)
	}
}
