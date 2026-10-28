package localpreview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
)

func TestPairSourceCredentialsBeforeJournal(t *testing.T) {
	for _, field := range []string{"original", "unselected", "prose"} {
		for _, adhoc := range []bool{false, true} {
			t.Run(field+map[bool]string{true: "-adhoc", false: "-test"}[adhoc], func(t *testing.T) {
				req := runnerRequest(t)
				req.Agent.Model = "unit"
				original := *req.Agent
				original.Examples = agentfile.CloneExamples(req.Agent.Examples)
				req.SourceAgent = &original
				req.Agent.Examples = agentfile.CloneExamples(req.Agent.Examples)
				req.Examples = req.Agent.Examples[:1]
				req.Route = modelaccess.Route{Provider: "copilot", Model: "unit", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
				if adhoc {
					input := "hello"
					req.Input = &input
					req.Examples = nil
					req.SkipAssertions = true
				}
				switch field {
				case "original":
					original.Examples[1].Assertions[0].Value = "provider-canary"
				case "unselected":
					req.Agent.Examples[1].Assertions[0].Value = "provider-canary"
				case "prose":
					req.SourceText = []string{"unselected prose provider-canary"}
				}
				out, err := NewRunner(RunnerOptions{Access: func(context.Context, []modelaccess.Route) (modelaccess.PreviewAccess, error) {
					return &trustedPreviewAccess{}, nil
				}, JournalDir: func() (string, error) { t.Error("source credential reached journal"); return "", errors.New("journal") }}).Run(t.Context(), req)
				if !errors.Is(err, agentpreview.ErrCredentialContent) || len(out.Examples) != 0 {
					t.Fatalf("credential escaped: %+v %v", out, err)
				}
			})
		}
	}
}

func TestPairToolCredentialsBeforeJournal(t *testing.T) {
	for _, field := range []string{"original", "unselected", "prose", "mocks"} {
		t.Run(field, func(t *testing.T) {
			req := runnerRequest(t)
			req.Agent.Tools = []string{localtools.Operation}
			req.ToolBindings = toolBindingFile(t)
			original := *req.Agent
			original.Examples = agentfile.CloneExamples(req.Agent.Examples)
			req.SourceAgent = &original
			req.Agent.Examples = agentfile.CloneExamples(req.Agent.Examples)
			req.Examples = nil
			input := "hello"
			req.Input = &input
			req.SkipAssertions = true
			switch field {
			case "original":
				original.Instructions = "tool-secret-canary"
			case "unselected":
				req.Agent.Examples[1].Assertions[0].Value = "tool-secret-canary"
			case "prose":
				req.SourceText = []string{"source prose tool-secret-canary"}
			case "mocks":
				req.Agent.Examples[1].Mocks = &agentfile.Mocks{Tools: []agentfile.MockTool{{Name: localtools.Operation, Mode: "recorded", Calls: []agentfile.MockCall{{Result: map[string]any{"credential": "tool-secret-canary"}}}}}}
			}
			out, err := NewRunner(RunnerOptions{Tools: func(_ context.Context, b localtools.Binding) (*localtools.Host, error) {
				return localtools.NewHost(b, "tool-secret-canary", func(context.Context) ([]localtools.PullRequest, error) {
					t.Fatal("preflight executed tool")
					return nil, nil
				})
			}, JournalDir: func() (string, error) { t.Fatal("tool credential reached journal"); return "", nil }}).Run(t.Context(), req)
			if !errors.Is(err, agentpreview.ErrUnsafeToolContent) || len(out.Examples) != 0 {
				t.Fatalf("tool credential escaped: %+v %v", out, err)
			}
		})
	}
}

func TestPairSourceValidationBeforeJournal(t *testing.T) {
	req := runnerRequest(t)
	req.SnapshotRevision = "snapshot"
	called := false
	req.ValidateSource = func(context.Context) error { called = true; return errors.New("private path") }
	out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { t.Fatal("changed source reached journal"); return "", nil }}).Run(t.Context(), req)
	if !called || !errors.Is(err, agentpreview.ErrSourceChanged) || errors.Is(err, agentpreview.ErrDiagnosticsFailed) || out.Journal != "" {
		t.Fatalf("source error: %+v %v", out, err)
	}
}

func TestPairSourceValidationAtEveryTurn(t *testing.T) {
	for _, window := range []string{"journal", "between-turns", "model-send"} {
		t.Run(window, func(t *testing.T) {
			req := runnerRequest(t)
			req.SnapshotRevision = "snapshot"
			changed := false
			req.ValidateSource = func(context.Context) error {
				if changed {
					return errors.New("private replaced source")
				}
				return nil
			}
			e := &runnerExecutor{}
			e.execute = func(ctx context.Context, turn Turn) (Execution, error) {
				changed = true
				if window == "model-send" {
					_, err := turn.Call(ctx, api.ModelRequest{})
					return Execution{}, err
				}
				return Execution{Answer: "Hello, " + turn.Input, SessionUID: "first", Journaled: true}, nil
			}
			r := NewRunner(RunnerOptions{JournalDir: func() (string, error) {
				if window == "journal" {
					changed = true
				}
				return "/unused", nil
			}}).(*runner)
			r.open = func(string, string) (Executor, string, error) { return e, "private-run", nil }
			out, err := r.Run(t.Context(), req)
			want := 1
			if window == "journal" {
				want = 0
			}
			if !errors.Is(err, agentpreview.ErrSourceChanged) || out.State == agentpreview.Complete || len(e.turns) != want || out.Journal != "" {
				t.Fatalf("source guard missed %s: %v turns%d state%s", window, err, len(e.turns), out.State)
			}
		})
	}
}

func TestPairCompletionCarriesSnapshot(t *testing.T) {
	req := runnerRequest(t)
	req.SnapshotRevision = "snapshot"
	out, err := injectedRunner(&runnerExecutor{}, time.Second).Run(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, ex := range out.Examples {
		if ex.Completion.SnapshotRevision != req.SnapshotRevision || ex.Completion.FileRevision != req.Agent.Revision {
			t.Fatal("pair attribution lost")
		}
	}
}
