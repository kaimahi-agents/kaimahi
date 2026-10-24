package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestLocalFileRunnerJournaledAppSnapshot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "state")
	t.Setenv("KMX_HOME", home)
	runner := NewLocalFileRunner()
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("constructor performed IO: %v", err)
	}
	opt := directOptions(t, "fake", true)
	opt.Operation = "run"
	input := "Ada"
	opt.Input = &input
	opt.CaseID = "first"
	opt.ShowContent = true
	a, err := agentfile.Load(opt.File)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(opt.File); err != nil {
		t.Fatal(err)
	}
	result, err := (&App{FileRunner: runner}).PreviewAgentSnapshot(t.Context(), a, opt)
	if err != nil || result.State != "complete" || result.FullPass || result.Verdict != runtime.EvaluationPass || result.AdHoc == nil || result.AdHoc.AnswerExcerpt != "Hello, Ada" || strings.Contains(result.Runner, "no journal") {
		t.Fatalf("snapshot adapter: %+v / %v", result, err)
	}
	paths, err := filepath.Glob(filepath.Join(home, "local-preview", "journals", "*.db"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("snapshot did not retain one invocation journal: %v / %v", paths, err)
	}
	// Use the private outcome to verify retention without exposing its handle in JSON.
	out, err := runner.Run(t.Context(), agentpreview.Request{Agent: a, Examples: a.Examples[:1], Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}})
	if err != nil || out.Journal == "" || out.NoJournal || out.Examples[0].Assertions[0].Judge == nil {
		t.Fatalf("native judge/journal missing: %+v / %v (paths=%v)", out, err, paths)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), out.Journal) || strings.Contains(string(b), home) {
		t.Fatal("private journal location in public result")
	}
}

func TestLocalFileRunnerNativeJudgeExactLocalEndpoint(t *testing.T) {
	for _, path := range []string{"", "/v1", "/custom/"} {
		t.Run(path, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(base, "state")
			t.Setenv("KMX_HOME", home)
			sends, judgeSends := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				if r.URL.Path != strings.TrimRight(path, "/")+"/chat/completions" {
					t.Errorf("endpoint rewritten: %s", r.URL.Path)
				}
				var payload directChat
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				answer := "Hello, PRIVATE INPUT"
				if len(payload.Messages) > 0 && payload.Messages[0].Content == runtime.EvaluationJudgePolicy {
					judgeSends++
					answer = `{"score":95,"reasoning":"private native judge reasoning"}`
				}
				previewEnvelope(w, "unit", answer)
			}))
			defer server.Close()
			opt := directOptions(t, "unit", true)
			opt.Route = &modelaccess.Route{Provider: "ollama", Model: "unit", Endpoint: strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + path, Auth: "none"}
			result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentFile(t.Context(), opt)
			if err != nil || !result.FullPass || result.ModelRoute.Provider != "ollama" || judgeSends != 4 || sends != 6 {
				t.Fatalf("native judge: %+v / %v sends=%d judge=%d", result, err, sends, judgeSends)
			}
		})
	}
}

func TestLocalFileRunnerJudgeProgressFailureStopsSamples(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "state")
	t.Setenv("KMX_HOME", home)
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		previewEnvelope(w, "unit", "Hello, PRIVATE INPUT")
	}))
	defer server.Close()
	opt := directOptions(t, "unit", true)
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	opt.Progress = func(message string) error {
		if message == "Judge started" {
			return errors.New("sink closed")
		}
		return nil
	}
	result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentFile(context.Background(), opt)
	if err == nil || result.ReasonCode != "diagnostics_failed" || sends != 1 || result.Verdict != runtime.EvaluationUnknown {
		t.Fatalf("judge progress failure: %+v / %v sends=%d", result, err, sends)
	}
}

func TestLocalFileRunnerZeroExamplesAdHoc(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "state")
	t.Setenv("KMX_HOME", home)
	opt := directOptions(t, "fake", false)
	zeroPreviewFile(t, opt.File)
	opt.Operation, opt.ShowContent = "run", true
	input := "Ada"
	opt.Input = &input
	result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentFile(t.Context(), opt)
	if err != nil || result.State != "complete" || result.FullPass || result.Verdict != runtime.EvaluationUnknown || result.AdHoc == nil || result.AdHoc.AnswerExcerpt != "Hello, Ada" || strings.Contains(result.Runner, "no journal") {
		t.Fatalf("journaled zero-example ad hoc: %+v / %v", result, err)
	}
	opt.Operation, opt.Input = "test", nil
	if result, err = (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentFile(t.Context(), opt); err == nil || result.ReasonCode != "no_tests" {
		t.Fatalf("zero-example test: %+v / %v", result, err)
	}
	if _, err := NewLocalFileRunner().Run(t.Context(), agentpreview.Request{Agent: &agentfile.Agent{Model: "fake"}, Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}}); err == nil {
		t.Fatal("journaled runner accepted no examples and no input")
	}
}
