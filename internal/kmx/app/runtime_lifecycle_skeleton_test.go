package app

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Task 5 implements Orka's Render and Deploy, Task 6 implements Status;
// Evaluate is permanently unsupported. The verb Orka still declines must
// return the one shared, typed agentruntime.UnsupportedVerbError — not an
// ad hoc string error — naming the exact runtime ID and verb.
func TestOrkaLifecycleSkeletonReturnsSharedUnsupportedVerbError(t *testing.T) {
	adapter := orkaRuntimeAdapter{app: &App{}}
	if caps := adapter.Capabilities(); caps.Evaluate {
		t.Fatalf("Orka declares an unimplemented lifecycle verb supported: %+v", caps)
	}
	cases := []struct {
		verb string
		call func() error
	}{
		{agentruntime.VerbEvaluate, func() error {
			_, err := adapter.Evaluate(context.Background(), agentruntime.AgentRef{}, agentruntime.EvaluationRequest{})
			return err
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		var unsupported *agentruntime.UnsupportedVerbError
		if !errors.As(err, &unsupported) {
			t.Fatalf("verb %q: err = %v, not *UnsupportedVerbError", tc.verb, err)
		}
		if unsupported.Runtime != agentruntime.Orka || unsupported.Verb != tc.verb {
			t.Fatalf("verb %q: unsupported = %+v", tc.verb, unsupported)
		}
	}
}

// Task 8 wraps legacy kagent's retained combined-status slice behind Status
// (runtime_kagent.go and its tests). The verbs it still declines — Render,
// Deploy and Evaluate, permanently unsupported per DESIGN.md §3 — must keep
// returning the one shared, typed error naming the exact runtime ID and verb.
func TestKagentLifecycleSkeletonReturnsSharedUnsupportedVerbError(t *testing.T) {
	adapter := kagentRuntimeAdapter{app: &App{}}
	if caps := adapter.Capabilities(); caps.Render || caps.Deploy || caps.Evaluate {
		t.Fatalf("kagent declares a lifecycle verb it cannot perform: %+v", caps)
	}
	cases := []struct {
		verb string
		call func() error
	}{
		{agentruntime.VerbRender, func() error {
			_, err := adapter.Render(context.Background(), agentruntime.PortableAgent{}, agentruntime.RenderOptions{})
			return err
		}},
		{agentruntime.VerbDeploy, func() error {
			_, err := adapter.Deploy(context.Background(), agentruntime.RenderedBundle{}, agentruntime.DeployOptions{})
			return err
		}},
		{agentruntime.VerbEvaluate, func() error {
			_, err := adapter.Evaluate(context.Background(), agentruntime.AgentRef{}, agentruntime.EvaluationRequest{})
			return err
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		var unsupported *agentruntime.UnsupportedVerbError
		if !errors.As(err, &unsupported) {
			t.Fatalf("verb %q: err = %v, not *UnsupportedVerbError", tc.verb, err)
		}
		if unsupported.Runtime != agentruntime.Kagent || unsupported.Verb != tc.verb {
			t.Fatalf("verb %q: unsupported = %+v", tc.verb, unsupported)
		}
	}
}
