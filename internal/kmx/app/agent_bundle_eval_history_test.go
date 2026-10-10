package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

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
