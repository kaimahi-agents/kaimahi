package app

import (
	"bytes"
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
	opt.Wait = 30 * time.Second // no --wait: Follow is the activation flag.
	err := f.app.TaskResult(opt)
	if !errors.Is(err, ErrTaskPending) {
		t.Fatalf("err=%v", err)
	}
}
