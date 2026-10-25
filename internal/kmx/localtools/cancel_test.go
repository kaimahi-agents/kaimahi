package localtools

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func TestTrustedHostPreservesCancellationDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host, err := NewTrustedHost(trustedDefinitions(), func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
		cancel()
		return api.ToolResult{}, errors.New("remote error")
	}, func(...string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Execute(ctx, controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "c", Tool: "ping", Mediation: api.MediationControllerMediated, IdempotencyKey: "e:c"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}
