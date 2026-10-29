package mcp

import (
	"context"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

// Unlike the authoring ACK this projection contains no physical/display paths.
// Keep acknowledged partial effects even when App correctly rejects a save ACK.
type pairCaptureResult struct {
	State      string             `json:"state"`
	ReasonCode string             `json:"reasonCode,omitempty"`
	Agent      pairSourceAck      `json:"agent"`
	Tests      pairSourceAck      `json:"tests"`
	History    *FileHistoryStatus `json:"history,omitempty"`
}

func (b *pairBinding) prepareCapture(receipt **pairSaveResult) func(context.Context, *agentfile.Pair) (app.PairRecordingSaver, error) {
	return func(ctx context.Context, p *agentfile.Pair) (app.PairRecordingSaver, error) {
		if p == nil || b.pair == nil || p.SnapshotRevision != b.pair.SnapshotRevision {
			return nil, agentfile.ErrRecordingChanged
		}
		if err := b.check(ctx); err != nil {
			return nil, agentfile.ErrRecordingChanged
		}
		used := false
		return func(ctx context.Context, records []agentfile.Recording) (app.PairRecordingSaveResult, error) {
			if used {
				return app.PairRecordingSaveResult{}, agentfile.ErrRecordingSave
			}
			used = true
			agentBytes := b.agent.source
			var testsBytes []byte
			if b.tests != nil {
				testsBytes = b.tests.source
			}
			external := p.Tests.State == "external"
			var err error
			if external {
				testsBytes, err = agentfile.ApplyTestRecordings(testsBytes, records)
			} else {
				agentBytes, err = agentfile.ApplyRecordings(agentBytes, records)
			}
			if err != nil {
				return app.PairRecordingSaveResult{}, err
			}
			parsed, err := agentfile.Parse(p.AgentPath, agentBytes)
			if err != nil {
				return app.PairRecordingSaveResult{}, agentfile.ErrRecordingInvalid
			}
			desired, err := agentfile.ComposePair(p.AgentPath, parsed, p.Tests.Selection, testsBytes, external)
			if err != nil {
				return app.PairRecordingSaveResult{}, agentfile.ErrRecordingInvalid
			}
			saved := b.publish(ctx, desired, agentBytes, testsBytes)
			*receipt = &saved
			if saved.State != "complete" {
				return app.PairRecordingSaveResult{}, agentfile.ErrRecordingSave
			}
			target := app.FileRecordingTarget{Role: "agent", Path: p.AgentPath, SelectionKey: p.Tests.Selection.Key}
			revision := desired.Agent.Revision
			if external {
				target.Role, target.Path = "tests", p.Tests.Selection.Path
				revision = desired.Tests.Revision
			}
			return app.PairRecordingSaveResult{Revision: revision, AgentRevision: desired.Agent.Revision, SnapshotRevision: desired.SnapshotRevision, Target: target}, nil
		}, nil
	}
}

func copilotCaptureConsent(ctx context.Context, agent *agentfile.Agent, physical, caseID string, replay, ack bool) error {
	if agent == nil || !agent.HasFrontmatter || replay || ack {
		return nil
	}
	settings, err := localtools.ReadSettingsSnapshot(ctx, physical, agent.Tools)
	if err != nil {
		return nil
	}
	for _, example := range agent.Examples {
		if caseID != "" && caseID != example.ID || example.Mocks == nil {
			continue
		}
		for _, tool := range example.Mocks.Tools {
			if tool.Mode == "recorded" && len(tool.Calls) == 0 && !localtools.IsDisabled(settings.Settings, tool.Name) {
				return refusal("confirmation_required")
			}
		}
	}
	return nil
}
