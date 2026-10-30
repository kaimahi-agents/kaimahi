package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFilePairEarlyRefusalHasWritableSummary(t *testing.T) {
	p := previewPair(t)
	result, err := (&App{}).PreviewAgentPairSnapshot(t.Context(), p, FilePreviewOptions{File: p.AgentPath, Operation: "test", Model: "bad model!"})
	if err == nil || result.State != "refused" || result.ReasonCode != "invalid_options" || result.FullPass {
		t.Fatalf("expected pre-execution refusal, got %+v %v", result, err)
	}
	var summary bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&summary, result); err != nil {
		t.Fatalf("pre-execution paired refusal lost its summary: %v", err)
	}
	if strings.Contains(summary.String(), p.AgentPath) || strings.Contains(summary.String(), p.Tests.Selection.Path) {
		t.Fatal("refusal summary leaked a source path")
	}
}

func TestFilePairSourcePathProjection(t *testing.T) {
	p := previewPair(t)
	for _, full := range []bool{false, true} {
		result, _ := (&App{FileRunner: pairedScript(t, runtime.EvaluationPass, nil)}).PreviewAgentPairSnapshot(t.Context(), p, FilePreviewOptions{File: p.AgentPath, Operation: "test", FullPath: full})
		want := filepath.Base(p.Tests.Selection.Path)
		if full {
			want = p.Tests.Selection.Path
		}
		if result.Tests.Path != want {
			t.Fatalf("FullPath=%t leaked/wrong tests path %q, want %q", full, result.Tests.Path, want)
		}
	}
}

func TestFilePairSafeSummaryMetadata(t *testing.T) {
	p := previewPair(t)
	result, err := (&App{FileRunner: pairedScript(t, runtime.EvaluationPass, nil)}).PreviewAgentPairSnapshot(t.Context(), p, FilePreviewOptions{File: p.AgentPath, Operation: "test", FullPath: true})
	if err != nil {
		t.Fatal(err)
	}
	result.DurationMs = max(result.DurationMs, int64(1))
	var out bytes.Buffer
	if err := WriteFilePreviewGitHubSummary(&out, result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), p.Tests.Selection.Path) || strings.Contains(out.String(), p.AgentPath) || strings.Contains(out.String(), "Private prose") || !strings.Contains(out.String(), p.SnapshotRevision) {
		t.Fatal("unsafe or missing summary metadata")
	}
	if err := WriteFilePreview(&out, result, "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), p.Tests.Selection.Path) || !strings.Contains(out.String(), p.Tests.Revision) {
		t.Fatal("text omitted tests source")
	}
	for _, field := range []string{"snapshot", "tests", "state", "raw", "key", "ack", "saved-snapshot"} {
		t.Run(field, func(t *testing.T) {
			bad := result
			tests := *result.Tests
			bad.Tests = &tests
			switch field {
			case "snapshot":
				bad.SnapshotRevision = ""
			case "tests":
				bad.Tests = nil
			case "state":
				bad.Tests.State = "unknown"
			case "raw":
				bad.Tests.Revision = ""
			case "key":
				bad.Tests.SelectionKey = ""
			case "ack":
				bad.RecordingTarget = &FileRecordingTarget{Role: "tests", Path: p.Tests.Selection.Path}
			case "saved-snapshot":
				bad.SavedSnapshotRevision = p.SnapshotRevision
			}
			if validFilePreviewSummary(bad) {
				t.Fatal("accepted malformed paired result")
			}
		})
	}
}
