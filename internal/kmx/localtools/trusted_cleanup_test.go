package localtools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func TestTrustedHostPreservesCleanupClassificationWithoutDetails(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			host, err := NewTrustedHost([]api.ToolDefinition{Definition()}, func(context.Context, controller.ToolCallContext, api.ToolCall) (api.ToolResult, error) {
				if canceled {
					cancel()
				}
				return api.ToolResult{}, fmt.Errorf("private executor diagnostics: %w", ErrCommandCleanup)
			}, func(...string) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = host.Execute(ctx, controller.ToolCallContext{SessionUID: "session"}, api.ToolCall{ID: "call", Tool: Definition().Name, Args: map[string]any{}, Mediation: api.MediationControllerMediated, IdempotencyKey: "key"})
			if !errors.Is(err, ErrCommandCleanup) || err.Error() != errReadFailed.Error() || errors.Is(err, context.Canceled) != canceled {
				t.Fatalf("cleanup/cancellation classification or redaction lost: %v", err)
			}
		})
	}
}
