package mcp

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type hostProgressKey struct{}

// The native progress token binds relay metadata to the exact outer Test.
func withHostProgress(ctx context.Context, operation string, req *sdk.CallToolRequest) context.Context {
	if operation != "test" || req.Session == nil || req.Params.GetProgressToken() == nil {
		return ctx
	}
	token := req.Params.GetProgressToken()
	var mu sync.Mutex
	sequence := 0
	notify := func(value any) {
		if ctx.Err() != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		message, _ := json.Marshal(value)
		sequence++
		// Progress is observational; delivery failure cannot change the final result.
		_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ProgressToken: token, Progress: float64(sequence), Message: string(message)})
	}
	return context.WithValue(ctx, hostProgressKey{}, notify)
}

func caseProgressNotifier(ctx context.Context) func(app.FilePreviewCaseProgress) {
	if notify, ok := ctx.Value(hostProgressKey{}).(func(any)); ok {
		return func(row app.FilePreviewCaseProgress) { notify(row) }
	}
	return nil
}
