package localpreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

const canaryKey = "kmx-preview-canary-7f3c9b2e4d1a"

func greeterBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "agentfile", "testdata", "greeter.agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func parseAgent(t *testing.T, raw []byte) *agentfile.Agent {
	t.Helper()
	agent, err := agentfile.Parse("greeter.agent.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func greeter(t *testing.T, edits ...string) *agentfile.Agent {
	t.Helper()
	raw := string(greeterBytes(t))
	for i := 0; i < len(edits); i += 2 {
		if !strings.Contains(raw, edits[i]) {
			t.Fatalf("fixture lacks %q", edits[i])
		}
		raw = strings.Replace(raw, edits[i], edits[i+1], 1)
	}
	return parseAgent(t, []byte(raw))
}

func journalDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "local-preview")
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestFakeInstructionRegressionFailsThenFixes(t *testing.T) {
	dir := journalDir(t)
	ctx := testContext(t)
	for _, step := range []struct {
		name  string
		agent *agentfile.Agent
		want  agentruntime.EvaluationVerdict
	}{
		{"starter", greeter(t), agentruntime.EvaluationPass},
		{"regression", greeter(t, "Reply with exactly `Hello, `", "Reply with exactly `Hi, `"), agentruntime.EvaluationFail},
		{"fixed", greeter(t, "Reply with exactly `Hello, `", "Reply with exactly `Hello, ` "), agentruntime.EvaluationPass},
	} {
		result, err := Test(ctx, step.agent, Options{JournalDir: dir})
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if result.Verdict != step.want || result.Label != Label || result.ModelRoute != "fake" || !result.Journaled || result.RunID == "" || result.ModelSends != 1 {
			t.Fatalf("%s: result %+v", step.name, result)
		}
		row := result.Examples[0]
		if row.Execution != "completed" || row.Assertions[0].Verdict != step.want || row.SessionUID == "" {
			t.Fatalf("%s: example %+v", step.name, row)
		}
		if step.want == agentruntime.EvaluationFail && (row.Answer != "Hi, Ada" || row.Assertions[0].Reason != "assertion not satisfied") {
			t.Fatalf("regression answer/reason = %q/%q", row.Answer, row.Assertions[0].Reason)
		}
	}
}

func TestFakeCannotPassEveryCheck(t *testing.T) {
	ctx := testContext(t)
	for name, agent := range map[string]*agentfile.Agent{
		"unproduced":   greeter(t, "value: Hello, Ada", "value: Goodbye, Ada"),
		"no directive": greeter(t, "Reply with exactly `Hello, ` followed by the input name.", "Greet the person warmly."),
	} {
		result, err := Test(ctx, agent, Options{JournalDir: journalDir(t)})
		if err != nil || result.Verdict != agentruntime.EvaluationFail {
			t.Fatalf("%s: %v %+v", name, err, result)
		}
	}
	if answer, tool := FakeAnswer("Greet warmly.", "Ada"); answer != FakeNoDirectiveAnswer || tool {
		t.Fatalf("no directive answer = %q", answer)
	}
}

func TestRunAdHocInputAndExecutorSeam(t *testing.T) {
	ctx := testContext(t)
	result, err := Run(ctx, greeter(t), "Grace", Options{JournalDir: journalDir(t)})
	if err != nil || result.Examples[0].Answer != "Hello, Grace" || result.Verdict != agentruntime.EvaluationUnknown || !result.Journaled {
		t.Fatalf("run = %v %+v", err, result)
	}
	if _, err := Run(ctx, greeter(t), " ", Options{JournalDir: journalDir(t)}); err == nil {
		t.Fatal("blank input accepted")
	}
	seam := &recordingExecutor{}
	result, err = Test(ctx, greeter(t), Options{Executor: seam})
	if err != nil || result.Journaled || result.RunID != "" || result.Verdict != agentruntime.EvaluationPass || seam.turns != 1 || !seam.closed {
		t.Fatalf("seam = %v %+v %+v", err, result, seam)
	}
}

type recordingExecutor struct {
	turns  int
	closed bool
}

func (r *recordingExecutor) Execute(ctx context.Context, turn Turn) (Execution, error) {
	r.turns++
	response, err := turn.Call(ctx, modelRequest(turn))
	if err != nil {
		return Execution{}, err
	}
	return Execution{Answer: response.Message.Text()}, nil
}

func (r *recordingExecutor) Close() error { r.closed = true; return nil }

func TestInspectReplaysWithoutModelCall(t *testing.T) {
	dir := journalDir(t)
	ctx := testContext(t)
	result, err := Test(ctx, greeter(t), Options{JournalDir: dir})
	if err != nil || result.Verdict != agentruntime.EvaluationPass {
		t.Fatal(err, result)
	}
	for range 2 {
		inspection, err := Inspect(ctx, dir, result.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.ModelCalls != 0 || len(inspection.Sessions) != 1 || !strings.Contains(inspection.Label, "not a test result") {
			t.Fatalf("inspection = %+v", inspection)
		}
		s := inspection.Sessions[0]
		if s.Outcome != "completed" || s.Answer != "Hello, Ada" || s.Input != "Ada" || s.Model != FakeModel || s.ModelCalls != 0 ||
			s.Agent != "greeter" || s.Example != "greeting" || s.Revision != result.Revision || s.SessionUID != result.Examples[0].SessionUID {
			t.Fatalf("session = %+v", s)
		}
	}
	if _, err := Inspect(ctx, dir, "20990101T000000.000000000Z-000000000000"); err == nil {
		t.Fatal("missing run inspected")
	}
	if _, err := Inspect(ctx, dir, "../escape"); err == nil {
		t.Fatal("invalid run id accepted")
	}
	if runs, _ := ListRuns(dir); len(runs) != 1 {
		t.Fatalf("inspection created journals: %v", runs)
	}
}

// compatibleServer is a hermetic OpenAI-compatible endpoint.
type compatibleServer struct {
	*httptest.Server
	hits    atomic.Int64
	handler func(w http.ResponseWriter, r *http.Request, body chatRequest)
}

func newCompatible(t *testing.T, tls bool, handler func(http.ResponseWriter, *http.Request, chatRequest)) *compatibleServer {
	t.Helper()
	s := &compatibleServer{handler: handler}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		var body chatRequest
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body: %v", err)
		}
		s.handler(w, r, body)
	})
	if tls {
		s.Server = httptest.NewTLSServer(h)
	} else {
		s.Server = httptest.NewServer(h)
	}
	t.Cleanup(s.Close)
	return s
}

func reply(content string) string {
	encoded, _ := json.Marshal(content)
	return `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":` + string(encoded) + `}}]}`
}

func modelAgent(t *testing.T, model string) *agentfile.Agent {
	return greeter(t, "- **Preferred:** fake", "- **Preferred:** "+model)
}

func TestCompatibleEndpointExactRequestAuthAndOneSend(t *testing.T) {
	t.Setenv("KMX_TEST_PREVIEW_KEY", canaryKey)
	agent := modelAgent(t, "example/model-1")
	server := newCompatible(t, true, func(w http.ResponseWriter, r *http.Request, body chatRequest) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+canaryKey || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request %s %s auth=%t", r.Method, r.URL.Path, r.Header.Get("Authorization") != "")
		}
		if body.Model != "example/model-1" || body.Stream || len(body.Messages) != 2 || body.Messages[0] != (chatMessage{"system", agent.Instructions}) || body.Messages[1] != (chatMessage{"user", "Ada"}) {
			t.Errorf("body = %+v", body)
		}
		fmt.Fprint(w, reply("Hello, Ada"))
	})
	result, err := Test(testContext(t), agent, Options{JournalDir: journalDir(t), Endpoint: &Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_PREVIEW_KEY"}, Transport: server.Client().Transport})
	if err != nil || result.Verdict != agentruntime.EvaluationPass || result.ModelRoute != "compatible" || result.Model != "example/model-1" || server.hits.Load() != 1 || result.ModelSends != 1 {
		t.Fatalf("result = %v %+v hits=%d", err, result, server.hits.Load())
	}
}

func TestCredentialAndPromptAbsentFromJournalAndErrors(t *testing.T) {
	t.Setenv("KMX_TEST_PREVIEW_KEY", canaryKey)
	dir := journalDir(t)
	agent := greeter(t, "- **Preferred:** fake", "- **Preferred:** example/model-1", "examples:\n", "examples:\n  - id: second\n    input: Lin\n    assertions:\n      - id: second-text\n        type: contains\n        value: Hello, Lin\n")
	server := newCompatible(t, true, func(w http.ResponseWriter, r *http.Request, body chatRequest) {
		if body.Messages[1].Content == "Lin" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"bad key %s for prompt %s"}}`, r.Header.Get("Authorization"), body.Messages[1].Content)
			return
		}
		fmt.Fprint(w, reply("Hello, Ada"))
	})
	result, err := Test(testContext(t), agent, Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_PREVIEW_KEY"}, Transport: server.Client().Transport})
	if err != nil || result.Verdict != agentruntime.EvaluationUnknown || result.Examples[0].Execution != "refused" || result.Examples[0].Refusal.Kind != RefusedHTTPStatus || result.Examples[1].Verdict != agentruntime.EvaluationPass {
		t.Fatalf("result = %v %+v", err, result)
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte(canaryKey)) || strings.Contains(result.Examples[0].Reason, "Lin") || !strings.Contains(result.Examples[0].Reason, "HTTP 401") {
		t.Fatalf("result leaks content: %s", encoded)
	}
	inspection, err := Inspect(testContext(t), dir, result.RunID)
	if err != nil || len(inspection.Sessions) != 2 || inspection.Sessions[0].Outcome != "incomplete" || inspection.Sessions[1].Outcome != "completed" {
		t.Fatalf("inspection = %v %+v", err, inspection)
	}
	// Retention is disclosed: the journal does hold the answer content.
	if journal := scanJournal(t, dir); !bytes.Contains(journal, []byte("Hello, Ada")) {
		t.Fatal("journal bytes were not scanned")
	}
}

func TestCompatibleRepliesRefusedWithoutExecution(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		kind   string
	}{
		"http error":   {http.StatusInternalServerError, `{"error":{"message":"SECRET-BODY"}}`, RefusedHTTPStatus},
		"not found":    {http.StatusNotFound, `SECRET-BODY`, RefusedHTTPStatus},
		"malformed":    {http.StatusOK, `{"choices":[SECRET-BODY`, RefusedMalformed},
		"no choices":   {http.StatusOK, `{"choices":[]}`, RefusedMalformed},
		"two choices":  {http.StatusOK, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"a"}},{"finish_reason":"stop","message":{"role":"assistant","content":"b"}}]}`, RefusedMalformed},
		"oversized":    {http.StatusOK, reply(strings.Repeat("x", MaxReplyBytes)), RefusedOversized},
		"truncated":    {http.StatusOK, `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"Hello"}}]}`, RefusedTruncated},
		"tool call":    {http.StatusOK, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"rm","arguments":"{}"}}]}}]}`, RefusedToolCall},
		"hidden tool":  {http.StatusOK, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada","Tool_Calls":[{"id":"c"}]}}]}`, RefusedToolCall},
		"legacy fn":    {http.StatusOK, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada","function_call":{"name":"rm"}}}]}`, RefusedToolCall},
		"model refuse": {http.StatusOK, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":null,"refusal":"SECRET-BODY"}}]}`, RefusedMalformed},
		"redirect":     {http.StatusTemporaryRedirect, ``, RefusedHTTPStatus},
		// encoding/json keeps the last duplicate; a duplicate key at any depth,
		// including a case-insensitive alias, is malformed, never text.
		"shadowed tool call":  {http.StatusOK, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada","tool_calls":[{"id":"c"}],"tool_calls":null}}]}`, RefusedMalformed},
		"shadowed truncation": {http.StatusOK, `{"choices":[{"finish_reason":"length","finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada"}}]}`, RefusedMalformed},
		"aliased finish":      {http.StatusOK, `{"choices":[{"finish_reason":"length","Finish_Reason":"stop","message":{"role":"assistant","content":"Hello, Ada"}}]}`, RefusedMalformed},
		"nested duplicate":    {http.StatusOK, `{"usage":{"total_tokens":1,"TOTAL_TOKENS":2},"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada"}}]}`, RefusedMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			dir := journalDir(t)
			server := newCompatible(t, false, func(w http.ResponseWriter, r *http.Request, _ chatRequest) {
				if r.Header.Get("Authorization") != "" {
					t.Error("keyless route sent an Authorization header")
				}
				if tc.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			result, err := Test(testContext(t), modelAgent(t, "m"), Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: server.URL}})
			row := result.Examples[0]
			if err != nil || result.Verdict != agentruntime.EvaluationUnknown || row.Execution != "refused" || row.Refusal == nil || row.Refusal.Kind != tc.kind || server.hits.Load() != 1 {
				t.Fatalf("result = %v %+v hits=%d", err, result, server.hits.Load())
			}
			if strings.Contains(row.Reason, "SECRET-BODY") || row.Answer != "" {
				t.Fatalf("reason leaks body: %q", row.Reason)
			}
			inspection, err := Inspect(testContext(t), dir, result.RunID)
			// START, INPUT, MODEL_CALL, ERROR: no tool call or result was recorded.
			if err != nil || inspection.Sessions[0].Outcome != "incomplete" || inspection.Sessions[0].Records != 4 || inspection.ModelCalls != 0 {
				t.Fatalf("inspection = %v %+v", err, inspection)
			}
		})
	}
}

func TestFakeToolCallReplyIsRefused(t *testing.T) {
	agent := greeter(t, "Reply with exactly `Hello, ` followed by the input name.", "Call the tool `search` first.")
	result, err := Test(testContext(t), agent, Options{JournalDir: journalDir(t)})
	if err != nil || result.Examples[0].Refusal == nil || result.Examples[0].Refusal.Kind != RefusedToolCall || result.Verdict != agentruntime.EvaluationUnknown {
		t.Fatalf("result = %v %+v", err, result)
	}
}

func TestCancellationAndDeadlineNeverPass(t *testing.T) {
	started := make(chan struct{}, 1)
	server := newCompatible(t, false, func(w http.ResponseWriter, r *http.Request, _ chatRequest) {
		started <- struct{}{}
		<-r.Context().Done()
	})
	dir := journalDir(t)
	ctx, cancel := context.WithCancel(testContext(t))
	go func() { <-started; cancel() }()
	result, err := Test(ctx, modelAgent(t, "m"), Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: server.URL}})
	if !errors.Is(err, context.Canceled) || result.Verdict != agentruntime.EvaluationUnknown || result.Examples[0].Execution != "canceled" || server.hits.Load() != 1 {
		t.Fatalf("canceled = %v %+v hits=%d", err, result, server.hits.Load())
	}
	inspection, err := Inspect(testContext(t), dir, result.RunID)
	if err != nil || inspection.Sessions[0].Outcome != "incomplete" {
		t.Fatalf("inspection = %v %+v", err, inspection)
	}
	result, err = Test(testContext(t), modelAgent(t, "m"), Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: server.URL}, ExampleTimeout: 1500 * time.Millisecond})
	if err != nil || result.Verdict != agentruntime.EvaluationUnknown || result.Examples[0].Execution != "canceled" || server.hits.Load() != 2 {
		t.Fatalf("deadline = %v %+v", err, result)
	}
}

func TestRefusalsBeforeAnyEffect(t *testing.T) {
	t.Setenv("KMX_TEST_EMPTY_KEY", "")
	t.Setenv("KMX_TEST_PREVIEW_KEY", canaryKey)
	server := newCompatible(t, false, func(w http.ResponseWriter, _ *http.Request, _ chatRequest) { fmt.Fprint(w, reply("Hello, Ada")) })
	toolAgent := greeter(t)
	toolAgent.Examples[0].Assertions = append(toolAgent.Examples[0].Assertions, agentruntime.EvaluationAssertion{ID: "tool", Type: "toolCalled", Tool: "search"})
	judgeAgent := greeter(t)
	min := 50
	judgeAgent.Examples[0].Assertions = []agentruntime.EvaluationAssertion{{ID: "j", Type: "llmJudge", Rubric: "`Hello`", MinScore: &min, JudgeModel: "judge", JudgeVersion: "1"}}
	for name, tc := range map[string]struct {
		agent   *agentfile.Agent
		options Options
		kind    string
	}{
		"missing model config": {modelAgent(t, "llama3.2"), Options{}, RefusedModelUnconfigured},
		"fake with endpoint":   {greeter(t), Options{Endpoint: &Endpoint{BaseURL: server.URL}}, RefusedModelUnconfigured},
		"plaintext remote":     {modelAgent(t, "m"), Options{Endpoint: &Endpoint{BaseURL: "http://example.invalid/v1"}}, RefusedEndpoint},
		"credential in url":    {modelAgent(t, "m"), Options{Endpoint: &Endpoint{BaseURL: "https://user:pw@example.invalid"}}, RefusedEndpoint},
		"key over http":        {modelAgent(t, "m"), Options{Endpoint: &Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_PREVIEW_KEY"}}, RefusedCredential},
		"empty key":            {modelAgent(t, "m"), Options{Endpoint: &Endpoint{BaseURL: "https://example.invalid", KeyEnv: "KMX_TEST_EMPTY_KEY"}}, RefusedCredential},
		"key value as name":    {modelAgent(t, "m"), Options{Endpoint: &Endpoint{BaseURL: "https://example.invalid", KeyEnv: "sk-not a name"}}, RefusedCredential},
		"tool assertion":       {toolAgent, Options{}, RefusedTools},
		"judge without wiring": {judgeAgent, Options{}, RefusedJudge},
		"unused judge":         {greeter(t), Options{JudgeEndpoint: &Endpoint{BaseURL: server.URL}}, RefusedJudge},
	} {
		t.Run(name, func(t *testing.T) {
			dir := journalDir(t)
			tc.options.JournalDir = dir
			_, err := Test(testContext(t), tc.agent, tc.options)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Kind != tc.kind || refusal.Remedy == "" {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), canaryKey) || strings.Contains(err.Error(), "llama3.2") {
				t.Fatalf("refusal quotes input: %v", err)
			}
			if _, statErr := os.Lstat(dir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("journal directory created before refusal: %v", statErr)
			}
		})
	}
	if server.hits.Load() != 0 {
		t.Fatalf("refusals sent %d requests", server.hits.Load())
	}
}

func TestJournalRetentionAndClear(t *testing.T) {
	dir := journalDir(t)
	ctx := testContext(t)
	for range MaxJournals + 2 {
		if _, err := Run(ctx, greeter(t), "Ada", Options{JournalDir: dir}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := ListRuns(dir)
	if err != nil || len(runs) != MaxJournals {
		t.Fatalf("runs = %d %v", len(runs), err)
	}
	if goruntime.GOOS != "windows" {
		info, _ := os.Stat(dir)
		file, _ := os.Stat(filepath.Join(dir, runs[0]+".db"))
		if info.Mode().Perm() != 0o700 || file.Mode().Perm()&0o077 != 0 {
			t.Fatalf("modes = %v %v", info.Mode(), file.Mode())
		}
	}
	unrelated := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir, runs[0]); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir, "../notes"); err == nil {
		t.Fatal("invalid run id cleared")
	}
	if after, _ := ListRuns(dir); len(after) != MaxJournals-1 {
		t.Fatalf("after single clear: %d", len(after))
	}
	if err := Clear(dir, ""); err != nil {
		t.Fatal(err)
	}
	if after, _ := ListRuns(dir); len(after) != 0 {
		t.Fatalf("after clear: %v", after)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("clear removed an unrelated file")
	}
}

func TestJournalPathWithURISpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a dir #1 %20 ?x", "journals")
	ctx := testContext(t)
	result, err := Test(ctx, greeter(t), Options{JournalDir: dir})
	if err != nil || result.Verdict != agentruntime.EvaluationPass {
		t.Fatal(err, result)
	}
	if info, err := os.Stat(filepath.Join(dir, result.RunID+".db")); err != nil || info.Size() == 0 {
		t.Fatalf("journal not at the selected path: %v", err)
	}
	if inspection, err := Inspect(ctx, dir, result.RunID); err != nil || len(inspection.Sessions) != 1 {
		t.Fatal(err, inspection)
	}
	if got := journalDSN(`C:\Users\A B\kmx\r.db`); goruntime.GOOS == "windows" && got != "file:///C:/Users/A%20B/kmx/r.db?mode=rw&_txlock=immediate" {
		t.Fatalf("windows dsn = %q", got)
	}
}

func TestPermissiveJournalDirectoryRefused(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("mode bits are not the Windows access boundary")
	}
	dir := journalDir(t)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Test(testContext(t), greeter(t), Options{JournalDir: dir})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Kind != RefusedJournal {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("journal written into permissive directory")
	}
	if _, err := Test(testContext(t), greeter(t), Options{JournalDir: "relative"}); err == nil {
		t.Fatal("relative journal directory accepted")
	}
}

// scanJournal returns every byte retained in dir, failing if any file holds
// the credential or its variable name.
func scanJournal(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatal("no journal files", err)
	}
	var journal []byte
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(canaryKey)) || bytes.Contains(raw, []byte("KMX_TEST_PREVIEW_KEY")) {
			t.Fatalf("credential in journal file %s", entry.Name())
		}
		journal = append(journal, raw...)
	}
	return journal
}

func TestEchoedCredentialRefusedWithoutJournaling(t *testing.T) {
	t.Setenv("KMX_TEST_PREVIEW_KEY", canaryKey)
	dir := journalDir(t)
	agent := greeter(t, "- **Preferred:** fake", "- **Preferred:** example/model-1", "examples:\n", "examples:\n  - id: escaped\n    input: Lin\n    assertions:\n      - id: escaped-text\n        type: contains\n        value: Hello\n")
	server := newCompatible(t, true, func(w http.ResponseWriter, r *http.Request, body chatRequest) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if body.Messages[1].Content == "Lin" {
			// A JSON escape must not hide the credential from the check.
			fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Lin. Your key is \u%04x%s"}}]}`, key[0], key[1:])
			return
		}
		fmt.Fprint(w, reply("Hello, Ada. You sent "+r.Header.Get("Authorization")))
	})
	result, err := Test(testContext(t), agent, Options{JournalDir: dir, Endpoint: &Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_PREVIEW_KEY"}, Transport: server.Client().Transport})
	if err != nil || result.Verdict != agentruntime.EvaluationUnknown || len(result.Examples) != 2 || server.hits.Load() != 2 {
		t.Fatalf("result = %v %+v", err, result)
	}
	for _, row := range result.Examples {
		if row.Execution != "refused" || row.Refusal == nil || row.Refusal.Kind != RefusedCredentialEcho || row.Answer != "" || row.Verdict == agentruntime.EvaluationPass {
			t.Fatalf("row = %+v", row)
		}
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte(canaryKey)) {
		t.Fatalf("result leaks the credential: %s", encoded)
	}
	inspection, err := Inspect(testContext(t), dir, result.RunID)
	if err != nil || len(inspection.Sessions) != 2 {
		t.Fatalf("inspection = %v %+v", err, inspection)
	}
	for _, s := range inspection.Sessions {
		if s.Outcome != "incomplete" || s.Answer != "" {
			t.Fatalf("session = %+v", s)
		}
	}
	encoded, _ = json.Marshal(inspection)
	if bytes.Contains(encoded, []byte(canaryKey)) {
		t.Fatalf("inspection leaks the credential: %s", encoded)
	}
	// The journal is scanned, not empty: it records the inputs.
	if journal := scanJournal(t, dir); !bytes.Contains(journal, []byte("Lin")) {
		t.Fatal("journal bytes were not scanned")
	}
}

func TestPruningKeepsExecutingRunsAndOrdersByCreation(t *testing.T) {
	dir := journalDir(t)
	ctx := testContext(t)
	active, activeID, err := openJournalExecutor(dir, FakeModel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = active.Close() })
	var created []string
	for range MaxJournals + 2 {
		result, err := Run(ctx, greeter(t), "Ada", Options{JournalDir: dir})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, result.RunID)
	}
	runs, err := ListRuns(dir)
	if err != nil || len(runs) != MaxJournals || runs[0] != activeID {
		t.Fatalf("runs = %v %v", runs, err)
	}
	// Oldest finished runs were pruned in creation order.
	if want := append([]string{activeID}, created[len(created)-(MaxJournals-1):]...); !slices.Equal(runs, want) {
		t.Fatalf("runs = %v, want %v", runs, want)
	}
	if _, err := Inspect(ctx, dir, activeID); err != nil {
		t.Fatalf("executing run not inspectable: %v", err)
	}
	var refusal *Refusal
	if err := Clear(dir, activeID); !errors.As(err, &refusal) || refusal.Kind != RefusedJournal {
		t.Fatalf("executing run cleared: %v", err)
	}
	if err := Clear(dir, ""); !errors.As(err, &refusal) {
		t.Fatalf("clear all ignored an executing run: %v", err)
	}
	if runs, _ := ListRuns(dir); !slices.Equal(runs, []string{activeID}) {
		t.Fatalf("after clear all: %v", runs)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir, activeID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left after clear: %v", entries)
	}
}

func TestStaleRunLockIsCleared(t *testing.T) {
	dir := journalDir(t)
	result, err := Run(testContext(t), greeter(t), "Ada", Options{JournalDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// An exited process leaves its lock file but holds no lock.
	stale := filepath.Join(dir, result.RunID+lockSuffix)
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir, result.RunID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left after clear: %v", entries)
	}
}

func TestClearAllRemovesOrphanRunLocks(t *testing.T) {
	dir := journalDir(t)
	if err := privateDir(dir, true); err != nil {
		t.Fatal(err)
	}
	// A process that exited between locking and creating its journal leaves
	// only an unlocked lock file.
	orphan := filepath.Join(dir, "20990101T000000.000000000Z-000000000001"+lockSuffix)
	unrelated := filepath.Join(dir, "notes"+lockSuffix)
	for _, path := range []string{orphan, unrelated} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A run that has locked but not yet created its journal is executing.
	starting := filepath.Join(dir, "20990101T000000.000000000Z-000000000002"+lockSuffix)
	held, err := os.OpenFile(starting, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if !tryLock(held) {
		t.Fatal("lock not taken")
	}
	var refusal *Refusal
	if err := Clear(dir, ""); !errors.As(err, &refusal) || refusal.Kind != RefusedJournal {
		t.Fatalf("clear all ignored a starting run: %v", err)
	}
	if _, err := os.Lstat(orphan); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan lock kept: %v", err)
	}
	for _, path := range []string{unrelated, starting} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s removed: %v", filepath.Base(path), err)
		}
	}
	unlock(held)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Clear(dir, ""); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != filepath.Base(unrelated) {
		t.Fatalf("entries after clear = %v", entries)
	}
}

func TestRunRefusesWhenClearRemovesItsUnheldLock(t *testing.T) {
	dir := journalDir(t)
	t.Cleanup(func() { lockFileOpened = func(string) {} })
	lockFileOpened = func(string) {
		if err := Clear(dir, ""); err != nil {
			t.Errorf("concurrent clear: %v", err)
		}
	}
	_, err := Run(testContext(t), greeter(t), "Ada", Options{JournalDir: dir})
	if !errors.Is(err, errLockCleared) {
		t.Fatalf("run = %v, want %v", err, errLockCleared)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left after refused run: %v", entries)
	}
}

func TestClearKeepsLockOfRunThatLocksAsClearReleases(t *testing.T) {
	dir := journalDir(t)
	opened, proceed := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		lockFileOpened = func(string) {}
		lockReleased = func(string) {}
	})
	lockFileOpened = func(string) {
		close(opened)
		<-proceed
	}
	type started struct {
		runID string
		lock  *runLock
		err   error
	}
	result := make(chan started, 1)
	go func() {
		runID, _, lock, err := newJournal(dir, time.Now())
		result <- started{runID, lock, err}
	}()
	<-opened
	// Clear locks the new run's lock file before the run does. The run takes
	// the lock as soon as Clear releases it, before Clear finishes.
	var run started
	lockReleased = func(string) {
		lockReleased = func(string) {}
		close(proceed)
		run = <-result
	}
	_ = Clear(dir, "")
	if run.err != nil {
		if !errors.Is(run.err, errLockCleared) {
			t.Fatalf("new run = %v, want %v", run.err, errLockCleared)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("files left after refused run: %v", entries)
		}
		return
	}
	// A run that started must keep its lock file, so its journal is never
	// pruned or cleared while it executes.
	defer run.lock.release()
	if _, err := os.Lstat(run.lock.path); err != nil {
		t.Fatalf("executing run lost its lock file: %v", err)
	}
	if err := removeFinishedRun(dir, run.runID); !errors.Is(err, errRunActive) {
		t.Fatalf("removing the executing run = %v, want %v", err, errRunActive)
	}
}

func TestInspectWithoutDeadlineAndNeverCreatesJournal(t *testing.T) {
	dir := journalDir(t)
	result, err := Test(testContext(t), greeter(t), Options{JournalDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), dir, result.RunID)
	if err != nil || len(inspection.Sessions) != 1 || inspection.Sessions[0].Answer != "Hello, Ada" {
		t.Fatalf("inspection = %v %+v", err, inspection)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(canceled, dir, result.RunID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inspection = %v", err)
	}
	// The journal open itself refuses a missing file, so a run removed after
	// Inspect's existence check is not recreated empty.
	missing := filepath.Join(dir, "20990101T000000.000000000Z-000000000000.db")
	if _, err := openHost(missing, "", nil); err == nil {
		t.Fatal("opened a missing journal")
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("journal created: %v", err)
	}
}

// stubJudge satisfies JudgeFactory without any network effect.
func stubJudge(string, string, http.RoundTripper, []agentruntime.EvaluationCase) (agentruntime.EvaluationJudge, error) {
	return func(context.Context, agentruntime.EvaluationCase, agentruntime.EvaluationEvidence) []agentruntime.EvaluationAssertionResult {
		return nil
	}, nil
}

func TestJournalBoundCredentialRefusedBeforeJournal(t *testing.T) {
	// Opaque values that match no known credential shape, so only the
	// resolved-value check can stop them.
	const judgeKey = "kmx-judge-canary-0c4e8a1f5b9d"
	t.Setenv("KMX_TEST_PREVIEW_KEY", canaryKey)
	t.Setenv("KMX_TEST_JUDGE_KEY", judgeKey)
	server := newCompatible(t, true, func(w http.ResponseWriter, _ *http.Request, _ chatRequest) { fmt.Fprint(w, reply("Hello, Ada")) })
	agentRoute := &Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_PREVIEW_KEY"}
	keylessRoute := &Endpoint{BaseURL: server.URL}
	judgeRoute := &Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_JUDGE_KEY"}
	instructions := func(key string) *agentfile.Agent {
		return greeter(t, "- **Preferred:** fake", "- **Preferred:** m", "followed by the input name.", "followed by the input name. Token "+key+".")
	}
	exampleInput := func(key string) *agentfile.Agent {
		return greeter(t, "- **Preferred:** fake", "- **Preferred:** m", "input: Ada", "input: Ada "+key)
	}
	withJudge := func(agent *agentfile.Agent) *agentfile.Agent {
		minScore := 50
		agent.Examples[0].Assertions = append(agent.Examples[0].Assertions, agentruntime.EvaluationAssertion{ID: "j", Type: "llmJudge", Rubric: "greets", MinScore: &minScore, JudgeModel: "judge", JudgeVersion: "1"})
		return agent
	}
	for name, tc := range map[string]struct {
		agent  *agentfile.Agent
		input  *string
		agentE *Endpoint
		judgeE *Endpoint
	}{
		"test instructions":           {agent: instructions(canaryKey), agentE: agentRoute},
		"test example input":          {agent: exampleInput(canaryKey), agentE: agentRoute},
		"run instructions":            {agent: instructions(canaryKey), input: new("Ada"), agentE: agentRoute},
		"run input":                   {agent: instructions(""), input: new("Ada " + canaryKey), agentE: agentRoute},
		"judge key in instructions":   {agent: withJudge(instructions(judgeKey)), agentE: keylessRoute, judgeE: judgeRoute},
		"judge key in example input":  {agent: withJudge(exampleInput(judgeKey)), agentE: keylessRoute, judgeE: judgeRoute},
		"agent key with judge routed": {agent: withJudge(exampleInput(canaryKey)), agentE: agentRoute, judgeE: judgeRoute},
	} {
		t.Run(name, func(t *testing.T) {
			dir := journalDir(t)
			options := Options{JournalDir: dir, Endpoint: tc.agentE, JudgeEndpoint: tc.judgeE, Judge: stubJudge, Transport: server.Client().Transport}
			var result Result
			var err error
			if tc.input != nil {
				result, err = Run(testContext(t), tc.agent, *tc.input, options)
			} else {
				result, err = Test(testContext(t), tc.agent, options)
			}
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Kind != RefusedCredentialContent || result.RunID != "" || len(result.Examples) != 0 {
				t.Fatalf("result = %v %+v", err, result)
			}
			if strings.Contains(err.Error(), canaryKey) || strings.Contains(err.Error(), judgeKey) {
				t.Fatalf("refusal quotes the credential: %v", err)
			}
			if _, statErr := os.Lstat(dir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("journal directory created before refusal: %v", statErr)
			}
		})
	}
	if server.hits.Load() != 0 {
		t.Fatalf("refusals sent %d requests", server.hits.Load())
	}
	// The same content runs when no credential is resolved for it.
	result, err := Run(testContext(t), instructions(canaryKey), "Ada", Options{JournalDir: journalDir(t), Endpoint: keylessRoute, Transport: server.Client().Transport})
	if err != nil || result.Examples[0].Answer != "Hello, Ada" {
		t.Fatalf("keyless run = %v %+v", err, result)
	}
}

func TestRunInputSizeBound(t *testing.T) {
	ctx := testContext(t)
	dir := journalDir(t)
	_, err := Run(ctx, greeter(t), strings.Repeat("a", agentfile.MaxInputBytes+1), Options{JournalDir: dir})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Kind != RefusedRequest {
		t.Fatalf("oversized input = %v", err)
	}
	if _, statErr := os.Lstat(dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("journal directory created before refusal: %v", statErr)
	}
	input := strings.Repeat("a", agentfile.MaxInputBytes)
	result, err := Run(ctx, greeter(t), input, Options{JournalDir: dir})
	if err != nil || result.Examples[0].Answer != "Hello, "+input {
		t.Fatalf("bounded input = %v %+v", err, result.Examples)
	}
}
