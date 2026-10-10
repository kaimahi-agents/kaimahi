package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func diffFixture(verdict string) sessionsEvaluationReceipt {
	reason := map[string]string{"pass": "assertion satisfied", "fail": "assertion not satisfied", "unknown": "answer unavailable"}[verdict]
	return sessionsEvaluationReceipt{
		SchemaVersion: 2, RunID: strings.Repeat("1", 32), Bundle: "sample", PortableDigest: strings.Repeat("a", 64), CasesDigest: strings.Repeat("b", 64), FullCaseSet: true, GitCommit: "uncommitted",
		Target: sessionsEvaluationTarget{Runtime: "agentsessions", Identity: sessionsHostIdentity{Version: 1, Provenance: "host-reported", Address: "Example.COM.:08080", Harness: "chat", Model: "local/model:1"}}, Result: verdict,
		Cases: []sessionsEvaluationResult{{ID: "case-one", Verdict: verdict, CaseDigest: strings.Repeat("c", 64), InputDigest: strings.Repeat("d", 64), SessionUID: "session-1", Harness: "chat", Model: "local/model:1", JournalHead: sessionsJournalHead{Seq: 5, Hash: strings.Repeat("e", 64)}, AnswerSHA256: strings.Repeat("f", 64), Assertions: []agentruntime.EvaluationAssertionResult{{ID: "assert-one", Type: "contains", DefinitionDigest: strings.Repeat("0", 64), Verdict: agentruntime.EvaluationVerdict(verdict), Reason: reason}}}},
	}
}

func diffWrite(t *testing.T, receipt any) string {
	t.Helper()
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return diffWriteRaw(t, raw)
}

func diffTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func diffWriteRaw(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(diffTempDir(t), "receipt.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func diffExecute(t *testing.T, before, after any) (string, error) {
	t.Helper()
	var out bytes.Buffer
	a := &App{Out: &out, Err: &out} // Deliberately no configuration or runner.
	err := a.DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWrite(t, before), After: diffWrite(t, after)})
	return out.String(), err
}

// Catch verdict-only comparison and refusing normal revision changes.
func TestEvaluationDiffTransitionsAcrossRevisions(t *testing.T) {
	for _, tc := range []struct {
		before, after, status string
		failure               bool
	}{
		{"pass", "pass", "stable", false}, {"fail", "fail", "stable", false}, {"pass", "fail", "regression", true}, {"fail", "pass", "fix", false},
		{"pass", "unknown", "lostEvidence", true}, {"fail", "unknown", "lostEvidence", true}, {"unknown", "pass", "gainedEvidence", true}, {"unknown", "fail", "gainedEvidence", true}, {"unknown", "unknown", "unknown", true},
	} {
		t.Run(tc.before+"-"+tc.after, func(t *testing.T) {
			before, after := diffFixture(tc.before), diffFixture(tc.after)
			after.PortableDigest = strings.Repeat("9", 64)
			after.GitCommit = strings.Repeat("2", 40)
			after.Target.Identity.Address = "example.com:8080"
			out, err := diffExecute(t, before, after)
			if (err != nil) != tc.failure || !strings.Contains(out, tc.status) || !strings.Contains(out, "assert-one") || !strings.Contains(out, after.PortableDigest) || !strings.Contains(out, after.GitCommit) {
				t.Fatalf("err=%v output=%s", err, out)
			}
			if err != nil && tc.status == "regression" && err.Error() != "evaluation comparison has regressions" {
				t.Fatalf("regression error: %v", err)
			}
			if strings.Contains(out, "evaluation passed") {
				t.Fatal("diff claimed evaluation passed")
			}
		})
	}
}

// Catch changed definitions/inputs being misreported as regressions, and hidden set changes.
func TestEvaluationDiffSetChanges(t *testing.T) {
	tests := []struct {
		name, status string
		change       func(*sessionsEvaluationReceipt)
	}{
		{"input", "inputChanged", func(r *sessionsEvaluationReceipt) { r.Cases[0].InputDigest = strings.Repeat("2", 64) }},
		{"definition", "definitionChanged", func(r *sessionsEvaluationReceipt) {
			r.Cases[0].Assertions[0].DefinitionDigest = strings.Repeat("2", 64)
		}},
		{"type", "definitionChanged", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "regex" }},
		{"source", "caseSourceChanged", func(r *sessionsEvaluationReceipt) { r.Cases[0].CaseDigest = strings.Repeat("2", 64) }},
		{"comment", "caseSetChanged: true", func(r *sessionsEvaluationReceipt) { r.CasesDigest = strings.Repeat("2", 64) }},
		{"case replaced", "caseAdded", func(r *sessionsEvaluationReceipt) { r.Cases[0].ID = "case-two" }},
		{"assertion replaced", "assertionAdded", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions[0].ID = "assert-two" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before, after := diffFixture("pass"), diffFixture("fail")
			tc.change(&after)
			out, err := diffExecute(t, before, after)
			if err == nil || !strings.Contains(out, tc.status) {
				t.Fatalf("err=%v out=%s", err, out)
			}
			if tc.name != "comment" && tc.name != "source" && strings.Contains(out, "regression") {
				t.Fatalf("false regression: %s", out)
			}
			if tc.name == "case replaced" && !strings.Contains(out, "caseRemoved") {
				t.Fatal(out)
			}
			if tc.name == "assertion replaced" && !strings.Contains(out, "assertionRemoved") {
				t.Fatal(out)
			}
		})
	}
}

func TestEvaluationDiffRuntimeCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionsEvaluationReceipt)
	}{
		{"endpoint", func(r *sessionsEvaluationReceipt) { r.Target.Identity.Address = "other.example:8080" }},
		{"model", func(r *sessionsEvaluationReceipt) {
			r.Target.Identity.Model = "other-model"
			r.Cases[0].Model = "other-model"
		}},
		{"harness", func(r *sessionsEvaluationReceipt) { r.Target.Identity.Harness = "other"; r.Cases[0].Harness = "other" }},
		{"missing model", func(r *sessionsEvaluationReceipt) {
			r.Target.Identity.Model = ""
			r.Cases[0].Model = ""
			r.Cases[0].Verdict = "unknown"
			r.Result = "unknown"
			r.Cases[0].Detail = evaluationExecutionUnavailable
			r.Cases[0].Assertions[0].Verdict = agentruntime.EvaluationUnknown
			r.Cases[0].Assertions[0].Reason = "answer unavailable"
		}},
		{"mixed", func(r *sessionsEvaluationReceipt) {
			r.Cases[0].ModelMixed = true
			r.Cases[0].Model = ""
			r.Target.Identity.Model = ""
			r.Cases[0].Verdict = "unknown"
			r.Result = "unknown"
			r.Cases[0].Detail = evaluationExecutionUnavailable
			r.Cases[0].Assertions[0].Verdict = agentruntime.EvaluationUnknown
			r.Cases[0].Assertions[0].Reason = "answer unavailable"
		}},
		{"partial", func(r *sessionsEvaluationReceipt) { r.FullCaseSet = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := diffFixture("pass"), diffFixture("pass")
			tc.change(&after)
			out, err := diffExecute(t, before, after)
			if err == nil || !strings.Contains(out, "incompatible") {
				t.Fatalf("err=%v out=%s", err, out)
			}
		})
	}
}

func diffOrkaFixture() bundleEvaluationReceipt {
	s := diffFixture("pass")
	c := s.Cases[0]
	return bundleEvaluationReceipt{SchemaVersion: 2, RunID: s.RunID, Bundle: s.Bundle, PortableDigest: s.PortableDigest, CasesDigest: s.CasesDigest, FullCaseSet: true, GitCommit: s.GitCommit, Target: bundleEvaluationTarget{Runtime: agentruntime.Orka, Context: "context", ClusterUID: "cluster-1", Namespace: "agents", Agent: "sample", AgentUID: "agent-1", Model: "local/model:1"}, Result: "pass", Cases: []bundleEvaluationResult{{ID: c.ID, Verdict: c.Verdict, CaseDigest: c.CaseDigest, InputDigest: c.InputDigest, Assertions: c.Assertions, TaskName: "sample-eval-1", TaskUID: "task-1", AnswerSHA256: c.AnswerSHA256}}}
}

func TestEvaluationDiffLiteralLegacyOrkaNullArrays(t *testing.T) {
	// Literal pre-v2 wire format: old matched/missing tags had no omitempty.
	const oldWire = `{"bundle":"sample","portableDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","casesDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","fullCaseSet":true,"gitCommit":"uncommitted","target":{"runtime":"orka","context":"context","namespace":"agents","clusterUID":"cluster-1","agent":"sample","agentUID":"agent-1"},"result":"pass","cases":[{"id":"case-one","verdict":"pass","matched":null,"missing":null,"taskName":"task-one","taskUID":"task-1","answerSHA256":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}]}`
	for _, raw := range []string{oldWire, strings.Replace(oldWire, `"matched":null`, `"matched":["PRIVATE_OPERAND_CANARY"]`, 1)} {
		var out bytes.Buffer
		err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWriteRaw(t, []byte(raw)), After: diffWrite(t, diffOrkaFixture())})
		if err == nil || err.Error() != "evaluation comparison is incompatible" || !strings.Contains(out.String(), "legacy receipt has no assertion evidence") || strings.Contains(out.String(), "PRIVATE_") {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	}
	for _, raw := range []string{
		strings.Replace(oldWire, `"bundle":"sample"`, `"bundle":"sample","schemaVersion":null`, 1),
		strings.Replace(oldWire, `"bundle":"sample"`, `"bundle":"sample","runID":null`, 1),
		strings.Replace(oldWire, `"fullCaseSet":true`, `"fullCaseSet":null`, 1),
		strings.Replace(oldWire, `"matched":null`, `"matched":null,"assertions":null`, 1),
		strings.Replace(oldWire, `"bundle":"sample"`, `"bundle":"sample","matched":null`, 1),
		strings.Replace(oldWire, `"matched":null`, `"matched":[null]`, 1),
	} {
		var out bytes.Buffer
		err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWriteRaw(t, []byte(raw)), After: diffWrite(t, diffOrkaFixture())})
		if err == nil || err.Error() != "invalid evaluation receipt" || out.Len() != 0 {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	}
	v2, _ := json.Marshal(diffOrkaFixture())
	for _, field := range []string{"matched", "missing", "assertions"} {
		raw := strings.Replace(string(v2), `"id":"case-one"`, `"id":"case-one","`+field+`":null`, 1)
		if field == "assertions" {
			rows, _ := json.Marshal(diffOrkaFixture().Cases[0].Assertions)
			raw = strings.Replace(string(v2), `"assertions":`+string(rows), `"assertions":null`, 1)
		}
		var out bytes.Buffer
		err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: diffWriteRaw(t, []byte(raw)), After: diffWrite(t, diffOrkaFixture())})
		if err == nil || err.Error() != "invalid evaluation receipt" {
			t.Fatalf("%s err=%v out=%s", field, err, out.String())
		}
	}
}

func TestEvaluationDiffModelProducerIdentity(t *testing.T) {
	for _, model := range []string{"public/model@v1", "model+v2"} {
		t.Run(model, func(t *testing.T) {
			s := diffFixture("pass")
			s.Target.Identity.Model = model
			s.Cases[0].Model = model
			if out, err := diffExecute(t, s, s); err != nil || !strings.Contains(out, "stable") {
				t.Fatalf("sessions err=%v out=%s", err, out)
			}
			o := diffOrkaFixture()
			o.Target.Model = model
			if out, err := diffExecute(t, o, o); err != nil || !strings.Contains(out, "stable") {
				t.Fatalf("orka err=%v out=%s", err, out)
			}
			different := o
			different.Target.Model = model + "other"
			if out, err := diffExecute(t, o, different); err == nil || !strings.Contains(out, "runtime identity differs") {
				t.Fatalf("model equality err=%v out=%s", err, out)
			}
		})
	}
	invalid := []string{"PRIVATE_MODEL\nCANARY", "PRIVATE_MODEL\x1bCANARY", strings.Repeat("m", 257)}
	for _, shape := range secretshapes.All() {
		invalid = append(invalid, shape.Example)
	}
	for _, model := range invalid {
		s := diffFixture("pass")
		s.Target.Identity.Model = model
		s.Cases[0].Model = model
		o := diffOrkaFixture()
		o.Target.Model = model
		for _, r := range []any{s, o} {
			out, err := diffExecute(t, r, r)
			if err == nil || err.Error() != "invalid evaluation receipt" || strings.Contains(out+err.Error(), model) {
				t.Fatalf("model rejected privately: err=%v out=%s", err, out)
			}
		}
	}
}

func TestEvaluationDiffOrkaAndLegacy(t *testing.T) {
	before, after := diffOrkaFixture(), diffOrkaFixture()
	after.Target.Context = "renamed-context"
	after.PortableDigest = strings.Repeat("9", 64)
	if out, err := diffExecute(t, before, after); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, change := range []func(*bundleEvaluationReceipt){func(r *bundleEvaluationReceipt) { r.Target.ClusterUID = "cluster-2" }, func(r *bundleEvaluationReceipt) { r.Target.AgentUID = "agent-2" }, func(r *bundleEvaluationReceipt) { r.Target.Agent = "other" }, func(r *bundleEvaluationReceipt) { r.Target.Namespace = "other" }, func(r *bundleEvaluationReceipt) { r.Target.Model = "other" }, func(r *bundleEvaluationReceipt) { r.Target.Model = "" }} {
		after = diffOrkaFixture()
		change(&after)
		out, err := diffExecute(t, before, after)
		if err == nil || !strings.Contains(out, "incompatible") {
			t.Fatalf("err=%v out=%s", err, out)
		}
	}
	if out, err := diffExecute(t, before, diffFixture("pass")); err == nil || !strings.Contains(out, "incompatible") {
		t.Fatalf("%v %s", err, out)
	}
	before.SchemaVersion = 0
	before.RunID = ""
	before.Cases[0].Assertions = nil
	before.Cases[0].CaseDigest = ""
	before.Cases[0].InputDigest = ""
	before.Cases[0].Matched = []string{"PRIVATE_OPERAND_CANARY"}
	before.Cases[0].Detail = "PRIVATE_DETAIL_CANARY"
	out, err := diffExecute(t, before, after)
	if err == nil || !strings.Contains(out, "legacy receipt has no assertion evidence") || strings.Contains(out, "PRIVATE_") {
		t.Fatalf("%v %s", err, out)
	}
}

// Catch accepting invalid evidence or exposing parser diagnostics and payload canaries.
func TestEvaluationDiffRejectsMalformedEvidencePrivately(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sessionsEvaluationReceipt)
	}{
		{"version", func(r *sessionsEvaluationReceipt) { r.SchemaVersion = 3 }}, {"run ID", func(r *sessionsEvaluationReceipt) { r.RunID = "PRIVATE_RUN_CANARY" }},
		{"portable", func(r *sessionsEvaluationReceipt) { r.PortableDigest = "bad" }}, {"cases digest", func(r *sessionsEvaluationReceipt) { r.CasesDigest = "bad" }}, {"commit", func(r *sessionsEvaluationReceipt) { r.GitCommit = "PRIVATE_COMMIT_CANARY" }}, {"bundle", func(r *sessionsEvaluationReceipt) { r.Bundle = "PRIVATE\nBUNDLE" }},
		{"empty cases", func(r *sessionsEvaluationReceipt) { r.Cases = nil }}, {"duplicate cases", func(r *sessionsEvaluationReceipt) { r.Cases = append(r.Cases, r.Cases[0]) }}, {"unsafe case ID", func(r *sessionsEvaluationReceipt) { r.Cases[0].ID = "PRIVATE\nCASE" }},
		{"aggregate", func(r *sessionsEvaluationReceipt) { r.Result = "fail" }}, {"case verdict", func(r *sessionsEvaluationReceipt) { r.Cases[0].Verdict = "fail"; r.Result = "fail" }},
		{"empty assertions", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions = nil }}, {"duplicate assertions", func(r *sessionsEvaluationReceipt) {
			r.Cases[0].Assertions = append(r.Cases[0].Assertions, r.Cases[0].Assertions[0])
		}},
		{"digest", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions[0].DefinitionDigest = "bad" }}, {"case digest", func(r *sessionsEvaluationReceipt) { r.Cases[0].CaseDigest = "bad" }}, {"input digest", func(r *sessionsEvaluationReceipt) { r.Cases[0].InputDigest = "bad" }},
		{"reason", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions[0].Reason = "PRIVATE_REASON_CANARY" }}, {"detail", func(r *sessionsEvaluationReceipt) { r.Cases[0].Detail = "PRIVATE_DETAIL_CANARY" }},
		{"tool pass", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "toolCalled" }}, {"type", func(r *sessionsEvaluationReceipt) { r.Cases[0].Assertions[0].Type = "PRIVATE_TYPE_CANARY" }},
		{"head", func(r *sessionsEvaluationReceipt) { r.Cases[0].JournalHead.Seq = 0 }}, {"answer", func(r *sessionsEvaluationReceipt) { r.Cases[0].AnswerSHA256 = "" }}, {"session", func(r *sessionsEvaluationReceipt) { r.Cases[0].SessionUID = "" }}, {"per case model", func(r *sessionsEvaluationReceipt) { r.Cases[0].Model = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, after := diffFixture("pass"), diffFixture("pass")
			tc.change(&after)
			out, err := diffExecute(t, before, after)
			if err == nil || !strings.Contains(err.Error(), "invalid evaluation receipt") {
				t.Fatalf("err=%v out=%s", err, out)
			}
			if strings.Contains(out+err.Error(), "PRIVATE") {
				t.Fatalf("leaked private data: %v %s", err, out)
			}
		})
	}
}

// Catch accepting null-typed members, unsupported tools as known, and nondeterministic rows.
func TestEvaluationDiffReportsEveryChangeBeforeFailing(t *testing.T) {
	before, after := diffFixture("pass"), diffFixture("fail")
	b := diffFixture("fail").Cases[0]
	a := diffFixture("pass").Cases[0]
	b.ID = "second"
	a.ID = "second"
	b.SessionUID = "session-2"
	a.SessionUID = "session-2"
	before.Cases = append(before.Cases, b)
	after.Cases = append(after.Cases, a)
	before.Result = "fail"
	after.Result = "fail"
	out, err := diffExecute(t, before, after)
	if err == nil || err.Error() != "evaluation comparison has regressions" || !strings.Contains(out, "regression") || !strings.Contains(out, "fix") || strings.Index(out, "case-one\t") > strings.Index(out, "second\t") {
		t.Fatalf("%v %s", err, out)
	}
}

type evaluationDiffFailWriter struct{}

func (evaluationDiffFailWriter) Write([]byte) (int, error) {
	return 0, errors.New("PRIVATE_WRITER_CANARY")
}
func TestEvaluationDiffOutputFailureIsPrivate(t *testing.T) {
	path := diffWrite(t, diffFixture("pass"))
	err := (&App{Out: evaluationDiffFailWriter{}}).DiffAgentEvaluations(EvaluationDiffOptions{Before: path, After: path})
	if err == nil || err.Error() != "write evaluation comparison failed" {
		t.Fatalf("err=%v", err)
	}
}

func TestEvaluationDiffUnavailableAndSortedEvidence(t *testing.T) {
	before, after := diffFixture("pass"), diffFixture("pass")
	row := after.Cases[0].Assertions[0]
	row.ID = "tool-one"
	row.Type = "toolNotCalled"
	row.Verdict = agentruntime.EvaluationUnknown
	row.Reason = "runtime does not support tools"
	before.Cases[0].Assertions = append(before.Cases[0].Assertions, row)
	after.Cases[0].Assertions = append(after.Cases[0].Assertions, row)
	before.Cases[0].Verdict = "unknown"
	before.Result = "unknown"
	after.Cases[0].Verdict = "unknown"
	after.Result = "unknown"
	out, err := diffExecute(t, before, after)
	if err == nil || !strings.Contains(out, "toolNotCalled") || !strings.Contains(out, "unknown/unknown") {
		t.Fatalf("%v %s", err, out)
	}
	for i := 0; i < 10; i++ {
		again, _ := diffExecute(t, before, after)
		if again != out {
			t.Fatal("nondeterministic output")
		}
	}
	unavailable := diffFixture("unknown")
	unavailable.Cases[0].Detail = evaluationExecutionUnavailable
	unavailable.Cases[0].SessionUID = ""
	unavailable.Cases[0].JournalHead = sessionsJournalHead{}
	unavailable.Cases[0].Harness = ""
	unavailable.Cases[0].Model = ""
	unavailable.Cases[0].AnswerSHA256 = ""
	unavailable.Target.Identity.Harness = ""
	unavailable.Target.Identity.Model = ""
	if out, err := diffExecute(t, unavailable, diffFixture("pass")); err == nil || !strings.Contains(out, "runtime identity unavailable") {
		t.Fatalf("%v %s", err, out)
	}
	legacy := diffFixture("pass")
	legacy.SchemaVersion = 0
	legacy.RunID = ""
	legacy.Cases[0].Assertions = nil
	legacy.Cases[0].CaseDigest = ""
	legacy.Cases[0].InputDigest = ""
	legacy.Cases[0].Detail = "PRIVATE_LEGACY_CANARY"
	if out, err := diffExecute(t, legacy, diffFixture("pass")); err == nil || !strings.Contains(out, "legacy receipt") || strings.Contains(out, "PRIVATE") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestEvaluationDiffOrkaReferenceCoherence(t *testing.T) {
	for _, change := range []func(*bundleEvaluationReceipt){
		func(r *bundleEvaluationReceipt) { r.Cases[0].TaskName = "" }, func(r *bundleEvaluationReceipt) { r.Cases[0].TaskUID = "" }, func(r *bundleEvaluationReceipt) { r.Cases[0].AnswerSHA256 = "" },
		func(r *bundleEvaluationReceipt) { r.Cases[0].Matched = []string{"PRIVATE_OPERAND_CANARY"} }, func(r *bundleEvaluationReceipt) { r.Cases = append(r.Cases, r.Cases[0]); r.Cases[1].ID = "other" },
	} {
		r := diffOrkaFixture()
		change(&r)
		out, err := diffExecute(t, r, diffOrkaFixture())
		if err == nil || !strings.Contains(err.Error(), "invalid evaluation receipt") || strings.Contains(out+err.Error(), "PRIVATE") {
			t.Fatalf("%v %s", err, out)
		}
	}
	r := diffOrkaFixture()
	r.Result = "unknown"
	r.Cases[0].Verdict = "unknown"
	r.Cases[0].Assertions[0].Verdict = agentruntime.EvaluationUnknown
	r.Cases[0].Assertions[0].Reason = "answer unavailable"
	r.Cases[0].Detail = evaluationExecutionUnavailable
	r.Cases[0].TaskName = ""
	r.Cases[0].TaskUID = ""
	r.Cases[0].AnswerSHA256 = ""
	if out, err := diffExecute(t, r, diffOrkaFixture()); err == nil || !strings.Contains(out, "gainedEvidence") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestEvaluationDiffStrictJSONAndFiles(t *testing.T) {
	raw, _ := json.Marshal(diffFixture("pass"))
	for _, data := range []string{
		strings.Replace(string(raw), `"schemaVersion":2`, `"schemaVersion":2,"SchemaVersion":2`, 1),
		strings.Replace(string(raw), `"id":"assert-one"`, `"id":"assert-one","ID":"hidden"`, 1),
		strings.Replace(string(raw), `"type":"contains"`, `"type":"contains","pattern":"PRIVATE_PAYLOAD_CANARY"`, 1),
		strings.Replace(string(raw), `"sessionUID":"session-1"`, `"sessionUID":"session-1","modelMixed":null`, 1),
		strings.Replace(string(raw), `"fullCaseSet":true`, `"fullCaseSet":null`, 1),
		strings.Replace(string(raw), `"seq":5`, `"seq":null`, 1),
		strings.Replace(string(raw), `"runID":"`+strings.Repeat("1", 32)+`"`, `"runID":null`, 1),
		string(raw) + ` {"answer":"PRIVATE_PAYLOAD_CANARY"}`, `{"target":{"runtime":"PRIVATE_RUNTIME_CANARY"}}`, strings.Repeat(" ", maxSessionsReceiptBytes+1),
	} {
		path := filepath.Join(diffTempDir(t), "bad.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: path, After: diffWrite(t, diffFixture("pass"))})
		if err == nil || strings.Contains(out.String()+err.Error(), "PRIVATE") {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	}
	real := diffWrite(t, diffFixture("pass"))
	dir := diffTempDir(t)
	link := filepath.Join(dir, "linked.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(dir, "parent")
	if err := os.Symlink(filepath.Dir(real), parent); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	padded := filepath.Join(dir, "bounded.json")
	if err := os.WriteFile(padded, append(raw, bytes.Repeat([]byte(" "), maxSessionsReceiptBytes-len(raw))...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&App{Out: &bytes.Buffer{}}).DiffAgentEvaluations(EvaluationDiffOptions{Before: padded, After: real}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(parent, "receipt.json"), fifo, dir, filepath.Join(dir, "PRIVATE_PATH_CANARY.json")} {
		var out bytes.Buffer
		err := (&App{Out: &out}).DiffAgentEvaluations(EvaluationDiffOptions{Before: path, After: real})
		if err == nil || strings.Contains(out.String()+err.Error(), "PRIVATE") {
			t.Fatalf("err=%v out=%s", err, out.String())
		}
	}
}
