package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileJudgeDisplayOmitsInvalidEvidenceWithoutVeto(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*runtime.EvaluationAssertionResult)
	}{
		{"model", func(r *runtime.EvaluationAssertionResult) { r.Judge.Model = "other" }},
		{"version", func(r *runtime.EvaluationAssertionResult) { r.Judge.ModelVersion = "v2" }},
		{"minimum", func(r *runtime.EvaluationAssertionResult) { r.Judge.MinScore = 70 }},
		{"single", func(r *runtime.EvaluationAssertionResult) { r.Judge.Scores = []int{90} }},
		{"out-of-range", func(r *runtime.EvaluationAssertionResult) { r.Judge.Scores = []int{90, 101} }},
		{"verdict", func(r *runtime.EvaluationAssertionResult) { r.Judge.Scores = []int{1, 2} }},
		{"matches", func(r *runtime.EvaluationAssertionResult) { r.Judge.ModelMatches = nil }},
		{"policy", func(r *runtime.EvaluationAssertionResult) { r.Judge.EvaluatorDigest = strings.Repeat("0", 64) }},
		{"missing", func(r *runtime.EvaluationAssertionResult) { r.Judge = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := directOptions(t, "unit", true)
			opt.ShowContent = true
			opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: "http://127.0.0.1/v1", Auth: "none"}
			runner := &scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(out *agentpreview.Outcome) {
				for i := range out.Examples {
					row := &out.Examples[i].Assertions[0]
					row.Judge = &runtime.EvaluationJudgeEvidence{Model: "unit", ModelVersion: "v1", MinScore: 80, Provenance: "user-declared", RequestStyle: runtime.EvaluationJudgeRequestStyle, EvaluatorDigest: runtime.EvaluationJudgeEvaluatorDigest(), ConfigurationDigest: strings.Repeat("a", 64), Scores: []int{90, 95}, ModelMatches: []string{"requested", "requested"}}
					tc.mutate(row)
				}
			}}
			result, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), opt)
			if err != nil || !result.FullPass || result.ReasonCode != "checks_passed" {
				t.Fatalf("display data vetoed otherwise-safe run: %+v / %v", result, err)
			}
			for _, display := range judgeDisplay(t, result) {
				if display != nil {
					t.Fatalf("invalid evidence displayed: %+v", display)
				}
			}
		})
	}
}

type evaluatedFilePreview struct {
	judge runtime.EvaluationJudge
	rows  [][]runtime.EvaluationAssertionResult
}

func (r *evaluatedFilePreview) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	scripted := &scriptedPreview{verdict: runtime.EvaluationPass}
	out, err := scripted.Run(ctx, req)
	for i, ex := range req.Examples {
		rows := r.judge(ctx, runtime.EvaluationCase{ID: ex.ID, Input: ex.Input, Assertions: ex.Assertions}, runtime.EvaluationEvidence{Answer: "answer", AnswerAvailable: true})
		out.Examples[i].Assertions = rows
		r.rows = append(r.rows, rows)
	}
	return out, err
}

func TestFileJudgeDisplayUsesEvaluatorKeyAndSharedRedaction(t *testing.T) {
	const key = "opaque-diagnostic-key"
	for _, tc := range []struct {
		name, reason, want string
		scores             bool
	}{
		{"plain", "plain rationale", "plain rationale", true},
		{"terminal", "plain \x1b[31mrationale", "plain rationale", true},
		{"bounded", strings.Repeat("é", 2048), strings.Repeat("é", 2048), true},
		{"header", "Authorization: Bearer public", "", true},
		{"normalized-header", "x-ap\x01i-key: public", "", true},
		{"header-block", "Trace: public\nResult: public", "", true},
		{"key", key, "", false},
		{"normalized-key", "opaque-diagnostic-\nkey", "", false},
		{"shape", "ghp_" + strings.Repeat("a", 36), "", false},
		{"oversized", strings.Repeat("x", 4097), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KMX_TEST_FILE_DISPLAY_KEY", key)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+key {
					t.Error("judge credential not used")
				}
				content, _ := json.Marshal(map[string]any{"score": 95, "reasoning": tc.reason})
				previewEnvelope(w, "unit", string(content))
			}))
			defer server.Close()
			opt := directOptions(t, "unit", true)
			opt.ShowContent = true
			opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: "http://127.0.0.1/v1", Auth: "none"}
			parsed := mustPreviewAgent(t, opt.File)
			cases := []runtime.EvaluationCase{}
			for _, ex := range parsed.Examples {
				cases = append(cases, runtime.EvaluationCase{ID: ex.ID, Input: ex.Input, Assertions: ex.Assertions})
			}
			judge, err := newEvaluationCaseJudge(evaluationJudgeConfig{BaseURL: server.URL, KeyEnv: "KMX_TEST_FILE_DISPLAY_KEY", transport: server.Client().Transport}, cases, nil)
			if err != nil {
				t.Fatal(err)
			}
			runner := &evaluatedFilePreview{judge: judge}
			result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(localPreviewJudgeContext(t), parsed, opt)
			if result.FullPass != tc.scores || (err == nil) != tc.scores {
				t.Fatalf("result=%+v / %v", result, err)
			}
			for _, display := range judgeDisplay(t, result) {
				if (display != nil) != tc.scores {
					t.Fatalf("display=%+v", display)
				}
				if display != nil {
					if tc.want == "" && len(display.Reasons) != 0 || tc.want != "" && (len(display.Reasons) != 2 || display.Reasons[0] != tc.want || display.Reasons[1] != tc.want) {
						t.Fatalf("unsafe/unbounded reasons: %+v", display)
					}
				}
			}
			raw, _ := json.Marshal(runner.rows)
			if strings.Contains(string(raw), "reasoning") || strings.Contains(string(raw), key) || strings.Contains(string(raw), "plain rationale") {
				t.Fatal("rationale escaped into runtime evidence")
			}
			var summary bytes.Buffer
			if err := WriteFilePreview(&summary, result, "text"); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(summary.String(), "plain rationale") || strings.Contains(summary.String(), key) {
				t.Fatal("rationale escaped into summary")
			}
		})
	}
}
