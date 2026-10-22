package localpreview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func runnerRequest(t *testing.T) agentpreview.Request {
	t.Helper()
	a := greeter(t)
	second := a.Examples[0]
	second.ID, second.Input = "second", "Grace"
	a.Examples = append(a.Examples, second)
	return agentpreview.Request{Agent: a, Examples: a.Examples, Route: modelaccess.Route{Provider: "fake", Model: "fake", Auth: "none"}}
}

type runnerExecutor struct {
	turns    []Turn
	execute  func(context.Context, Turn) (Execution, error)
	closeErr error
	onClose  func() error
	closes   int
}

func (e *runnerExecutor) Execute(ctx context.Context, turn Turn) (Execution, error) {
	e.turns = append(e.turns, turn)
	if e.execute != nil {
		return e.execute(ctx, turn)
	}
	return Execution{Answer: "Hello, " + turn.Input, SessionUID: fmt.Sprint(len(e.turns)), Journaled: true}, nil
}
func (e *runnerExecutor) Close() error {
	e.closes++
	if e.onClose != nil {
		return e.onClose()
	}
	return e.closeErr
}
func injectedRunner(e *runnerExecutor, timeout time.Duration) agentpreview.Runner {
	r := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return "/unused", nil }, ExampleTimeout: timeout}).(*runner)
	r.open = func(string, string) (Executor, string, error) { return e, "private-run", nil }
	return r
}

func TestRunnerSelectedOrderAndAdHocOneJournal(t *testing.T) {
	req := runnerRequest(t)
	omitted := req.Examples[0]
	omitted.ID = "omitted"
	req.Agent.Examples = append(req.Agent.Examples, omitted)
	req.Examples = []agentfile.Example{req.Examples[1], req.Examples[0]}
	input := "Ada"
	req.Input = &input
	dir := journalDir(t)
	out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return dir, nil }}).Run(testContext(t), req)
	if err != nil || out.State != agentpreview.Complete || out.NoJournal || out.Journal == "" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if out.AdHoc == nil || out.AdHoc.Answer != "Hello, Ada" || len(out.Examples) != 2 {
		t.Fatalf("missing selected/ad-hoc results: %+v", out)
	}
	replay, err := Inspect(testContext(t), dir, out.Journal)
	if err != nil || len(replay.Sessions) != 3 {
		t.Fatalf("one journal must contain all turns: %+v / %v", replay, err)
	}
	used := map[string]bool{}
	completions := []agentpreview.Completion{*out.AdHoc, out.Examples[0].Completion, out.Examples[1].Completion}
	for i, c := range completions {
		if c.ID == "" || used[c.ID] || c.FileRevision != req.Agent.Revision || c.Provider != "fake" || c.Model != "fake" {
			t.Fatalf("bad attribution: %+v", c)
		}
		used[c.ID] = true
		if replay.Sessions[i].SessionUID != c.ID {
			t.Fatalf("not journal Session UID: %+v / %+v", c, replay.Sessions[i])
		}
	}
	for i, row := range out.Examples {
		if row.ID != req.Examples[i].ID || row.Assertions[0].DefinitionDigest != agentruntime.EvaluationAssertionDigest(req.Examples[i].Assertions[0]) {
			t.Fatalf("lost selected assertion: %+v", row)
		}
	}
}

func TestRunnerAbortKeepsSelectedRowsAndDigests(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail error
		want error
	}{
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded},
		{"cancel", context.Canceled, context.Canceled},
		{"model", errors.New("execution failed"), nil},
		{"grpc deadline", status.Error(codes.DeadlineExceeded, "deadline"), context.DeadlineExceeded},
		{"grpc cancel", status.Error(codes.Canceled, "cancelled"), context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := runnerRequest(t)
			e := &runnerExecutor{execute: func(context.Context, Turn) (Execution, error) { return Execution{}, tc.fail }}
			out, err := injectedRunner(e, time.Second).Run(t.Context(), req)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || len(e.turns) != 1 || e.closes != 1 || out.State == agentpreview.Complete || out.Journal != "" {
				t.Fatalf("abort: %+v / %v / %+v", out, err, e)
			}
			if len(out.Examples) != len(req.Examples) || out.Examples[1].Completion.State != agentpreview.NotRun {
				t.Fatalf("later rows lost: %+v", out)
			}
			for i, row := range out.Examples {
				for j, a := range row.Assertions {
					if a.DefinitionDigest != agentruntime.EvaluationAssertionDigest(req.Examples[i].Assertions[j]) || a.Verdict != agentruntime.EvaluationUnknown {
						t.Fatalf("lost unexecuted assertion: %+v", a)
					}
				}
			}
		})
	}
}

func TestRunnerOversizedAnswerStopsJudgingAndLaterSends(t *testing.T) {
	req := runnerRequest(t)
	req.Agent.Model = "m"
	minScore := 50
	req.Examples[0].Assertions = append(req.Examples[0].Assertions, agentruntime.EvaluationAssertion{ID: "judge", Type: "llmJudge", Rubric: "greets", MinScore: &minScore, JudgeModel: "m", JudgeVersion: "1"})
	server := newCompatible(t, false, func(w http.ResponseWriter, _ *http.Request, _ chatRequest) {
		fmt.Fprint(w, reply(strings.Repeat("a", 256*1024+1)))
	})
	req.Route = modelaccess.Route{Provider: "compatible", Model: "m", Endpoint: server.URL, Auth: "none"}
	judged := 0
	judge := func(string, string, http.RoundTripper, []agentruntime.EvaluationCase) (agentruntime.EvaluationJudge, error) {
		return func(_ context.Context, c agentruntime.EvaluationCase, e agentruntime.EvaluationEvidence) []agentruntime.EvaluationAssertionResult {
			judged++
			return agentruntime.EvaluateAssertions(c, e)
		}, nil
	}
	out, err := NewRunner(RunnerOptions{JournalDir: func() (string, error) { return journalDir(t), nil }, Judge: judge}).Run(testContext(t), req)
	if err == nil || out.State != agentpreview.Failed || out.Journal != "" || server.hits.Load() != 1 || judged != 0 {
		t.Fatalf("oversized completion did not abort before later work: state=%s err=%v sends=%d judged=%d", out.State, err, server.hits.Load(), judged)
	}
	for _, row := range out.Examples {
		if row.Completion.State != agentpreview.NotRun || row.Completion.Answer != "" {
			t.Fatal("oversized answer or later completion escaped")
		}
	}
}

func TestRunnerPerTurnDeadlineStopsLaterTurns(t *testing.T) {
	e := &runnerExecutor{execute: func(ctx context.Context, _ Turn) (Execution, error) { <-ctx.Done(); return Execution{}, ctx.Err() }}
	out, err := injectedRunner(e, time.Millisecond).Run(t.Context(), runnerRequest(t))
	if !errors.Is(err, context.DeadlineExceeded) || len(e.turns) != 1 || out.State == agentpreview.Complete {
		t.Fatalf("deadline not propagated: %+v / %v", out, err)
	}
}

func TestRunnerCloseAndAttributionVetoCompletion(t *testing.T) {
	for _, name := range []string{"close", "empty", "duplicate", "unjournaled"} {
		t.Run(name, func(t *testing.T) {
			e := &runnerExecutor{}
			switch name {
			case "close":
				e.closeErr = errors.New("close failed")
			default:
				e.execute = func(_ context.Context, tr Turn) (Execution, error) {
					id := "same"
					if name == "empty" {
						id = ""
					}
					return Execution{Answer: "Hello, " + tr.Input, SessionUID: id, Journaled: name != "unjournaled"}, nil
				}
			}
			out, err := injectedRunner(e, time.Second).Run(t.Context(), runnerRequest(t))
			if err == nil || out.State == agentpreview.Complete || out.Journal != "" || e.closes != 1 {
				t.Fatalf("bad journal accepted: %+v / %v", out, err)
			}
			if name == "close" && !errors.Is(err, agentpreview.ErrJournalFailed) {
				t.Fatalf("close not classified: %v", err)
			}
		})
	}
}

func TestRunnerProgressFailureStopsSends(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			req := runnerRequest(t)
			sinkErr := errors.New("sink failed")
			reports := 0
			req.Progress = func(string) error {
				reports++
				if reports == failAt {
					return sinkErr
				}
				return nil
			}
			e := &runnerExecutor{}
			out, err := injectedRunner(e, time.Second).Run(t.Context(), req)
			wantTurns := 0
			if failAt == 2 {
				wantTurns = 1
			}
			if !errors.Is(err, agentpreview.ErrDiagnosticsFailed) || !errors.Is(err, sinkErr) || len(e.turns) != wantTurns || out.State == agentpreview.Complete {
				t.Fatalf("sink failure ignored: %+v / %v / %+v", out, err, e)
			}
		})
	}
}

func TestRunnerLocalRoutesKeepExactEndpointAndProvider(t *testing.T) {
	for _, provider := range []string{"compatible", "ollama", "foundry-local"} {
		for _, path := range []string{"", "/v1", "/custom/"} {
			t.Run(provider+path, func(t *testing.T) {
				var gotPath string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotPath = r.URL.Path
					fmt.Fprint(w, `{"model":"unit","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Hello, Ada"}}]}`)
				}))
				defer server.Close()
				req := runnerRequest(t)
				copy := *req.Agent
				copy.Model = "unit"
				req.Agent = &copy
				req.Examples = req.Examples[:1]
				req.Route = modelaccess.Route{Provider: provider, Model: "unit", Endpoint: strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + path, Auth: "none"}
				e := &runnerExecutor{execute: func(ctx context.Context, tr Turn) (Execution, error) {
					reply, err := tr.Call(ctx, modelRequest(tr))
					answer := ""
					if err == nil {
						answer = reply.Message.Parts[0].Text.Text
					}
					return Execution{Answer: answer, SessionUID: "one", Journaled: true}, err
				}}
				out, err := injectedRunner(e, time.Second).Run(t.Context(), req)
				if err != nil || out.Examples[0].Completion.Provider != provider || gotPath != strings.TrimRight(path, "/")+"/chat/completions" {
					t.Fatalf("route changed: %+v / %v path=%q", out, err, gotPath)
				}
			})
		}
	}
}

func TestRunnerCancellationDuringCloseVetoesJournal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e := &runnerExecutor{onClose: func() error { cancel(); return nil }}
	out, err := injectedRunner(e, time.Second).Run(ctx, runnerRequest(t))
	if !errors.Is(err, context.Canceled) || out.State != agentpreview.Cancelled || out.Journal != "" || e.closes != 1 {
		t.Fatalf("finalization ignored cancellation: %+v / %v", out, err)
	}
}

func TestRunnerCancelledBeforeEffectsAndConstructorInert(t *testing.T) {
	resolutions := 0
	r := NewRunner(RunnerOptions{JournalDir: func() (string, error) { resolutions++; return journalDir(t), nil }})
	if resolutions != 0 {
		t.Fatal("constructor resolved journal path")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out, err := r.Run(ctx, runnerRequest(t))
	if !errors.Is(err, context.Canceled) || out.State != agentpreview.Cancelled || resolutions != 0 || len(out.Examples) != 2 {
		t.Fatalf("cancel preflight: %+v / %v resolutions=%d", out, err, resolutions)
	}
}
