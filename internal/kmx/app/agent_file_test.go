package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// scriptedPreview tests the caller contract, not Sessions or model execution.
type scriptedPreview struct {
	verdict runtime.EvaluationVerdict
	calls   int
	request agentpreview.Request
	mutate  func(*agentpreview.Outcome)
	err     error
}

var _ agentpreview.Runner = (*scriptedPreview)(nil)

func (f *scriptedPreview) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	f.calls++
	f.request = req
	complete := func(id string) agentpreview.Completion {
		return agentpreview.Completion{State: agentpreview.Complete, ID: id, FileRevision: req.Agent.Revision, Provider: req.Route.Provider, Model: req.Route.Model, Answer: "PRIVATE ANSWER\x1b[31m"}
	}
	out := agentpreview.Outcome{State: agentpreview.Complete, Journal: "PRIVATE JOURNAL"}
	if req.Input != nil {
		c := complete("adhoc")
		out.AdHoc = &c
	}
	for i, ex := range req.Examples {
		row := agentpreview.ExampleOutcome{ID: ex.ID, Completion: complete(fmt.Sprint(i))}
		for _, a := range ex.Assertions {
			row.Assertions = append(row.Assertions, runtime.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: runtime.EvaluationAssertionDigest(a), Verdict: f.verdict, Reason: "PRIVATE REASON"})
		}
		out.Examples = append(out.Examples, row)
	}
	if f.mutate != nil {
		f.mutate(&out)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, f.err
}

func previewFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "greeter.agent.md")
	text := "# Greeter\n\n## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n\n## Accountable Responsibilities\nPRIVATE INSTRUCTIONS\n\n## Non-Responsibilities\nNo tools.\n\n## Collaboration and Review Authority\nNone.\n\n## Model\n- **Preferred:** fake\n\n## Examples\n```yaml\nexamples:\n"
	for _, id := range []string{"first", "second"} {
		text += fmt.Sprintf("  - id: %s\n    input: PRIVATE INPUT\n    assertions:\n      - id: text\n        type: contains\n        value: PRIVATE OPERAND\n", id)
	}
	text += "```\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFilePreviewFailureOnlyRequiresJoinedFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agentpreview.ExampleOutcome)
		want   bool
		code   string
	}{
		{"failed", nil, true, "assertion_failed"},
		{"passing", func(r *agentpreview.ExampleOutcome) { r.Assertions[0].Verdict = runtime.EvaluationPass }, false, "checks_passed"},
		{"unknown", func(r *agentpreview.ExampleOutcome) { r.Assertions[0].Verdict = runtime.EvaluationUnknown }, false, "judge_unknown"},
		{"incomplete", func(r *agentpreview.ExampleOutcome) { r.Completion.State = "" }, false, "invalid_completion"},
		{"unmatched-case", func(r *agentpreview.ExampleOutcome) { r.ID = "other" }, false, "invalid_completion"},
		{"unmatched-assertion", func(r *agentpreview.ExampleOutcome) { r.Assertions[0].ID = "other" }, false, "invalid_completion"},
		{"unmatched-definition", func(r *agentpreview.ExampleOutcome) {
			r.Assertions[0].DefinitionDigest = "sha256:" + strings.Repeat("0", 64)
		}, false, "invalid_completion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(out *agentpreview.Outcome) {
				row := &out.Examples[1]
				row.Assertions[0].Verdict = runtime.EvaluationFail
				row.Completion.Answer = strings.Repeat("x", 4097)
				if tc.mutate != nil {
					tc.mutate(row)
				}
			}}
			input := "ad-hoc"
			r, _ := (&App{FileRunner: fake}).PreviewAgentFile(t.Context(), FilePreviewOptions{File: previewFile(t), Operation: "run", Input: &input, ShowFailedContent: true})
			if r.ReasonCode != tc.code || fake.calls != 1 {
				t.Fatalf("result: %+v / sends=%d", r, fake.calls)
			}
			if r.Examples[0].AnswerExcerpt != "" || r.AdHoc == nil || r.AdHoc.AnswerExcerpt != "" {
				t.Fatal("passing or ad-hoc answer disclosed")
			}
			row := r.Examples[1]
			if tc.want {
				if row.AnswerExcerpt != strings.Repeat("x", 4096) || !row.Truncated {
					t.Fatal("bounded failure excerpt lost")
				}
			} else if row.AnswerExcerpt != "" || row.Truncated {
				t.Fatal("unvalidated failure answer disclosed")
			}
		})
	}
}

func TestFilePreviewSelectionAndPrivacy(t *testing.T) {
	for _, op := range []string{"run", "test"} {
		for _, subset := range []bool{false, true} {
			t.Run(fmt.Sprint(op, subset), func(t *testing.T) {
				path := previewFile(t)
				before, _ := os.ReadFile(path)
				fake := &scriptedPreview{verdict: runtime.EvaluationPass}
				a := &App{FileRunner: fake}
				opt := FilePreviewOptions{File: path, Operation: op}
				if subset {
					opt.CaseID = "second"
				}
				result, err := a.PreviewAgentFile(context.Background(), opt)
				if err != nil {
					t.Fatal(err)
				}
				if result.FullPass == subset || result.Verdict != runtime.EvaluationPass || result.State != "complete" {
					t.Fatalf("result: %+v", result)
				}
				if len(fake.request.Examples) != len(result.Selection.CaseIDs) || result.Selection.TotalCases != 2 {
					t.Fatal("wrong selection")
				}
				if subset && (fake.request.Examples[0].ID != "second" || result.Selection.Scope != "subset") {
					t.Fatal("subset not explicit")
				}
				if !subset && fake.request.Examples[0].ID != "first" {
					t.Fatal("authored order lost")
				}
				if fake.request.Route.Provider != "fake" || fake.request.Route.Model != "fake" || fake.calls != 1 {
					t.Fatal("wrong route or repeated execution")
				}
				var out bytes.Buffer
				if err := WriteFilePreview(&out, result, "json"); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"PRIVATE", "Answer", "Journal", "Instructions", "OPERAND"} {
					if strings.Contains(out.String(), secret) {
						t.Fatalf("leaked %s: %s", secret, out.String())
					}
				}
				after, _ := os.ReadFile(path)
				if !bytes.Equal(before, after) {
					t.Fatal("source changed")
				}
				out.Reset()
				if err := WriteFilePreview(&out, result, "text"); err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"local preview", result.File.Revision, "fake/fake", "text contains pass"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing %q: %s", want, out.String())
					}
				}
				if subset && !strings.Contains(out.String(), "partial") {
					t.Fatal("subset hidden")
				}
			})
		}
	}
}

func TestFilePreviewNonPass(t *testing.T) {
	cases := []struct {
		name    string
		verdict runtime.EvaluationVerdict
		mutate  func(*agentpreview.Outcome)
		cause   error
		code    string
		exit    int
	}{
		{"fail", runtime.EvaluationFail, nil, nil, "assertion_failed", 2},
		{"unknown", runtime.EvaluationUnknown, nil, nil, "judge_unknown", 2},
		{"journal", runtime.EvaluationPass, nil, agentpreview.ErrJournalFailed, "journal_failed", 1},
		{"cancel", runtime.EvaluationPass, nil, context.Canceled, "cancelled", 130},
		{"deadline", runtime.EvaluationPass, nil, context.DeadlineExceeded, "deadline_exceeded", 1},
		{"provider", runtime.EvaluationPass, nil, errors.New("PRIVATE HTTP ERROR"), "execution_failed", 1},
		{"missing", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples = o.Examples[:1] }, nil, "invalid_completion", 1},
		{"revision", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples[0].Completion.FileRevision = "stale" }, nil, "invalid_completion", 1},
		{"model", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples[0].Completion.Model = "other" }, nil, "invalid_completion", 1},
		{"duplicate execution", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples[1].Completion.ID = o.Examples[0].Completion.ID }, nil, "invalid_completion", 1},
		{"missing assertion", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples[0].Assertions = nil }, nil, "invalid_completion", 1},
		{"digest", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples[0].Assertions[0].DefinitionDigest = "other" }, nil, "invalid_completion", 1},
		{"bad verdict", runtime.EvaluationVerdict("nonsense"), nil, nil, "invalid_completion", 1},
		{"invalid answer encoding", runtime.EvaluationPass, func(o *agentpreview.Outcome) { o.Examples[0].Completion.Answer = string([]byte{0xff}) }, nil, "invalid_completion", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &scriptedPreview{verdict: tc.verdict, mutate: tc.mutate, err: tc.cause}
			result, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: previewFile(t), Operation: "test"})
			var status *FilePreviewError
			if !errors.As(err, &status) || status.ExitCode() != tc.exit || result.ReasonCode != tc.code || result.FullPass {
				t.Fatalf("%+v / %v", result, err)
			}
			if tc.exit != 2 && result.Verdict != runtime.EvaluationUnknown {
				t.Fatal("execution failure became verdict")
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestFilePreviewRefusalsBeforeRunner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*FilePreviewOptions)
		code   string
	}{
		{"case", func(o *FilePreviewOptions) { o.CaseID = "absent" }, "invalid_selection"},
		{"operation", func(o *FilePreviewOptions) { o.Operation = "publish" }, "invalid_options"},
		{"test input", func(o *FilePreviewOptions) { s := "input"; o.Input = &s }, "invalid_options"},
		{"invalid input encoding", func(o *FilePreviewOptions) { o.Operation = "run"; s := string([]byte{0xff}); o.Input = &s }, "invalid_options"},
		{"route mismatch", func(o *FilePreviewOptions) {
			o.Route = &modelaccess.Route{Provider: "fake", Model: "other", Auth: "none"}
		}, "model_unavailable"},
		{"missing file", func(o *FilePreviewOptions) { o.File += "-missing" }, "invalid_file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &scriptedPreview{verdict: runtime.EvaluationPass}
			opt := FilePreviewOptions{File: previewFile(t), Operation: "test"}
			tc.change(&opt)
			result, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), opt)
			if err == nil || result.ReasonCode != tc.code || f.calls != 0 || result.FullPass {
				t.Fatalf("%+v / %v / calls %d", result, err, f.calls)
			}
		})
	}
	result, err := (&App{}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: previewFile(t), Operation: "test"})
	if err == nil || result.ReasonCode != "runner_unavailable" {
		t.Fatalf("%+v / %v", result, err)
	}
}

func TestFilePreviewAdHocAndJudgeRoute(t *testing.T) {
	f := &scriptedPreview{verdict: runtime.EvaluationPass}
	input := "ad-hoc"
	judge := modelaccess.Route{Provider: "compatible", Model: "judge", Endpoint: "http://127.0.0.1:1234", Auth: "none"}
	result, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: previewFile(t), Operation: "run", Input: &input, JudgeRoute: &judge, ShowContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.JudgeRoute == nil || result.JudgeRoute.Model != "judge" {
		t.Fatal("explicit judge route hidden")
	}
	if result.AdHoc == nil || result.AdHoc.AnswerExcerpt == "" || result.AdHoc.Truncated || strings.Contains(result.AdHoc.AnswerExcerpt, "\x1b") || f.request.Input == nil || *f.request.Input != input || f.request.JudgeRoute == nil || *f.request.JudgeRoute != judge {
		t.Fatalf("%+v", result)
	}
}

func zeroPreviewFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.SplitAfter(string(data), "```yaml\n")[0] + "examples: []\n```\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Zero-test charters are runnable ad hoc but never evaluated or fully green.
func TestFilePreviewZeroExamples(t *testing.T) {
	for _, tc := range []struct {
		name string
		opt  func(*FilePreviewOptions)
		code string
	}{
		{"test", func(*FilePreviewOptions) {}, "no_tests"},
		{"run without input", func(o *FilePreviewOptions) { o.Operation = "run" }, "no_tests"},
		{"run case", func(o *FilePreviewOptions) { o.Operation = "run"; s := "Ada"; o.Input = &s; o.CaseID = "first" }, "invalid_selection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &scriptedPreview{verdict: runtime.EvaluationPass}
			opt := FilePreviewOptions{File: zeroPreviewFile(t, previewFile(t)), Operation: "test"}
			tc.opt(&opt)
			result, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), opt)
			var refusal *FilePreviewError
			if !errors.As(err, &refusal) || refusal.ExitCode() != 1 || result.ReasonCode != tc.code || f.calls != 0 || result.FullPass || result.State != "refused" || result.Verdict != runtime.EvaluationUnknown {
				t.Fatalf("%+v / %v / calls %d", result, err, f.calls)
			}
			if tc.code == "no_tests" && (result.Reason != "No tests yet — add or suggest one" || err.Error() != result.Reason || result.Selection.Scope != "full" || result.Selection.TotalCases != 0 || len(result.Selection.CaseIDs) != 0 || result.File.Revision == "" || len(result.Examples) != 0) {
				t.Fatalf("no_tests projection: %+v", result)
			}
		})
	}
	f := &scriptedPreview{verdict: runtime.EvaluationPass}
	input := "Ada"
	result, err := (&App{FileRunner: f}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: zeroPreviewFile(t, previewFile(t)), Operation: "run", Input: &input})
	if err != nil || f.calls != 1 || len(f.request.Examples) != 0 || result.State != "complete" || result.Verdict != runtime.EvaluationUnknown || result.FullPass || result.ReasonCode != "completion_ready" || result.AdHoc == nil || result.AdHoc.State != "complete" || len(result.Examples) != 0 {
		t.Fatalf("zero-example ad-hoc run: %+v / %v", result, err)
	}
}
