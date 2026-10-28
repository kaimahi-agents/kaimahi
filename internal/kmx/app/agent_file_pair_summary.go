package app

import "github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"

// Paths are validated but never projected into Actions summaries.
func validPairPreviewMetadata(r FilePreviewResult) bool {
	if r.SnapshotRevision == "" {
		return r.Tests == nil && r.RecordingTarget == nil && r.SavedSnapshotRevision == ""
	}
	p := retainedPairMetadata(r.SnapshotRevision, r.Tests)
	if !suitePrefixedDigest(r.File.Revision) || !validRetainedPairMetadata(p, r.File.Revision) {
		return false
	}
	path := r.Tests.Path
	if len(path) > 4096 || secretshapes.Match(path) != nil {
		return false
	}
	if safe, ok := suiteDiagnosticText(path, len(path), nil); !ok || safe != path {
		return false
	}
	if (p.State == "external" || p.State == "absent" || p.SelectionKey != "") && path == "" || p.State == "none" && path != "" {
		return false
	}
	if r.SavedRevision == "" {
		return r.RecordingTarget == nil && r.SavedSnapshotRevision == ""
	}
	if r.RecordingTarget == nil || !suitePrefixedDigest(r.SavedSnapshotRevision) || r.SavedSnapshotRevision == r.SnapshotRevision {
		return false
	}
	target := r.RecordingTarget
	if target.SelectionKey != p.SelectionKey {
		return false
	}
	if p.State == "external" {
		return target.Role == "tests" && target.Path == path && r.SavedRevision != p.Revision
	}
	return p.State == "inline" && target.Role == "agent" && target.Path != "" && secretshapes.Match(target.Path) == nil && r.SavedRevision != r.File.Revision
}
