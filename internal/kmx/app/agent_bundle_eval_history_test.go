package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Each case is valid on its own; only the combined receipt exceeds 1 MiB.
func oversizedEvaluationSuite() map[string]string {
	cases := make(map[string]string)
	for i := 0; i < 5; i++ {
		var body strings.Builder
		fmt.Fprintf(&body, "id: case-%d\ninput: hello\nexpectContains:\n", i)
		body.WriteString(strings.Repeat("- hello\n", 500))
		body.WriteString("assertions:\n")
		for j := 0; j < 500; j++ {
			fmt.Fprintf(&body, "- {id: %s%04d, type: contains, value: hello}\n", strings.Repeat("z", 59), j)
		}
		cases[fmt.Sprintf("case-%d.yaml", i)] = body.String()
	}
	return cases
}

func TestEvaluationReceiptSizeBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts []int
		want   string
	}{
		{name: "empty", want: "at least one"},
		{name: "small", counts: []int{1}},
		{name: "maximum single case", counts: []int{1000}},
		// 8192 + 5*4096 + 2656*384 = 1048576 exactly.
		{name: "exact byte budget", counts: []int{1000, 1000, 654, 1, 1}},
		{name: "one row over budget", counts: []int{1000, 1000, 655, 1, 1}, want: "1 MiB"},
		{name: "many cases overhead", counts: make([]int, 200), want: "1 MiB"},
		{name: "maximum one assertion cases", counts: oneAssertionCaseCounts(232)},
		{name: "one case over budget", counts: oneAssertionCaseCounts(233), want: "at most 232 cases with one assertion each"},
		{name: "reader case cap unreachable", counts: oneAssertionCaseCounts(1001), want: "at most 232 cases with one assertion each"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cases := make([]bundleEvaluationCase, len(tc.counts))
			for i, count := range tc.counts {
				if count == 0 {
					count = 20
				}
				// Exercise combined legacy and explicit row accounting.
				cases[i].Case.ExpectContains = make([]string, count/2)
				cases[i].Case.Assertions = make([]agentruntime.EvaluationAssertion, count-count/2)
			}
			err := preflightEvaluationReceiptSize(cases)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("budget error=%v; want %q", err, tc.want)
			}
		})
	}
}

func TestEvaluationReceiptKnownMetadataBudget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model string
		fixed []string
		count int
		want  string
	}{
		{name: "small metadata", model: "test-model", fixed: []string{"sample", "kind-test"}, count: 231},
		{name: "small metadata reduces case limit", model: "test-model", fixed: []string{"sample", "kind-test"}, count: 232, want: "at most 231 cases"},
		{name: "oversized fixed metadata", fixed: []string{strings.Repeat("x", maxSessionsReceiptBytes)}, count: 1, want: "at most 0 cases"},
		{name: "escaped fixed metadata", fixed: []string{strings.Repeat("<", maxSessionsReceiptBytes/6)}, count: 1, want: "at most 0 cases"},
		{name: "model repeated per case", model: strings.Repeat("x", 10000), count: 80, want: "at most 71 cases"},
		{name: "model escaping repeated per case", model: strings.Repeat("<", 2000), count: 70, want: "at most 62 cases"},
		{name: "combined fixed metadata", fixed: []string{strings.Repeat("x", 600000), strings.Repeat("x", 500000)}, count: 1, want: "at most 0 cases"},
		{name: "model exceeds entire budget", model: strings.Repeat("x", maxSessionsReceiptBytes), count: 1, want: "at most 0 cases"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cases := make([]bundleEvaluationCase, tc.count)
			for i := range cases {
				cases[i].Case.ExpectContains = []string{"hello"}
			}
			metadata := append([]string{tc.model}, tc.fixed...)
			err := preflightEvaluationReceiptSize(cases, metadata...)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("metadata budget error=%v; want %q", err, tc.want)
			}
		})
	}
}

func oneAssertionCaseCounts(count int) []int {
	counts := make([]int, count)
	for i := range counts {
		counts[i] = 1
	}
	return counts
}

func TestEvaluationReceiptBudgetCoversIndentedEscapedEvidence(t *testing.T) {
	id, digest, escaped := strings.Repeat("z", 63), strings.Repeat("a", 64), strings.Repeat("<", 256)
	row := agentruntime.EvaluationAssertionResult{ID: id, Type: "toolNotCalled", DefinitionDigest: digest, Verdict: agentruntime.EvaluationUnknown, Reason: "runtime does not support tools"}
	for _, receipt := range []any{
		bundleEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("e", 32), Bundle: id, PortableDigest: digest, CasesDigest: digest, FullCaseSet: true, GitCommit: digest, Result: "unknown", Target: bundleEvaluationTarget{Runtime: agentruntime.Orka, Context: escaped, Namespace: id, ClusterUID: escaped, Agent: id, AgentUID: escaped, Model: escaped}, Cases: []bundleEvaluationResult{{ID: id, Verdict: "unknown", CaseDigest: digest, InputDigest: digest, TaskName: strings.Repeat("z", 62), TaskUID: escaped, AnswerSHA256: digest, Detail: evaluationExecutionUnavailable}}},
		sessionsEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("e", 32), Bundle: id, PortableDigest: digest, CasesDigest: digest, FullCaseSet: true, GitCommit: digest, Result: "unknown", Target: sessionsEvaluationTarget{Runtime: "agentsessions", Identity: sessionsHostIdentity{Version: 1, Provenance: "host-reported", Address: escaped, Harness: escaped, Model: escaped}}, Cases: []sessionsEvaluationResult{{ID: id, Verdict: "unknown", CaseDigest: digest, InputDigest: digest, SessionUID: escaped, Harness: "chat", Model: escaped, JournalHead: sessionsJournalHead{Seq: 9223372036854775807, Hash: digest}, AnswerSHA256: digest, Detail: evaluationExecutionUnavailable}}},
	} {
		before, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		var envelope any
		switch r := receipt.(type) {
		case bundleEvaluationReceipt:
			r.Cases = nil
			envelope = r
		case sessionsEvaluationReceipt:
			r.Cases = nil
			envelope = r
		}
		top, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		// Measure the actual indented schemas, with maximum case/digest lengths.
		// An unexpected observed model can survive a failed chat execution;
		// an invalid created harness instead returns before observing any model.
		t.Logf("maximum metadata: envelope=%d case=%d", len(top)+1, len(before)-len(top))
		if len(top)+1 > 8192 || len(before)-len(top) > 4096 {
			t.Fatalf("metadata exceeds reserved budget: envelope=%d case=%d", len(top)+1, len(before)-len(top))
		}
		if r, ok := receipt.(sessionsEvaluationReceipt); ok {
			r.Cases = append([]sessionsEvaluationResult(nil), r.Cases...)
			r.Cases[0].Harness, r.Cases[0].Model = escaped, ""
			r.Cases[0].JournalHead = sessionsJournalHead{}
			r.Cases[0].AnswerSHA256 = ""
			invalidSession, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("invalid created session metadata: case=%d", len(invalidSession)-len(top))
			if len(invalidSession)-len(top) > 4096 {
				t.Fatalf("invalid created session exceeds case reserve: %d", len(invalidSession)-len(top))
			}
		}
		switch r := receipt.(type) {
		case bundleEvaluationReceipt:
			r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{row, row}
			receipt = r
		case sessionsEvaluationReceipt:
			r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{row, row}
			receipt = r
		}
		after, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("two maximum assertion rows: %d bytes", len(after)-len(before))
		if len(after)-len(before) > 2*384 {
			t.Fatalf("indented maximum rows exceed budget: %d", len(after)-len(before))
		}
		// Check a whole maximum-count receipt too, including array separators
		// and the final newline the writers append.
		switch r := receipt.(type) {
		case bundleEvaluationReceipt:
			entry := r.Cases[0]
			entry.Assertions = entry.Assertions[:1]
			r.Cases = make([]bundleEvaluationResult, 232)
			for i := range r.Cases {
				r.Cases[i] = entry
			}
			receipt = r
		case sessionsEvaluationReceipt:
			entry := r.Cases[0]
			entry.Assertions = entry.Assertions[:1]
			r.Cases = make([]sessionsEvaluationResult, 232)
			for i := range r.Cases {
				r.Cases[i] = entry
			}
			receipt = r
		}
		full, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("232 maximum-length cases with one assertion: %d bytes", len(full)+1)
		if len(full)+1 > maxSessionsReceiptBytes {
			t.Fatalf("accepted case count produces oversized receipt: %d", len(full)+1)
		}
	}
}

func TestOrkaTypedAssertionsAndUnavailableRows(t *testing.T) {
	for _, tc := range []struct{ name, assertion, answer, phase, want, rowWant string }{
		{"negative pass", "type: notContains\n  value: refuse", "hello", "", "pass", "pass"},
		{"negative fail", "type: notContains\n  value: refuse", "I refuse", "", "fail", "fail"},
		{"regex pass", "type: regex\n  pattern: '^hello$'", "hello", "", "pass", "pass"},
		{"regex fail", "type: regex\n  pattern: '^hello$'", "goodbye", "", "fail", "fail"},
		{"disabled positive", "type: toolCalled\n  tool: inventory", "hello", "", "unknown", "unknown"},
		{"disabled negative", "type: toolNotCalled\n  tool: inventory", "hello", "", "unknown", "unknown"},
		{"empty", "type: notContains\n  value: refuse", "", "", "unknown", "unknown"},
		{"unprintable", "type: notContains\n  value: refuse", "\x00", "", "unknown", "unknown"},
		{"failed execution", "type: notContains\n  value: refuse", "hello", "Failed", "fail", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "id: typed\ninput: public prompt\nassertions:\n- id: check\n  " + tc.assertion + "\n"
			f := newEvalFixture(t, map[string]string{"typed.yaml": body})
			f.answers["public prompt"] = tc.answer
			if tc.phase != "" {
				t.Setenv("KMX_EVAL_PHASE", tc.phase)
			}
			err := f.app.EvaluateAgentBundle(f.opt)
			if (err == nil) != (tc.want == "pass") {
				t.Fatalf("error=%v; want %s", err, tc.want)
			}
			r, raw := f.receipt(t)
			if r.Result != tc.want || f.taskCreates(t) != 1 {
				t.Fatalf("receipt=%+v creates=%d", r, f.taskCreates(t))
			}
			var data struct {
				SchemaVersion int    `json:"schemaVersion"`
				RunID         string `json:"runID"`
				Target        struct {
					Model string `json:"model"`
				} `json:"target"`
				Cases []struct {
					CaseDigest, InputDigest string
					Assertions              []agentruntime.EvaluationAssertionResult `json:"assertions"`
				} `json:"cases"`
			}
			if err := json.Unmarshal([]byte(raw), &data); err != nil {
				t.Fatal(err)
			}
			if data.SchemaVersion != 2 || len(data.RunID) != 32 || data.Target.Model == "" || len(data.Cases) != 1 || len(data.Cases[0].Assertions) != 1 {
				t.Fatalf("missing v2 evidence: %s", raw)
			}
			c := data.Cases[0]
			if c.CaseDigest != agentruntime.EvaluationCaseDigest([]byte(body)) || c.InputDigest != agentruntime.EvaluationInputDigest("public prompt") || string(c.Assertions[0].Verdict) != tc.rowWant {
				t.Fatalf("case=%+v", c)
			}
			if strings.HasPrefix(tc.name, "disabled") && c.Assertions[0].Reason != "runtime does not support tools" {
				t.Fatalf("tool row=%+v", c.Assertions[0])
			}
			for _, canary := range []string{"public prompt", "inventory", "refuse", "^hello$", "\"matched\"", "\"missing\""} {
				if strings.Contains(raw, canary) {
					t.Fatalf("receipt contains operand: %s", canary)
				}
			}
		})
	}
}

func TestOrkaAdapterPrevalidatesAssertions(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := (orkaRuntimeAdapter{app: f.app}).Evaluate(ctx, agentruntime.AgentRef{Namespace: "orka-system", Name: f.name, UID: "agent-uid"}, agentruntime.EvaluationRequest{CaseID: "one", Input: "hi", PortableDigest: f.digest, Assertions: []agentruntime.EvaluationAssertion{{ID: "a", Type: "regex", Pattern: "["}}})
	if err == nil || !strings.Contains(err.Error(), "RE2") || f.taskCreates(t) != 0 {
		t.Fatalf("invalid definition execution: %v", err)
	}
}

func TestOrkaAssertionCapPrecedesTaskCreation(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	request := agentruntime.EvaluationRequest{CaseID: "one", Input: "hello", PortableDigest: f.digest, ExpectContains: make([]string, 1000), Assertions: []agentruntime.EvaluationAssertion{{ID: "check", Type: "contains", Value: "hello"}}}
	for i := range request.ExpectContains {
		request.ExpectContains[i] = "hello"
	}
	_, err := (orkaRuntimeAdapter{app: f.app, resultPort: f.opt.ResultPort}).Evaluate(ctx, agentruntime.AgentRef{Namespace: "orka-system", Name: f.name, UID: "agent-uid"}, request)
	if err == nil || !strings.Contains(err.Error(), "at most 1000") || f.taskCreates(t) != 0 {
		t.Fatalf("over-limit request: %v tasks=%d", err, f.taskCreates(t))
	}
}

func TestEvaluationRetainsPrivateHistoryAndLatest(t *testing.T) {
	f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
	f.answers["Say hello."] = "hello private-answer-canary"
	var previous []byte
	for i := 0; i < 2; i++ {
		if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
			t.Fatal(err)
		}
		_, latest := f.receipt(t)
		paths, err := filepath.Glob(filepath.Join(f.bundle, "receipts", "runs", "eval-*.json"))
		if err != nil || len(paths) != i+1 {
			t.Fatalf("history=%v %v", paths, err)
		}
		found := false
		for _, p := range paths {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(raw, []byte(latest)) {
				found = true
			}
			if i == 1 && bytes.Equal(raw, previous) {
				previous = nil
			}
			if bytes.Contains(raw, []byte("private-answer-canary")) {
				t.Fatal("history leaked answer")
			}
			info, _ := os.Stat(p)
			if info.Mode().Perm() != 0600 {
				t.Fatalf("file permissions %o", info.Mode().Perm())
			}
		}
		if !found || i == 1 && previous != nil {
			t.Fatal("latest not archived or old run overwritten")
		}
		previous = []byte(latest)
	}
	for _, p := range []string{filepath.Join(f.bundle, "receipts"), filepath.Join(f.bundle, "receipts", "runs")} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("directory mode: %v %v", info, err)
		}
	}
	evidence, err := readBundleGateEvidence(f.bundle)
	if err != nil || len(evidence) != 1 {
		t.Fatalf("history pollutes gates: %v %v", evidence, err)
	}
	if !strings.Contains(f.out.String(), "history: ") || !strings.Contains(f.out.String(), "receipt: ") {
		t.Fatalf("missing output paths: %s", f.out)
	}
}

func TestV2WriterRefusesArbitraryDiagnosticsAndOversizedReceipts(t *testing.T) {
	for _, mode := range []string{"detail", "reason", "oversized", "missing digest"} {
		t.Run(mode, func(t *testing.T) {
			f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
			f.answers["Say hello."] = "hello"
			if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
				t.Fatal(err)
			}
			r, before := f.receipt(t)
			r.RunID = strings.Repeat("e", 32)
			switch mode {
			case "detail":
				r.Cases[0].Detail = "private-error-canary"
			case "reason":
				r.Cases[0].Assertions[0].Reason = "private-reason-canary"
			case "oversized":
				r.GitCommit = strings.Repeat("x", maxSessionsReceiptBytes)
			case "missing digest":
				r.Cases[0].InputDigest = ""
			}
			err := writeBundleEvaluationReceipt(f.bundle, "cluster-uid", r)
			if err == nil || strings.Contains(err.Error(), "canary") {
				t.Fatalf("unsafe write: %v", err)
			}
			_, after := f.receipt(t)
			if after != before {
				t.Fatal("unsafe write changed latest")
			}
		})
	}
}

func TestUnavailableExecutionRequiresEveryAssertionUnknown(t *testing.T) {
	for _, detail := range []string{evaluationExecutionUnavailable, evaluationResultUnavailable, evaluationExecutionFailed} {
		for _, mixed := range []bool{false, true} {
			name := detail + " all unknown"
			if mixed {
				name = detail + " mixed pass unknown"
			}
			t.Run(name, func(t *testing.T) {
				verdict := "unknown"
				if detail == evaluationExecutionFailed {
					verdict = "fail"
				}
				rows := []agentruntime.EvaluationAssertionResult{
					{ID: "text", Type: "contains", DefinitionDigest: strings.Repeat("a", 64), Verdict: agentruntime.EvaluationUnknown, Reason: "answer unavailable"},
					{ID: "tool", Type: "toolCalled", DefinitionDigest: strings.Repeat("b", 64), Verdict: agentruntime.EvaluationUnknown, Reason: "runtime does not support tools"},
				}
				if mixed {
					rows[0].Verdict = agentruntime.EvaluationPass
					rows[0].Reason = "assertion satisfied"
				}
				cd, id := strings.Repeat("c", 64), strings.Repeat("d", 64)
				if evaluationCaseEvidence(cd, id, verdict, detail, rows) == mixed {
					t.Error("unavailable execution accepted known row or rejected unknown rows")
				}
				gateReceipt := bundleEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("e", 32), Cases: []bundleEvaluationResult{{ID: "one", Verdict: verdict, Detail: detail, CaseDigest: cd, InputDigest: id, Assertions: rows}}}
				if bundleReceiptCaseResultsPass(gateReceipt, []bundleEvaluationCase{{Case: agentruntime.EvaluationCase{ID: "one"}}}) {
					t.Error("unavailable execution qualified gate")
				}
				for _, runtime := range []string{"orka", "sessions"} {
					t.Run(runtime, func(t *testing.T) {
						bundle := t.TempDir()
						var err error
						if runtime == "orka" {
							err = writeBundleEvaluationReceipt(bundle, "uid", bundleEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("e", 32), Result: verdict, Target: bundleEvaluationTarget{ClusterUID: "uid"}, Cases: []bundleEvaluationResult{{ID: "one", Verdict: verdict, Detail: detail, CaseDigest: cd, InputDigest: id, Assertions: rows}}})
						} else {
							_, err = writeSessionsEvaluationReceipt(bundle, sessionsEvaluationReceipt{SchemaVersion: 2, RunID: strings.Repeat("e", 32), Result: verdict, Cases: []sessionsEvaluationResult{{ID: "one", Verdict: verdict, Detail: detail, CaseDigest: cd, InputDigest: id, Assertions: rows}}})
						}
						if (err != nil) != mixed {
							t.Fatalf("write error=%v; mixed=%v", err, mixed)
						}
						if mixed {
							paths, _ := filepath.Glob(filepath.Join(bundle, "receipts", "runs", "eval-*.json"))
							if len(paths) != 0 {
								t.Fatal("invalid evidence archived")
							}
						}
					})
				}
			})
		}
	}
}

func TestLegacyWriterStripsOperandsWithoutArchiving(t *testing.T) {
	bundle := t.TempDir()
	r := bundleEvaluationReceipt{Target: bundleEvaluationTarget{ClusterUID: "uid"}, Cases: []bundleEvaluationResult{{Matched: []string{"private-match-canary"}, Missing: []string{"private-missing-canary"}}}}
	if err := writeBundleEvaluationReceipt(bundle, "uid", r); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(bundleEvaluationReceiptPath(bundle, "", "", "uid"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "canary") || len(r.Cases[0].Matched) != 1 {
		t.Fatal("writer leaked operands or mutated caller")
	}
	if _, err := os.Stat(filepath.Join(bundle, "receipts", "runs")); !os.IsNotExist(err) {
		t.Fatalf("archived legacy receipt: %v", err)
	}
}

func TestEvaluationHistoryCollisionAndLinksPreserveLatest(t *testing.T) {
	for _, mode := range []string{"collision", "runs link", "history link", "latest failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
			f.answers["Say hello."] = "hello"
			if err := f.app.EvaluateAgentBundle(f.opt); err != nil {
				t.Fatal(err)
			}
			r, before := f.receipt(t)
			runs := filepath.Join(f.bundle, "receipts", "runs")
			paths, _ := filepath.Glob(filepath.Join(runs, "eval-*.json"))
			if len(paths) != 1 {
				t.Fatalf("missing history: %v", paths)
			}
			switch mode {
			case "runs link":
				if err := os.RemoveAll(runs); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), runs); err != nil {
					t.Fatal(err)
				}
			case "history link":
				if err := os.Remove(paths[0]); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.bundle, "agent.yaml"), paths[0]); err != nil {
					t.Fatal(err)
				}
			case "latest failure":
				var m map[string]any
				_ = json.Unmarshal([]byte(before), &m)
				m["runID"] = strings.Repeat("e", 32)
				raw, _ := json.Marshal(m)
				_ = json.Unmarshal(raw, &r)
				latest := bundleEvaluationReceiptPath(f.bundle, "kind-test", "orka-system", "cluster-uid")
				if err := os.Remove(latest); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(latest, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeBundleEvaluationReceipt(f.bundle, "cluster-uid", r); err == nil {
				t.Fatal("unsafe/colliding write succeeded")
			}
			if mode != "latest failure" {
				_, after := f.receipt(t)
				if after != before {
					t.Fatal("failed archive write changed latest")
				}
			}
			if mode == "collision" {
				raw, _ := os.ReadFile(paths[0])
				if string(raw) != before {
					t.Fatal("immutable run overwritten")
				}
			}
			if mode == "latest failure" {
				paths, _ = filepath.Glob(filepath.Join(runs, "eval-*.json"))
				if len(paths) != 2 {
					t.Fatal("latest failure did not retain archive")
				}
			}
		})
	}
}
