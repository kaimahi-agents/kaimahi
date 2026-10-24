package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// Decode the public JSON shape so this regression first fails on behavior,
// rather than depending on a not-yet-added Go field.
type fileJudgeDisplay struct {
	Scores   []int    `json:"scores"`
	MinScore int      `json:"minScore"`
	Reasons  []string `json:"reasons"`
}

func judgeDisplay(t *testing.T, result FilePreviewResult) []*fileJudgeDisplay {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Examples []struct {
			Assertions []struct {
				Judge *fileJudgeDisplay `json:"judge"`
			} `json:"assertions"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	var displays []*fileJudgeDisplay
	for _, ex := range view.Examples {
		displays = append(displays, ex.Assertions[0].Judge)
	}
	return displays
}

func TestFileJudgeDisplayActualSamplesAndCaseIsolation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		scores                [2]int
		mode                  string
		show, failed, display bool
		verdict               runtime.EvaluationVerdict
	}{
		{"pass", [2]int{95, 90}, "", true, false, true, runtime.EvaluationPass},
		{"fail", [2]int{10, 20}, "", true, false, true, runtime.EvaluationFail},
		{"split", [2]int{95, 10}, "", true, false, true, runtime.EvaluationUnknown},
		{"default", [2]int{10, 20}, "", false, false, false, runtime.EvaluationFail},
		{"failed-only", [2]int{10, 20}, "", false, true, true, runtime.EvaluationFail},
		{"passing-failed-only", [2]int{95, 90}, "", false, true, false, runtime.EvaluationPass},
		{"split-failed-only", [2]int{95, 10}, "", false, true, false, runtime.EvaluationUnknown},
		{"partial-unavailable", [2]int{95, 90}, "unavailable", true, false, false, runtime.EvaluationUnknown},
		{"partial-malformed", [2]int{95, 90}, "malformed", true, false, false, runtime.EvaluationUnknown},
		{"model-mismatch", [2]int{95, 90}, "mismatch", true, false, false, runtime.EvaluationUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KMX_HOME", t.TempDir())
			samples := map[string]int{}
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				var payload directChat
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				model, answer := payload.Model, "Hello, PRIVATE INPUT"
				if payload.Messages[0].Content == runtime.EvaluationJudgePolicy {
					var content struct{ Rubric string }
					if err := json.Unmarshal([]byte(payload.Messages[1].Content), &content); err != nil {
						t.Error(err)
					}
					i := samples[content.Rubric]
					samples[content.Rubric]++
					answer = fmt.Sprintf(`{"score":%d,"reasoning":%q}`, tc.scores[i], "reason for "+content.Rubric+" sample "+fmt.Sprint(i))
					if i == 1 {
						switch tc.mode {
						case "unavailable":
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						case "malformed":
							answer = `{"score":101,"reasoning":"invalid"}`
						case "mismatch":
							model = "other-model"
						}
					}
				}
				previewEnvelope(w, model, answer)
			}))
			defer server.Close()
			opt := directOptions(t, "unit", true)
			opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
			opt.ShowContent, opt.ShowFailedContent = tc.show, tc.failed
			parsed := mustPreviewAgent(t, opt.File)
			for i := range parsed.Examples {
				parsed.Examples[i].Assertions[0].JudgeModel = ""
				parsed.Examples[i].Assertions[0].Rubric = parsed.Examples[i].ID
			}
			result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentSnapshot(t.Context(), parsed, opt)
			if result.Verdict != tc.verdict || (err == nil) != (tc.verdict == runtime.EvaluationPass) {
				t.Fatalf("result=%+v / %v", result, err)
			}
			displays := judgeDisplay(t, result)
			for i, display := range displays {
				if (display != nil) != tc.display {
					t.Fatalf("case %d display=%+v result=%+v", i, display, result)
				}
				if display != nil {
					wantReasons := []string{"reason for " + parsed.Examples[i].ID + " sample 0", "reason for " + parsed.Examples[i].ID + " sample 1"}
					if !reflect.DeepEqual(display.Scores, tc.scores[:]) || display.MinScore != 80 || !reflect.DeepEqual(display.Reasons, wantReasons) {
						t.Fatalf("case %d display=%+v want reasons=%v", i, display, wantReasons)
					}
				}
			}
			if tc.mode == "" && sends != 6 {
				t.Fatalf("expected independent two samples per case: sends=%d", sends)
			}
			data, _ := json.Marshal(result)
			if !tc.display && strings.Contains(string(data), "reason for") {
				t.Fatal("undisclosed rationale escaped")
			}
		})
	}
}
