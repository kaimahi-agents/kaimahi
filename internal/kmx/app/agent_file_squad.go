package app

import (
	"context"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// PreviewSquadSnapshot revalidates explicit native bytes, then shares the same
// frozen tool-free execution path. This projection is never written as a
// canonical charter and supplies no missing native Purpose or origin proof.
func (a *App) PreviewSquadSnapshot(ctx context.Context, d *agentfile.SquadCharter, opt FilePreviewOptions) (FilePreviewResult, error) {
	if d == nil {
		return a.PreviewAgentSnapshot(ctx, nil, opt)
	}
	if secretshapes.Match(opt.SquadBrief) != nil {
		return a.previewAgentSnapshot(ctx, nil, opt, &agentfile.Error{Field: "Brief", Problem: "remove credential-shaped content from the shared brief"}, a.timeNow())
	}
	native, err := agentfile.ParseSquad(".squad/agents/"+d.Profile.MemberID+"/charter.md", d.Source(), d.Profile)
	if err != nil || !native.Enriched {
		return a.previewAgentSnapshot(ctx, nil, opt, err, a.timeNow())
	}
	parsed := &agentfile.Agent{Identity: agentfile.Identity{ID: native.Profile.MemberID}, Title: native.Title, Instructions: native.Instructions, Model: native.Model, Examples: native.Examples, Revision: native.Revision, Locations: native.Locations, NativeTaskInstructions: native.TaskInstructions}
	if opt.SquadBrief != "" {
		briefContext := "# Shared team brief\n\n" + opt.SquadBrief + "\n\n"
		parsed.Instructions = briefContext + parsed.Instructions
		parsed.NativeTaskInstructions = briefContext + parsed.NativeTaskInstructions
	}
	return a.previewAgentSnapshot(ctx, parsed, opt, nil, a.timeNow())
}
