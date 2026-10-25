package localpreview

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

func TestLateModelFailureStaysWithOriginalTurn(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	originalError := errors.New("old-turn-error")
	old := &turnSlot{call: func(context.Context, api.ModelRequest) (api.ModelResponse, error) {
		close(entered)
		<-release
		return api.ModelResponse{}, originalError
	}}
	next := &turnSlot{}
	executor := &journalExecutor{}
	executor.setSlot(old)
	go func() { defer close(done); _, _ = executor.model(t.Context(), api.ModelRequest{}) }()
	<-entered
	executor.setSlot(next)
	close(release)
	<-done
	if old.err != originalError || next.err != nil {
		t.Fatal("late callback error escaped its original turn")
	}
}
