package app

import (
	"context"
	"encoding/json"
	"fmt"
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
