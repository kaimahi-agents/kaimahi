package app

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	evaluationExecutionUnavailable = "evaluation execution unavailable"
	evaluationExecutionFailed      = "evaluation execution failed"
	evaluationResultUnavailable    = "evaluation result unavailable"
)

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
