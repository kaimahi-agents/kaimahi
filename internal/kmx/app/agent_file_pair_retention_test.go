package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFilePairRetentionFreezesExpectedPair(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	p := previewPair(t)
	opt := WithPairFilePreviewRetention(FilePreviewOptions{File: p.AgentPath, Operation: "test"}, p)
	raw, err := os.ReadFile(p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Tests.Selection.Path, append(raw, []byte("changed prose\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := agentfile.LoadPair(p.AgentPath, p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := (&App{FileRunner: pairedScript(t, runtime.EvaluationFail, nil)}).PreviewAgentPairSnapshot(t.Context(), changed, opt)
	if result.Retention.Status != "unavailable" {
		t.Fatalf("retention rebound to unadmitted pair: %+v", result.Retention)
	}
}

func TestFilePairRetentionRecreatedAndLegacyCompatibility(t *testing.T) {
	t.Setenv("KMX_HOME", filepath.Join(t.TempDir(), "state"))
	filename := previewFile(t)
	legacy, _ := failingFilePreview(t, filename)
	if legacy.Retention.Status != "retained" {
		t.Fatal(legacy.Retention)
	}
	report := func(selector string) string {
		t.Helper()
		var out bytes.Buffer
		if err := (&App{Out: &out}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: filename, TestsFile: selector}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if !strings.Contains(report(""), "current") {
		t.Fatal("legacy inline not current")
	}
	p := previewPair(t)
	tests := filepath.Join(filepath.Dir(filename), "other-tests.md")
	raw, err := os.ReadFile(p.Tests.Selection.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tests, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report(tests), "historical") {
		t.Fatal("legacy agent-only record became current under external tests")
	}
	pair, err := agentfile.LoadPair(filename, tests)
	if err != nil {
		t.Fatal(err)
	}
	opt := WithPairFilePreviewRetention(FilePreviewOptions{File: filename, Operation: "test"}, pair)
	result, _ := (&App{FileRunner: pairedScript(t, runtime.EvaluationFail, nil)}).PreviewAgentPairSnapshot(t.Context(), pair, opt)
	id := result.Retention.Diagnostics[0].ID
	check := func(expected string) {
		t.Helper()
		var out bytes.Buffer
		if err := (&App{Out: &out}).ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: filename, TestsFile: tests, ID: id}); err != nil || !strings.Contains(out.String(), expected) {
			t.Fatalf("explain: %s %v", out.String(), err)
		}
	}
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	check("historical")
	if err := os.WriteFile(tests, raw, 0600); err != nil {
		t.Fatal(err)
	}
	check("current")
	store, err := openFileFailureStore(filename, false, retainedFileFailureCodec{})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.scan()
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	var paired *fileFailureRecord
	for i := range entries {
		if entries[i].file.Pair != nil {
			paired = entries[i].file
		}
	}
	if paired == nil {
		t.Fatal("pair metadata lost")
	}
	for _, field := range []string{"snapshot", "state", "key", "raw"} {
		t.Run(field, func(t *testing.T) {
			bad := *paired
			p := *bad.Pair
			bad.Pair = &p
			switch field {
			case "snapshot":
				p.SnapshotRevision = ""
			case "state":
				p.State = "unexpected"
			case "key":
				p.SelectionKey = ""
			case "raw":
				p.Revision = ""
			}
			if validateFileFailureRecord(bad, nil) == nil {
				t.Fatal("accepted incomplete pair metadata")
			}
			if _, err := (retainedFileFailureCodec{subject: bad.Subject, revision: bad.Revision, agentID: bad.AgentID, operation: bad.Operation, selection: bad.Selection, model: bad.Model, judge: bad.Judge, assertions: bad.Assertions, pair: &p}).encode(bad.Sequence, bad.Detail, nil); err == nil {
				t.Fatal("encoded incomplete pair metadata")
			}
		})
	}
}
