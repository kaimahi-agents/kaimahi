package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func suiteOutputFixture(t *testing.T, outName string) (string, string, string) {
	t.Helper()
	workspace := t.TempDir()
	base := t.TempDir()
	state := filepath.Join(base, "state")
	out := filepath.Join(base, outName)
	return workspace, state, out
}
func TestSuiteHistoryPrivateImmutableAndExport(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	workspace, state, out := suiteOutputFixture(t, "receipt.json")
	output, err := preflightSuiteReceiptOutput(workspace, state, out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	saved, err := publishSuiteEvaluationReceipt(s, sub, r, output)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.HistoryPublished || !saved.ExportPublished || saved.ReceiptDigest != "sha256:"+suiteTestHash(mustSuiteCanonical(t, r)) {
		t.Fatal("publication lacks complete identity")
	}
	for _, path := range []string{saved.HistoryPath, out} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("not private receipt: %v", err)
		}
		raw, _ := os.ReadFile(path)
		if !bytes.Equal(raw, mustSuiteCanonical(t, r)) {
			t.Fatal("not exact canonical bytes")
		}
		parsed, err := readSuiteEvaluationReceiptFile(path)
		if err != nil || parsed.RunID != r.RunID {
			t.Fatalf("bounded explicit receipt read: %v", err)
		}
	}
	for _, path := range []string{state, filepath.Join(state, "suite-evidence"), filepath.Join(state, "suite-evidence", "runs")} {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0700 {
			t.Fatal("history directory not 0700")
		}
	}
	if _, err := publishSuiteEvaluationReceipt(s, sub, r, output); err == nil {
		t.Fatal("immutable run overwritten")
	}
	entries, _ := os.ReadDir(filepath.Dir(saved.HistoryPath))
	if len(entries) != 1 {
		t.Fatal("stage file survived publication")
	}
}
func TestSuiteHistoryLateCollisionRetainsHonestHistory(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	workspace, state, out := suiteOutputFixture(t, "receipt.json")
	output, err := preflightSuiteReceiptOutput(workspace, state, out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := os.WriteFile(out, []byte("PRIVATE_COLLISION_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	saved, err := publishSuiteEvaluationReceipt(s, sub, r, output)
	if err == nil || !saved.HistoryPublished || saved.ExportPublished {
		t.Fatal("late export collision was hidden or history lost")
	}
	if strings.Contains(err.Error(), "PRIVATE_COLLISION_CANARY") || strings.Contains(err.Error(), out) {
		t.Fatal("raw filesystem error/path leaked")
	}
	raw, _ := os.ReadFile(out)
	if string(raw) != "PRIVATE_COLLISION_CANARY" {
		t.Fatal("late collision overwritten")
	}
	if _, err := readSuiteEvaluationReceiptFile(saved.HistoryPath); err != nil {
		t.Fatal("honest history disappeared")
	}
}
func TestSuiteHistoryAtomicConcurrentRunAndOut(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	workspace, state, out := suiteOutputFixture(t, "receipt.json")
	output, err := preflightSuiteReceiptOutput(workspace, state, out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := publishSuiteEvaluationReceipt(s, sub, r, output); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("exclusive immutable publication successes: %d", successes.Load())
	}
	raw, _ := os.ReadFile(out)
	if !bytes.Equal(raw, mustSuiteCanonical(t, r)) {
		t.Fatal("raced export partial or changed")
	}
	entries, _ := os.ReadDir(filepath.Join(state, "suite-evidence", "runs"))
	if len(entries) != 1 {
		t.Fatal("raced history left stages")
	}
}
func TestSuiteHistoryConcurrentDifferentRunsCannotOverwriteExport(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	workspace, state, out := suiteOutputFixture(t, "receipt.json")
	output, err := preflightSuiteReceiptOutput(workspace, state, out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	second := suiteCloneReceipt(t, r)
	second.RunID = strings.Repeat("b", 32)
	var wins, archived atomic.Int32
	var wg sync.WaitGroup
	for _, receipt := range []suiteEvaluationReceipt{r, second} {
		wg.Add(1)
		go func(receipt suiteEvaluationReceipt) {
			defer wg.Done()
			saved, err := publishSuiteEvaluationReceipt(s, sub, receipt, output)
			if saved.HistoryPublished {
				archived.Add(1)
			}
			if err == nil && saved.ExportPublished {
				wins.Add(1)
			}
		}(receipt)
	}
	wg.Wait()
	if wins.Load() != 1 || archived.Load() != 2 {
		t.Fatalf("expected one exclusive export and two honest runs, got %d/%d", wins.Load(), archived.Load())
	}
	parsed, err := readSuiteEvaluationReceiptFile(out)
	if err != nil || parsed.RunID != r.RunID && parsed.RunID != second.RunID {
		t.Fatal("export not one complete run")
	}
}
func TestSuiteHistoryLateDirectorySubstitution(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	workspace, state, out := suiteOutputFixture(t, "receipt.json")
	parent := filepath.Join(filepath.Dir(out), "export")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	out = filepath.Join(parent, "receipt.json")
	output, err := preflightSuiteReceiptOutput(workspace, state, out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if err := os.Rename(parent, parent+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workspace, parent); err != nil {
		t.Fatal(err)
	}
	saved, err := publishSuiteEvaluationReceipt(s, sub, r, output)
	if err == nil || !saved.HistoryPublished || saved.ExportPublished {
		t.Fatal("late parent substitution not refused with retained history")
	}
	if _, err := os.Stat(filepath.Join(workspace, "receipt.json")); !os.IsNotExist(err) {
		t.Fatal("export entered authored workspace")
	}
}
func TestSuiteHistoryLatePrivateDirectoryPermissionDrift(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	for _, component := range []string{".", "suite-evidence", "suite-evidence/runs"} {
		t.Run(component, func(t *testing.T) {
			workspace, state, out := suiteOutputFixture(t, "receipt.json")
			output, err := preflightSuiteReceiptOutput(workspace, state, out)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			if err := os.Chmod(filepath.Join(state, component), 0755); err != nil {
				t.Fatal(err)
			}
			saved, err := publishSuiteEvaluationReceipt(s, sub, r, output)
			if err == nil || saved.HistoryPublished || saved.ExportPublished {
				t.Fatal("history permissions drift accepted")
			}
		})
	}
}
func TestSuiteHistorySameOutAndPartialEvidence(t *testing.T) {
	s, sub, r, _ := suiteEvidenceFixture(t)
	workspace, state, _ := suiteOutputFixture(t, "ignored")
	out := filepath.Join(state, "suite-evidence", "runs", "eval-"+r.RunID+".json")
	output, err := preflightSuiteReceiptOutput(workspace, state, out)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	r.Placement.Complete = false
	r.Placement.Resources = r.Placement.Resources[:1]
	r.Placement.Resources[0].Applied = false
	r.Placement.Resources[0].Ready = false
	r.Placement.Resources[0].UID = ""
	r.Placement.Resources[0].Generation = 0
	r.Placement.Resources[0].SpecDigest = ""
	r.Placement.Resources[0].Ambiguous = true
	r.Detail = "placement unavailable"
	r.Result = "unknown"
	r.PromotionEligible = false
	for i := range r.Cases {
		c := &r.Cases[i]
		c.Verdict = "unknown"
		c.Detail = evaluationExecutionUnavailable
		c.TaskName = ""
		c.TaskUID = ""
		c.AnswerSHA256 = ""
		for j := range c.Assertions {
			c.Assertions[j].Verdict = "unknown"
			c.Assertions[j].Reason = "answer unavailable"
		}
	}
	saved, err := publishSuiteEvaluationReceipt(s, sub, r, output)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.HistoryPublished || !saved.ExportPublished || saved.HistoryPath != out {
		t.Fatal("same-path export was treated as collision")
	}
	parsed, err := readSuiteEvaluationReceiptFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Result != "unknown" || parsed.Placement.Complete || parsed.Placement.Resources[0].UID != "" {
		t.Fatal("unknown outcome fabricated placement")
	}
}
func TestSuiteHistoryPreflightRefusalsBeforeExecution(t *testing.T) {
	for _, mode := range []string{"inside-workspace", "state-inside-workspace", "out-in-cache", "collision", "symlink-file", "symlink-parent", "non-directory", "private-state", "workspace-link", "missing-out-parent", "state-parent-is-workspace"} {
		t.Run(mode, func(t *testing.T) {
			workspace, state, out := suiteOutputFixture(t, "receipt.json")
			switch mode {
			case "inside-workspace":
				out = filepath.Join(workspace, "receipt.json")
			case "state-inside-workspace":
				state = filepath.Join(workspace, "state")
			case "out-in-cache":
				out = filepath.Join(state, "suites", "receipt.json")
			case "collision":
				os.WriteFile(out, []byte("PRIVATE_PATH_PAYLOAD"), 0600)
			case "symlink-file":
				os.Symlink(filepath.Join(workspace, "absent"), out)
			case "symlink-parent":
				link := filepath.Join(filepath.Dir(out), "link")
				os.Symlink(workspace, link)
				out = filepath.Join(link, "receipt.json")
			case "non-directory":
				os.WriteFile(state, []byte("PRIVATE_PATH_PAYLOAD"), 0600)
			case "private-state":
				os.Mkdir(state, 0755)
			case "workspace-link":
				link := filepath.Join(filepath.Dir(out), "link")
				os.Symlink(workspace, link)
				workspace = link
			case "missing-out-parent":
				out = filepath.Join(filepath.Dir(out), "missing", "receipt.json")
			case "state-parent-is-workspace":
				state = filepath.Dir(workspace)
			}
			output, err := preflightSuiteReceiptOutput(workspace, state, out)
			if err == nil {
				output.Close()
				t.Fatal("invalid output preflight accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_PATH_PAYLOAD") {
				t.Fatal("raw preflight error leaked")
			}
		})
	}
}
func TestSuiteHistoryReaderBoundedPrivateAndPayloadSafe(t *testing.T) {
	for _, mode := range []string{"permissions", "link", "oversize", "directory", "unknown", "duplicate", "null"} {
		t.Run(mode, func(t *testing.T) {
			_, _, r, _ := suiteEvidenceFixture(t)
			path := filepath.Join(t.TempDir(), "receipt.json")
			raw := mustSuiteCanonical(t, r)
			switch mode {
			case "oversize":
				raw = bytes.Repeat([]byte(" "), suiteEvidenceMaxBytes+1)
			case "unknown":
				raw = bytes.Replace(raw, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":1,"PRIVATE_READER_PAYLOAD":"CANARY"`), 1)
			case "duplicate":
				raw = bytes.Replace(raw, []byte(`"runID":`), []byte(`"runID":"PRIVATE_READER_PAYLOAD","runID":`), 1)
			case "null":
				raw = bytes.Replace(raw, []byte(`"requiredImages":[]`), []byte(`"requiredImages":null`), 1)
			}
			if mode == "directory" {
				os.Mkdir(path, 0700)
			} else {
				os.WriteFile(path, raw, 0600)
			}
			if mode == "permissions" {
				os.Chmod(path, 0644)
			}
			if mode == "link" {
				target := path + "-target"
				os.Rename(path, target)
				os.Symlink(target, path)
			}
			_, err := readSuiteEvaluationReceiptFile(path)
			if err == nil {
				t.Fatal("unsafe receipt read accepted")
			}
			if strings.Contains(err.Error(), "PRIVATE_READER_PAYLOAD") || strings.Contains(err.Error(), "CANARY") {
				t.Fatal("raw reader payload leaked")
			}
		})
	}
}
