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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func directOptions(t *testing.T, model string, judge bool) FilePreviewOptions {
	t.Helper()
	path := previewFile(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "PRIVATE INSTRUCTIONS", "Reply with exactly `Hello, ` followed by the input name.")
	text = strings.ReplaceAll(text, "**Preferred:** fake", "**Preferred:** "+model)
	text = strings.ReplaceAll(text, "value: PRIVATE OPERAND", "value: Hello, PRIVATE INPUT")
	if judge {
		text = strings.ReplaceAll(text, "type: contains\n        value: Hello, PRIVATE INPUT", "type: llmJudge\n        rubric: Answer contains `Hello,`.\n        minScore: 80\n        judgeModel: "+model+"\n        judgeVersion: v1")
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return FilePreviewOptions{File: path, Operation: "test"}
}

func TestDirectPreviewFakeFailFixAndNoJournal(t *testing.T) {
	opt := directOptions(t, "fake", true)
	opt.ShowContent = true
	a := &App{FileRunner: NewDirectFileRunner()}
	result, err := a.PreviewAgentFile(context.Background(), opt)
	if err != nil || !result.FullPass || result.Runner != "preview runner (no journal)" || result.Retention.Status != "not_requested" {
		t.Fatalf("%+v / %v", result, err)
	}
	var text bytes.Buffer
	if err := WriteFilePreview(&text, result, "text"); err != nil || !strings.Contains(text.String(), "preview runner (no journal)") {
		t.Fatal("no-journal label missing from text")
	}
	data, _ := os.ReadFile(opt.File)
	if err := os.WriteFile(opt.File, []byte(strings.ReplaceAll(string(data), "`Hello, `", "`Welcome, `")), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = a.PreviewAgentFile(context.Background(), opt)
	if err == nil || result.Verdict != runtime.EvaluationFail || result.FullPass {
		t.Fatalf("instruction regression hidden: %+v / %v", result, err)
	}
	if err := os.WriteFile(opt.File, data, 0600); err != nil {
		t.Fatal(err)
	}
	opt.CaseID = "first"
	result, err = a.PreviewAgentFile(context.Background(), opt)
	if err != nil || result.FullPass || result.Verdict != runtime.EvaluationPass {
		t.Fatalf("subset: %+v / %v", result, err)
	}
	opt.CaseID = ""
	opt.Operation = "run"
	input := "Ada"
	opt.Input = &input
	result, err = a.PreviewAgentFile(context.Background(), opt)
	if err != nil || !result.FullPass || result.AdHoc == nil || result.AdHoc.AnswerExcerpt != "Hello, Ada" {
		t.Fatalf("ad-hoc: %+v / %v", result, err)
	}
	parsed, _ := agentfile.Load(opt.File)
	raw, err := a.FileRunner.Run(context.Background(), agentpreview.Request{Agent: parsed, Examples: parsed.Examples, Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}})
	if err != nil || !raw.NoJournal || raw.Journal != "" || raw.Examples[0].Assertions[0].Judge != nil || runtime.EvaluationJudgeEvidenceValid(raw.Examples[0].Assertions[0]) {
		t.Fatalf("preview manufactured native evidence: %+v / %v", raw, err)
	}
	if err := os.Remove(opt.File); err != nil {
		t.Fatal(err)
	}
	result, err = a.PreviewAgentSnapshot(context.Background(), parsed, opt)
	if err != nil || !result.FullPass {
		t.Fatalf("direct runner reopened rooted source: %+v / %v", result, err)
	}
}

func previewEnvelope(w http.ResponseWriter, model, answer string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": answer}}}})
}
func TestDirectPreviewCompatibleSingleJudgeSample(t *testing.T) {
	for _, sample := range []struct {
		content string
		verdict runtime.EvaluationVerdict
	}{
		{`{"score":95,"reasoning":"satisfied"}`, runtime.EvaluationPass},
		{`{"score":10,"reasoning":"not satisfied"}`, runtime.EvaluationFail},
		{`{"score":101,"reasoning":"invalid"}`, runtime.EvaluationUnknown},
		{"unavailable", runtime.EvaluationUnknown},
		{`{"score":95,"score":95,"reasoning":"duplicate"}`, runtime.EvaluationUnknown},
	} {
		t.Run(string(sample.verdict)+sample.content, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" || r.Method != "POST" {
					t.Errorf("wrong endpoint: %s %s", r.Method, r.URL.Path)
				}
				var payload struct {
					Model    string `json:"model"`
					Stream   bool   `json:"stream"`
					Messages []struct{ Role, Content string }
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "unit" || payload.Stream || len(payload.Messages) != 2 {
					t.Error("wrong request")
				}
				var check map[string]any
				if json.Unmarshal([]byte(payload.Messages[1].Content), &check) == nil && check["rubric"] != nil {
					if sample.content == "unavailable" {
						http.Error(w, "PRIVATE JUDGE ERROR", 500)
					} else {
						previewEnvelope(w, "unit", sample.content)
					}
				} else {
					if strings.Contains(payload.Messages[0].Content, "## Examples") || payload.Messages[1].Content != "PRIVATE INPUT" {
						t.Error("instructions/input drift")
					}
					previewEnvelope(w, "unit", "Hello, PRIVATE INPUT")
				}
			}))
			defer server.Close()
			opt := directOptions(t, "unit", true)
			opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL + "/v1", Auth: "none"}
			var diagnostics strings.Builder
			opt.Progress = func(message string) error {
				diagnostics.WriteString(message + "\n")
				return nil
			}
			result, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(context.Background(), opt)
			if result.Verdict != sample.verdict || (err == nil) != (sample.verdict == runtime.EvaluationPass) || calls.Load() != 4 {
				t.Fatalf("calls %d / %+v / %v", calls.Load(), result, err)
			}
			for _, want := range []string{"endpoint=" + server.URL + "/v1", "HTTP=200", "elapsed=", "Judge first/"} {
				if !strings.Contains(diagnostics.String(), want) {
					t.Fatalf("missing %q: %s", want, diagnostics.String())
				}
			}
			if strings.Contains(diagnostics.String(), "PRIVATE") || strings.Contains(diagnostics.String(), "satisfied") {
				t.Fatal("diagnostics disclosed content or rationale")
			}
		})
	}
}
func TestDirectPreviewHTTPRefusesNoRetryOrLaterSends(t *testing.T) {
	for _, mode := range []string{"error", "forbidden", "redirect", "tool", "model", "missing_model", "null_model", "empty_model", "null_response", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "error":
					http.Error(w, "PRIVATE PROVIDER ERROR", 500)
				case "forbidden":
					http.Error(w, "PRIVATE PROVIDER ERROR", http.StatusForbidden)
				case "redirect":
					http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
				case "tool":
					fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"answer","tool_calls":[{}]}}]}`)
				case "model":
					previewEnvelope(w, "other", "answer")
				case "missing_model":
					fmt.Fprint(w, `{}`)
				case "null_model":
					fmt.Fprint(w, `{"model":null}`)
				case "empty_model":
					fmt.Fprint(w, `{"model":""}`)
				case "null_response":
					fmt.Fprint(w, `null`)
				case "oversized":
					fmt.Fprint(w, strings.Repeat("x", 2*1024*1024))
				}
			}))
			defer server.Close()
			opt := directOptions(t, "unit", false)
			opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL + "/PRIVATE_PATH", Auth: "none"}
			var diagnostics strings.Builder
			opt.Progress = func(message string) error {
				diagnostics.WriteString(message + "\n")
				return nil
			}
			result, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(context.Background(), opt)
			if err == nil || result.FullPass || result.Verdict != runtime.EvaluationUnknown || calls.Load() != 1 || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("calls %d / %+v / %v", calls.Load(), result, err)
			}
			want := "invalid_completion"
			switch mode {
			case "error", "forbidden", "redirect":
				want = "http_error"
			case "model":
				want = "model_mismatch"
			case "oversized":
				want = "unreadable_or_oversized_response"
			}
			if !strings.Contains(diagnostics.String(), want) || strings.Contains(diagnostics.String(), "PRIVATE") {
				t.Fatalf("unsafe or missing failure diagnostics: %s", diagnostics.String())
			}
			if mode == "forbidden" && !strings.Contains(diagnostics.String(), "HTTP=403") {
				t.Fatalf("missing forbidden HTTP status: %s", diagnostics.String())
			}
		})
	}
}

func TestDirectPreviewVerboseConnectionRefused(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	opt := directOptions(t, "unit", false)
	opt.Route = &modelaccess.Route{Provider: "ollama", Model: "unit", Endpoint: server.URL + "/v1", Auth: "none"}
	var diagnostics strings.Builder
	opt.Progress = func(message string) error {
		diagnostics.WriteString(message + "\n")
		return nil
	}
	result, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(context.Background(), opt)
	if err == nil || result.FullPass || !strings.Contains(diagnostics.String(), "connection_refused") {
		t.Fatalf("missing connection failure: %v / %s", err, diagnostics.String())
	}
	if strings.Contains(diagnostics.String(), "HTTP=") {
		t.Fatalf("HTTP status reported without a response: %s", diagnostics.String())
	}
}

func TestDirectPreviewProgressFailureStopsLaterSends(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		previewEnvelope(w, "unit", "Hello, PRIVATE INPUT")
	}))
	defer server.Close()
	opt := directOptions(t, "unit", true)
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	opt.Progress = func(message string) error {
		if strings.Contains(message, "finished:") {
			return errors.New("PRIVATE DIAGNOSTIC ERROR")
		}
		return nil
	}
	result, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(context.Background(), opt)
	if err == nil || result.ReasonCode != "diagnostics_failed" || calls.Load() != 1 || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("diagnostic failure: %+v / %v / calls=%d", result, err, calls.Load())
	}
}
func TestDirectPreviewCancellationStopsBeforeJudge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		previewEnvelope(w, "unit", "Hello, PRIVATE INPUT")
		cancel()
	}))
	defer server.Close()
	opt := directOptions(t, "unit", true)
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	r, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(ctx, opt)
	if err == nil || r.FullPass || r.ReasonCode != "cancelled" || calls.Load() != 1 {
		t.Fatalf("%+v / %v / calls %d", r, err, calls.Load())
	}
}
func TestDirectPreviewRejectsRemoteCompatibleRoute(t *testing.T) {
	_, err := NewDirectFileRunner().(*directFileRunner).client(context.Background(), modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: "https://example.invalid", Auth: "none"})
	if !errors.Is(err, agentpreview.ErrModelUnavailable) {
		t.Fatalf("remote compatible route accepted: %v", err)
	}
}

func TestDirectPreviewExplicitJudgeOverride(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		previewEnvelope(w, "judge", `{"score":99,"reasoning":"satisfied"}`)
	}))
	defer server.Close()
	opt := directOptions(t, "fake", true)
	data, _ := os.ReadFile(opt.File)
	if err := os.WriteFile(opt.File, []byte(strings.ReplaceAll(string(data), "judgeModel: fake", "judgeModel: judge")), 0600); err != nil {
		t.Fatal(err)
	}
	opt.JudgeRoute = &modelaccess.Route{Provider: "compatible", Model: "judge", Endpoint: server.URL, Auth: "none"}
	result, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(context.Background(), opt)
	if err != nil || !result.FullPass || calls.Load() != 2 || result.JudgeRoute == nil || result.JudgeRoute.Model != "judge" {
		t.Fatalf("%+v / %v / calls %d", result, err, calls.Load())
	}
}

func TestDirectPreviewRefusesCopilotWithoutAccessDependency(t *testing.T) {
	opt := directOptions(t, "unit", false)
	opt.Route = &modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: "https://api.githubcopilot.com", Auth: "copilot"}
	r, err := (&App{FileRunner: &directFileRunner{}}).PreviewAgentFile(context.Background(), opt)
	if err == nil || r.FullPass || r.ReasonCode != "model_unavailable" {
		t.Fatalf("missing dependency accepted: %+v / %v", r, err)
	}
}

func TestDirectPreviewCancelledBeforeSend(t *testing.T) {
	opt := directOptions(t, "unit", false)
	parsed, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	out, err := NewDirectFileRunner().Run(ctx, agentpreview.Request{Agent: parsed, Examples: parsed.Examples, Route: modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: "http://127.0.0.1:1", Auth: "none"}})
	if !errors.Is(err, context.Canceled) || out.State != agentpreview.Cancelled {
		t.Fatalf("%+v / %v", out, err)
	}
}

func TestDirectPreviewNoJournalModeIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		noJournal bool
		handle    string
		good      bool
	}{
		{false, "", false}, {false, "journal", true}, {true, "", true}, {true, "journal", false},
	} {
		fake := &scriptedPreview{verdict: runtime.EvaluationPass, mutate: func(o *agentpreview.Outcome) { o.NoJournal = tc.noJournal; o.Journal = tc.handle }}
		r, err := (&App{FileRunner: fake}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: previewFile(t), Operation: "test"})
		if (err == nil) != tc.good || r.FullPass != tc.good || tc.good && tc.noJournal && r.Runner != "preview runner (no journal)" {
			t.Fatalf("mode %+v: %+v / %v", tc, r, err)
		}
	}
	fake := &scriptedPreview{verdict: runtime.EvaluationPass, err: fmt.Errorf("wrapped: %w", agentpreview.ErrJournalFailed), mutate: func(o *agentpreview.Outcome) { o.NoJournal = true; o.Journal = "" }}
	r, err := (&App{FileRunner: fake}).PreviewAgentFile(context.Background(), FilePreviewOptions{File: previewFile(t), Operation: "test"})
	var status *FilePreviewError
	if !errors.As(err, &status) || r.ReasonCode != "journal_failed" || r.Verdict != runtime.EvaluationUnknown {
		t.Fatal("journal error bypassed with no-journal mode")
	}
}

func TestDirectPreviewZeroExamplesAdHoc(t *testing.T) {
	opt := directOptions(t, "fake", false)
	zeroPreviewFile(t, opt.File)
	opt.Operation, opt.ShowContent = "run", true
	input := "Ada"
	opt.Input = &input
	result, err := (&App{FileRunner: NewDirectFileRunner()}).PreviewAgentFile(context.Background(), opt)
	if err != nil || result.State != "complete" || result.FullPass || result.Verdict != runtime.EvaluationUnknown || result.AdHoc == nil || result.AdHoc.AnswerExcerpt == "" || result.Runner != "preview runner (no journal)" {
		t.Fatalf("direct zero-example ad hoc: %+v / %v", result, err)
	}
	a, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirectFileRunner().Run(context.Background(), agentpreview.Request{Agent: a, Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}}); err == nil {
		t.Fatal("direct runner accepted no examples and no input")
	}
}
