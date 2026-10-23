package localpreview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"testing"

	"github.com/agent-primitives/sessions/api"
)

type judgeErrorTransport struct{}

func (judgeErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("PRIVATE RAW TRANSPORT BODY")
}

func TestJournaledJudgeNeverRecordsRawTransportErrors(t *testing.T) {
	dir := journalDir(t)
	e, id, err := openJournalExecutor(dir, "fake")
	if err != nil {
		t.Fatal(err)
	}
	var executor Executor = e
	var failure error
	a := greeter(t)
	transport := &runnerJudgeTransport{next: judgeErrorTransport{}, request: agentpreview.Request{Agent: a}, executor: &executor, failure: &failure, check: func(...string) error { return nil }, validate: func(ctx context.Context) error { return ctx.Err() }}
	req, _ := http.NewRequestWithContext(testContext(t), "POST", "http://127.0.0.1/chat/completions", io.NopCloser(strings.NewReader(`{"model":"fake","stream":false,"messages":[{"role":"system","content":"policy"},{"role":"user","content":"input"}]}`)))
	_, err = transport.Sample(req, "fake", "")
	if err == nil {
		t.Fatal("transport failure accepted")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	path, _ := JournalPath(dir, id)
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(data)+err.Error(), "PRIVATE RAW") {
		t.Fatal("raw transport error escaped to journal/error")
	}
}

func TestJournalExecutorMixedModelsUseExactHarnessAndReplay(t *testing.T) {
	dir := journalDir(t)
	executor, id, err := openJournalExecutor(dir, "agent-model")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	agent := greeter(t)
	for _, model := range []string{"agent-model", "judge-model", "judge-model"} {
		_, err := executor.Execute(testContext(t), Turn{AgentID: agent.Identity.ID, Revision: agent.Revision, ExampleID: "example", Instructions: "system", Input: "input", Model: model, Call: func(_ context.Context, req api.ModelRequest) (api.ModelResponse, error) {
			calls++
			if req.Model != model {
				t.Errorf("harness substituted %q for %q", req.Model, model)
			}
			return api.ModelResponse{Message: *api.TextMessage("assistant", "answer")}, nil
		}})
		if err != nil {
			t.Errorf("model %s: %v", model, err)
		}
	}
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(testContext(t), dir, id)
	if err != nil || len(inspection.Sessions) != 3 {
		t.Fatalf("replay: %+v / %v", inspection, err)
	}
	if inspection.ModelCalls != 0 {
		t.Fatal("replay made a model call")
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}
