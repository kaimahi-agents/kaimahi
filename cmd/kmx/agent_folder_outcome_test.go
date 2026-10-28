package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type folderRunner func(context.Context, agentpreview.Request) (agentpreview.Outcome, error)

func (f folderRunner) Run(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
	return f(ctx, req)
}

func TestFolderTestMixedOutcomesAndAbort(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		verdict                                    runtime.EvaluationVerdict
		cause                                      error
		code, calls, passed, unknown, could, unrun int
	}{
		{"assertion", runtime.EvaluationFail, nil, 1, 3, 1, 0, 0, 0},
		{"unknown", runtime.EvaluationUnknown, nil, 2, 3, 1, 1, 0, 0},
		{"journal", runtime.EvaluationPass, agentpreview.ErrJournalFailed, 2, 2, 0, 0, 1, 1},
		{"execution", runtime.EvaluationPass, errors.New("PRIVATE transport error"), 2, 2, 0, 0, 1, 1},
		{"source", runtime.EvaluationPass, agentpreview.ErrSourceChanged, 2, 2, 0, 0, 1, 1},
		{"signin", runtime.EvaluationPass, copilot.ErrAuthRequired, 2, 2, 0, 0, 1, 1},
		{"quota", runtime.EvaluationPass, copilot.ErrQuota, 2, 2, 0, 0, 1, 1},
		{"cancel", runtime.EvaluationPass, context.Canceled, 130, 2, 0, 0, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := folderDirectory(t)
			for _, file := range []string{"a.agent.md", "b.agent.md", "c.agent.md"} {
				folderAgent(t, dir, file)
			}
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			calls := 0
			deps.fileRunner = folderRunner(func(ctx context.Context, req agentpreview.Request) (agentpreview.Outcome, error) {
				calls++
				f := &cliPreviewFake{verdict: runtime.EvaluationPass}
				if calls == 1 {
					f.verdict = runtime.EvaluationFail
				}
				if calls == 2 {
					f.verdict, f.cause = tc.verdict, tc.cause
				}
				return f.Run(ctx, req)
			})
			r := readFolderReport(t, &out, execute([]string{"test", "-o", "json", "--no-content"}, deps), tc.code)
			if calls != tc.calls || r.Summary.Passed != tc.passed || r.Summary.Unknown != tc.unknown || r.Summary.CouldNotRun != tc.could || r.Summary.NotRun != tc.unrun || r.Files[0].Status != "failed" {
				t.Fatalf("calls=%d counts=%+v", calls, r.Summary)
			}
			if tc.unrun > 0 && (r.Files[2].Result != nil || r.Files[2].Status != "not_run") {
				t.Fatal("fabricated skipped result")
			}
		})
	}
}

func TestFolderTestLocalIntakeErrorsContinue(t *testing.T) {
	dir := folderDirectory(t)
	for _, name := range []string{"a-invalid", "b-missing", "c-empty", "d-valid"} {
		_, tests := folderAgent(t, dir, name+".agent.md")
		switch name {
		case "a-invalid":
			if err := os.WriteFile(tests, []byte("not a tests document"), 0600); err != nil {
				t.Fatal(err)
			}
		case "b-missing":
			if err := os.Remove(tests); err != nil {
				t.Fatal(err)
			}
		case "c-empty":
			if err := os.WriteFile(tests, []byte("---\nschemaVersion: 1\n---\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = fake
	r := readFolderReport(t, &out, execute([]string{"test", "-o", "json"}, deps), 2)
	if fake.calls != 1 || r.Summary.CouldNotRun != 3 || r.Summary.Passed != 1 || r.Summary.NotRun != 0 {
		t.Fatalf("calls=%d counts=%+v", fake.calls, r.Summary)
	}
}

type folderHookWriter struct {
	bytes.Buffer
	hook func([]byte) error
}

func (w *folderHookWriter) Write(b []byte) (int, error) {
	if err := w.hook(b); err != nil {
		return 0, err
	}
	return w.Buffer.Write(b)
}
func (w *folderHookWriter) Close() error { return nil }

func TestFolderTestCancellationBetweenFiles(t *testing.T) {
	dir := folderDirectory(t)
	folderAgent(t, dir, "a.agent.md")
	folderAgent(t, dir, "b.agent.md")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &folderHookWriter{hook: func(b []byte) error {
		if bytes.Contains(b, []byte(`"index":1`)) {
			cancel()
		}
		return nil
	}}
	var diag bytes.Buffer
	deps, _ := testDependencies(&bytes.Buffer{}, &diag)
	deps.stdout = out
	fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = fake
	state := &commandState{deps: deps, argv: []string{"test", "-o", "json"}}
	root := newRootCommand(state)
	root.SetArgs(state.argv)
	r := readFolderReport(t, &out.Buffer, root.ExecuteContext(ctx), 130)
	if fake.calls != 1 || r.Summary.Passed != 1 || r.Summary.NotRun != 1 || r.Files[1].Result != nil {
		t.Fatalf("cancel crossed file boundary: calls%d counts%+v", fake.calls, r.Summary)
	}
}

func TestFolderTestOutputFailuresStopLaterFiles(t *testing.T) {
	for _, kind := range []string{"stdout", "partial-stdout", "route", "summary", "close"} {
		t.Run(kind, func(t *testing.T) {
			dir := folderDirectory(t)
			folderAgent(t, dir, "a.agent.md")
			folderAgent(t, dir, "b.agent.md")
			folderAgent(t, dir, "c.agent.md")
			var out, diag, markdown bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			fake := &cliPreviewFake{verdict: runtime.EvaluationUnknown}
			deps.fileRunner = fake
			deps.openGitHubSummary = func() (io.WriteCloser, error) { return writeCloseBuffer{&markdown}, nil }
			switch kind {
			case "stdout":
				deps.stdout = previewBrokenWriter{}
			case "partial-stdout":
				deps.stdout = &folderHookWriter{hook: func(b []byte) error {
					if bytes.Contains(b, []byte(`"index":2`)) {
						return errors.New("partial output failure")
					}
					return nil
				}}
			case "route":
				deps.stderr = previewBrokenWriter{}
			case "summary":
				deps.openGitHubSummary = func() (io.WriteCloser, error) {
					return &folderHookWriter{hook: func(b []byte) error {
						if bytes.Contains(b, []byte("### File")) {
							return errors.New("broken summary section")
						}
						return nil
					}}, nil
				}
			case "close":
				deps.openGitHubSummary = func() (io.WriteCloser, error) { return brokenSummary{closeErr: errors.New("broken close")}, nil }
			}
			err := execute([]string{"test", "-o", "json", "--github-summary"}, deps)
			wantCalls := map[string]int{"stdout": 0, "partial-stdout": 2, "route": 1, "summary": 1, "close": 3}[kind]
			if err == nil || exitCode(err) != 1 || fake.calls != wantCalls || strings.Contains(out.String(), `"summary":`) {
				t.Fatalf("I/O precedence/abort: %v calls%d stdout%s", err, fake.calls, out.String())
			}
			if kind == "stdout" && markdown.Len() != 0 {
				t.Fatal("JSON failure wrote summary")
			}
		})
	}
}

func TestFolderTestSummaryPrivacyAndBound(t *testing.T) {
	dir := folderDirectory(t)
	file, _ := folderAgent(t, dir, "PRIVATE.agent.md")
	var out, diag, markdown bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	fake := &cliPreviewFake{verdict: runtime.EvaluationFail}
	deps.fileRunner = fake
	opened := 0
	deps.openGitHubSummary = func() (io.WriteCloser, error) { opened++; return writeCloseBuffer{&markdown}, nil }
	readFolderReport(t, &out, execute([]string{"test", "-o", "json", "--github-summary"}, deps), 1)
	if opened != 1 || strings.Count(markdown.String(), "## kmx folder test") != 1 || strings.Count(markdown.String(), "### Summary") != 1 || strings.Contains(markdown.String(), "PRIVATE") || strings.Contains(markdown.String(), file) {
		t.Fatalf("unsafe/duplicate folder summary: %s", markdown.String())
	}
	// Exercise the real section budget with one safe result; no source/model loop.
	state := &commandState{deps: deps}
	cmd := newAgentTestCommand(state)
	cmd.SetContext(t.Context())
	cmd.SetErr(&diag)
	result, _ := previewFileCommand(cmd, state, file, fileCommandOptions{}, "", "", nil)
	markdown.Reset()
	summary := folderSummaryWriter{out: &markdown}
	if err := summary.start(); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3000; i++ {
		if err := summary.file(folderFileResult{Index: i, Result: &result}); err != nil {
			t.Fatal(err)
		}
	}
	if err := summary.finish(folderTestCounts{Total: 3000, Failed: 3000}, 1, "completed"); err != nil {
		t.Fatal(err)
	}
	if summary.omitted == 0 || markdown.Len() > folderSummaryMaxBytes || !strings.Contains(markdown.String(), "3000 failed") || !strings.Contains(markdown.String(), fmt.Sprintf("%d additional file section(s)", summary.omitted)) {
		t.Fatalf("unbounded/missing counts: bytes%d omitted%d", markdown.Len(), summary.omitted)
	}
}

func TestFolderTestPreflightAndWindowsNoEffects(t *testing.T) {
	for _, windows := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary-preflight", true: "windows"}[windows], func(t *testing.T) {
			dir := folderDirectory(t)
			folderAgent(t, dir, "a.agent.md")
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = fake
			opened := 0
			deps.openGitHubSummary = func() (io.WriteCloser, error) { opened++; return nil, errors.New("summary refused") }
			args := []string{"test", "-o", "json", "--github-summary"}
			if windows {
				deps.goos = "windows"
				args = []string{"test"}
				if err := os.WriteFile(filepath.Join(dir, ".github"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := execute(args, deps)
			if windows && (!errors.Is(err, errWindowsOperationUnsupported) || opened != 0) || !windows && (exitCode(err) != 2 || opened != 1) || fake.calls != 0 || *loads != 0 || out.Len()+diag.Len() != 0 {
				t.Fatalf("preflight crossed effects: %v opened%d calls%d", err, opened, fake.calls)
			}
		})
	}
}

func TestFolderTestTextHasOneSummary(t *testing.T) {
	dir := folderDirectory(t)
	folderAgent(t, dir, "a.agent.md")
	folderAgent(t, dir, "b.agent.md")
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.fileRunner = &cliPreviewFake{verdict: runtime.EvaluationPass}
	if err := execute([]string{"test", "--verbose"}, deps); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Test summary:") != 1 || strings.Count(out.String(), "Testing 2 agent") != 1 || strings.Contains(out.String(), "kmx local preview of this charter") || !strings.Contains(out.String(), "2 passed") {
		t.Fatal(out.String())
	}
}

func TestFolderTestUnrunNamesStayWithheld(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			dir := folderDirectory(t)
			folderAgent(t, dir, "a.agent.md")
			const private = "arbitrary-private-value"
			folderAgent(t, dir, "z-"+private+".agent.md")
			var out, diag, markdown bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			fake := &cliPreviewFake{cause: agentpreview.ErrCredentialContent}
			deps.fileRunner = fake
			deps.openGitHubSummary = func() (io.WriteCloser, error) { return writeCloseBuffer{&markdown}, nil }
			args := []string{"test", "-o", format}
			if format == "json" {
				args = append(args, "--github-summary")
			}
			err := execute(args, deps)
			if exitCode(err) != 2 || fake.calls != 1 || strings.Contains(out.String()+diag.String()+markdown.String(), private) {
				t.Fatalf("unexecuted filename bypassed redaction: calls%d error%v\n%s", fake.calls, err, out.String())
			}
			if format == "json" {
				r := readFolderReport(t, &out, err, 2)
				if r.Summary.NotRun != 1 || r.Files[1].Result != nil || r.Files[1].File != "[agent file]" {
					t.Fatalf("unrun identity was asserted: %+v", r.Files[1])
				}
			}
		})
	}
}

func TestFolderTestRefusedNamesStayWithheld(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			dir := folderDirectory(t)
			const private = "arbitrary-private-value"
			_, tests := folderAgent(t, dir, "b-"+private+".agent.md")
			if err := os.Remove(tests); err != nil {
				t.Fatal(err)
			}
			folderAgent(t, dir, "c.agent.md")
			var out, diag, markdown bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			fake := &cliPreviewFake{cause: agentpreview.ErrCredentialContent}
			deps.fileRunner = fake
			deps.openGitHubSummary = func() (io.WriteCloser, error) { return writeCloseBuffer{&markdown}, nil }
			args := []string{"test", "-o", format}
			if format == "json" {
				args = append(args, "--github-summary")
			}
			err := execute(args, deps)
			if exitCode(err) != 2 || fake.calls != 1 || strings.Contains(out.String()+diag.String()+markdown.String(), private) {
				t.Fatalf("early refusal bypassed redaction: calls%d error%v\n%s", fake.calls, err, out.String())
			}
			if format == "json" {
				r := readFolderReport(t, &out, err, 2)
				if r.Summary.CouldNotRun != 2 || r.Files[0].File != "[agent file]" || r.Files[0].Result.Tests.Path != "[tests file]" {
					t.Fatalf("early refusal asserted unchecked names: %+v", r.Files[0])
				}
			}
		})
	}
}

func TestFolderTestInvalidToolSettingsAbort(t *testing.T) {
	dir := folderDirectory(t)
	file, _ := folderAgent(t, dir, "a.agent.md")
	folderAgent(t, dir, "b.agent.md")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, bytes.Replace(raw, []byte("tools: []"), []byte("tools: [ping]"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kmx.yaml"), []byte("tools: {ping: invalid-state}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = fake
	r := readFolderReport(t, &out, execute([]string{"test", "-o", "json"}, deps), 2)
	if fake.calls != 0 || r.Summary.CouldNotRun != 1 || r.Summary.NotRun != 1 || r.Files[0].Result.ReasonCode != "invalid_tool_settings" || r.Files[1].Result != nil {
		t.Fatalf("tool-settings refusal did not abort: calls%d counts%+v", fake.calls, r.Summary)
	}
}
