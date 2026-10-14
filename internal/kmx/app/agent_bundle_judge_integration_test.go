package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func integrationJudgeRow(scores []int) agentruntime.EvaluationAssertionResult {
	a := judgeCase().Assertions[0]
	verdict := agentruntime.EvaluationUnknown
	reason := "judge response unavailable"
	if len(scores) == 2 {
		if scores[0] >= 50 && scores[1] >= 50 {
			verdict, reason = agentruntime.EvaluationPass, "assertion satisfied"
		} else if scores[0] < 50 && scores[1] < 50 {
			verdict, reason = agentruntime.EvaluationFail, "assertion not satisfied"
		} else {
			reason = "judge verdict split"
		}
	}
	return agentruntime.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: agentruntime.EvaluationAssertionDigest(a), Verdict: verdict, Reason: reason, Judge: &agentruntime.EvaluationJudgeEvidence{Model: a.JudgeModel, ModelVersion: a.JudgeVersion, Provenance: "user-declared", EvaluatorDigest: agentruntime.EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: strings.Repeat("8", 64), MinScore: 50, Scores: scores}}
}

func integrationJudgeReceipt(scores []int) sessionsEvaluationReceipt {
	r := diffFixture("pass")
	row := integrationJudgeRow(scores)
	r.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{row}
	r.Result, r.Cases[0].Verdict = string(row.Verdict), string(row.Verdict)
	return r
}

// Catch rejecting legitimate judge evidence, or accepting forged scores/identity/verdicts.
func TestJudgeReceiptIntegrityBoundaries(t *testing.T) {
	for _, scores := range [][]int{{100, 80}, {0, 20}, {49, 50}, nil} {
		r := integrationJudgeReceipt(scores)
		if !evaluationCaseEvidence(r.Cases[0].CaseDigest, r.Cases[0].InputDigest, r.Result, "", r.Cases[0].Assertions) {
			t.Fatalf("valid judge rows refused: %v", scores)
		}
		if _, err := readEvaluationDiffSource(diffWrite(t, r)); err != nil {
			t.Fatalf("valid receipt refused: %v", err)
		}
		if _, err := writeSessionsEvaluationReceipt(t.TempDir(), r); err != nil {
			t.Fatalf("writer refused: %v", err)
		}
	}
	for _, mutate := range []func(*agentruntime.EvaluationAssertionResult){
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge = nil },
		func(r *agentruntime.EvaluationAssertionResult) { r.Type = "contains" },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.Model = "unsafe\ncanary" },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.ModelVersion = "" },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.Provenance = "provider-attested" },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.EvaluatorDigest = strings.Repeat("9", 64) },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.ConfigurationDigest = "bad" },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.MinScore = 101 },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.Scores = []int{100} },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.Scores = []int{100, 101} },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.Scores = []int{0, 20} },
		func(r *agentruntime.EvaluationAssertionResult) { r.Judge.Scores = nil },
		func(r *agentruntime.EvaluationAssertionResult) { r.Reason = "private-reason-canary" },
	} {
		r := integrationJudgeReceipt([]int{100, 80})
		mutate(&r.Cases[0].Assertions[0])
		if evaluationCaseEvidence(r.Cases[0].CaseDigest, r.Cases[0].InputDigest, r.Result, "", r.Cases[0].Assertions) {
			t.Fatalf("forged row accepted: %+v", r.Cases[0].Assertions[0])
		}
		if _, err := writeSessionsEvaluationReceipt(t.TempDir(), r); err == nil {
			t.Fatal("writer accepted forged row")
		}
		orka := diffOrkaFixture()
		orka.Cases[0].Assertions = r.Cases[0].Assertions
		if err := writeBundleEvaluationReceipt(t.TempDir(), orka.Target.ClusterUID, orka); err == nil {
			t.Fatal("Orka writer accepted forged row")
		}
		gateDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(gateDir, "receipts"), 0700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gateDir, "receipts", "eval-test.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBundleGateEvidence(gateDir); err == nil {
			t.Fatal("gate reader accepted forged judge evidence")
		}
	}
}

// Typed decoding cannot distinguish missing threshold from zero: inspect raw members.
func TestJudgeStrictRawReceiptShape(t *testing.T) {
	r := integrationJudgeReceipt([]int{100, 80})
	original, _ := json.Marshal(r.Cases[0].Assertions[0].Judge)
	raw, _ := json.Marshal(r)
	variants := []string{"null", `{}`, strings.Replace(string(original), `"minScore":50,`, "", 1), strings.Replace(string(original), `"minScore":50`, `"minScore":null`, 1), strings.Replace(string(original), `"scores":[100,80]`, `"scores":null`, 1), strings.Replace(string(original), `"scores":[100,80]`, `"scores":[100,80.0]`, 1), strings.Replace(string(original), `"scores":[100,80]`, `"scores":[100]`, 1), strings.Replace(string(original), `"scores":[100,80]`, `"scores":[100,80,90]`, 1), strings.Replace(string(original), `"minScore":50`, `"minScore":50,"minScore":50`, 1), strings.Replace(string(original), `"model":`, `"unexpected":1,"model":`, 1)}
	for _, field := range []string{"model", "modelVersion", "provenance", "evaluatorDigest", "configurationDigest"} {
		var e map[string]json.RawMessage
		_ = json.Unmarshal(original, &e)
		delete(e, field)
		v, _ := json.Marshal(e)
		variants = append(variants, string(v))
	}
	for _, value := range variants {
		body := bytes.Replace(raw, original, []byte(value), 1)
		path := diffWriteRaw(t, body)
		if _, err := readEvaluationDiffSource(path); err == nil {
			t.Fatalf("diff accepted malformed judge: %s", value)
		}
		// Gate reader sees the same strict shape before authored checks.
		gateDir := t.TempDir()
		_ = os.Mkdir(filepath.Join(gateDir, "receipts"), 0700)
		_ = os.WriteFile(filepath.Join(gateDir, "receipts", "eval-test.json"), body, 0600)
		if _, err := readBundleGateEvidence(gateDir); err == nil {
			t.Fatalf("gate accepted malformed judge: %s", value)
		}
	}
	zero := integrationJudgeReceipt([]int{0, 1})
	zero.Cases[0].Assertions[0].Judge.MinScore = 0
	zero.Cases[0].Assertions[0].Verdict = agentruntime.EvaluationPass
	zero.Cases[0].Assertions[0].Reason = "assertion satisfied"
	zero.Result, zero.Cases[0].Verdict = "pass", "pass"
	if _, err := readEvaluationDiffSource(diffWrite(t, zero)); err != nil {
		t.Fatalf("explicit zero refused: %v", err)
	}
}

// Endpoint/version/threshold changes must never become stable or fixes.
func TestJudgeDiffIdentityAndScores(t *testing.T) {
	for _, mutate := range []func(*agentruntime.EvaluationJudgeEvidence){
		func(e *agentruntime.EvaluationJudgeEvidence) { e.ConfigurationDigest = strings.Repeat("7", 64) },
		func(e *agentruntime.EvaluationJudgeEvidence) { e.Model = "other/model" },
		func(e *agentruntime.EvaluationJudgeEvidence) { e.ModelVersion = "pinned-v2" },
		func(e *agentruntime.EvaluationJudgeEvidence) { e.MinScore = 60 },
	} {
		before, after := integrationJudgeReceipt([]int{0, 20}), integrationJudgeReceipt([]int{100, 80})
		mutate(after.Cases[0].Assertions[0].Judge)
		out, err := diffExecute(t, before, after)
		if err == nil || !strings.Contains(out, "identityChanged") || strings.Contains(out, "\tfix\t") || strings.Contains(out, "\tstable\t") {
			t.Fatalf("changed judge compared: err=%v out=%s", err, out)
		}
	}
	for _, tc := range []struct {
		before, after []int
		status        string
		failed        bool
	}{{[]int{100, 80}, []int{90, 70}, "stable", false}, {[]int{0, 20}, []int{100, 80}, "fix", false}, {[]int{100, 80}, []int{0, 20}, "regression", true}, {[]int{100, 80}, []int{49, 50}, "lostEvidence", true}} {
		out, err := diffExecute(t, integrationJudgeReceipt(tc.before), integrationJudgeReceipt(tc.after))
		if (err != nil) != tc.failed || !strings.Contains(out, tc.status) || !strings.Contains(out, "scores") || !strings.Contains(out, "[100 80]") && tc.status != "stable" {
			t.Fatalf("score transition: %v %s", err, out)
		}
	}
}

// Catch scores disappearing when an assertion or its entire case is added/removed.
func TestEvaluationDiffAddedRemovedJudgeScores(t *testing.T) {
	for _, tc := range []struct {
		name, status, scoresLine  string
		wholeCase, added          bool
		beforeScores, afterScores []int
	}{
		{"case removed", "assertionRemoved", "judge scores case-one/changed: [80 100]/[]", true, false, []int{80, 100}, nil},
		{"assertion removed", "assertionRemoved", "judge scores case-one/changed: [80 100]/[]", false, false, []int{80, 100}, nil},
		{"assertion added", "assertionAdded", "judge scores case-one/changed: []/[80 100]", false, true, nil, []int{80, 100}},
		{"case added", "assertionAdded", "judge scores case-one/changed: []/[80 100]", true, true, nil, []int{80, 100}},
	} {
		for _, kind := range []string{"known judge", "unknown judge", "nonjudge"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				before, after := diffFixture("pass"), diffFixture("pass")
				present, absent := &before, &after
				if tc.added {
					present, absent = &after, &before
				}
				row := integrationJudgeRow([]int{80, 100})
				wantBefore, wantAfter := tc.beforeScores, tc.afterScores
				wantLine := tc.scoresLine
				switch kind {
				case "unknown judge":
					row = integrationJudgeRow(nil)
					present.Result, present.Cases[0].Verdict = "unknown", "unknown"
					wantBefore, wantAfter = nil, nil
					wantLine = "judge scores case-one/changed: []/[]"
				case "nonjudge":
					row = diffFixture("pass").Cases[0].Assertions[0]
					wantBefore, wantAfter = nil, nil
				}
				row.ID = "changed"
				present.Cases[0].Assertions = append(present.Cases[0].Assertions, row)
				if tc.wholeCase {
					absent.Cases[0].ID = "case-other"
				}
				b, err := readEvaluationDiffSource(diffWrite(t, before))
				if err != nil {
					t.Fatal(err)
				}
				a, err := readEvaluationDiffSource(diffWrite(t, after))
				if err != nil {
					t.Fatal(err)
				}
				report := compareEvaluations(b, a)
				if !report.Incomplete || report.Regression || report.IncompatibleReason != "" {
					t.Fatalf("set change classification: %+v", report)
				}
				found := false
				for _, got := range report.Rows {
					if got.CaseID == "case-one" && got.AssertionID == "changed" {
						found = true
						if got.Status != tc.status || !reflect.DeepEqual(got.BeforeScores, wantBefore) || !reflect.DeepEqual(got.AfterScores, wantAfter) {
							t.Errorf("row status/scores: %+v; want %s %v/%v", got, tc.status, wantBefore, wantAfter)
						}
					}
					if got.BeforeType != "llmJudge" && got.AfterType != "llmJudge" && (got.BeforeScores != nil || got.AfterScores != nil) {
						t.Errorf("nonjudge row has scores: %+v", got)
					}
				}
				if !found {
					t.Fatal("missing changed assertion row")
				}
				out, err := diffExecute(t, before, after)
				if err == nil || err.Error() != "evaluation comparison is incomplete" || !strings.Contains(out, "comparison: incomplete") {
					t.Fatalf("set change exit/output: %v %s", err, out)
				}
				if kind == "nonjudge" {
					if strings.Contains(out, "judge scores") {
						t.Errorf("nonjudge scores rendered: %s", out)
					}
				} else if !strings.Contains(out, wantLine+"\n") {
					t.Errorf("missing %q in output: %s", wantLine, out)
				}
			})
		}
	}
}

func TestJudgeGateBindsAuthoredIdentityAndThreshold(t *testing.T) {
	s := integrationJudgeReceipt([]int{100, 80})
	c := s.Cases[0]
	r := diffOrkaFixture()
	r.Cases[0].Assertions = c.Assertions
	r.Cases[0].Verdict = "pass"
	authored := judgeCase()
	authored.ID = r.Cases[0].ID
	cases := []bundleEvaluationCase{{Digest: r.Cases[0].CaseDigest, Case: authored}}
	r.Cases[0].InputDigest = agentruntime.EvaluationInputDigest(authored.Input)
	if !bundleReceiptCaseResultsPass(r, cases) {
		t.Fatal("valid authored judge gate refused")
	}
	for _, mutate := range []func(*agentruntime.EvaluationJudgeEvidence){
		func(e *agentruntime.EvaluationJudgeEvidence) { e.Model = "other/model" }, func(e *agentruntime.EvaluationJudgeEvidence) { e.ModelVersion = "v2" }, func(e *agentruntime.EvaluationJudgeEvidence) { e.MinScore = 60 }, func(e *agentruntime.EvaluationJudgeEvidence) { e.EvaluatorDigest = strings.Repeat("1", 64) }, func(e *agentruntime.EvaluationJudgeEvidence) { e.Scores = []int{100, 49} },
	} {
		copy := r
		copy.Cases = append([]bundleEvaluationResult(nil), r.Cases...)
		copy.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{integrationJudgeRow([]int{100, 80})}
		mutate(copy.Cases[0].Assertions[0].Judge)
		if bundleReceiptCaseResultsPass(copy, cases) {
			t.Fatal("gate accepted stale/forged identity")
		}
	}
}

func TestJudgeReplaySeparatesAnswerFromJudgeVerification(t *testing.T) {
	for _, scores := range [][]int{{100, 80}, {0, 20}, {49, 50}, nil} {
		a, o, r, s := verifyAppFixture(t)
		r.SchemaVersion, r.RunID = 2, strings.Repeat("a", 32)
		c := &r.Cases[0]
		row := integrationJudgeRow(scores)
		c.Assertions = []agentruntime.EvaluationAssertionResult{row}
		c.CaseDigest, c.InputDigest = strings.Repeat("c", 64), strings.Repeat("d", 64)
		c.Verdict, r.Result = string(row.Verdict), string(row.Verdict)
		saveVerifyInput(t, o.ReceiptPath, r)
		if err := a.VerifyAgentSessions(o); err != nil || s.reads.Load() != 1 {
			t.Fatalf("valid judge replay refused: %v reads=%d", err, s.reads.Load())
		}
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.ReceiptPath), "verify-"+filepath.Base(o.ReceiptPath)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(`"judgeAssertionsNotVerified": 1`)) || !bytes.Contains(raw, []byte(`"modelCalls": 0`)) || !strings.Contains(a.Out.(*bytes.Buffer).String(), "judge assertions not verified: 1") {
			t.Fatalf("dishonest replay: %s output=%s", raw, a.Out)
		}
	}
	for _, mode := range []string{"tool unknown", "ordinary unknown", "no answer", "execution unavailable"} {
		a, o, r, s := verifyAppFixture(t)
		r.SchemaVersion, r.RunID = 2, strings.Repeat("a", 32)
		c := &r.Cases[0]
		c.CaseDigest, c.InputDigest = strings.Repeat("c", 64), strings.Repeat("d", 64)
		c.Assertions = []agentruntime.EvaluationAssertionResult{integrationJudgeRow(nil)}
		c.Verdict, r.Result = "unknown", "unknown"
		switch mode {
		case "tool unknown":
			c.Assertions = append(c.Assertions, agentruntime.EvaluationAssertionResult{ID: "tool", Type: "toolCalled", DefinitionDigest: strings.Repeat("e", 64), Verdict: agentruntime.EvaluationUnknown, Reason: "runtime does not support tools"})
		case "ordinary unknown":
			c.Assertions = append(c.Assertions, agentruntime.EvaluationAssertionResult{ID: "text", Type: "contains", DefinitionDigest: strings.Repeat("e", 64), Verdict: agentruntime.EvaluationUnknown, Reason: "answer unavailable"})
		case "no answer":
			c.AnswerSHA256 = ""
		case "execution unavailable":
			c.Detail = evaluationExecutionUnavailable
		}
		saveVerifyInput(t, o.ReceiptPath, r)
		if err := a.VerifyAgentSessions(o); err == nil || s.reads.Load() != 0 {
			t.Fatalf("invalid replay accepted: %s %v", mode, err)
		}
	}
}

// Removing the callback or invoking it before attribution must fail this test.
func TestJudgeOrkaPortAfterAttribution(t *testing.T) {
	for _, mode := range []string{"success", "failed", "unreadable", "nil port"} {
		t.Run(mode, func(t *testing.T) {
			f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
			f.answers["public input"] = "public answer"
			if mode == "failed" {
				t.Setenv("KMX_EVAL_PHASE", "Failed")
			}
			if mode == "unreadable" {
				f.resultStatus = 500
			}
			c := judgeCase()
			calls := 0
			port := agentruntime.EvaluationJudge(func(ctx context.Context, c agentruntime.EvaluationCase, e agentruntime.EvaluationEvidence) []agentruntime.EvaluationAssertionResult {
				if ctx.Err() != nil {
					t.Fatal("case cancelled before judge")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("judge lacks deadline")
				}
				if e.AnswerAvailable {
					calls++
					if e.Answer != "public answer" || !strings.Contains(f.out.String(), "public answer") {
						t.Fatal("wrong/unprinted answer")
					}
					return []agentruntime.EvaluationAssertionResult{integrationJudgeRow([]int{100, 80})}
				}
				row := integrationJudgeRow(nil)
				row.Reason = "answer unavailable"
				return []agentruntime.EvaluationAssertionResult{row}
			})
			if mode == "nil port" {
				port = nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			r, err := (orkaRuntimeAdapter{app: f.app, resultPort: f.opt.ResultPort}).Evaluate(ctx, agentruntime.AgentRef{Namespace: "orka-system", Name: f.name, UID: "agent-uid"}, agentruntime.EvaluationRequest{CaseID: c.ID, Input: c.Input, Assertions: c.Assertions, PortableDigest: f.digest, Judge: port})
			if err != nil {
				t.Fatal(err)
			}
			want := "unknown"
			if mode == "success" {
				want = "pass"
			}
			if mode == "failed" {
				want = "fail"
			}
			if string(r.Verdict) != want || len(r.Assertions) != 1 || f.taskCreates(t) != 1 {
				t.Fatalf("bad port result: %+v", r)
			}
			if mode == "success" && calls != 1 || mode != "success" && calls != 0 {
				t.Fatalf("judged unavailable answer: %d", calls)
			}
			if mode != "nil port" && r.Assertions[0].Judge == nil {
				t.Fatal("unknown identity missing")
			}
		})
	}
}

func TestJudgeAppIntegration(t *testing.T) {
	for _, runtime := range []string{"orka", "sessions"} {
		for _, tc := range []struct {
			name              string
			scores            []int
			want              string
			failed, malformed bool
		}{{"pass", []int{100, 80}, "pass", false, false}, {"fail", []int{0, 20}, "fail", false, false}, {"split", []int{49, 50}, "unknown", false, false}, {"unavailable", nil, "unknown", false, true}, {"execution failed", nil, "fail", true, false}} {
			t.Run(runtime+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := int(calls.Add(1)) - 1
					if tc.malformed {
						fmt.Fprint(w, "malformed private-provider-canary")
						return
					}
					score := tc.scores[n%2]
					fmt.Fprint(w, judgeEnvelope(fmt.Sprintf(`{"score":%d,"reasoning":"terminal-reason-canary"}`, score)))
				}))
				defer server.Close()
				body := "id: public\ninput: public input\nexpectContains: [public]\nassertions:\n- id: quality\n  type: llmJudge\n  rubric: rubric-private-canary\n  minScore: 50\n  judgeModel: fixture/model\n  judgeVersion: pinned-v1\n"
				var a *App
				var opt EvaluateAgentBundleOptions
				var f *evalFixture
				if runtime == "orka" {
					f = newEvalFixture(t, map[string]string{"a.yaml": body})
					f.answers["public input"] = "public answer"
					a, opt = f.app, f.opt
					if tc.failed {
						t.Setenv("KMX_EVAL_PHASE", "Failed")
					}
				} else {
					s := &sessionsEvalServer{model: "test-model", answers: map[string]string{"public input": "public answer"}}
					if tc.failed {
						s.endState = "FAILED"
					}
					a, opt, _ = newSessionsAppFixture(t, s)
					_ = os.Remove(filepath.Join(opt.BundleDir, "eval", "b.yaml"))
					if err := os.WriteFile(filepath.Join(opt.BundleDir, "eval", "a.yaml"), []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				opt.JudgeBaseURL = server.URL
				err := a.EvaluateAgentBundle(opt)
				if (err == nil) != (tc.want == "pass") {
					t.Fatalf("app outcome: %v", err)
				}
				var raw []byte
				var rows []agentruntime.EvaluationAssertionResult
				var verdict string
				if runtime == "orka" {
					r, text := f.receipt(t)
					raw = []byte(text)
					rows = r.Cases[0].Assertions
					verdict = r.Result
					if f.taskCreates(t) != 1 {
						t.Fatal("agent retried")
					}
				} else {
					r, text := readSessionsAppReceipt(t, opt.BundleDir)
					raw = text
					rows = r.Cases[0].Assertions
					verdict = r.Result
				}
				if verdict != tc.want || len(rows) != 2 || rows[1].Judge == nil || !agentruntime.EvaluationJudgeEvidenceValid(rows[1]) {
					t.Fatalf("bad configured evidence: %s", raw)
				}
				wantCalls := int32(2)
				if tc.failed {
					wantCalls = 0
				}
				if calls.Load() != wantCalls {
					t.Fatalf("judge calls: %d", calls.Load())
				}
				if !tc.failed && !tc.malformed && !strings.Contains(a.Out.(*bytes.Buffer).String(), "terminal-reason-canary") {
					t.Fatal("reasoning not printed")
				}
				for _, canary := range []string{"terminal-reason-canary", "rubric-private-canary", "public answer", "public input", "private-provider-canary", server.URL} {
					if bytes.Contains(raw, []byte(canary)) {
						t.Fatalf("receipt leaked %s", canary)
					}
				}
				if rows[0].Verdict != agentruntime.EvaluationPass && !tc.failed {
					t.Fatal("ordinary assertions lost")
				}
			})
		}
	}
}

func TestJudgeAppPreflightNoExecution(t *testing.T) {
	for _, runtime := range []string{"orka", "sessions"} {
		for _, mode := range []string{"missing", "invalid key", "header key", "oversized", "unused selection"} {
			t.Run(runtime+"/"+mode, func(t *testing.T) {
				body := "id: public\ninput: public input\nassertions:\n- {id: quality, type: llmJudge, rubric: rubric, minScore: 50, judgeModel: fixture/model, judgeVersion: pinned-v1}\n"
				var a *App
				var opt EvaluateAgentBundleOptions
				var f *evalFixture
				var s *sessionsEvalServer
				if runtime == "orka" {
					f = newEvalFixture(t, map[string]string{"a.yaml": body, "b.yaml": evalHelloCase})
					a, opt = f.app, f.opt
				} else {
					s = &sessionsEvalServer{model: "test-model"}
					a, opt, _ = newSessionsAppFixture(t, s)
					_ = os.WriteFile(filepath.Join(opt.BundleDir, "eval", "a.yaml"), []byte(body), 0600)
				}
				if mode != "missing" {
					opt.JudgeBaseURL = "https://judge.example.invalid"
				}
				switch mode {
				case "invalid key":
					opt.JudgeKeyEnv = "bad-key!"
				case "header key":
					opt.JudgeKeyEnv = "TASK3_JUDGE_KEY"
					t.Setenv("TASK3_JUDGE_KEY", "private-key-canary\n")
				case "unused selection":
					if runtime == "orka" {
						opt.Case = "greet"
					} else {
						opt.Case = "two"
					}
				case "oversized":
					var b strings.Builder
					b.WriteString("id: public\ninput: public input\nassertions:\n")
					for i := 0; i < 1000; i++ {
						fmt.Fprintf(&b, "- {id: q%d, type: llmJudge, rubric: rubric, minScore: 50, judgeModel: '%s', judgeVersion: '%s'}\n", i, strings.Repeat("<", 256), strings.Repeat(">", 256))
					}
					_ = os.WriteFile(filepath.Join(opt.BundleDir, "eval", "a.yaml"), []byte(b.String()), 0600)
				}
				want := map[string]string{"missing": "--judge-base-url", "invalid key": "environment variable name", "header key": "invalid header bytes", "oversized": "1 MiB receipt budget", "unused selection": "no selected llmJudge"}[mode]
				if err := a.EvaluateAgentBundle(opt); err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "private-key-canary") {
					t.Fatalf("wrong preflight error; want %q: %v", want, err)
				}
				if runtime == "orka" {
					if f.taskCreates(t) != 0 {
						t.Fatal("preflight executed Task")
					}
				} else {
					s.mu.Lock()
					n := len(s.creates)
					s.mu.Unlock()
					if n != 0 {
						t.Fatal("preflight created session")
					}
				}
			})
		}
	}
}

type integrationAnswerWriter struct {
	before func()
	fail   bool
}

func (w integrationAnswerWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("public answer")) {
		if w.before != nil {
			w.before()
		}
		if w.fail {
			return 0, errors.New("private-writer-canary")
		}
	}
	return len(p), nil
}

func TestJudgeOrkaRejectsAnswerBeforeJudging(t *testing.T) {
	for _, mode := range []string{"revision changed", "output failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newEvalFixture(t, map[string]string{"a.yaml": evalHelloCase})
			f.answers["public input"] = "public answer"
			writer := integrationAnswerWriter{fail: mode == "output failed"}
			if mode == "revision changed" {
				writer.before = func() { f.seedAgent(t, strings.Repeat("7", 64)) }
			}
			f.app.Out = writer
			c := judgeCase()
			calls := 0
			port := agentruntime.EvaluationJudge(func(ctx context.Context, c agentruntime.EvaluationCase, e agentruntime.EvaluationEvidence) []agentruntime.EvaluationAssertionResult {
				if e.AnswerAvailable {
					calls++
				}
				row := integrationJudgeRow(nil)
				row.Reason = "answer unavailable"
				return []agentruntime.EvaluationAssertionResult{row}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			r, err := (orkaRuntimeAdapter{app: f.app, resultPort: f.opt.ResultPort}).Evaluate(ctx, agentruntime.AgentRef{Namespace: "orka-system", Name: f.name, UID: "agent-uid"}, agentruntime.EvaluationRequest{CaseID: c.ID, Input: c.Input, Assertions: c.Assertions, PortableDigest: f.digest, Judge: port})
			if err != nil || r.Verdict != agentruntime.EvaluationUnknown || r.Detail != evaluationResultUnavailable || calls != 0 || r.Assertions[0].Judge == nil {
				t.Fatalf("unattributed answer judged: %+v %v calls=%d", r, err, calls)
			}
		})
	}
}

// Measured actual deeply indented wire cost, not merely the estimated count.
func TestJudgeReceiptBudgetCoversSerializedRows(t *testing.T) {
	for _, identity := range []string{strings.Repeat("<", 256), strings.Repeat(`"`, 256), "fixture/model"} {
		a := judgeCase().Assertions[0]
		a.JudgeModel, a.JudgeVersion = identity, identity
		row := integrationJudgeRow([]int{100, 100})
		row.ID = strings.Repeat("z", 63)
		row.Judge.Model, row.Judge.ModelVersion = identity, identity
		row.Judge.MinScore = 100
		base := diffOrkaFixture()
		base.Cases[0].Assertions = nil
		before, _ := json.MarshalIndent(base, "", "  ")
		base.Cases[0].Assertions = []agentruntime.EvaluationAssertionResult{row}
		after, _ := json.MarshalIndent(base, "", "  ")
		cost := len(after) - len(before)
		budget := evaluationAssertionReceiptBytes(a)
		t.Logf("serialized row %d bytes; reserved %d", cost, budget)
		if cost > budget {
			t.Fatalf("judge row underbudget: cost=%d reserve=%d", cost, budget)
		}
		cases := make([]bundleEvaluationCase, 1)
		cases[0].Case.Assertions = []agentruntime.EvaluationAssertion{a}
		count := 0
		for preflightEvaluationReceiptSize(cases) == nil {
			count++
			cases = append(cases, cases[0])
		}
		base.Cases = make([]bundleEvaluationResult, count)
		for i := range base.Cases {
			base.Cases[i] = bundleEvaluationResult{ID: strings.Repeat("z", 63), CaseDigest: strings.Repeat("a", 64), InputDigest: strings.Repeat("b", 64), Verdict: "pass", TaskName: strings.Repeat("z", 62), TaskUID: strings.Repeat("x", 256), AnswerSHA256: strings.Repeat("f", 64), Assertions: []agentruntime.EvaluationAssertionResult{row}}
		}
		full, _ := json.MarshalIndent(base, "", "  ")
		if len(full)+1 > maxSessionsReceiptBytes {
			t.Fatalf("accepted suite exceeds exact size: %d", len(full)+1)
		}
	}
}

func TestJudgeUnavailableExecutionRejectsScores(t *testing.T) {
	row := integrationJudgeRow([]int{49, 50})
	for _, detail := range []string{evaluationExecutionUnavailable, evaluationResultUnavailable, evaluationExecutionFailed} {
		verdict := "unknown"
		if detail == evaluationExecutionFailed {
			verdict = "fail"
		}
		if evaluationCaseEvidence(strings.Repeat("c", 64), strings.Repeat("d", 64), verdict, detail, []agentruntime.EvaluationAssertionResult{row}) {
			t.Fatalf("unattributed scores accepted: %s", detail)
		}
	}
}

func TestJudgeReceiptBudgetAccountsEscapedIdentity(t *testing.T) {
	cases := make([]bundleEvaluationCase, 232)
	for i := range cases {
		cases[i].Case = judgeCase()
		cases[i].Case.Assertions[0].JudgeModel = strings.Repeat("<", 256)
		cases[i].Case.Assertions[0].JudgeVersion = strings.Repeat(">", 256)
	}
	if err := preflightEvaluationReceiptSize(cases); err == nil {
		t.Fatal("judge identity ignored in preflight")
	}
	cases = cases[:1]
	cases[0].Case.Assertions = make([]agentruntime.EvaluationAssertion, 1000)
	for i := range cases[0].Case.Assertions {
		cases[0].Case.Assertions[i] = judgeCase().Assertions[0]
		cases[0].Case.Assertions[i].JudgeModel = strings.Repeat("<", 256)
		cases[0].Case.Assertions[i].JudgeVersion = strings.Repeat(">", 256)
	}
	if err := preflightEvaluationReceiptSize(cases); err == nil {
		t.Fatal("maximum judge identity rows exceed budget but accepted")
	}
}
