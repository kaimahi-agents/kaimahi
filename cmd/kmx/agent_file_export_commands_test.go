package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	agentruntime "github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func cliRunProfileDir(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestRunProfileExportCLIIsOffline(t *testing.T) {
	file, err := filepath.EvalSymlinks(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(cliRunProfileDir(t), "profile.json")
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	fake := &cliPreviewFake{}
	deps.fileRunner = fake
	err = execute([]string{"run", "export", file, "--out", dest}, deps)
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("native Windows output should refuse")
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		var summary agentfile.RunProfileSummary
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil || summary.TargetExamples != "not-run" {
			t.Fatal("missing safe summary")
		}
		if strings.Contains(out.String(), "PRIVATE") || strings.Contains(out.String(), file) {
			t.Fatal("content/path leaked into summary")
		}
		if _, err := os.Stat(dest); err != nil {
			t.Fatal(err)
		}
	}
	if *loads != 0 || fake.calls != 0 {
		t.Fatalf("export initialized config or invoked a runner: %d/%d", *loads, fake.calls)
	}
}
func TestRunProfileExportCLIRefusesFlagsAndHelpIsInert(t *testing.T) {
	for _, args := range [][]string{
		{"run", "export"}, {"run", "export", "missing"}, {"run", "export", "missing", "--out", ""},
		{"run", "export", "missing", "--out", "profile", "--provider", "copilot"},
		{"run", "export", "missing", "--out", "profile", "--context", "kind-example"},
		{"run", "export", "missing", "--out", "profile", "--container-engine", "docker"},
		{"run", "export", "missing", "--out", "profile", "--prompt", "private"},
		{"run", "export", "missing", "extra", "--out", "profile"},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err == nil || out.Len() != 0 || *loads != 0 {
			t.Fatalf("bad offline refusal %v: output=%s loads=%d", args, &out, *loads)
		}
	}
	for _, args := range [][]string{{"run", "export", "--help"}, {"help", "run", "export"}, {"__complete", "run", ""}, {"__complete", "run", "export", ""}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err != nil || *loads != 0 {
			t.Fatalf("help/completion: %v %v", args, err)
		}
		if len(args) == 3 && args[0] == "__complete" {
			if !strings.Contains(out.String(), "export") {
				t.Fatal("export not offered in completion")
			}
			if !strings.HasSuffix(out.String(), ":0\n") {
				t.Fatal("run parent disables default file completion")
			}
		}
	}
}
func TestRunProfileExportKeepsExistingRunPaths(t *testing.T) {
	file, err := filepath.EvalSymlinks(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{file, filepath.Join(filepath.Dir(file), "export")} {
		if input != file {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		fake := &cliPreviewFake{verdict: agentruntime.EvaluationPass}
		deps.fileRunner = fake
		if err := execute([]string{"run", input, "--case", "greeting"}, deps); err != nil || fake.calls != 1 || *loads != 0 {
			t.Fatalf("ordinary run path changed: %v", err)
		}
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	_ = execute([]string{"run", "--agent", "example", "--prompt", "export"}, deps)
	if *loads != 1 {
		t.Fatal("prompt named export was treated as subcommand")
	}
}
