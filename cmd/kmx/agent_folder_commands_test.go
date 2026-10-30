package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

type folderReport struct {
	SchemaVersion int
	Kind          string
	Files         []struct {
		Index                   int
		Directory, File, Status string
		Result                  *app.FilePreviewResult
	}
	Summary    struct{ Total, Passed, Failed, Unknown, CouldNotRun, NotRun int }
	ExitCode   int
	ReasonCode string
}

func folderAgent(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	file := filepath.Join(dir, filepath.FromSlash(name))
	tests, ok := agentfile.DefaultTestsPath(file)
	if !ok {
		t.Fatal("invalid fixture name")
	}
	for _, path := range []string{file, tests} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("---\nname: greeter\nmodel: fake\ntools: []\n---\nReply with exactly `Hello from pair`.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tests, []byte(cliPairTests), 0600); err != nil {
		t.Fatal(err)
	}
	return file, tests
}

func folderDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("KMX_HOME", privateCommandHome(t))
	return dir
}

func readFolderReport(t *testing.T, out *bytes.Buffer, err error, code int) folderReport {
	t.Helper()
	actual := 0
	if err != nil {
		actual = exitCode(err)
	}
	if actual != code {
		t.Fatalf("exit=%d want%d: %v\n%s", actual, code, err, out.String())
	}
	var report folderReport
	if parse := json.Unmarshal(out.Bytes(), &report); parse != nil || report.SchemaVersion != 1 || report.Kind != "kmx.agent-folder-test" || report.ExitCode != code {
		t.Fatalf("not one folder result: %v\n%s", parse, out.String())
	}
	return report
}

func TestFilePreviewExecutionSeam(t *testing.T) {
	dir := folderDirectory(t)
	file, _ := folderAgent(t, dir, "g.agent.md")
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = runner
	state := &commandState{deps: deps}
	cmd := newAgentTestCommand(state)
	cmd.SetOut(&out)
	cmd.SetErr(&diag)
	cmd.SetContext(t.Context())
	veto := errors.New("summary intake veto")
	result, err := previewFileCommand(cmd, state, file, fileCommandOptions{}, "", "", func() error { return veto })
	if !errors.Is(err, veto) || runner.calls != 0 || result.SchemaVersion != 0 || out.Len() != 0 {
		t.Fatalf("pre-execution veto lost: %+v %v", result, err)
	}
	result, err = previewFileCommand(cmd, state, file, fileCommandOptions{}, "", "", nil)
	if err != nil || runner.calls != 1 || !result.FullPass || result.SnapshotRevision == "" || out.Len() != 0 {
		t.Fatalf("execution must return a pair result without rendering: %+v %v", result, err)
	}
}

func TestFolderTestDiscoversOnlyDirectAgents(t *testing.T) {
	dir := folderDirectory(t)
	for _, name := range []string{"z.agent.md", "a.agent.md", ".github/agents/a.agent.md", "nested/ignored.agent.md", ".github/agents/nested/ignored.agent.md"} {
		folderAgent(t, dir, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.yaml"), []byte("invalid workspace: ["), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.fileRunner = runner
	report := readFolderReport(t, &out, execute([]string{"test", "-o", "json", "--no-content"}, deps), 0)
	var names []string
	for _, row := range report.Files {
		names = append(names, filepath.ToSlash(filepath.Join(row.Directory, row.File)))
		if row.Status != "passed" || row.Result == nil || !row.Result.FullPass || row.Result.SnapshotRevision == "" || row.Result.Tests == nil || row.Result.Tests.State != "external" {
			t.Fatalf("missing per-file identity: %+v", row)
		}
	}
	if !reflect.DeepEqual(names, []string{".github/agents/a.agent.md", "a.agent.md", "z.agent.md"}) || runner.calls != 3 || *loads != 0 || report.Summary.Total != 3 || report.Summary.Passed != 3 {
		t.Fatalf("discovery=%v calls=%d config=%d counts=%+v", names, runner.calls, *loads, report.Summary)
	}
	if strings.Contains(out.String(), "PRIVATE") {
		t.Fatal("--no-content leaked content")
	}
}

func TestFolderTestSelectorsRefuseBeforeDiscovery(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			dir := folderDirectory(t)
			for i := 0; i < count; i++ {
				folderAgent(t, dir, string(rune('a'+i))+".agent.md")
			}
			// An invalid optional discovery folder must not mask selector misuse.
			if err := os.WriteFile(filepath.Join(dir, ".github"), []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"--tests", "--case"} {
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
				deps.fileRunner = runner
				err := execute([]string{"test", flag, "chosen", "--github-summary", "-o", "json"}, deps)
				if exitCode(err) != 2 || !strings.Contains(err.Error(), flag+" requires an explicit agent FILE") || runner.calls != 0 || *loads != 0 || out.Len() != 0 {
					t.Fatalf("selector crossed preflight: %v %s", err, out.String())
				}
			}
		})
	}
}

func TestFolderTestEmptyAndInvalidDiscovery(t *testing.T) {
	for _, invalid := range []string{"empty", "github-file", "agents-file"} {
		t.Run(invalid, func(t *testing.T) {
			dir := folderDirectory(t)
			if invalid != "empty" {
				path := filepath.Join(dir, ".github")
				if invalid == "agents-file" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(path, "agents")
				}
				if err := os.WriteFile(path, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = runner
			r := readFolderReport(t, &out, execute([]string{"test", "-o", "json"}, deps), 2)
			want := "no_files"
			if invalid != "empty" {
				want = "discovery_failed"
			}
			if r.ReasonCode != want || len(r.Files) != 0 || runner.calls != 0 || *loads != 0 {
				t.Fatalf("not an attributable empty/refusal: %+v calls%d", r, runner.calls)
			}
		})
	}
}
