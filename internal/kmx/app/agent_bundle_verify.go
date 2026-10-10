package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// VerifyAgentSessionsOptions names the destination independently of receipt data.
type VerifyAgentSessionsOptions struct {
	ReceiptPath, Sessions, SessionsCA string
	Timeout                           time.Duration
}

type sessionsVerificationReceipt struct {
	Version             int                        `json:"version"`
	SourceReceiptSHA256 string                     `json:"sourceReceiptSHA256"`
	ReferenceRevision   string                     `json:"referenceRevision"`
	HostImplementation  string                     `json:"hostImplementation"`
	Address             string                     `json:"address"`
	Result              string                     `json:"result"`
	Cases               []sessionsVerificationCase `json:"cases"`
}

type sessionsVerificationCase struct {
	ID          string              `json:"id"`
	SessionUID  string              `json:"sessionUID"`
	JournalHead sessionsJournalHead `json:"journalHead"`
	agentsessions.VerifyResult
}

// VerifyAgentSessions checks a receipt's recorded prefixes under the local reference
// chat harness. It does not re-evaluate expectations or attest the host's code.
func (a *App) VerifyAgentSessions(opt VerifyAgentSessionsOptions) error {
	if a.Out == nil {
		return fmt.Errorf("verify requires an output stream")
	}
	if strings.TrimSpace(opt.Sessions) == "" {
		return fmt.Errorf("--sessions is required")
	}
	address, err := agentsessions.NormalizeAddress(opt.Sessions)
	if err != nil {
		return err
	}
	timeout := opt.Timeout
	if timeout == 0 {
		timeout = defaultEvaluationCaseTimeout
	}
	if timeout < 10*time.Second || timeout > maxEvaluationCaseTimeout {
		return fmt.Errorf("--timeout must be between 10s and %s", maxEvaluationCaseTimeout)
	}
	receipt, raw, err := readSessionsVerificationSource(opt.ReceiptPath)
	if err != nil {
		return err
	}
	recordedAddress, err := agentsessions.NormalizeAddress(receipt.Target.Identity.Address)
	if err != nil {
		return fmt.Errorf("invalid sessions receipt address")
	}
	if address != recordedAddress {
		return fmt.Errorf("--sessions does not match the receipt's recorded address")
	}
	// No connection (including DNS/TLS) is attempted until both receipt and the
	// operator's destination agree. Dial the original operator spelling so comparison
	// normalization cannot relax the transport's literal-loopback-only plaintext rule.
	ctx, stop := signal.NotifyContext(a.operationContext(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := agentsessions.Dial(agentsessions.Options{Address: opt.Sessions, CAFile: opt.SessionsCA})
	if err != nil {
		return err
	}
	defer client.Close()
	sourceHash := sha256.Sum256(raw)
	report := sessionsVerificationReceipt{Version: 1, SourceReceiptSHA256: fmt.Sprintf("%x", sourceHash), ReferenceRevision: agentsessions.ReferenceRevision, HostImplementation: "unknown", Address: address, Result: "equivalent"}
	for _, entry := range receipt.Cases {
		result, err := client.VerifyCase(ctx, agentsessions.VerifyRequest{SessionUID: entry.SessionUID, Harness: entry.Harness, Model: entry.Model, Head: agentsessions.JournalHead{Seq: entry.JournalHead.Seq, Hash: entry.JournalHead.Hash}, AnswerSHA256: entry.AnswerSHA256})
		if err != nil && result.Status == "" {
			return fmt.Errorf("sessions verification unavailable")
		}
		report.Cases = append(report.Cases, sessionsVerificationCase{ID: entry.ID, SessionUID: entry.SessionUID, JournalHead: entry.JournalHead, VerifyResult: result})
		if result.Status != "equivalent" && report.Result == "equivalent" {
			report.Result = result.Status
		}
		fmt.Fprintf(a.Out, "%s: %s (model calls: %d)\n", entry.ID, result.Status, result.ModelCalls)
	}
	reportPath := filepath.Join(filepath.Dir(opt.ReceiptPath), "verify-"+filepath.Base(opt.ReceiptPath)+".json")
	reportRaw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sessions verification report: %w", err)
	}
	if err := writePrivateAgentFile(reportPath, append(reportRaw, '\n')); err != nil {
		return fmt.Errorf("verification finished but its report was not saved: %w", err)
	}
	fmt.Fprintf(a.Out, "verification %s (local reference; host implementation/version unknown)\nreport: %s\n", report.Result, reportPath)
	if report.Result != "equivalent" {
		return fmt.Errorf("sessions receipt did not verify: %s", report.Result)
	}
	return nil
}

const maxSessionsReceiptBytes = 1 << 20

func readSessionsVerificationSource(path string) (sessionsEvaluationReceipt, []byte, error) {
	var receipt sessionsEvaluationReceipt
	// The sibling report shares this directory. Match evaluation's refusal of a
	// linked receipt directory, and never consume a linked or non-regular input.
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return receipt, nil, fmt.Errorf("read sessions receipt: parent must be a directory, not a link")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return receipt, nil, fmt.Errorf("read sessions receipt: %w", err)
	}
	if !info.Mode().IsRegular() {
		return receipt, nil, fmt.Errorf("sessions receipt must be a regular file, not a link")
	}
	file, err := os.Open(path)
	if err != nil {
		return receipt, nil, fmt.Errorf("read sessions receipt: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSessionsReceiptBytes+1))
	if err != nil {
		return receipt, nil, fmt.Errorf("read sessions receipt: %w", err)
	}
	if len(raw) > maxSessionsReceiptBytes {
		return receipt, nil, fmt.Errorf("sessions receipt exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(raw) || !unambiguousSessionsReceiptJSON(raw) || decoder.Decode(&receipt) != nil {
		return receipt, nil, fmt.Errorf("invalid sessions receipt JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return receipt, nil, fmt.Errorf("invalid sessions receipt JSON")
	}
	if receipt.Target.Runtime != "agentsessions" {
		return receipt, nil, fmt.Errorf("verify requires an agentsessions receipt")
	}
	if receipt.Target.Identity.Version != 1 || receipt.Target.Identity.Provenance != "host-reported" {
		return receipt, nil, fmt.Errorf("unsupported sessions receipt identity")
	}
	if !verificationDigest(receipt.PortableDigest) || !verificationDigest(receipt.CasesDigest) || !verificationIdentity(receipt.Bundle) || len(receipt.Cases) == 0 || len(receipt.Cases) > 1000 || receipt.Result != "pass" && receipt.Result != "fail" {
		return receipt, nil, fmt.Errorf("invalid sessions receipt evidence")
	}
	if !evaluationReceiptVersion(receipt.SchemaVersion, receipt.RunID) {
		return receipt, nil, fmt.Errorf("invalid sessions receipt version")
	}
	if !evaluationReceiptJSONShape(raw, receipt.SchemaVersion, receipt.RunID, false) {
		return receipt, nil, fmt.Errorf("invalid sessions receipt JSON shape")
	}
	seen := map[string]bool{}
	sessions := map[string]bool{}
	var outcomes []bundleEvaluationResult
	for _, c := range receipt.Cases {
		if !verificationIdentity(c.ID) || !verificationIdentity(c.SessionUID) || !verificationIdentity(c.Harness) || !verificationIdentity(c.Model) || c.ModelMixed || c.Verdict != "pass" && c.Verdict != "fail" || c.JournalHead.Seq < 1 || !verificationDigest(c.JournalHead.Hash) || !verificationDigest(c.AnswerSHA256) || seen[c.ID] || sessions[c.SessionUID] {
			return receipt, nil, fmt.Errorf("invalid sessions receipt case evidence")
		}
		if receipt.SchemaVersion == 2 {
			if !evaluationCaseEvidence(c.CaseDigest, c.InputDigest, c.Verdict, c.Detail, c.Assertions) {
				return receipt, nil, fmt.Errorf("invalid sessions receipt assertion evidence")
			}
		} else if c.CaseDigest != "" || c.InputDigest != "" || len(c.Assertions) != 0 {
			return receipt, nil, fmt.Errorf("invalid legacy sessions receipt evidence")
		}
		seen[c.ID], sessions[c.SessionUID] = true, true
		outcomes = append(outcomes, bundleEvaluationResult{Verdict: c.Verdict})
	}
	if receipt.SchemaVersion == 2 && bundleEvaluationOverall(outcomes) != receipt.Result {
		return receipt, nil, fmt.Errorf("incoherent sessions receipt result")
	}
	return receipt, raw, nil
}

// encoding/json accepts duplicate keys, including case-folded struct fields.
// Refuse ambiguity before decoding so no later member can silently hide evidence.
func unambiguousSessionsReceiptJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var scan func(int) bool
	scan = func(depth int) bool {
		token, err := decoder.Token()
		if err != nil || depth > 32 {
			return false
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		if delimiter != '{' && delimiter != '[' {
			return false
		}
		var keys []string
		for decoder.More() {
			if delimiter == '{' {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || len(keys) >= 64 {
					return false
				}
				for _, seen := range keys {
					if strings.EqualFold(key, seen) {
						return false
					}
				}
				keys = append(keys, key)
			}
			if !scan(depth + 1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && (delimiter == '{' && closing == json.Delim('}') || delimiter == '[' && closing == json.Delim(']'))
	}
	if !scan(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

// Check raw members as well as typed values: null scalars must not become zero,
// and empty v2 members must not disguise a receipt as legacy evidence.
func evaluationReceiptJSONShape(raw []byte, version int, runID string, legacyOrka bool) bool {
	if !evaluationReceiptVersion(version, runID) || !evaluationDiffNullJSON(raw, legacyOrka && version == 0) {
		return false
	}
	if version == 2 {
		return true
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return false
	}
	for key, value := range root {
		if !strings.EqualFold(key, "cases") {
			continue
		}
		var cases []map[string]json.RawMessage
		if json.Unmarshal(value, &cases) != nil {
			return false
		}
		for _, c := range cases {
			for member := range c {
				if strings.EqualFold(member, "caseDigest") || strings.EqualFold(member, "inputDigest") || strings.EqualFold(member, "assertions") {
					return false
				}
			}
		}
	}
	return true
}

// These payload-free checks are shared with offline v2 receipt consumers.
func evaluationReceiptVersion(version int, runID string) bool {
	return version == 0 && runID == "" || version == 2 && evaluationRunID(runID)
}

var evaluationEvidenceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

func evaluationAssertionRows(rows []agentruntime.EvaluationAssertionResult) bool {
	if len(rows) == 0 || len(rows) > agentruntime.EvaluationMaxAssertions {
		return false
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !evaluationEvidenceID.MatchString(row.ID) || !verificationIdentity(row.ID) || seen[row.ID] || !verificationDigest(row.DefinitionDigest) {
			return false
		}
		seen[row.ID] = true
		switch row.Type {
		case "toolCalled", "toolNotCalled":
			if row.Verdict != agentruntime.EvaluationUnknown || row.Reason != "runtime does not support tools" {
				return false
			}
		case "contains", "notContains", "regex":
			switch row.Verdict {
			case agentruntime.EvaluationPass:
				if row.Reason != "assertion satisfied" {
					return false
				}
			case agentruntime.EvaluationFail:
				if row.Reason != "assertion not satisfied" {
					return false
				}
			case agentruntime.EvaluationUnknown:
				if row.Reason != "answer unavailable" {
					return false
				}
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

func evaluationCaseEvidence(caseDigest, inputDigest, verdict, detail string, rows []agentruntime.EvaluationAssertionResult) bool {
	if !verificationDigest(caseDigest) || !verificationDigest(inputDigest) || !evaluationAssertionRows(rows) {
		return false
	}
	if detail != "" && detail != evaluationExecutionUnavailable && detail != evaluationResultUnavailable && detail != evaluationExecutionFailed {
		return false
	}
	if detail != "" {
		// No assertion can claim known evidence when execution attribution failed.
		for _, row := range rows {
			if row.Verdict != agentruntime.EvaluationUnknown {
				return false
			}
		}
		if detail == evaluationExecutionFailed {
			return verdict == "fail"
		}
		return verdict == "unknown"
	}
	return verdict == string(agentruntime.EvaluationAssertionsVerdict(rows))
}

func verificationDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func verificationIdentity(value string) bool {
	if len(value) == 0 || len(value) > 256 || secretshapes.Match(value) != nil {
		return false
	}
	for _, c := range value {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}
