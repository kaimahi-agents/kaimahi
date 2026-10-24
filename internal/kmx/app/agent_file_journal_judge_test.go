package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestLocalFileJudgeSamplesHaveIndependentSessions(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		var payload directChat
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		answer := "Hello, PRIVATE INPUT"
		if payload.Messages[0].Content == runtime.EvaluationJudgePolicy {
			if payload.MaxCompletionTokens != 1024 || payload.Model != "judge-model" {
				t.Error("judge wire changed")
			}
			answer = `{"score":95,"reasoning":"good"}`
		}
		previewEnvelope(w, payload.Model, answer)
	}))
	defer server.Close()
	opt := directOptions(t, "unit", true)
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	opt.JudgeRoute = &modelaccess.Route{Provider: "compatible", Model: "judge-model", Endpoint: server.URL, Auth: "none"}
	// Keep the authored model exact; the override only chooses the provider route.
	parsed := mustPreviewAgent(t, opt.File)
	for i := range parsed.Examples {
		for j := range parsed.Examples[i].Assertions {
			if parsed.Examples[i].Assertions[j].Type == "llmJudge" {
				parsed.Examples[i].Assertions[j].JudgeModel = "judge-model"
			}
		}
	}
	result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentSnapshot(t.Context(), parsed, opt)
	if err != nil || !result.FullPass || sends != 6 {
		t.Fatalf("result=%+v err=%v sends=%d", result, err, sends)
	}
	dir, err := config.LocalPreviewJournalDir()
	if err != nil {
		t.Fatal(err)
	}
	ids, err := localpreview.ListRuns(dir)
	if err != nil || len(ids) != 1 {
		t.Fatalf("runs=%v %v", ids, err)
	}
	replay, err := localpreview.Inspect(t.Context(), dir, ids[0])
	if err != nil || len(replay.Sessions) != 6 || replay.ModelCalls != 0 {
		t.Fatalf("replay=%+v / %v", replay, err)
	}
	for i, session := range replay.Sessions {
		expected := "judge-model"
		if i%3 == 0 {
			expected = "unit"
		}
		if session.Model != expected {
			t.Errorf("session %d model=%s want=%s", i, session.Model, expected)
		}
	}
	if sends != 6 {
		t.Fatal("replay sent an inference")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.db"))
	if len(paths) != 1 {
		t.Fatal("not one journal")
	}
}

func mustPreviewAgent(t *testing.T, path string) *agentfile.Agent {
	t.Helper()
	a, err := agentfile.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLocalFileMalformedJudgeStopsSecondSampleAndExamples(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		var payload directChat
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		answer := "Hello, PRIVATE INPUT"
		if strings.Contains(payload.Messages[0].Content, "evaluator") {
			answer = `{"score":999,"reasoning":"invalid"}`
		}
		previewEnvelope(w, "unit", answer)
	}))
	defer server.Close()
	opt := directOptions(t, "unit", true)
	opt.Route = &modelaccess.Route{Provider: "compatible", Model: "unit", Endpoint: server.URL, Auth: "none"}
	result, err := (&App{FileRunner: NewLocalFileRunner()}).PreviewAgentFile(t.Context(), opt)
	if err == nil || result.FullPass || sends != 2 {
		t.Fatalf("result=%+v err=%v sends=%d", result, err, sends)
	}
}
