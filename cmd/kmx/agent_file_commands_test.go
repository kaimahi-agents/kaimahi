package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type cliPreviewFake struct {
	calls    int
	request  agentpreview.Request
	verdict  runtime.EvaluationVerdict
	cause    error
	deadline time.Time
}

var _ agentpreview.Runner = (*cliPreviewFake)(nil)

func (f *cliPreviewFake) Run(ctx context.Context, r agentpreview.Request) (agentpreview.Outcome, error) {
	f.deadline, _ = ctx.Deadline()
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "agent file.agent.md")
	s := "# Greeter\n## Identity\n- **ID:** greeter\n- **Purpose:** Greet.\n## Accountable Responsibilities\nGreet the input.\n## Non-Responsibilities\nNo tools.\n## Collaboration and Review Authority\nNone.\n## Model\n- **Preferred:** fake\n## Examples\n```yaml\nexamples:\n  - id: greeting\n    input: PRIVATE INPUT\n    assertions:\n      - id: text\n        type: contains\n        value: PRIVATE OPERAND\n```\n"
	if err := os.WriteFile(path, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateCommandHome(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "state")
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
func TestFileCommandsCopilotRoute(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		for _, endpoint := range []string{"", modelaccess.CopilotEndpoint, "https://private.invalid"} {
			t.Run(operation+"/"+endpoint, func(t *testing.T) {
				path := cliPreviewFile(t)
				source, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, bytes.ReplaceAll(source, []byte("**Preferred:** fake"), []byte("**Preferred:** gpt-test")), 0600); err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
				deps.fileRunner = runner
				args := []string{operation, path, "--provider", "copilot", "-o", "json"}
				if endpoint != "" {
					args = append(args, "--endpoint", endpoint)
				}
				err = execute(args, deps)
				if endpoint == "https://private.invalid" {
					if err == nil || runner.calls != 0 || strings.Contains(out.String()+diag.String(), endpoint) {
						t.Fatalf("unsafe endpoint: %v / %s", err, out.String())
					}
					return
				}
				want := modelaccess.Route{Provider: "copilot", Model: "gpt-test", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
				if err != nil || runner.calls != 1 || runner.request.Route != want || *loads != 0 {
					t.Fatalf("route=%+v err=%v calls=%d config=%d", runner.request.Route, err, runner.calls, *loads)
				}
				var result app.FilePreviewResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.FullPass {
					t.Fatalf("invalid JSON result: %s / %v", out.String(), err)
				}
			})
		}
	}
}

func TestFileCommandsCopilotFailures(t *testing.T) {
	for _, operation := range []string{"run", "test"} {
		for _, output := range []string{"text", "json"} {
			t.Run(operation+"/"+output, func(t *testing.T) {
				path := cliPreviewFile(t)
				source, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, bytes.ReplaceAll(source, []byte("**Preferred:** fake"), []byte("**Preferred:** gpt-test")), 0600); err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, _ := testDependencies(&out, &diag)
				deps.fileRunner = &cliPreviewFake{cause: errors.Join(copilot.ErrAuthRequired, errors.New("PRIVATE AUTH CAUSE"))}
				err = execute([]string{operation, path, "--provider", "copilot", "-o", output}, deps)
				if err == nil {
					t.Fatal("authentication failure succeeded")
				}
				if text := out.String() + diag.String() + err.Error(); !strings.Contains(text, "copilot_auth_required") || strings.Contains(text, "PRIVATE") {
					t.Fatalf("unsafe or unclassified error: %s", text)
				}
				if output == "json" {
					var result app.FilePreviewResult
					if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.ReasonCode != "copilot_auth_required" || result.FullPass {
						t.Fatalf("invalid failure JSON: %s / %v", out.String(), err)
					}
				}
			})
		}
	}
}

func TestFileCommandExitCodes(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
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
		{runtime.EvaluationPass, context.DeadlineExceeded, 1, "deadline_exceeded"},
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

func TestFileCommandGitHubSummaryPreservesExitAndReport(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	for _, tc := range []struct {
		name    string
		verdict runtime.EvaluationVerdict
		cause   error
		exit    int
		state   string
		code    string
	}{
		{"pass", runtime.EvaluationPass, nil, 0, "complete", "checks_passed"},
		{"fail", runtime.EvaluationFail, nil, 2, "complete", "assertion_failed"},
		{"unknown", runtime.EvaluationUnknown, nil, 2, "complete", "judge_unknown"},
		{"journal", runtime.EvaluationPass, agentpreview.ErrJournalFailed, 1, "error", "journal_failed"},
		{"cancelled", runtime.EvaluationPass, context.Canceled, 130, "cancelled", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary := filepath.Join(t.TempDir(), "step-summary.md")
			if err := os.WriteFile(summary, []byte("# Existing\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GITHUB_STEP_SUMMARY", summary)
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.fileRunner = &cliPreviewFake{verdict: tc.verdict, cause: tc.cause}
			err := execute([]string{"test", cliPreviewFile(t), "-o", "json", "--github-summary"}, deps)
			actual := 0
			if err != nil {
				actual = exitCode(err)
			}
			var result app.FilePreviewResult
			if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			markdown, readErr := os.ReadFile(summary)
			if readErr != nil || actual != tc.exit || *loads != 0 || result.State != tc.state || result.ReasonCode != tc.code || !bytes.HasPrefix(markdown, []byte("# Existing\n")) {
				t.Fatalf("exit=%d result=%+v config=%d summary=%q read=%v err=%v", actual, result, *loads, markdown, readErr, err)
			}
			for _, want := range []string{"## kmx test:", result.File.Revision, "fake/fake", "### Tests", "### Checks", "Local preview report only"} {
				if !strings.Contains(string(markdown), want) {
					t.Fatalf("summary missing %q: %s", want, markdown)
				}
			}
			for _, private := range []string{"PRIVATE", result.File.Path, "eval-failures", "local-preview"} {
				if strings.Contains(string(markdown), private) {
					t.Fatalf("summary leaked %q: %s", private, markdown)
				}
			}
			if !strings.Contains(out.String(), `"durationMs":`) {
				t.Fatalf("JSON report has no additive timing: %s", out.String())
			}
		})
	}
}

func TestFileCommandGitHubSummaryRefusesBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
	}{
		{"missing environment", []string{"test", "FILE", "-o", "json", "--github-summary"}, ""},
		{"relative environment", []string{"test", "FILE", "-o", "json", "--github-summary"}, "summary.md"},
		{"requires json", []string{"test", "FILE", "--github-summary"}, filepath.Join(t.TempDir(), "unused.md")},
		{"requires file", []string{"test", "--github-summary", "-o", "json"}, filepath.Join(t.TempDir(), "unused.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := cliPreviewFile(t)
			args := append([]string(nil), tc.args...)
			for i := range args {
				if args[i] == "FILE" {
					args[i] = file
				}
			}
			t.Setenv("GITHUB_STEP_SUMMARY", tc.env)
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = runner
			if err := execute(args, deps); err == nil || runner.calls != 0 || *loads != 0 || out.Len() != 0 {
				t.Fatalf("refusal crossed execution boundary: err=%v calls=%d config=%d stdout=%q", err, runner.calls, *loads, out.String())
			}
		})
	}
}

func TestFileCommandGitHubSummaryIsNotWrittenAfterJSONOutputFailure(t *testing.T) {
	summary := filepath.Join(t.TempDir(), "step-summary.md")
	if err := os.WriteFile(summary, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	var diag bytes.Buffer
	deps, _ := testDependencies(&bytes.Buffer{}, &diag)
	deps.stdout = previewBrokenWriter{}
	deps.fileRunner = &cliPreviewFake{verdict: runtime.EvaluationPass}
	err := execute([]string{"test", cliPreviewFile(t), "-o", "json", "--github-summary"}, deps)
	got, readErr := os.ReadFile(summary)
	if err == nil || exitCode(err) != 1 || readErr != nil || string(got) != "keep\n" {
		t.Fatalf("output failure changed summary: err=%v read=%v summary=%q", err, readErr, got)
	}
}

func TestFileCommandGitHubSummaryReportsPreExecutionRefusal(t *testing.T) {
	file := cliPreviewFile(t)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, bytes.ReplaceAll(raw, []byte("**Preferred:** fake"), []byte("**Preferred:** real-model")), 0600); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(t.TempDir(), "step-summary.md")
	if err := os.WriteFile(summary, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = runner
	err = execute([]string{"test", file, "-o", "json", "--github-summary"}, deps)
	markdown, readErr := os.ReadFile(summary)
	if err == nil || exitCode(err) != 1 || runner.calls != 0 || readErr != nil || !strings.Contains(string(markdown), "model_unavailable") || strings.Contains(string(markdown), file) {
		t.Fatalf("refusal report: err=%v calls=%d read=%v summary=%q", err, runner.calls, readErr, markdown)
	}
}

func TestFileCommandGitHubSummaryReportsRedactedRefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		code  string
	}{
		{"unsafe tool content", agentpreview.ErrUnsafeToolContent, "unsafe_tool_content"},
		{"credential content", agentpreview.ErrCredentialContent, "credential_content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, summary bytes.Buffer
			deps, _ := testDependencies(&out, &bytes.Buffer{})
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass, cause: tc.cause}
			deps.fileRunner = runner
			deps.openGitHubSummary = func() (io.WriteCloser, error) { return writeCloseBuffer{Buffer: &summary}, nil }
			err := execute([]string{"test", cliPreviewFile(t), "-o", "json", "--github-summary"}, deps)
			if err == nil || exitCode(err) != 1 || runner.calls != 1 || !strings.Contains(out.String(), `"reasonCode":"`+tc.code+`"`) || !strings.Contains(summary.String(), "`unavailable`") || !strings.Contains(summary.String(), tc.code) || strings.Contains(out.String()+summary.String(), "PRIVATE") {
				t.Fatalf("redacted refusal: err=%v output=%s summary=%s", err, out.String(), summary.String())
			}
		})
	}
}

type writeCloseBuffer struct{ *bytes.Buffer }

func (writeCloseBuffer) Close() error { return nil }

type brokenSummary struct {
	writeErr error
	closeErr error
}

func (b brokenSummary) Write(p []byte) (int, error) {
	if b.writeErr != nil {
		return 0, b.writeErr
	}
	return len(p), nil
}
func (b brokenSummary) Close() error { return b.closeErr }

func TestFileCommandGitHubSummaryWriteAndCloseFailuresExitOneAfterExecution(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	for _, tc := range []struct {
		name string
		sink brokenSummary
	}{
		{"write", brokenSummary{writeErr: errors.New("broken summary write")}},
		{"close", brokenSummary{closeErr: errors.New("broken summary close")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			runner := &cliPreviewFake{verdict: runtime.EvaluationFail}
			deps.fileRunner = runner
			deps.openGitHubSummary = func() (io.WriteCloser, error) { return tc.sink, nil }
			err := execute([]string{"test", cliPreviewFile(t), "-o", "json", "--github-summary"}, deps)
			if err == nil || exitCode(err) != 1 || runner.calls != 1 || !strings.Contains(out.String(), `"reasonCode":"assertion_failed"`) || !strings.Contains(err.Error(), "cannot write GitHub Actions summary") {
				t.Fatalf("summary failure: err=%v exit=%d calls=%d stdout=%s", err, exitCode(err), runner.calls, out.String())
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
		{"run", "FILE", "--github-summary"},
		{"eval", "FILE", "--github-summary"},
		{"test", "FILE", "--case", ""},
		{"test", "FILE", "-o", "yaml"},
		{"eval", "FILE"},
		{"test", "--case", "greeting"},
		{"run", "--agent", "live", "--prompt", "x", "--case", "greeting"},
		{"run", "--agent", "live", "--prompt", "x", "--verbose"},
		{"test", "--verbose"},
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
func TestFileCommandRequiresWiredRunner(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	deps.fileRunner = nil
	err := execute([]string{"test", cliPreviewFile(t), "-o", "json"}, deps)
	if err == nil || *loads != 0 || !strings.Contains(out.String(), "runner_unavailable") {
		t.Fatalf("%v / %s", err, out.String())
	}
}
func TestFileCommandJournaledPreviewDemo(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	path := cliPreviewFile(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	working := strings.ReplaceAll(string(data), "Greet the input.", "Reply with exactly `PRIVATE OPERAND`.")
	for _, broken := range []bool{false, true, false} {
		text := working
		if broken {
			text = strings.ReplaceAll(text, "`PRIVATE OPERAND`", "`different response`")
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"run", "test"} {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			err := execute([]string{op, path, "-o", "json"}, deps)
			var r app.FilePreviewResult
			if decodeErr := json.Unmarshal(out.Bytes(), &r); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if (err != nil) != broken || r.FullPass == broken || strings.Contains(r.Runner, "no journal") || *loads != 0 {
				t.Fatalf("broken=%v: %+v / %v", broken, r, err)
			}
		}
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

func TestFilePreviewVerboseStderrOnly(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	for _, op := range []string{"run", "test"} {
		for _, verbose := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", op, verbose), func(t *testing.T) {
				path := cliPreviewFile(t)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("Greet the input."), []byte("Reply with exactly `PRIVATE OPERAND`.")), 0600); err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				args := []string{op, path, "-o", "json"}
				if verbose {
					args = append(args, "--verbose")
				}
				if op == "run" {
					args = append(args, "--prompt", "PRIVATE PROMPT")
				}
				if err := execute(args, deps); err != nil {
					t.Fatal(err)
				}
				var result app.FilePreviewResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.FullPass || *loads != 0 {
					t.Fatalf("invalid result: %v / %s; config=%d", err, out.String(), *loads)
				}
				if verbose {
					for _, want := range []string{"Completion greeting started", "Completion greeting finished", "elapsed=", "Total elapsed="} {
						if !strings.Contains(diag.String(), want) {
							t.Fatalf("missing %q in diagnostics: %s", want, diag.String())
						}
					}
					for _, duplicate := range []string{"Parsed agent", "revision=", "model=", "Runner:", "Assertion ", "Preview finished:", "checks_passed", "HTTP="} {
						if strings.Contains(diag.String(), duplicate) {
							t.Fatalf("duplicated summary %q in diagnostics: %s", duplicate, diag.String())
						}
					}
				} else if strings.Contains(diag.String(), "[verbose]") {
					t.Fatalf("unexpected diagnostics: %s", diag.String())
				}
				if strings.Contains(out.String()+diag.String(), "PRIVATE") || strings.Contains(diag.String(), path) {
					t.Fatal("verbose disclosed source content or full path")
				}
			})
		}
	}
}

func TestFilePreviewVerboseWriteFailure(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.stderr = previewBrokenWriter{}
	deps.fileRunner = app.NewLocalFileRunner()
	err := execute([]string{"test", cliPreviewFile(t), "--verbose"}, deps)
	if err == nil || !strings.Contains(err.Error(), "cannot write local preview diagnostics") || out.Len() != 0 {
		t.Fatalf("diagnostic failure did not refuse: %v, stdout=%s", err, out.String())
	}
}

func TestFilePromptCollectionPrecedesPreviewBudget(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.stdin, deps.fileRunner = reader, fake
	collected := make(chan time.Time, 1)
	go func() {
		// Represent user think time, outside the execution budget.
		time.Sleep(100 * time.Millisecond)
		_, _ = writer.Write([]byte("Ada"))
		collected <- time.Now()
		_ = writer.Close()
	}()
	if err := execute([]string{"run", cliPreviewFile(t), "--prompt-file", "-", "-o", "json"}, deps); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || *loads != 0 || fake.request.Input == nil || *fake.request.Input != "Ada" {
		t.Fatalf("prompt not attributed: %+v / config=%d", fake.request, *loads)
	}
	if fake.deadline.Before((<-collected).Add(5 * time.Minute)) {
		t.Fatal("prompt collection consumed the five-minute preview execution budget")
	}
}

func TestFileHelpIsCanonicalOnly(t *testing.T) {
	for _, verb := range []string{"run", "test", "eval"} {
		t.Run(verb, func(t *testing.T) {
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = fake
			if err := execute([]string{verb, "--help"}, deps); err != nil {
				t.Fatal(err)
			}
			if verb == "eval" {
				// Workspace flags legitimately use FILE as a metavariable; file-mode
				// descriptions and selectors must remain canonical-only.
				if strings.Contains(out.String(), "test FILE") || strings.Contains(out.String(), "FILE supports both") || strings.Contains(out.String(), "FILE: answer") || strings.Contains(out.String(), "--case ") || strings.Count(diag.String(), evalDeprecationNotice) != 1 {
					t.Fatalf("legacy help advertises file preview or loses notice: %s / %s", out.String(), diag.String())
				}
				for _, help := range []string{
					"output: text|json (JSON: Orka only)",
					"print sanitized bounded failure excerpts, expected values and judge rationale on stderr (not evidence)",
				} {
					if !strings.Contains(out.String(), help) {
						t.Errorf("legacy help changed: missing %q", help)
					}
				}
			} else if !strings.Contains(strings.ToLower(out.String()), verb+" file") || !strings.Contains(out.String(), "--case") {
				t.Fatalf("canonical file preview missing from %s help: %s", verb, out.String())
			}
			if verb != "eval" {
				for _, disclosure := range []string{"cleartext SQLite journals", "local-preview/journals", "newest 20 runs", "active runs", "Stop previews", "fake, local and Copilot models", "embedded Agent Sessions", "judge payloads and reasoning", "provider-owned", "before journaling", "Replay makes zero model calls"} {
					if !strings.Contains(out.String(), disclosure) {
						t.Errorf("%s help omits retention disclosure %q", verb, disclosure)
					}
				}
				if verb == "test" {
					for _, disclosure := range []string{"eval-failures", "last 10 per resolved file subject", "test explain --file FILE", "deleting journals does not clear failure diagnostics", "--github-summary", "GITHUB_STEP_SUMMARY"} {
						if !strings.Contains(out.String(), disclosure) {
							t.Errorf("test help omits file-failure disclosure %q", disclosure)
						}
					}
				}
				for _, stale := range []string{"NoJournal", "preview runner (no journal)", "whole invocation is direct"} {
					if strings.Contains(out.String(), stale) {
						t.Errorf("%s help retains stale Copilot description %q", verb, stale)
					}
				}
			}
			if *loads != 0 || fake.calls != 0 {
				t.Fatal("help performed operations")
			}
		})
	}
}

type previewBrokenWriter struct{}

func (previewBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("broken output") }
