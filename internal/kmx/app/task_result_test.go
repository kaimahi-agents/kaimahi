package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func taskResultFixture(t *testing.T) (*evalFixture, TaskResultOptions) {
	t.Helper()
	f := runFixture(t)
	f.answers["private prompt"] = "retrieved answer"
	if err := f.app.RunAgent(runOption(f)); err != nil {
		t.Fatal(err)
	}
	name := ""
	for _, c := range orkaCalls(t, f.dir) {
		if c.Document != nil && c.Document["kind"] == "Task" {
			name = c.Document["metadata"].(map[string]any)["name"].(string)
		}
	}
	if name == "" {
		t.Fatal("no Task")
	}
	f.out.Reset()
	f.app.Err.(*bytes.Buffer).Reset()
	return f, TaskResultOptions{Task: name, Namespace: "orka-system", ResultPort: f.opt.ResultPort}
}

func TestTaskResultReadsTerminalAnswerOnlyOnStdout(t *testing.T) {
	f, opt := taskResultFixture(t)
	if err := f.app.TaskResult(opt); err != nil {
		t.Fatal(err)
	}
	if f.out.String() != "retrieved answer\n" {
		t.Fatalf("stdout=%q", f.out)
	}
	if f.taskCreates(t) != 1 {
		t.Fatal("result created Task")
	}
	if !strings.Contains(f.app.Err.(*bytes.Buffer).String(), "authority") {
		t.Fatal("missing session authority notice")
	}
}

func TestTaskResultNoWaitReportsPendingWithoutOpeningResultSession(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_PHASE", "Running")
	before := len(orkaCalls(t, f.dir))
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) {
		t.Fatalf("err=%v", err)
	}
	if f.out.Len() != 0 || !strings.Contains(f.app.Err.(*bytes.Buffer).String(), "Running") {
		t.Fatalf("out=%q errout=%q", f.out, f.app.Err)
	}
	for _, c := range orkaCalls(t, f.dir)[before:] {
		for _, arg := range c.Args {
			if arg == "token" || arg == "port-forward" {
				t.Fatalf("pending task opened session: %v", c.Args)
			}
		}
	}
}

func TestTaskResultSucceededWithoutResultIsPending(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_RESULT_AVAILABLE", "false")
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) || f.out.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, f.out)
	}
	if !strings.Contains(f.app.Err.(*bytes.Buffer).String(), "Succeeded") {
		t.Fatal("phase missing")
	}
}

func TestTaskResultNoWaitDoesNotPollAnUnavailableHTTPAnswer(t *testing.T) {
	f, opt := taskResultFixture(t)
	f.answers["private prompt"] = ""
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) || f.out.Len() != 0 {
		t.Fatalf("err=%v stdout=%q", err, f.out)
	}
}

func TestTaskResultRejectsNonAITaskAndMissingTask(t *testing.T) {
	f, original := taskResultFixture(t)
	for _, tc := range []struct {
		name, want string
		change     func(*TaskResultOptions)
	}{
		{"missing", "not found", func(opt *TaskResultOptions) { opt.Task = "absent" }},
		{"wrong type", "not an AI Task", func(opt *TaskResultOptions) {
			raw, _ := os.ReadFile(filepath.Join(f.dir, "task-"+opt.Task+".json"))
			_ = os.WriteFile(filepath.Join(f.dir, "task-"+opt.Task+".json"), bytes.Replace(raw, []byte(`"type":"ai"`), []byte(`"type":"tool"`), 1), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := original
			tc.change(&opt)
			err := f.app.TaskResult(opt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
			if f.out.Len() != 0 {
				t.Fatalf("stdout=%q", f.out)
			}
		})
	}
}

func TestTaskResultWaitDeadlineDuringTaskGetIsPending(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_TASK_GET_DELAY", "3s")
	deadline := orkaTestDeadline(t, f.app, "task-result-inspect")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	opt.Wait = 5 * time.Minute
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded || f.out.Len() != 0 {
		t.Fatalf("deadline during Task GET: err=%v stdout=%q", err, f.out)
	}
}

func TestTaskResultNoWaitReadTimeoutIsNotPending(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_TASK_GET_DELAY", "3s")
	deadline := orkaTestDeadline(t, f.app, "task-result-inspect")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	err := f.app.TaskResult(opt)
	if err == nil || errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded || f.out.Len() != 0 {
		t.Fatalf("no-wait Task GET failure: err=%v stdout=%q", err, f.out)
	}
}

func TestTaskResultWaitDeadlineDuringSessionStartupIsPending(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_FORWARD_DELAY", "3s")
	deadline := orkaTestDeadline(t, f.app, "orka-result-forward")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	opt.Wait = 5 * time.Minute
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded || f.out.Len() != 0 {
		t.Fatalf("session startup deadline: err=%v stdout=%q", err, f.out)
	}
}

func TestTaskResultWaitReportsPhaseChangesAndChecksUID(t *testing.T) {
	for _, tc := range []struct {
		name, swap string
		wantAnswer bool
	}{
		{"completion", "", true},
		{"replaced Task", "3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, opt := taskResultFixture(t)
			t.Setenv("KMX_EVAL_PHASE_SEQUENCE", "Running,Running,Succeeded")
			if tc.swap != "" {
				t.Setenv("KMX_EVAL_TASK_SWAP_ON_READ", tc.swap)
			}
			opt.Wait = 10 * time.Second
			err := f.app.TaskResult(opt)
			if tc.wantAnswer {
				if err != nil || f.out.String() != "retrieved answer\n" {
					t.Fatalf("err=%v answer=%q", err, f.out)
				}
				output := f.app.Err.(*bytes.Buffer).String()
				if strings.Count(output, "phase: Running") != 1 || strings.Count(output, "phase: Succeeded") != 1 {
					t.Fatalf("repeated or missing phases: %q", output)
				}
			} else if err == nil || errors.Is(err, ErrTaskPending) || !strings.Contains(err.Error(), "replaced") || f.out.Len() != 0 {
				t.Fatalf("replaced Task: err=%v stdout=%q", err, f.out)
			}
		})
	}
}

func TestTaskResultWaitTimeoutIsPending(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_PHASE", "Running")
	deadline := orkaTestDeadline(t, f.app, "task-result-status")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	opt.Wait = 5 * time.Minute
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded || f.out.Len() != 0 {
		t.Fatalf("timeout: err=%v stdout=%q", err, f.out)
	}
}

func TestTaskResultWaitSucceededWithoutReadableResultTimesOut(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_RESULT_AVAILABLE", "false")
	deadline := orkaTestDeadline(t, f.app, "task-result-status")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	f.app.Run.Context = ctx
	opt.Wait = 5 * time.Minute
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) || deadline().Err() != context.DeadlineExceeded || f.out.Len() != 0 {
		t.Fatalf("unavailable answer: err=%v stdout=%q", err, f.out)
	}
	if !strings.Contains(f.app.Err.(*bytes.Buffer).String(), "phase: Succeeded") {
		t.Fatal("successful phase not reported")
	}
}

func TestTaskResultFailedAndCancelledDoNotReadAnswer(t *testing.T) {
	for _, phase := range []string{"Failed", "Cancelled"} {
		t.Run(phase, func(t *testing.T) {
			f, opt := taskResultFixture(t)
			t.Setenv("KMX_EVAL_PHASE", phase)
			err := f.app.TaskResult(opt)
			if err == nil || errors.Is(err, ErrTaskPending) || !strings.Contains(err.Error(), phase) {
				t.Fatalf("err=%v", err)
			}
			if f.out.Len() != 0 {
				t.Fatalf("stdout=%q", f.out)
			}
		})
	}
}

func TestTaskResultNoWaitUsesOnlyOnePhaseRead(t *testing.T) {
	f, opt := taskResultFixture(t)
	t.Setenv("KMX_EVAL_PHASE", "Running")
	// No wait duration requests a single phase observation.
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) {
		t.Fatalf("err=%v", err)
	}
}
