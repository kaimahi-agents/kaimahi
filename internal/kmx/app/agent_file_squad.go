package app

import (
	"context"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

// PreviewSquadSnapshot revalidates explicit native bytes, then shares the same
// frozen tool-free execution path. This projection is never written as a
// canonical charter and supplies no missing native Purpose or origin proof.
func (a *App) PreviewSquadSnapshot(ctx context.Context, d *agentfile.SquadCharter, opt FilePreviewOptions) (FilePreviewResult, error) {
	if d == nil {
		return a.PreviewAgentSnapshot(ctx, nil, opt)
	}
	native, err := agentfile.ParseSquad(".squad/agents/"+d.Profile.MemberID+"/charter.md", d.Source(), d.Profile)
	if err != nil || !native.Enriched {
		return a.previewAgentSnapshot(ctx, nil, opt, err, a.timeNow())
	}
	parsed := &agentfile.Agent{Identity: agentfile.Identity{ID: native.Profile.MemberID}, Title: native.Title, Instructions: native.Instructions, Model: native.Model, Examples: native.Examples, Revision: native.Revision, Locations: native.Locations, NativeTaskInstructions: native.TaskInstructions}
	if opt.SquadBrief != "" {
		parsed.Instructions = "# Shared team brief\n\n" + opt.SquadBrief + "\n\n" + parsed.Instructions
	}
	return a.previewAgentSnapshot(ctx, parsed, opt, nil, a.timeNow())
}
