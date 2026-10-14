package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const (
	evaluationExecutionUnavailable = "evaluation execution unavailable"
	evaluationExecutionFailed      = "evaluation execution failed"
	evaluationResultUnavailable    = "evaluation result unavailable"
)

// preflightEvaluationReceiptSize budgets the selected suite before execution.
// 384 bytes covers each indented assertion row (63-byte safe ID, fixed
// type/reason, SHA-256 digest and separators). llmJudge adds its measured
// serialized identity and score object. 4 KiB covers case metadata:
// sessions can retain a 256-byte UID and unexpected model escaped up to six
// bytes per byte, but then the harness is chat. An invalid created harness
// returns before observing a model. IDs/digests do not escape; Orka Task names
// are at most 62 bytes. Tests measure both schemas and session failure shapes.
// Reserve another 8 KiB for the envelope. Known metadata is added using its
// actual JSON string size, including escaping. The first optional string is
// the model: reserve it once in the target and once per case (conservative for
// Orka, required for sessions). Remaining strings occur once. Metadata learned
// during execution (e.g. Task UID) still needs the writer's exact size check.
func preflightEvaluationReceiptSize(cases []bundleEvaluationCase, metadata ...string) error {
	if len(cases) == 0 {
		return fmt.Errorf("evaluation requires at least one case")
	}
	const caseBytes, assertionBytes, envelopeBytes = 4096, 384, 8192
	remaining := maxSessionsReceiptBytes - envelopeBytes
	maxCases := 0
	tooLarge := func() error {
		return fmt.Errorf("selected evaluation cases exceed the conservative 1 MiB receipt budget; supports at most %d cases with one assertion each (fewer with additional assertions); reduce the suite or use --case <id> to run one case", maxCases)
	}
	modelBytes := 0
	for i, value := range metadata {
		// Marshaling a string cannot fail. Measure rather than assume a byte
		// limit: JSON expands HTML characters, controls and invalid UTF-8.
		raw, _ := json.Marshal(value)
		if len(raw) > remaining {
			return tooLarge()
		}
		remaining -= len(raw)
		if i == 0 {
			modelBytes = len(raw)
		}
	}
	// Preserve the ordinary boundary; judge suites report a safe minimum-row
	// limit accounting for their largest configured identity.
	maxRowBytes := assertionBytes
	for _, c := range cases {
		for _, assertion := range c.Case.Assertions {
			if size := evaluationAssertionReceiptBytes(assertion); size > maxRowBytes {
				maxRowBytes = size
			}
		}
	}
	maxCases = remaining / (caseBytes + maxRowBytes + modelBytes)
	for _, c := range cases {
		if remaining < caseBytes+modelBytes {
			return tooLarge()
		}
		remaining -= caseBytes + modelBytes
		// Consume legacy counts and explicit rows separately to keep arithmetic
		// bounded by the remaining budget.
		if len(c.Case.ExpectContains) > remaining/assertionBytes {
			return tooLarge()
		}
		remaining -= len(c.Case.ExpectContains) * assertionBytes
		for _, assertion := range c.Case.Assertions {
			size := evaluationAssertionReceiptBytes(assertion)
			if size > remaining {
				return tooLarge()
			}
			remaining -= size
		}
	}
	return nil
}

// The row reserve covers ordinary fields; add the actual indented judge
// object, including HTML/quote escaping and worst-case scores/threshold.
func evaluationAssertionReceiptBytes(assertion agentruntime.EvaluationAssertion) int {
	if assertion.Type != "llmJudge" {
		return 384
	}
	evidence := agentruntime.EvaluationJudgeEvidence{Model: assertion.JudgeModel, ModelVersion: assertion.JudgeVersion, Provenance: "user-declared", EvaluatorDigest: agentruntime.EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: agentruntime.EvaluationJudgeEvaluatorDigest(), MinScore: 100, Scores: []int{100, 100}}
	raw, _ := json.MarshalIndent(evidence, "          ", "  ")
	// judge member, comma/newline, and indentation on its opening line.
	return 384 + len(raw) + 24
}

func evaluationRunID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func evaluationHistoryPath(bundle, runID string) string {
	return filepath.Join(bundle, "receipts", "runs", "eval-"+runID+".json")
}

// History is immutable, separate from the latest filenames consumed by gates.
// Legacy hand-built receipts remain latest-only; no assertion evidence is invented.
func writeEvaluationRunAndLatest(bundle string, version int, runID, latest string, raw []byte) error {
	if len(raw) > maxSessionsReceiptBytes {
		return fmt.Errorf("evaluation receipt exceeds 1 MiB")
	}
	if version != 2 {
		if version != 0 || runID != "" {
			return fmt.Errorf("unsupported evaluation receipt version")
		}
		return writePrivateAgentFile(latest, raw)
	}
	if !evaluationRunID(runID) {
		return fmt.Errorf("invalid evaluation run identity")
	}
	root, err := os.OpenRoot(bundle)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, dir := range []string{"receipts", "receipts/runs"} {
		if err := root.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := root.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("evaluation receipt directories must be directories, not links")
		}
		if err := root.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	path := filepath.Join("receipts", "runs", "eval-"+runID+".json")
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create immutable evaluation receipt: %w", err)
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			_ = root.Remove(path)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	complete = true
	// A failed latest update leaves the complete archived run available.
	info, err := os.Lstat(latest)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("latest evaluation receipt must be a regular file, not a link")
	}
	return writePrivateAgentFile(latest, raw)
}
