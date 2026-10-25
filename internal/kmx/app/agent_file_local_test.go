package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestLocalFileRunnerRunFailRetainExplainJourney(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "state")
	t.Setenv("KMX_HOME", home)
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private state leaf exists before execution: %v", err)
	}
	credentialEnv := "KMX_PHASE4_AMBIENT_TOKEN"
	credential := strings.Join([]string{"github", "pat", strings.Repeat("a", 82)}, "_")
	t.Setenv(credentialEnv, credential)

	filename := filepath.Join(base, "greeter.agent.md")
	source := strings.Join([]string{
		"# Greeter",
		"",
		"## Identity",
		"- **ID:** greeter",
		"- **Purpose:** Greet.",
		"",
		"## Accountable Responsibilities",
		"Reply with exactly `Hello, ` followed by the input name.",
		"",
		"## Non-Responsibilities",
		"No tools.",
		"",
		"## Collaboration and Review Authority",
		"None.",
		"",
		"## Model",
		"- **Preferred:** fake",
		"",
		"## Examples",
		"```yaml",
		"examples:",
		"  - id: greeting",
		"    input: Ada",
		"    assertions:",
		"      - id: greeting-text",
		"        type: contains",
		"        value: Hello, Ada",
		"```",
		"",
	}, "\n")
	if err := os.WriteFile(filename, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	initial, err := agentfile.Load(filename)
	if err != nil {
		t.Fatal(err)
	}

	var sends atomic.Int64
	progress := func(message string) error {
		if strings.HasPrefix(message, "Completion ") && strings.Contains(message, " started:") {
			sends.Add(1)
		}
		return nil
	}
	runner := NewLocalFileRunner()
	input := "Grace"
	run, err := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{
		File: filename, Operation: "run", Input: &input, ShowContent: true, Progress: progress,
	})
	if err != nil || run.State != "complete" || run.Verdict != runtime.EvaluationPass || !run.FullPass || run.ModelRoute != (FilePreviewRoute{Provider: "fake", Model: "fake"}) || run.AdHoc == nil || run.AdHoc.AnswerExcerpt != "Hello, Grace" || run.Retention.Status != "not_requested" {
		t.Fatalf("package-free run = %+v, %v", run, err)
	}

	regressed := strings.Replace(source, "Reply with exactly `Hello, `", "Reply with exactly `Hi, `", 1)
	if regressed == source {
		t.Fatal("instruction edit did not apply")
	}
	if err := os.WriteFile(filename, []byte(regressed), 0o600); err != nil {
		t.Fatal(err)
	}
	edited, err := agentfile.Load(filename)
	if err != nil || edited.Revision == initial.Revision {
		t.Fatalf("edited source revision = %v, %v", edited, err)
	}
	failed, previewErr := (&App{FileRunner: runner}).PreviewAgentFile(t.Context(), FilePreviewOptions{
		File: filename, Operation: "test", RetentionFile: filename, Progress: progress,
	})
	var status *FilePreviewError
	if !errors.As(previewErr, &status) || status.ExitCode() != 2 || failed.State != "complete" || failed.Verdict != runtime.EvaluationFail || failed.File.Revision != edited.Revision || failed.Retention.Status != "retained" || len(failed.Retention.Diagnostics) != 1 || failed.Retention.Diagnostics[0].CaseID != "greeting" {
		t.Fatalf("retained failing test = %+v, %v", failed, previewErr)
	}
	if failed.ModelRoute != (FilePreviewRoute{Provider: "fake", Model: "fake"}) || len(failed.Examples) != 1 || failed.Examples[0].ID != "greeting" || len(failed.Examples[0].Assertions) != 1 || failed.Examples[0].Assertions[0].ID != "greeting-text" || failed.Examples[0].Assertions[0].Type != "contains" || failed.Examples[0].Assertions[0].Verdict != runtime.EvaluationFail {
		t.Fatalf("failed Test attribution = %+v", failed)
	}
	if sends.Load() != 3 {
		t.Fatalf("model sends before explain = %d, want 3", sends.Load())
	}

	scanState := func(label, root string) map[string][]byte {
		t.Helper()
		content := map[string][]byte{}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			content[filepath.ToSlash(relative)] = raw
			return nil
		}); err != nil {
			t.Fatalf("scan %s: %v", label, err)
		}
		if len(content) == 0 {
			t.Fatalf("scan %s found no retained files", label)
		}
		for path, raw := range content {
			for _, forbidden := range []string{credential, credentialEnv} {
				if bytes.Contains(raw, []byte(forbidden)) {
					t.Fatalf("%s file %s retained ambient credential material", label, path)
				}
			}
		}
		return content
	}
	containsState := func(content map[string][]byte, value string) bool {
		for _, raw := range content {
			if bytes.Contains(raw, []byte(value)) {
				return true
			}
		}
		return false
	}
	equalState := func(left, right map[string][]byte) bool {
		if len(left) != len(right) {
			return false
		}
		for path, want := range left {
			if got, ok := right[path]; !ok || !bytes.Equal(got, want) {
				return false
			}
		}
		return true
	}
	journalRoot := filepath.Join(home, "local-preview", "journals")
	failureRoot := filepath.Join(home, "eval-failures")
	journalBefore := scanState("journal", journalRoot)
	failureBefore := scanState("failure diagnostics", failureRoot)
	if !containsState(journalBefore, "Hello, Grace") || !containsState(journalBefore, "Hi, Ada") {
		t.Fatal("journal scan did not cover both executions")
	}
	diagnosticID := failed.Retention.Diagnostics[0].ID
	if !containsState(failureBefore, diagnosticID) || !containsState(failureBefore, edited.Revision) {
		t.Fatal("failure scan did not cover the retained diagnostic")
	}
	journalsBefore, err := filepath.Glob(filepath.Join(journalRoot, "*.db"))
	if err != nil || len(journalsBefore) != 2 {
		t.Fatalf("journals before explain = %v, %v", journalsBefore, err)
	}

	var out bytes.Buffer
	explain := &App{Out: &out}
	if err := explain.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: filename, ID: diagnosticID}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		diagnosticID,
		"revision " + edited.Revision,
		`"greeting"`,
		`agent="greeter"`,
		"fake/fake",
		"judge fake/fake",
		"assertion greeting-text (contains) fail",
		"current",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("offline explain missing %q: %s", want, out.String())
		}
	}
	if err := os.WriteFile(filename, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := explain.ExplainSuiteEvaluation(t.Context(), SuiteEvaluationExplainOptions{File: filename, ID: diagnosticID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "historical") || !strings.Contains(out.String(), "revision "+edited.Revision) {
		t.Fatalf("offline historical explain = %s", out.String())
	}
	journalsAfter, err := filepath.Glob(filepath.Join(journalRoot, "*.db"))
	if err != nil || strings.Join(journalsAfter, "\n") != strings.Join(journalsBefore, "\n") {
		t.Fatalf("offline explain changed journal set: %v, %v", journalsAfter, err)
	}
	if journalAfter := scanState("journal after explain", journalRoot); !equalState(journalAfter, journalBefore) {
		t.Fatal("offline explain changed retained journal files")
	}
	if failureAfter := scanState("failure diagnostics after explain", failureRoot); !equalState(failureAfter, failureBefore) {
		t.Fatal("offline explain changed retained failure files")
	}
}

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
