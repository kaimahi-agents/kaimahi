package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
)

func TestRunAndResultCommandValidation(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "agents/sample", "--agent", "sample", "--prompt", "hello"}, "--agent"},
		{[]string{"run", "agents/sample", "--prompt", "hello", "--prompt-file", "-"}, "--prompt"},
		{[]string{"run", "agents/sample"}, "Invalid agent file"},
		{[]string{"run", "agents/sample", "--prompt", "hello", "--context", "kind-test"}, "usage"},
		{[]string{"run", "--agent", "sample", "--prompt", "hello", "--to-context", "kind-test"}, "--to-context"},
		{[]string{"run", "--agent", "sample", "--prompt", "hello", "--wait", "0s"}, "--wait"},
		{[]string{"cluster", "task", "result", "sample", "--wait", "0s"}, "--wait"},
		{[]string{"cluster", "task", "result"}, "usage"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			deps, loads := testDependencies(&out, &errOut)
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if *loads != 0 {
				t.Fatalf("loaded config %d times before argument validation", *loads)
			}
		})
	}
}

func TestRunAndResultHelpShowTimeoutContract(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"run", "--help"}, []string{"--prompt-file", "--wait", "exit code 2", "automatically"}},
		{[]string{"cluster", "task", "result", "--help"}, []string{"--wait", "exit code 2", "automatically"}},
		{[]string{"eval", "--help"}, []string{"--result-port", "automatically"}},
		{[]string{"lift", "--help"}, []string{"--result-port", "automatically"}},
	} {
		var out, errOut bytes.Buffer
		deps, loads := testDependencies(&out, &errOut)
		if err := execute(tc.args, deps); err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v help missing %q", tc.args, want)
			}
		}
		if *loads != 0 || strings.Contains(out.String(), "19180") {
			t.Errorf("%v help loaded config or advertised a fixed result port: %s", tc.args, out.String())
		}
	}
}

func TestPendingTaskHasDistinctExitCode(t *testing.T) {
	if got := exitCode(fmt.Errorf("waiting: %w", app.ErrTaskPending)); got != 2 {
		t.Fatalf("pending exit code = %d", got)
	}
	if got := exitCode(errors.New("task failed")); got != 1 {
		t.Fatalf("failed exit code = %d", got)
	}
}

func TestTaskResultWaitRequiresDuration(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	cmd, _, err := newRootCommand(&commandState{deps: deps}).Find([]string{"cluster", "task", "result"})
	if err != nil {
		t.Fatal(err)
	}
	flag := cmd.Flags().Lookup("wait")
	if flag == nil || flag.DefValue != "0s" {
		t.Fatalf("result wait default = %v", flag)
	}
	if err := cmd.ParseFlags([]string{"--wait", "5m"}); err != nil {
		t.Fatal(err)
	}
	if got, err := cmd.Flags().GetDuration("wait"); err != nil || got != 5*time.Minute {
		t.Fatalf("result wait = %v, %v", got, err)
	}
}

func TestRunWaitDefaultIsFiveMinutes(t *testing.T) {
	var out, errOut bytes.Buffer
	deps, _ := testDependencies(&out, &errOut)
	cmd, _, err := newRootCommand(&commandState{deps: deps}).Find([]string{"run"})
	if err != nil {
		t.Fatal(err)
	}
	flag := cmd.Flags().Lookup("wait")
	if flag == nil || flag.DefValue != (5*time.Minute).String() {
		t.Fatalf("run wait flag = %v", flag)
	}
}
