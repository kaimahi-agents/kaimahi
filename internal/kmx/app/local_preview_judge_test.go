package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

const localPreviewCanary = "kmx-judge-canary-51c0e9a2"

// localPreviewJudgeAgent returns the greeter with one llmJudge example per
// rubric, in order, each judged by judge/model version 1.
func localPreviewJudgeAgent(t *testing.T, model string, rubrics ...string) *agentfile.Agent {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "agentfile", "testdata", "greeter.agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.Replace(text, "- **Preferred:** fake", "- **Preferred:** "+model, 1)
	examples := "examples:\n"
	for i, rubric := range rubrics {
		examples += fmt.Sprintf("  - id: judged-%d\n    input: Ada\n    assertions:\n      - id: judge\n        type: llmJudge\n        rubric: %q\n        minScore: 50\n        judgeModel: judge/model\n        judgeVersion: \"1\"\n", i, rubric)
	}
	start := strings.Index(text, "examples:\n")
	end := strings.LastIndex(text, "```")
	agent, err := agentfile.Parse("greeter.agent.md", []byte(text[:start]+examples+text[end:]))
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func localPreviewJudgeContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func localPreviewVerdicts(result localpreview.Result) []agentruntime.EvaluationVerdict {
	var out []agentruntime.EvaluationVerdict
	for _, example := range result.Examples {
		out = append(out, example.Verdict)
	}
	return out
}

func TestLocalPreviewFakeModelUsesFakeJudge(t *testing.T) {
	agent := localPreviewJudgeAgent(t, "fake", "Greets `Hello, Ada`", "Says `Goodbye`", "Is friendly")
	result, err := localpreview.Test(localPreviewJudgeContext(t), agent, localpreview.Options{JournalDir: filepath.Join(t.TempDir(), "j"), Judge: LocalPreviewJudge})
	if err != nil {
		t.Fatal(err)
	}
	got := localPreviewVerdicts(result)
	want := []agentruntime.EvaluationVerdict{agentruntime.EvaluationPass, agentruntime.EvaluationFail, agentruntime.EvaluationUnknown}
	if fmt.Sprint(got) != fmt.Sprint(want) || result.JudgeRoute != "fake" || result.Verdict != agentruntime.EvaluationFail {
		t.Fatalf("verdicts = %v result=%+v", got, result)
	}
	judged := result.Examples[0].Assertions[0].Judge
	if judged == nil || len(judged.Scores) != 2 || judged.Model != "judge/model" || result.Examples[2].Assertions[0].Reason != "judge response malformed" {
		t.Fatalf("judge evidence = %+v", result.Examples)
	}
}

// judgeEndpoint serves agent completions and scripted judge scores by rubric.
type judgeEndpoint struct {
	mu     sync.Mutex
	agent  int
	judged map[string]int
	auth   []string
}

func (e *judgeEndpoint) handler(t *testing.T, scores map[string][2]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string `json:"model"`
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		e.auth = append(e.auth, r.Header.Get("Authorization"))
		if request.Messages[0].Content != agentruntime.EvaluationJudgePolicy {
			e.agent++
			fmt.Fprint(w, strings.Replace(judgeEnvelope("Hello, Ada"), "fixture/model", request.Model, 1))
			return
		}
		var payload struct{ Rubric string }
		_ = json.Unmarshal([]byte(request.Messages[1].Content), &payload)
		sample := e.judged[payload.Rubric]
		e.judged[payload.Rubric]++
		switch score := scores[payload.Rubric][sample].(type) {
		case int:
			fmt.Fprint(w, strings.Replace(judgeEnvelope(fmt.Sprintf(`{"score":%d,"reasoning":"prompt Ada"}`, score)), "fixture/model", request.Model, 1))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "down %s", localPreviewCanary)
		}
	}
}

func TestLocalPreviewJudgeDefaultsToAgentRoute(t *testing.T) {
	t.Setenv("KMX_TEST_LOCAL_PREVIEW_KEY", localPreviewCanary)
	scores := map[string][2]any{"pass": {90, 80}, "fail": {10, 20}, "split": {90, 10}, "unavailable": {nil, nil}}
	endpoint := &judgeEndpoint{judged: map[string]int{}}
	server := httptest.NewTLSServer(endpoint.handler(t, scores))
	defer server.Close()
	agent := localPreviewJudgeAgent(t, "agent/model", "pass", "fail", "split", "unavailable")
	result, err := localpreview.Test(localPreviewJudgeContext(t), agent, localpreview.Options{
		JournalDir: filepath.Join(t.TempDir(), "j"), Judge: LocalPreviewJudge, Transport: server.Client().Transport,
		Endpoint: &localpreview.Endpoint{BaseURL: server.URL, KeyEnv: "KMX_TEST_LOCAL_PREVIEW_KEY"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []agentruntime.EvaluationVerdict{agentruntime.EvaluationPass, agentruntime.EvaluationFail, agentruntime.EvaluationUnknown, agentruntime.EvaluationUnknown}
	if got := localPreviewVerdicts(result); fmt.Sprint(got) != fmt.Sprint(want) || result.JudgeRoute != "compatible" {
		t.Fatalf("verdicts = %v", got)
	}
	if endpoint.agent != 4 || len(endpoint.auth) != 12 {
		t.Fatalf("agent sends=%d total=%d", endpoint.agent, len(endpoint.auth))
	}
	for _, auth := range endpoint.auth {
		if auth != "Bearer "+localPreviewCanary {
			t.Fatal("judge did not use the agent route credential")
		}
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), localPreviewCanary) || strings.Contains(string(encoded), "prompt Ada") {
		t.Fatalf("result leaks judge content: %s", encoded)
	}
}

func TestLocalPreviewExplicitJudgeOverride(t *testing.T) {
	agentEndpoint := &judgeEndpoint{judged: map[string]int{}}
	agentServer := httptest.NewServer(agentEndpoint.handler(t, nil))
	defer agentServer.Close()
	judgeEndpointServer := &judgeEndpoint{judged: map[string]int{}}
	judgeServer := httptest.NewServer(judgeEndpointServer.handler(t, map[string][2]any{"pass": {70, 70}}))
	defer judgeServer.Close()
	agent := localPreviewJudgeAgent(t, "agent/model", "pass")
	result, err := localpreview.Test(localPreviewJudgeContext(t), agent, localpreview.Options{
		JournalDir: filepath.Join(t.TempDir(), "j"), Judge: LocalPreviewJudge,
		Endpoint:      &localpreview.Endpoint{BaseURL: agentServer.URL},
		JudgeEndpoint: &localpreview.Endpoint{BaseURL: judgeServer.URL},
	})
	if err != nil || result.Verdict != agentruntime.EvaluationPass || len(agentEndpoint.auth) != 1 || len(judgeEndpointServer.auth) != 2 || judgeEndpointServer.agent != 0 {
		t.Fatalf("override = %v %+v agent=%d judge=%d", err, result, len(agentEndpoint.auth), len(judgeEndpointServer.auth))
	}
}

// YAML comments in Examples are discarded by the agent file parser. Comment
// content never reaches a model or judge request, the journal, a result or an
// inspection, and a credential-shaped comment is refused without being quoted.
func TestLocalPreviewNeverForwardsExampleComments(t *testing.T) {
	const canary = "kmx-comment-canary-3b8e0d71"
	for _, secret := range []string{canary, secretshapes.All()[0].Example} {
		examples := fmt.Sprintf(`examples:
  # head %[1]s
  - id: greeting # line %[1]s
    input: Ada
    # assertions %[1]s
    assertions:
      - id: greeting-text
        type: contains
        value: Hello, Ada # value %[1]s
      - id: judge
        type: llmJudge
        rubric: "pass" # rubric %[1]s
        minScore: 50
        judgeModel: judge/model
        judgeVersion: "1"
  # foot %[1]s
`, secret)
		raw, err := os.ReadFile(filepath.Join("..", "agentfile", "testdata", "greeter.agent.md"))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		text = strings.Replace(text, "- **Preferred:** fake", "- **Preferred:** agent/model", 1)
		start, end := strings.Index(text, "examples:\n"), strings.LastIndex(text, "```")
		agent, err := agentfile.Parse("greeter.agent.md", []byte(text[:start]+examples+text[end:]))
		if secret != canary {
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("credential comment refusal = %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var bodies []byte
		endpoint := &judgeEndpoint{judged: map[string]int{}}
		inner := endpoint.handler(t, map[string][2]any{"pass": {90, 80}})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, body...)
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
			inner(w, r)
		}))
		dir := filepath.Join(t.TempDir(), "j")
		ctx := localPreviewJudgeContext(t)
		result, err := localpreview.Test(ctx, agent, localpreview.Options{
			JournalDir: dir, Judge: LocalPreviewJudge, Endpoint: &localpreview.Endpoint{BaseURL: server.URL},
		})
		server.Close()
		if err != nil || result.Verdict != agentruntime.EvaluationPass || endpoint.agent != 1 || len(endpoint.auth) != 3 {
			t.Fatalf("preview = %v %+v sends=%d", err, result, len(endpoint.auth))
		}
		inspection, err := localpreview.Inspect(ctx, dir, result.RunID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal([]any{result, inspection})
		journal := []byte{}
		if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			journal = append(journal, data...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"requests": bodies, "result": encoded, "journal": journal} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("%s carries Examples comment content", name)
			}
		}
		if !bytes.Contains(bodies, []byte("Ada")) || !bytes.Contains(journal, []byte("Hello, Ada")) {
			t.Fatal("requests and journal were not captured")
		}
	}
}
