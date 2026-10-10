package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// EvaluationDiffOptions names two local evaluation receipts. Diff never loads
// configuration, connects to a runtime, or re-evaluates authored assertions.
type EvaluationDiffOptions struct{ Before, After string }

type evaluationDiffIdentity struct {
	Runtime, Address, Harness, Model, ClusterUID, Namespace, Agent, AgentUID string
}

type normalizedEvaluation struct {
	Version                                                       int
	RunID, Bundle, PortableDigest, CasesDigest, GitCommit, Result string
	FullCaseSet, IdentityComplete                                 bool
	Identity                                                      evaluationDiffIdentity
	Cases                                                         map[string]normalizedEvaluationCase
}

type normalizedEvaluationCase struct {
	CaseDigest, InputDigest, Verdict string
	Assertions                       map[string]agentruntime.EvaluationAssertionResult
}

// Report rows intentionally cannot hold answers, operands, journal payloads or
// arbitrary diagnostics. Only validated identifiers and fixed statuses cross
// the normalization boundary.
type evaluationDiffRow struct {
	CaseID, AssertionID, BeforeType, AfterType, BeforeDigest, AfterDigest string
	Before, After, Status                                                 string
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
	// Inspect only the discriminant before choosing a typed receipt. The full
	// decoder below rejects every field outside that runtime's schema.
	var target struct {
		SchemaVersion int    `json:"schemaVersion"`
		RunID         string `json:"runID"`
		Target        struct {
			Runtime string `json:"runtime"`
		} `json:"target"`
	}
	if json.Unmarshal(raw, &target) != nil {
		return invalid()
	}
	legacyOrka := target.Target.Runtime == string(agentruntime.Orka) && target.SchemaVersion == 0 && target.RunID == ""
	if !evaluationDiffNullJSON(raw, legacyOrka) {
		return invalid()
	}
	decode := func(receipt any) bool {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(receipt) != nil {
			return false
		}
		var extra any
		return d.Decode(&extra) == io.EOF
	}
	switch target.Target.Runtime {
	case "agentsessions":
		var r sessionsEvaluationReceipt
		if !decode(&r) {
			return invalid()
		}
		n, ok := normalizeSessionsEvaluation(r)
		if !ok {
			return invalid()
		}
		return n, nil
	case string(agentruntime.Orka):
		var r bundleEvaluationReceipt
		if !decode(&r) {
			return invalid()
		}
		n, ok := normalizeOrkaEvaluation(r)
		if !ok {
			return invalid()
		}
		return n, nil
	default:
		return invalid()
	}
}

// Go's typed decoder accepts null as scalar zero. The only nullable members
// produced by old receipts were Orka cases' matched/missing arrays. Keep that
// wire compatibility without accepting null versions, identities or v2 rows.
func evaluationDiffNullJSON(raw []byte, legacyOrka bool) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var scan func(scope string, nullable bool) bool
	scan = func(scope string, nullable bool) bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if token == nil {
			return nullable
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		for decoder.More() {
			childScope := ""
			allowedNull := false
			if delimiter == '{' {
				member, err := decoder.Token()
				key, ok := member.(string)
				if err != nil || !ok {
					return false
				}
				if scope == "root" && strings.EqualFold(key, "cases") {
					childScope = "cases"
				}
				allowedNull = legacyOrka && scope == "case" && (strings.EqualFold(key, "matched") || strings.EqualFold(key, "missing"))
			} else if scope == "cases" {
				childScope = "case"
			}
			if !scan(childScope, allowedNull) {
				return false
			}
		}
		_, err = decoder.Token()
		return err == nil
	}
	if !scan("root", false) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

var evaluationDiffName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,255}$`)
var evaluationDiffCommit = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func diffIdentity(value string) bool {
	return evaluationDiffName.MatchString(value) && verificationIdentity(value)
}
func diffOptionalIdentity(value string) bool { return value == "" || diffIdentity(value) }
func diffOptionalDigest(value string) bool   { return value == "" || verificationDigest(value) }
func diffOptionalModel(value string) bool    { return value == "" || verificationIdentity(value) }

func evaluationDiffMetadata(version int, runID, bundle, portable, cases, commit, result string, full bool) (normalizedEvaluation, bool) {
	n := normalizedEvaluation{Version: version, RunID: runID, Bundle: bundle, PortableDigest: portable, CasesDigest: cases, GitCommit: commit, Result: result, FullCaseSet: full, Cases: map[string]normalizedEvaluationCase{}}
	ok := evaluationReceiptVersion(version, runID) && evaluationEvidenceID.MatchString(bundle) && verificationIdentity(bundle) && verificationDigest(portable) && verificationDigest(cases) && (commit == "uncommitted" || evaluationDiffCommit.MatchString(commit)) && (result == "pass" || result == "fail" || result == "unknown")
	return n, ok
}

func addEvaluationDiffCase(n *normalizedEvaluation, id, caseDigest, inputDigest, verdict, detail string, rows []agentruntime.EvaluationAssertionResult) bool {
	if !evaluationEvidenceID.MatchString(id) || !verificationIdentity(id) {
		return false
	}
	if _, exists := n.Cases[id]; exists {
		return false
	}
	if verdict != "pass" && verdict != "fail" && verdict != "unknown" {
		return false
	}
	if n.Version == 2 {
		if !evaluationCaseEvidence(caseDigest, inputDigest, verdict, detail, rows) {
			return false
		}
	} else if caseDigest != "" || inputDigest != "" || len(rows) != 0 {
		return false
	}
	c := normalizedEvaluationCase{CaseDigest: caseDigest, InputDigest: inputDigest, Verdict: verdict, Assertions: map[string]agentruntime.EvaluationAssertionResult{}}
	for _, row := range rows {
		c.Assertions[row.ID] = row
	}
	n.Cases[id] = c
	return true
}

func evaluationDiffAggregate(n normalizedEvaluation) bool {
	if len(n.Cases) == 0 || len(n.Cases) > 1000 {
		return false
	}
	var cases []bundleEvaluationResult
	for _, c := range n.Cases {
		cases = append(cases, bundleEvaluationResult{Verdict: c.Verdict})
	}
	return bundleEvaluationOverall(cases) == n.Result
}

func normalizeSessionsEvaluation(r sessionsEvaluationReceipt) (normalizedEvaluation, bool) {
	n, ok := evaluationDiffMetadata(r.SchemaVersion, r.RunID, r.Bundle, r.PortableDigest, r.CasesDigest, r.GitCommit, r.Result, r.FullCaseSet)
	if !ok {
		return n, false
	}
	identity := r.Target.Identity
	address, err := agentsessions.NormalizeAddress(identity.Address)
	if err != nil || identity.Version != 1 || identity.Provenance != "host-reported" || !diffOptionalIdentity(identity.Harness) || !diffOptionalModel(identity.Model) {
		return n, false
	}
	n.Identity = evaluationDiffIdentity{Runtime: "agentsessions", Address: address, Harness: identity.Harness, Model: identity.Model}
	n.IdentityComplete = identity.Harness != "" && identity.Model != ""
	sessions := map[string]bool{}
	for _, c := range r.Cases {
		if !addEvaluationDiffCase(&n, c.ID, c.CaseDigest, c.InputDigest, c.Verdict, c.Detail, c.Assertions) || !diffOptionalIdentity(c.SessionUID) || !diffOptionalIdentity(c.Harness) || !diffOptionalModel(c.Model) || !diffOptionalDigest(c.AnswerSHA256) || c.JournalHead.Seq < 0 || !diffOptionalDigest(c.JournalHead.Hash) {
			return n, false
		}
		if c.SessionUID != "" {
			if sessions[c.SessionUID] {
				return n, false
			}
			sessions[c.SessionUID] = true
		}
		if (c.JournalHead.Seq == 0) != (c.JournalHead.Hash == "") || c.SessionUID == "" && c.JournalHead.Seq != 0 || c.ModelMixed && c.Model != "" {
			return n, false
		}
		completed := c.Detail == "" && r.SchemaVersion == 2
		if completed && (c.SessionUID == "" || c.Harness == "" || c.Model == "" || c.ModelMixed || c.JournalHead.Seq < 1 || c.AnswerSHA256 == "") {
			return n, false
		}
		if c.Detail == evaluationExecutionFailed && (c.SessionUID == "" || c.JournalHead.Seq < 1) {
			return n, false
		}
		if c.ModelMixed || c.Harness == "" || c.Model == "" {
			n.IdentityComplete = false
		}
		if identity.Harness != "" && c.Harness != "" && identity.Harness != c.Harness || identity.Model != "" && c.Model != "" && identity.Model != c.Model {
			return n, false
		}
	}
	return n, evaluationDiffAggregate(n)
}

func normalizeOrkaEvaluation(r bundleEvaluationReceipt) (normalizedEvaluation, bool) {
	n, ok := evaluationDiffMetadata(r.SchemaVersion, r.RunID, r.Bundle, r.PortableDigest, r.CasesDigest, r.GitCommit, r.Result, r.FullCaseSet)
	if !ok {
		return n, false
	}
	target := r.Target
	if target.Context != "" && !verificationIdentity(target.Context) || !diffOptionalIdentity(target.ClusterUID) || !diffOptionalIdentity(target.Namespace) || !diffOptionalIdentity(target.Agent) || !diffOptionalIdentity(target.AgentUID) || !diffOptionalModel(target.Model) {
		return n, false
	}
	n.Identity = evaluationDiffIdentity{Runtime: string(agentruntime.Orka), ClusterUID: target.ClusterUID, Namespace: target.Namespace, Agent: target.Agent, AgentUID: target.AgentUID, Model: target.Model}
	n.IdentityComplete = target.ClusterUID != "" && target.Namespace != "" && target.Agent != "" && target.AgentUID != "" && target.Model != ""
	tasks, names := map[string]bool{}, map[string]bool{}
	for _, c := range r.Cases {
		if !addEvaluationDiffCase(&n, c.ID, c.CaseDigest, c.InputDigest, c.Verdict, c.Detail, c.Assertions) || !diffOptionalIdentity(c.TaskName) || !diffOptionalIdentity(c.TaskUID) || !diffOptionalDigest(c.AnswerSHA256) {
			return n, false
		}
		if r.SchemaVersion == 2 && (len(c.Matched) != 0 || len(c.Missing) != 0) {
			return n, false
		}
		if c.TaskUID != "" && c.TaskName == "" {
			return n, false
		}
		if c.TaskUID != "" {
			if tasks[c.TaskUID] {
				return n, false
			}
			tasks[c.TaskUID] = true
		}
		if c.TaskName != "" {
			if names[c.TaskName] {
				return n, false
			}
			names[c.TaskName] = true
		}
		if r.SchemaVersion == 2 {
			if c.Detail != evaluationExecutionUnavailable && (c.TaskName == "" || c.TaskUID == "") {
				return n, false
			}
			if c.Detail == "" && c.AnswerSHA256 == "" {
				return n, false
			}
		}
	}
	return n, evaluationDiffAggregate(n)
}

func compareEvaluations(before, after normalizedEvaluation) evaluationDiffReport {
	r := evaluationDiffReport{Before: before, After: after, CaseSetChanged: before.CasesDigest != after.CasesDigest}
	switch {
	case before.Version != 2 || after.Version != 2:
		r.IncompatibleReason = "legacy receipt has no assertion evidence"
	case !before.FullCaseSet || !after.FullCaseSet:
		r.IncompatibleReason = "partial case set"
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
				add(evaluationDiffRow{CaseID: id, AssertionID: aid, BeforeType: row.Type, BeforeDigest: row.DefinitionDigest, Before: string(row.Verdict), Status: "assertionRemoved"})
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
		for aid, br := range b.Assertions {
			ar, exists := a.Assertions[aid]
			row := evaluationDiffRow{CaseID: id, AssertionID: aid, BeforeType: br.Type, BeforeDigest: br.DefinitionDigest, Before: string(br.Verdict)}
			if !exists {
				row.Status = "assertionRemoved"
				add(row)
				continue
			}
			row.AfterType, row.AfterDigest, row.After = ar.Type, ar.DefinitionDigest, string(ar.Verdict)
			switch {
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
				add(evaluationDiffRow{CaseID: id, AssertionID: aid, AfterType: ar.Type, AfterDigest: ar.DefinitionDigest, After: string(ar.Verdict), Status: "assertionAdded"})
			}
		}
	}
	for id, a := range after.Cases {
		if _, exists := before.Cases[id]; !exists {
			add(evaluationDiffRow{CaseID: id, Status: "caseAdded", AfterDigest: a.CaseDigest})
			for aid, row := range a.Assertions {
				add(evaluationDiffRow{CaseID: id, AssertionID: aid, AfterType: row.Type, AfterDigest: row.DefinitionDigest, After: string(row.Verdict), Status: "assertionAdded"})
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

func writeEvaluationDiffReport(out io.Writer, r evaluationDiffReport) error {
	var text strings.Builder
	for _, item := range []struct {
		label string
		n     normalizedEvaluation
	}{{"before", r.Before}, {"after", r.After}} {
		fmt.Fprintf(&text, "%s: bundle %s runtime %s schema %d run %s\nportableDigest: %s\nGitCommit: %s\ncasesDigest: %s\nfullCaseSet: %t\n", item.label, item.n.Bundle, item.n.Identity.Runtime, item.n.Version, orDash(item.n.RunID), item.n.PortableDigest, item.n.GitCommit, item.n.CasesDigest, item.n.FullCaseSet)
	}
	fmt.Fprintf(&text, "caseSetChanged: %t\n", r.CaseSetChanged)
	if r.IncompatibleReason != "" {
		fmt.Fprintf(&text, "comparison incompatible: %s\n", r.IncompatibleReason)
	} else {
		fmt.Fprintln(&text, "CASE\tASSERTION\tTYPE BEFORE/AFTER\tVERDICT BEFORE/AFTER\tSTATUS\tDIGEST BEFORE/AFTER")
		for _, row := range r.Rows {
			fmt.Fprintf(&text, "%s\t%s\t%s/%s\t%s/%s\t%s\t%s/%s\n", row.CaseID, orDash(row.AssertionID), orDash(row.BeforeType), orDash(row.AfterType), orDash(row.Before), orDash(row.After), row.Status, orDash(row.BeforeDigest), orDash(row.AfterDigest))
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
