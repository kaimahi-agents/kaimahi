package mcp

import (
	"context"
	"encoding/json"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func caseProgressNotifier(ctx context.Context, operation string, req *sdk.CallToolRequest) func(app.FilePreviewCaseProgress) {
	if operation != "test" || req.Session == nil || req.Params.GetProgressToken() == nil {
		return nil
	}
	token := req.Params.GetProgressToken()
	completed := 0
	return func(row app.FilePreviewCaseProgress) {
		if ctx.Err() != nil {
			return
		}
		message, _ := json.Marshal(row)
		completed++
		// Progress is observational. A transport failure must not turn a finished
		// case into an execution failure or change the final result's bytes.
		_ = req.Session.NotifyProgress(ctx, &sdk.ProgressNotificationParams{ProgressToken: token, Progress: float64(completed), Message: string(message)})
	}
}
