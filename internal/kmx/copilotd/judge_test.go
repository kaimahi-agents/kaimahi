package copilotd

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

// fakeJudge records what the runner hands to a separate judge.
type fakeJudge struct {
	check    func(...string) error
	validate func(context.Context) error
	evaluate func(context.Context, runtime.EvaluationCase, runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error)
	calls    atomic.Int64
	ctx      context.Context
	evidence runtime.EvaluationEvidence
}

func (j *fakeJudge) CheckContent(fields ...string) error {
	if j.check != nil {
		return j.check(fields...)
	}
	return nil
}
func (j *fakeJudge) Validate(ctx context.Context) error {
	if j.validate != nil {
		return j.validate(ctx)
	}
	return nil
}
func (j *fakeJudge) Evaluate(ctx context.Context, c runtime.EvaluationCase, ev runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error) {
	j.calls.Add(1)
	j.ctx, j.evidence = ctx, ev
	if j.evaluate != nil {
		return j.evaluate(ctx, c, ev)
	}
	rows := runtime.EvaluateAssertions(c, ev)
	for i := range rows {
		if rows[i].Type == "llmJudge" {
			rows[i].Verdict = runtime.EvaluationPass
		}
	}
	return rows, nil
}

func judgeAssertion() runtime.EvaluationAssertion {
	min := 70
	return runtime.EvaluationAssertion{ID: "quality", Type: "llmJudge", Rubric: "judge-secret rubric", MinScore: &min, JudgeModel: "judge"}
}
func judgeRequest() agentpreview.Request {
	req := wireRequest()
	for i := range req.Examples {
		req.Examples[i].Assertions = []runtime.EvaluationAssertion{judgeAssertion()}
	}
	return req
}
func withJudge(j *fakeJudge, prepared *atomic.Int64, err error) func(*Options) {
	return func(o *Options) {
		o.Judge = func(context.Context, agentpreview.Request, func(...string) error) (JudgeAccess, error) {
			prepared.Add(1)
			if j == nil {
				return nil, err
			}
			return j, err
		}
	}
}

func TestRunnerJudgeScoresEachCaseUnderItsCaseContext(t *testing.T) {
	judge, prepared := &fakeJudge{}, new(atomic.Int64)
	r, _, _ := wireRunnerWith(t, false, false, withJudge(judge, prepared, nil))
	out, err := r.Run(t.Context(), judgeRequest())
	if err != nil || out.State != agentpreview.Complete || prepared.Load() != 1 || judge.calls.Load() != 2 {
		t.Fatalf("err=%v out=%+v prepared=%d calls=%d", err, out, prepared.Load(), judge.calls.Load())
	}
	for _, ex := range out.Examples {
		if len(ex.Assertions) != 1 || ex.Assertions[0].Verdict != runtime.EvaluationPass || ex.Completion.Answer != "marker" {
			t.Fatalf("%+v", ex)
		}
	}
	// The scorer shares the turn's deadline-bearing context, which is released after the case.
	if _, ok := judge.ctx.Deadline(); !ok || judge.ctx.Err() == nil || judge.evidence.Answer != "marker" || !judge.evidence.AnswerAvailable || judge.evidence.ToolCalls["bash"] != 7 {
		t.Fatalf("ctx=%v evidence=%+v", judge.ctx.Err(), judge.evidence)
	}
}

func TestRunnerJudgeCredentialsInObservedMetadataAndToolsAreWithheld(t *testing.T) {
	for _, tc := range []struct {
		name, secret string
		fixture      *wireFixture
	}{
		{"metadata escaped by JSON", "opaque&private", &wireFixture{HostVersion: "opaque&private"}},
		{"host-only tool name", "judge_bearer_only", &wireFixture{ToolName: "judge_bearer_only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := &fakeJudge{check: func(fields ...string) error {
				for _, field := range fields {
					if strings.Contains(field, tc.secret) {
						return agentpreview.ErrCredentialContent
					}
				}
				return nil
			}}
			r, _, _ := wireRunnerWith(t, false, false, withJudge(j, new(atomic.Int64), nil), tc.fixture)
			out, err := r.Run(t.Context(), judgeRequest())
			if !errors.Is(err, agentpreview.ErrCredentialContent) || len(out.Examples) != 1 || out.Examples[0].Completion.Answer != "" || len(out.Examples[0].Assertions) != 0 {
				t.Fatalf("err=%v out=%+v", err, out)
			}
		})
	}
}

func TestRunnerWithoutSelectedJudgeNeverPreparesOne(t *testing.T) {
	prepared := new(atomic.Int64)
	r, _, _ := wireRunnerWith(t, false, false, withJudge(&fakeJudge{}, prepared, nil))
	if _, err := r.Run(t.Context(), wireRequest()); err != nil || prepared.Load() != 0 {
		t.Fatalf("err=%v prepared=%d", err, prepared.Load())
	}
}

func TestRunnerJudgeSelectedWithoutCapabilityIsUnsupportedBeforeAuth(t *testing.T) {
	r, created, _ := wireRunner(t, false, false)
	if _, err := r.Run(t.Context(), judgeRequest()); !errors.Is(err, ErrUnsupported) || created.Load() != 0 {
		t.Fatalf("err=%v created=%d", err, created.Load())
	}
}

func TestRunnerJudgePreparationFailureStopsBeforeBeforeExecuteAndSends(t *testing.T) {
	unusable := errors.New("judge unusable")
	fixture := &wireFixture{}
	r, created, _ := wireRunnerWith(t, false, false, withJudge(nil, new(atomic.Int64), unusable), fixture)
	req := judgeRequest()
	before := false
	req.BeforeExecute = func() error { before = true; return nil }
	_, err := r.Run(t.Context(), req)
	if !errors.Is(err, unusable) || before || created.Load() != 0 || fixture.Sends.Load() != 0 {
		t.Fatalf("err=%v before=%v created=%d sends=%d", err, before, created.Load(), fixture.Sends.Load())
	}
}

func TestRunnerJudgeCheckerCoversSourcesAssertionsAndSnapshots(t *testing.T) {
	leak := func(fields ...string) error {
		for _, f := range fields {
			if strings.Contains(f, "judge-secret") {
				return copilot.ErrCredentialContent
			}
		}
		return nil
	}
	// A partial capability from a failed preparation must still withhold identities.
	for name, setup := range map[string]func(*agentpreview.Request){
		"rubric": func(*agentpreview.Request) {},
		"unselected": func(r *agentpreview.Request) {
			r.SourceAgent = &agentfile.Agent{Examples: []agentfile.Example{{ID: "other", Input: "judge-secret"}}}
		},
		"agent unselected": func(r *agentpreview.Request) {
			r.Agent.Examples = []agentfile.Example{{ID: "other", Assertions: []runtime.EvaluationAssertion{{ID: "x", Type: "llmJudge", Rubric: "judge-secret"}}}}
		},
		"source text": func(r *agentpreview.Request) {
			r.SourceText = []string{"judge-secret"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := &wireFixture{}
			judge := &fakeJudge{check: leak}
			r, _, _ := wireRunnerWith(t, false, false, withJudge(judge, new(atomic.Int64), errors.New("setup")), fixture)
			req := judgeRequest()
			if name != "rubric" {
				for i := range req.Examples {
					req.Examples[i].Assertions[0].Rubric = "ok" // only the case under test may leak
				}
			}
			setup(&req)
			if _, err := r.Run(t.Context(), req); !errors.Is(err, copilot.ErrCredentialContent) || fixture.Sends.Load() != 0 {
				t.Fatalf("err=%v sends=%d", err, fixture.Sends.Load())
			}
		})
	}
	t.Run("snapshot and answer", func(t *testing.T) {
		judge := &fakeJudge{check: leak}
		var snapshotCheck func(...string) error
		r, _, _ := wireRunnerWith(t, false, false, func(o *Options) {
			withJudge(judge, new(atomic.Int64), nil)(o)
			inner := o.Snapshot
			o.Snapshot = func(ctx context.Context, check func(...string) error) (agentpreview.SourceSnapshot, error) {
				snapshotCheck = check
				return inner(ctx, check)
			}
		})
		req := judgeRequest()
		req.Examples[0].Assertions[0].Rubric = "ok"
		req.Examples[1].Assertions[0].Rubric = "ok"
		if _, err := r.Run(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		if snapshotCheck("judge-secret") == nil {
			t.Fatal("snapshot was not checked with the judge credential")
		}
	})
	t.Run("returned answer", func(t *testing.T) {
		judge := &fakeJudge{check: func(f ...string) error {
			for _, v := range f {
				if v == "marker" {
					return copilot.ErrCredentialContent
				}
			}
			return nil
		}}
		r, _, _ := wireRunnerWith(t, false, false, withJudge(judge, new(atomic.Int64), nil))
		out, err := r.Run(t.Context(), judgeRequest())
		if !errors.Is(err, copilot.ErrCredentialContent) || judge.calls.Load() != 0 || out.Examples[0].Completion.Answer != "" {
			t.Fatalf("err=%v calls=%d out=%+v", err, judge.calls.Load(), out)
		}
	})
}

// Every failure after judging starts withholds the answer and assertions and stops later sends.
func TestRunnerJudgeFailuresWithholdAndStopLaterSends(t *testing.T) {
	unavailable := errors.New("judge unavailable")
	for name, tc := range map[string]struct {
		configure func(*fakeJudge, *agentpreview.Request, context.CancelFunc)
		want      error
	}{
		"execution failure": {func(j *fakeJudge, _ *agentpreview.Request, _ context.CancelFunc) {
			j.evaluate = func(context.Context, runtime.EvaluationCase, runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error) {
				return nil, unavailable
			}
		}, unavailable},
		"context ends while judging": {func(j *fakeJudge, _ *agentpreview.Request, cancel context.CancelFunc) {
			j.evaluate = func(ctx context.Context, c runtime.EvaluationCase, ev runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error) {
				cancel()
				return runtime.EvaluateAssertions(c, ev), nil
			}
		}, context.Canceled},
		"source changed": {func(j *fakeJudge, req *agentpreview.Request, _ context.CancelFunc) {
			req.ValidateSource = func(context.Context) error {
				if j.calls.Load() > 0 {
					// The CLI pair-currentness callback returns this sentinel.
					return agentfile.ErrRecordingChanged
				}
				return nil
			}
		}, agentpreview.ErrSourceChanged},
		"provider invalidated": {func(j *fakeJudge, _ *agentpreview.Request, _ context.CancelFunc) {
			j.validate = func(context.Context) error {
				if j.calls.Load() > 0 {
					return copilot.ErrAuthRevoked
				}
				return nil
			}
		}, copilot.ErrAuthRevoked},
		"wrong row identity": {func(j *fakeJudge, _ *agentpreview.Request, _ context.CancelFunc) {
			j.evaluate = func(_ context.Context, c runtime.EvaluationCase, ev runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error) {
				rows := runtime.EvaluateAssertions(c, ev)
				rows[0].DefinitionDigest = "sha256:" + strings.Repeat("0", 64)
				return rows, nil
			}
		}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			judge, fixture := &fakeJudge{}, &wireFixture{}
			r, _, closed := wireRunnerWith(t, false, false, withJudge(judge, new(atomic.Int64), nil), fixture)
			req := judgeRequest()
			tc.configure(judge, &req, cancel)
			out, err := r.Run(ctx, req)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || len(out.Examples) != 1 || fixture.Sends.Load() != 1 || closed.Load() != 1 {
				t.Fatalf("err=%v examples=%d sends=%d closed=%d", err, len(out.Examples), fixture.Sends.Load(), closed.Load())
			}
			c := out.Examples[0]
			if c.Completion.State == agentpreview.Complete || c.Completion.Answer != "" || len(c.Assertions) != 0 || c.Completion.Native == nil || out.State == agentpreview.Complete {
				t.Fatalf("stale content retained: %+v", c)
			}
		})
	}
}

func TestRunnerJudgeSharesCaseDeadlineWithTurn(t *testing.T) {
	judge := &fakeJudge{}
	judge.evaluate = func(ctx context.Context, c runtime.EvaluationCase, ev runtime.EvaluationEvidence) ([]runtime.EvaluationAssertionResult, error) {
		<-ctx.Done()
		// A provider can report another error as cancellation interrupts its read.
		return nil, errors.New("judge response read interrupted")
	}
	fixture := &wireFixture{}
	r, _, _ := wireRunnerWith(t, false, false, func(o *Options) {
		withJudge(judge, new(atomic.Int64), nil)(o)
		o.ExampleTimeout = 250 * time.Millisecond
	}, fixture)
	out, err := r.Run(t.Context(), judgeRequest())
	if !errors.Is(err, context.DeadlineExceeded) || fixture.Sends.Load() != 1 || len(out.Examples) != 1 || len(out.Examples[0].Assertions) != 0 {
		t.Fatalf("err=%v sends=%d out=%+v", err, fixture.Sends.Load(), out)
	}
}
