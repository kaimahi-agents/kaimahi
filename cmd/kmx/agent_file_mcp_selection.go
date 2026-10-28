package main

import (
	"context"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

// Only file Run/Test installs this private intent. Shared app/MCP callers keep
// the existing runner, and no resolved launch input enters a preview request.
type fileMCPIntentKey struct{}
type fileMCPIntent struct {
	file      string
	selection localtools.ServerSelection
}
type fileMCPOwner interface {
	Resolve(context.Context, []string, []localtools.ToolSetting) (*localtools.Host, error)
	Close() error
}
type fileMCPRunner struct {
	base  agentpreview.Runner
	owner func(context.Context, string, localtools.ServerSelection) fileMCPOwner
}

func newFileMCPRunner(base agentpreview.Runner) agentpreview.Runner {
	return &fileMCPRunner{base: base, owner: func(ctx context.Context, file string, selection localtools.ServerSelection) fileMCPOwner {
		return localtools.NewStdioInvocation(ctx, file, selection)
	}}
}
func (r *fileMCPRunner) Run(ctx context.Context, request agentpreview.Request) (agentpreview.Outcome, error) {
	intent, ok := ctx.Value(fileMCPIntentKey{}).(fileMCPIntent)
	if !ok || request.Agent == nil || len(request.Agent.Tools) == 0 || request.ToolBindings != "" {
		return r.base.Run(ctx, request)
	}
	owner := r.owner(ctx, intent.file, intent.selection)
	// Close must precede App's completion/capture/retention handling. Each Run
	// gets a new resolver and the frozen request states, never shared defaults.
	runner := app.NewAvailableHostFileRunner(func(resolveCtx context.Context, names []string) (*localtools.Host, error) {
		return owner.Resolve(resolveCtx, names, request.ToolSettings)
	})
	out, err := runner.Run(ctx, request)
	closeErr := owner.Close()
	if cause := ctx.Err(); cause != nil {
		return out, cause
	}
	if closeErr != nil {
		return agentpreview.Outcome{}, agentpreview.ErrToolsUnavailable
	}
	return out, err
}
