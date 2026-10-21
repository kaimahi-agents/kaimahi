package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type cliPreviewFake struct {
	calls   int
	request agentpreview.Request
	verdict runtime.EvaluationVerdict
	cause   error
}

var _ agentpreview.Runner = (*cliPreviewFake)(nil)

func (f *cliPreviewFake) Run(_ context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	f.calls++
	f.request = r
	complete := func(id string) agentpreview.Completion {
		return agentpreview.Completion{State: agentpreview.Complete, ID: id, FileRevision: r.Agent.Revision, Provider: r.Route.Provider, Model: r.Route.Model, Answer: "PRIVATE ANSWER"}
	}
	o := agentpreview.Outcome{State: agentpreview.Complete, Journal: "private-handle"}
	if r.Input != nil {
		c := complete("ad-hoc")
		o.AdHoc = &c
	}
	for _, e := range r.Examples {
		row := agentpreview.ExampleOutcome{ID: e.ID, Completion: complete(e.ID)}
		for _, a := range e.Assertions {
			row.Assertions = append(row.Assertions, runtime.EvaluationAssertionResult{ID: a.ID, Type: a.Type, DefinitionDigest: runtime.EvaluationAssertionDigest(a), Verdict: f.verdict, Reason: "PRIVATE REASON"})
		}
		o.Examples = append(o.Examples, row)
	}
	return o, f.cause
}
func cliPreviewFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent file.agent.md")
	s := "# Greeter\n## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n## Accountable Responsibilities\nGreet the input.\n## Non-Responsibilities\nNo tools.\n## Collaboration and Review Authority\nNone.\n## Model\n- **Preferred:** fake\n## Examples\n```yaml\nexamples:\n  - id: greeting\n    input: PRIVATE INPUT\n    assertions:\n      - id: text\n        type: contains\n        value: PRIVATE OPERAND\n```\n"
	if err := os.WriteFile(path, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestFileCommandsAgainstRunnerInterface(t *testing.T) {
	for _, op := range []string{"run", "test"} {
		t.Run(op, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			f := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = f
			args := []string{op, cliPreviewFile(t), "--case", "greeting", "-o", "json"}
			if op == "run" {
				args = append(args, "--prompt", "ad-hoc")
			}
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 || f.calls != 1 {
				t.Fatalf("config %d / sends %d", *loads, f.calls)
			}
			var r app.FilePreviewResult
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if r.FullPass || r.Selection.Scope != "subset" || r.Verdict != runtime.EvaluationPass || r.Operation != op {
				t.Fatalf("%+v", r)
			}
			if (r.AdHoc != nil) != (op == "run") {
				t.Fatal("ad-hoc attribution wrong")
			}
			if strings.Contains(out.String(), "PRIVATE") {
				t.Fatalf("privacy: %s", out.String())
			}
		})
	}
}
func TestFileCommandExitCodes(t *testing.T) {
	for _, tc := range []struct {
		verdict runtime.EvaluationVerdict
		cause   error
		exit    int
		code    string
	}{
		{runtime.EvaluationPass, nil, 0, "checks_passed"},
		{runtime.EvaluationFail, nil, 2, "assertion_failed"},
		{runtime.EvaluationUnknown, nil, 2, "judge_unknown"},
		{runtime.EvaluationPass, agentpreview.ErrJournalFailed, 1, "journal_failed"},
		{runtime.EvaluationPass, context.Canceled, 130, "cancelled"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			deps.fileRunner = &cliPreviewFake{verdict: tc.verdict, cause: tc.cause}
			err := execute([]string{"test", cliPreviewFile(t), "-o", "json"}, deps)
			actual := 0
			if err != nil {
				actual = exitCode(err)
			}
			if actual != tc.exit || !strings.Contains(out.String(), `"reasonCode":"`+tc.code+`"`) {
				t.Fatalf("exit %d / %v / %s", actual, err, out.String())
			}
		})
	}
}
func TestFileCommandsRefuseMixedModesBeforeConfig(t *testing.T) {
	for _, args := range [][]string{
		{"run", "FILE", "--agent", "deployed", "--prompt", "x"},
		{"run", "FILE", "--namespace", "orka-system"},
		{"run", "FILE", "--wait", "5m"},
		{"run", "FILE", "--prompt", "x", "--prompt-file", "-"},
		{"test", "FILE", "--workspace", "."},
		{"test", "FILE", "--out", "receipt.json"},
		{"test", "FILE", "--inference", "provider:model"},
		{"test", "FILE", "--case-timeout", "5m"},
		{"test", "FILE", "--context", "kind-test"},
		{"test", "FILE", "--container-engine", "docker"},
		{"test", "FILE", "--case", ""},
		{"test", "FILE", "-o", "yaml"},
		{"eval", "FILE"},
		{"test", "--case", "greeting"},
		{"run", "--agent", "live", "--prompt", "x", "--case", "greeting"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := cliPreviewFile(t)
			for i, arg := range args {
				if arg == "FILE" {
					args[i] = path
				}
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			f := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = f
			if err := execute(args, deps); err == nil {
				t.Fatal("mixed modes accepted")
			}
			if *loads != 0 || f.calls != 0 || out.Len() != 0 {
				t.Fatalf("refusal crossed argument boundary: config=%d calls=%d stdout=%q", *loads, f.calls, out.String())
			}
		})
	}
}
func TestFileCommandNoProductionFake(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	err := execute([]string{"test", cliPreviewFile(t), "-o", "json"}, deps)
	if err == nil || *loads != 0 || !strings.Contains(out.String(), "runner_unavailable") {
		t.Fatalf("%v / %s", err, out.String())
	}
}
func TestFilePreviewOutputWriteFailure(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.stdout = previewBrokenWriter{}
	deps.fileRunner = &cliPreviewFake{verdict: runtime.EvaluationPass}
	if err := execute([]string{"test", cliPreviewFile(t), "-o", "json"}, deps); err == nil || exitCode(err) != 1 {
		t.Fatalf("write failure: %v", err)
	}
}

type previewBrokenWriter struct{}

func (previewBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }
