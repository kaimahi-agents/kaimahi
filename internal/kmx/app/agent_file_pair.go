package app

import (
	"context"
	"path/filepath"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

type FilePreviewTestsSource struct {
	State        string `json:"state"`
	Path         string `json:"path,omitempty"`
	Revision     string `json:"revision,omitempty"`
	SelectionKey string `json:"selectionKey,omitempty"`
}
type FileRecordingTarget struct {
	Role         string `json:"role"`
	Path         string `json:"path"`
	SelectionKey string `json:"selectionKey,omitempty"`
}
type PairRecordingSaveResult struct {
	Revision, SnapshotRevision, AgentRevision string
	Target                                    FileRecordingTarget
}
type PairRecordingSaver func(context.Context, []agentfile.Recording) (PairRecordingSaveResult, error)

// PreviewAgentPairSnapshot uses a private execution copy; raw agent identity and
// instructions remain unchanged. The caller retains checked source authority.
func (a *App) PreviewAgentPairSnapshot(ctx context.Context, pair *agentfile.Pair, opt FilePreviewOptions) (FilePreviewResult, error) {
	if pair == nil || pair.Agent == nil {
		return a.PreviewAgentSnapshot(ctx, nil, opt)
	}
	frozen := *pair
	frozen.Tests = pair.Tests
	opt.pair, opt.pairSave = &frozen, &PairRecordingSaveResult{}
	opt.SourceFile = pair.AgentPath
	if opt.retentionSnapshot != pair.SnapshotRevision {
		opt.retentionScope = nil
	}
	execution := *pair.Agent
	execution.Examples = nil
	if pair.Tests.Suite != nil {
		execution.Examples = agentfile.CloneExamples(pair.Tests.Suite.Examples)
	}
	return a.PreviewAgentSnapshot(ctx, &execution, opt)
}
func pairTestsSource(pair *agentfile.Pair) *FilePreviewTestsSource {
	return &FilePreviewTestsSource{State: pair.Tests.State, Path: pair.Tests.Selection.Path, Revision: pair.Tests.Revision, SelectionKey: pair.Tests.Selection.Key}
}

// Display paths follow File's opt-in full-path boundary. Physical identities
// remain private in the frozen pair and never come back from the projection.
func pairPreviewPath(path string, full bool) string {
	if path == "" {
		return ""
	}
	if !full {
		return suiteDiagnosticIdentity(filepath.Base(path), nil)
	}
	if safe, ok := suiteDiagnosticText(path, len(path), nil); ok {
		return safe
	}
	return "withheld"
}
func pairRecordingTarget(pair *agentfile.Pair) FileRecordingTarget {
	if pair.Tests.State == "external" {
		return FileRecordingTarget{Role: "tests", Path: pair.Tests.Selection.Path, SelectionKey: pair.Tests.Selection.Key}
	}
	return FileRecordingTarget{Role: "agent", Path: pair.AgentPath, SelectionKey: pair.Tests.Selection.Key}
}
func validatePairRecordingSave(pair *agentfile.Pair, saved PairRecordingSaveResult) error {
	target := pairRecordingTarget(pair)
	before := pair.Agent.Revision
	if target.Role == "tests" {
		before = pair.Tests.Revision
	}
	if saved.Target != target || !suitePrefixedDigest(saved.Revision) || saved.Revision == before ||
		!suitePrefixedDigest(saved.SnapshotRevision) || saved.SnapshotRevision == pair.SnapshotRevision ||
		!suitePrefixedDigest(saved.AgentRevision) || target.Role == "tests" && saved.AgentRevision != pair.Agent.Revision ||
		target.Role == "agent" && saved.AgentRevision != saved.Revision {
		return agentfile.ErrRecordingSave
	}
	return nil
}

// WithPairFilePreviewRetention freezes the executed pair, not a post-capture read.
// It preserves the physical-agent subject and never retains historical paths.
func WithPairFilePreviewRetention(opt FilePreviewOptions, pair *agentfile.Pair) FilePreviewOptions {
	if pair == nil || pair.Agent == nil {
		return opt
	}
	opt.RetentionFile, opt.retentionSnapshot, opt.retentionScope = pair.AgentPath, pair.SnapshotRevision, nil
	scope, err := resolveFileFailureScope(pair.AgentPath)
	if err == nil {
		opt.RetentionFile, opt.retentionScope = scope.filename, &scope
	}
	return opt
}
