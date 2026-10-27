package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type answerPreviewRunner struct {
	cliPreviewFake
	answer string
}

func (f *answerPreviewRunner) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	out, err := f.cliPreviewFake.Run(ctx, req)
	if out.AdHoc != nil {
		out.AdHoc.Answer = f.answer
	}
	for i := range out.Examples {
		out.Examples[i].Completion.Answer = f.answer
	}
	return out, err
}

func TestFileRunAnswerOnStdout(t *testing.T) {
	answer := strings.Repeat("Hello Ada!\n", 600)
	for _, format := range []string{"text", "json"} {
		for _, noContent := range []bool{false, true} {
			t.Run(format+map[bool]string{true: "/no-content", false: "/content"}[noContent], func(t *testing.T) {
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				runner := &answerPreviewRunner{cliPreviewFake: cliPreviewFake{verdict: runtime.EvaluationPass}, answer: answer}
				deps.fileRunner = runner
				args := []string{"run", cliPreviewFile(t), "Ada", "-o", format}
				if noContent {
					args = append(args, "--no-content")
				}
				if err := execute(args, deps); err != nil {
					t.Fatal(err)
				}
				if *loads != 0 || runner.request.Input == nil || *runner.request.Input != "Ada" || len(runner.request.Examples) != 0 {
					t.Fatalf("request=%+v loads=%d", runner.request, *loads)
				}
				if !strings.HasPrefix(diag.String(), "Route: fake / fake\n") {
					t.Fatalf("route line: %q", diag.String())
				}
				if format == "text" {
					want := answer
					if noContent {
						want = ""
					}
					if out.String() != want {
						t.Fatalf("stdout length=%d want=%d", out.Len(), len(want))
					}
				} else {
					var result map[string]any
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					row := result["adHoc"].(map[string]any)
					got, has := row["answer"]
					if noContent && has || !noContent && got != answer {
						t.Fatalf("answer present=%v length=%d", has, len(out.Bytes()))
					}
				}
			})
		}
	}
}

func TestFileDefaultRouteAndOverrides(t *testing.T) {
	for _, tc := range []struct{ name, model, provider, url, wantProvider, wantModel string }{
		{"authored", "claude-haiku-5.5", "", "", "copilot", "claude-haiku-5.5"},
		{"omitted model", "", "", "", "copilot", "gpt-5.6-terra"},
		{"override", "fake", "", "", "copilot", "gpt-test"},
		{"local", "llama3.2", "ollama", "http://localhost:11434", "ollama", "llama3.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := cliPreviewFile(t)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.model == "" {
				source = bytes.ReplaceAll(source, []byte("## Model\n- **Preferred:** fake\n"), nil)
			} else {
				source = bytes.ReplaceAll(source, []byte("**Preferred:** fake"), []byte("**Preferred:** "+tc.model))
			}
			if err := os.WriteFile(path, source, 0600); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = runner
			args := []string{"test", path}
			if tc.provider != "" {
				args = append(args, "--provider", tc.provider, "--provider-url", tc.url)
			}
			if tc.name == "override" {
				args = append(args, "--model", tc.wantModel)
			}
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 || runner.request.Route.Provider != tc.wantProvider || runner.request.Route.Model != tc.wantModel {
				t.Fatalf("route=%+v loads=%d", runner.request.Route, *loads)
			}
			if !strings.HasPrefix(diag.String(), "Route: "+tc.wantProvider+" / "+tc.wantModel+"\n") {
				t.Fatalf("route: %q", diag.String())
			}
		})
	}
}

func TestFileFailureContentDefaultAndOptOut(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	for _, format := range []string{"text", "json"} {
		for _, noContent := range []bool{false, true} {
			t.Run(format+map[bool]string{true: "/no-content", false: "/content"}[noContent], func(t *testing.T) {
				var out, diag bytes.Buffer
				deps, _ := testDependencies(&out, &diag)
				deps.fileRunner = &answerPreviewRunner{cliPreviewFake: cliPreviewFake{verdict: runtime.EvaluationFail}, answer: "Actual first line\nSecond line\n"}
				args := []string{"test", cliPreviewFile(t), "-o", format}
				if noContent {
					args = append(args, "--no-content")
				}
				err := execute(args, deps)
				if err == nil || exitCode(err) != 1 {
					t.Fatalf("exit=%v", err)
				}
				for _, content := range []string{"PRIVATE OPERAND", "Actual first line"} {
					if strings.Contains(out.String(), content) == noContent {
						t.Fatalf("content toggle: %s", out.String())
					}
				}
				if format == "text" {
					if !strings.Contains(out.String(), "FAIL greeting (") {
						t.Fatalf("case line: %s", out.String())
					}
					if !noContent && !strings.Contains(out.String(), `Missing "PRIVATE OPERAND"`) {
						t.Fatalf("wording: %s", out.String())
					}
					if !strings.Contains(out.String(), "test explain --file FILE") {
						t.Fatalf("missing inspection step: %s", out.String())
					}
				} else {
					var r map[string]any
					if err := json.Unmarshal(out.Bytes(), &r); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestFileAuthOneActionableError(t *testing.T) {
	for _, cause := range []error{copilot.ErrAuthRequired, copilot.ErrAuthRevoked, copilot.ErrKeychain} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		deps.fileRunner = &cliPreviewFake{cause: errors.Join(cause, errors.New("PRIVATE PROVIDER DETAIL"))}
		err := execute([]string{"test", cliPreviewFile(t), "--model", "gpt-test"}, deps)
		if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "gh auth login") || !strings.Contains(err.Error(), "--provider ollama --provider-url URL") || strings.Contains(err.Error(), "PRIVATE") || out.Len() != 0 {
			t.Fatalf("err=%v stdout=%q stderr=%q", err, out.String(), diag.String())
		}
	}
}

func TestFileAuthJSONActionable(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.fileRunner = &cliPreviewFake{cause: copilot.ErrAuthRequired}
	err := execute([]string{"test", cliPreviewFile(t), "--model", "gpt-test", "-o", "json"}, deps)
	var result struct{ Reason string }
	if jsonErr := json.Unmarshal(out.Bytes(), &result); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if err == nil || exitCode(err) != 2 || !strings.Contains(result.Reason, "gh auth login") || !strings.Contains(result.Reason, "--provider ollama --provider-url URL") {
		t.Fatalf("unhelpful auth report: %v / %s", err, out.String())
	}
}

func TestFileFullAnswerWithholdsUnsafeTail(t *testing.T) {
	for _, answer := range []string{"", strings.Repeat("normal text ", 500) + "\nAuthorization: private-value"} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		deps.fileRunner = &answerPreviewRunner{cliPreviewFake: cliPreviewFake{verdict: runtime.EvaluationPass}, answer: answer}
		if err := execute([]string{"run", cliPreviewFile(t), "Ada", "-o", "json"}, deps); err != nil {
			t.Fatal(err)
		}
		var result struct{ AdHoc struct{ Answer *string } }
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		want := ""
		if answer != "" {
			want = "[content withheld]"
		}
		if result.AdHoc.Answer == nil || *result.AdHoc.Answer != want || strings.Contains(out.String(), "private-value") {
			t.Fatalf("unsafe or missing answer: %s", out.String())
		}
	}
}

func TestFileCouldNotRunExitTwo(t *testing.T) {
	for _, mode := range []string{"missing file", "no tests", "wrong endpoint"} {
		t.Run(mode, func(t *testing.T) {
			path := cliPreviewFile(t)
			args := []string{"test", path, "-o", "json"}
			switch mode {
			case "missing file":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "no tests":
				source, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				source = []byte(strings.Split(string(source), "## Examples")[0] + "## Examples\n```yaml\nexamples: []\n```\n")
				if err := os.WriteFile(path, source, 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong endpoint":
				args = append(args, "--model", "gpt-test", "--provider-url", "https://private.invalid")
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = runner
			err := execute(args, deps)
			if err == nil || exitCode(err) != 2 || runner.calls != 0 || *loads != 0 || strings.Contains(out.String()+diag.String(), "private.invalid") {
				t.Fatalf("refusal err=%v calls=%d config=%d output=%s", err, runner.calls, *loads, out.String())
			}
		})
	}
}

func TestFilePositionalInputHonorsExplicitCase(t *testing.T) {
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	runner := &answerPreviewRunner{cliPreviewFake: cliPreviewFake{verdict: runtime.EvaluationPass}, answer: "Hello, Ada"}
	deps.fileRunner = runner
	if err := execute([]string{"run", cliPreviewFile(t), "Ada", "--case", "greeting"}, deps); err != nil {
		t.Fatal(err)
	}
	if len(runner.request.Examples) != 1 || runner.request.Examples[0].ID != "greeting" || out.String() != "Hello, Ada\n" {
		t.Fatalf("selection ignored: %+v / %s", runner.request, out.String())
	}
}

func TestFileIntakeRefusalsExitTwo(t *testing.T) {
	for _, flags := range [][]string{{"--prompt-file", "absent-prompt.txt"}, {"--prompt", "Ada", "--prompt-file", "-"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		runner := &cliPreviewFake{}
		deps.fileRunner = runner
		err := execute(append([]string{"run", cliPreviewFile(t)}, flags...), deps)
		if err == nil || exitCode(err) != 2 || runner.calls != 0 || *loads != 0 {
			t.Fatalf("args=%v err=%v", flags, err)
		}
	}
}

func TestFileRemovedFlagsAndMixedInput(t *testing.T) {
	for _, tail := range [][]string{{"--show-content"}, {"--endpoint", "http://localhost:11434"}, {"Ada", "--prompt", "other"}, {"Ada", "--prompt-file", "-"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		runner := &cliPreviewFake{}
		deps.fileRunner = runner
		if err := execute(append([]string{"run", cliPreviewFile(t)}, tail...), deps); err == nil || runner.calls != 0 || *loads != 0 {
			t.Fatalf("args=%v err=%v calls=%d loads=%d", tail, err, runner.calls, *loads)
		}
	}
}
