package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// EvaluationDiffOptions names two local evaluation receipts. Diff never loads
// configuration, connects to a runtime, or re-evaluates authored assertions.
type EvaluationDiffOptions struct{ Before, After string }

type evaluationDiffIdentity struct {
	Runtime, Address, Harness, Model, ClusterUID, Namespace, Agent, AgentUID string
	RuntimeVersion, Engine, ModelConfigUID, SourceCommit                     string
	ControllerImage, ADKImage                                                kagentEvaluationImage
}

type normalizedEvaluation struct {
	SuiteIdentity, SuiteInventory                                 string
	Version                                                       int
	RunID, Bundle, PortableDigest, CasesDigest, GitCommit, Result string
	FullCaseSet, IdentityComplete, IdentityConflict               bool
	Identity                                                      evaluationDiffIdentity
	Cases                                                         map[string]normalizedEvaluationCase
}

type normalizedEvaluationCase struct {
	CaseDigest, InputDigest, Verdict string
	IdentityComplete                 bool
	Assertions                       map[string]agentruntime.EvaluationAssertionResult
}

// Report rows intentionally cannot hold answers, operands, journal payloads or
// arbitrary diagnostics. Only validated identifiers and fixed statuses cross
// the normalization boundary.
type evaluationDiffRow struct {
	CaseID, AssertionID, BeforeType, AfterType, BeforeDigest, AfterDigest string
	Before, After, Status                                                 string
	BeforeScores, AfterScores                                             []int
}

type evaluationDiffReport struct {
	Before, After                          normalizedEvaluation
	CaseSetChanged, Incomplete, Regression bool
	IncompatibleReason                     string
	Rows                                   []evaluationDiffRow
}

func (a *App) DiffAgentEvaluations(opt EvaluationDiffOptions) error {
	if a.Out == nil {
		return fmt.Errorf("evaluation comparison requires an output stream")
	}
	before, err := readEvaluationDiffSource(opt.Before)
	if err != nil {
		return err
	}
	after, err := readEvaluationDiffSource(opt.After)
	if err != nil {
		return err
	}
	report := compareEvaluations(before, after)
	if err := writeEvaluationDiffReport(a.Out, report); err != nil {
		return fmt.Errorf("write evaluation comparison failed")
	}
	if report.IncompatibleReason != "" {
		return fmt.Errorf("evaluation comparison is incompatible")
	}
	if report.Regression {
		return fmt.Errorf("evaluation comparison has regressions")
	}
	if report.Incomplete {
		return fmt.Errorf("evaluation comparison is incomplete")
	}
	return nil
}

func readEvaluationDiffSource(path string) (normalizedEvaluation, error) {
	var empty normalizedEvaluation
	invalid := func() (normalizedEvaluation, error) { return empty, fmt.Errorf("invalid evaluation receipt") }
	absolute, err := filepath.Abs(path)
	if err != nil {
		return invalid()
	}
	// Check every ancestor, not only the immediate parent: a linked ancestor
	// must not disguise a linked receipt directory.
	for dir := filepath.Dir(absolute); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return invalid()
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSessionsReceiptBytes {
		return invalid()
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return invalid()
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(absolute))
	if err != nil {
		return invalid()
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return invalid()
	}
	current, err := root.Lstat(filepath.Base(absolute))
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return invalid()
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSessionsReceiptBytes+1))
	if err != nil || len(raw) > maxSessionsReceiptBytes || !utf8.Valid(raw) || !unambiguousSessionsReceiptJSON(raw) {
		return invalid()
	}
	// Only native full-passing evidence is accepted; old receipts have no reader.
	var target struct {
		Domain string `json:"domain"`
	}
	if json.Unmarshal(raw, &target) != nil {
		return invalid()
	}
	if target.Domain == suiteSessionsPassingDomain {
		return invalid()
	}
	if target.Domain == "kmx.native-suite.passing/v1" {
		r, err := readSuitePassingReceipt(context.Background(), absolute)
		if err != nil {
			return invalid()
		}
		n, ok := normalizeSuitePassingEvaluation(r)
		if !ok {
			return invalid()
		}
		return n, nil
	}
	return invalid()
}

func compareEvaluations(before, after normalizedEvaluation) evaluationDiffReport {
	r := evaluationDiffReport{Before: before, After: after, CaseSetChanged: before.CasesDigest != after.CasesDigest}
	switch {
	case before.Identity.Runtime != after.Identity.Runtime:
		r.IncompatibleReason = "runtime identity differs"
	case before.SuiteIdentity != after.SuiteIdentity:
		r.IncompatibleReason = "prepared suite subject differs"
	case before.CasesDigest != after.CasesDigest || before.SuiteInventory != after.SuiteInventory:
		r.IncompatibleReason = "suite case inventory differs"
	case before.Bundle != after.Bundle:
		r.IncompatibleReason = "different bundle"
	case !before.FullCaseSet || !after.FullCaseSet:
		r.IncompatibleReason = "partial case set"
	case before.IdentityConflict || after.IdentityConflict:
		r.IncompatibleReason = "runtime identity differs"
	case !before.IdentityComplete || !after.IdentityComplete:
		r.IncompatibleReason = "runtime identity unavailable"
	case before.Identity != after.Identity:
		r.IncompatibleReason = "runtime identity differs"
	}
	if r.IncompatibleReason != "" {
		r.Incomplete = true
		return r
	}
	r.Incomplete = r.CaseSetChanged
	add := func(row evaluationDiffRow) {
		r.Rows = append(r.Rows, row)
		switch row.Status {
		case "regression":
			r.Regression = true
		case "stable", "fix":
		default:
			r.Incomplete = true
		}
	}
	for id, b := range before.Cases {
		a, exists := after.Cases[id]
		if !exists {
			add(evaluationDiffRow{CaseID: id, Status: "caseRemoved", BeforeDigest: b.CaseDigest})
			for aid, row := range b.Assertions {
				diffRow := evaluationDiffRow{CaseID: id, AssertionID: aid, BeforeType: row.Type, BeforeDigest: row.DefinitionDigest, Before: string(row.Verdict), Status: "assertionRemoved"}
				if row.Judge != nil {
					diffRow.BeforeScores = row.Judge.Scores
				}
				add(diffRow)
			}
			continue
		}
		sameInput := b.InputDigest == a.InputDigest
		if !sameInput {
			add(evaluationDiffRow{CaseID: id, Status: "inputChanged", BeforeDigest: b.InputDigest, AfterDigest: a.InputDigest})
		}
		if b.CaseDigest != a.CaseDigest {
			add(evaluationDiffRow{CaseID: id, Status: "caseSourceChanged", BeforeDigest: b.CaseDigest, AfterDigest: a.CaseDigest})
		}
		identityUnavailable := !b.IdentityComplete || !a.IdentityComplete
		if identityUnavailable {
			add(evaluationDiffRow{CaseID: id, Before: b.Verdict, After: a.Verdict, Status: "unknown", BeforeDigest: b.CaseDigest, AfterDigest: a.CaseDigest})
		}
		for aid, br := range b.Assertions {
			ar, exists := a.Assertions[aid]
			row := evaluationDiffRow{CaseID: id, AssertionID: aid, BeforeType: br.Type, BeforeDigest: br.DefinitionDigest, Before: string(br.Verdict)}
			if br.Judge != nil {
				row.BeforeScores = br.Judge.Scores
			}
			if !exists {
				row.Status = "assertionRemoved"
				add(row)
				continue
			}
			row.AfterType, row.AfterDigest, row.After = ar.Type, ar.DefinitionDigest, string(ar.Verdict)
			if ar.Judge != nil {
				row.AfterScores = ar.Judge.Scores
			}
			// Preserve structural changes, but make no assertion-level verdict
			// claims for a case whose runtime identity was not observed.
			if identityUnavailable {
				if br.Type != ar.Type || br.DefinitionDigest != ar.DefinitionDigest {
					row.Status = "definitionChanged"
					add(row)
				}
				continue
			}
			switch {
			case br.Type == "llmJudge" && ar.Type == "llmJudge" && !evaluationJudgeSameIdentity(br.Judge, ar.Judge):
				row.Status = "identityChanged"
			case br.Type != ar.Type || br.DefinitionDigest != ar.DefinitionDigest:
				row.Status = "definitionChanged"
			case !sameInput:
				row.Status = "inputChanged"
			case ar.Verdict == agentruntime.EvaluationUnknown && br.Verdict == agentruntime.EvaluationUnknown:
				row.Status = "unknown"
			case ar.Verdict == agentruntime.EvaluationUnknown:
				row.Status = "lostEvidence"
			case br.Verdict == agentruntime.EvaluationUnknown:
				row.Status = "gainedEvidence"
			case br.Verdict == agentruntime.EvaluationPass && ar.Verdict == agentruntime.EvaluationFail:
				row.Status = "regression"
			case br.Verdict == agentruntime.EvaluationFail && ar.Verdict == agentruntime.EvaluationPass:
				row.Status = "fix"
			default:
				row.Status = "stable"
			}
			add(row)
		}
		for aid, ar := range a.Assertions {
			if _, exists := b.Assertions[aid]; !exists {
				row := evaluationDiffRow{CaseID: id, AssertionID: aid, AfterType: ar.Type, AfterDigest: ar.DefinitionDigest, After: string(ar.Verdict), Status: "assertionAdded"}
				if ar.Judge != nil {
					row.AfterScores = ar.Judge.Scores
				}
				add(row)
			}
		}
	}
	for id, a := range after.Cases {
		if _, exists := before.Cases[id]; !exists {
			add(evaluationDiffRow{CaseID: id, Status: "caseAdded", AfterDigest: a.CaseDigest})
			for aid, row := range a.Assertions {
				diffRow := evaluationDiffRow{CaseID: id, AssertionID: aid, AfterType: row.Type, AfterDigest: row.DefinitionDigest, After: string(row.Verdict), Status: "assertionAdded"}
				if row.Judge != nil {
					diffRow.AfterScores = row.Judge.Scores
				}
				add(diffRow)
			}
		}
	}
	sort.Slice(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		if a.CaseID != b.CaseID {
			return a.CaseID < b.CaseID
		}
		if a.AssertionID != b.AssertionID {
			return a.AssertionID < b.AssertionID
		}
		return a.Status < b.Status
	})
	return r
}

func evaluationJudgeSameIdentity(before, after *agentruntime.EvaluationJudgeEvidence) bool {
	return before != nil && after != nil && before.RequestStyle == after.RequestStyle && before.Model == after.Model && before.ModelVersion == after.ModelVersion && before.Provenance == after.Provenance && before.EvaluatorDigest == after.EvaluatorDigest && before.ConfigurationDigest == after.ConfigurationDigest && before.MinScore == after.MinScore
}

func writeEvaluationDiffReport(out io.Writer, r evaluationDiffReport) error {
	var text strings.Builder
	for _, item := range []struct {
		label string
		n     normalizedEvaluation
	}{{"before", r.Before}, {"after", r.After}} {
		fmt.Fprintf(&text, "%s: suite %s runtime %s schema %d run %s\nsuiteDigest: %s\ncasesDigest: %s\nfullCaseSet: %t\n", item.label, item.n.Bundle, item.n.Identity.Runtime, item.n.Version, item.n.RunID, item.n.PortableDigest, item.n.CasesDigest, item.n.FullCaseSet)
	}
	fmt.Fprintf(&text, "caseSetChanged: %t\n", r.CaseSetChanged)
	if r.IncompatibleReason != "" {
		fmt.Fprintf(&text, "comparison incompatible: %s\n", r.IncompatibleReason)
	} else {
		fmt.Fprintln(&text, "CASE\tASSERTION\tTYPE BEFORE/AFTER\tVERDICT BEFORE/AFTER\tSTATUS\tDIGEST BEFORE/AFTER")
		for _, row := range r.Rows {
			fmt.Fprintf(&text, "%s\t%s\t%s/%s\t%s/%s\t%s\t%s/%s\n", row.CaseID, orDash(row.AssertionID), orDash(row.BeforeType), orDash(row.AfterType), orDash(row.Before), orDash(row.After), row.Status, orDash(row.BeforeDigest), orDash(row.AfterDigest))
			if row.BeforeType == "llmJudge" || row.AfterType == "llmJudge" {
				fmt.Fprintf(&text, "judge scores %s/%s: %v/%v\n", row.CaseID, row.AssertionID, row.BeforeScores, row.AfterScores)
			}
		}
		status := "complete"
		if r.Regression {
			status = "regressions"
		} else if r.Incomplete {
			status = "incomplete"
		}
		fmt.Fprintf(&text, "comparison: %s (not an evaluation-pass claim)\n", status)
	}
	_, err := io.WriteString(out, text.String())
	return err
}
