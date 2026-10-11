package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Sessions evidence is separate from Orka's deployed-Agent receipt. No prompts,
// answers, expectation text, tool payloads or arbitrary server errors are persisted.
// Unknown outcomes retain any session/journal references available for inspection.
type sessionsEvaluationReceipt struct {
	Bundle         string                     `json:"bundle"`
	PortableDigest string                     `json:"portableDigest"`
	CasesDigest    string                     `json:"casesDigest"`
	FullCaseSet    bool                       `json:"fullCaseSet"`
	GitCommit      string                     `json:"gitCommit"`
	Target         sessionsEvaluationTarget   `json:"target"`
	Result         string                     `json:"result"`
	Cases          []sessionsEvaluationResult `json:"cases"`
}

type sessionsEvaluationTarget struct {
	Runtime  string               `json:"runtime"`
	Identity sessionsHostIdentity `json:"identity"`
}

// Version 1 identifies only an endpoint, a returned harness name and journaled
// model name, not an attested host or verified harness descriptor. Descriptor
// discovery can introduce a new identity version without changing Orka evidence.
type sessionsHostIdentity struct {
	Version    int    `json:"version"`
	Provenance string `json:"provenance"`
	Address    string `json:"address"`
	Harness    string `json:"harness,omitempty"`
	Model      string `json:"model,omitempty"`
}

type sessionsJournalHead struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

type sessionsEvaluationResult struct {
	ID           string              `json:"id"`
	Verdict      string              `json:"verdict"`
	SessionUID   string              `json:"sessionUID,omitempty"`
	Harness      string              `json:"harness,omitempty"`
	Model        string              `json:"model,omitempty"`
	ModelMixed   bool                `json:"modelMixed,omitempty"`
	JournalHead  sessionsJournalHead `json:"journalHead"`
	AnswerSHA256 string              `json:"answerSHA256,omitempty"`
	Detail       string              `json:"detail,omitempty"`
}

// Keep the latest evaluation per endpoint/harness, like Orka's latest evaluation
// per destination. A runtime-specific domain separates their filename identities.
func writeSessionsEvaluationReceipt(bundle string, receipt sessionsEvaluationReceipt) (string, error) {
	dir := filepath.Join(bundle, "receipts")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := checkLiftReceiptsDir(bundle); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("agentsessions\x00" + receipt.Target.Identity.Address + "\x00chat"))
	path := filepath.Join(dir, fmt.Sprintf("eval-%x.json", key))
	return path, writePrivateAgentFile(path, append(raw, '\n'))
}
