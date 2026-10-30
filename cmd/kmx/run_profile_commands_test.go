package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

func TestRunProfileExportOffline(t *testing.T) {
	path := cliPreviewFile(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "absent-state")
	t.Setenv("KMX_HOME", home)
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	runner := &cliPreviewFake{}
	deps.fileRunner = runner
	if err := execute([]string{"package", "run-profile", "export", path}, deps); err != nil {
		t.Fatal(err)
	}
	want, err := agentfile.ExportRunProfile(path)
	if err != nil || !bytes.Equal(out.Bytes(), want) || diagnostics.Len() != 0 || *loads != 0 || runner.calls != 0 {
		t.Fatal("CLI did not emit just the canonical offline profile", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var profile map[string]any
	if err := decoder.Decode(&profile); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&profile); err != io.EOF {
		t.Fatal("stdout has more than one JSON document", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("export changed source", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("export touched KMX state", err)
	}
}

func TestRunProfileExportEmptyExplicitSelector(t *testing.T) {
	for _, value := range []string{"", " \t"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			path := cliPreviewFile(t)
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.newCopilot = func() copilotAccess { t.Fatal("selector refusal accessed auth"); return nil }
			err := execute([]string{"package", "run-profile", "export", path, "--tests", value}, deps)
			if err == nil || !strings.Contains(err.Error(), "nonempty --tests") || out.Len() != 0 || diag.Len() != 0 || *loads != 0 {
				t.Fatalf("empty selector silently selected tests or effects: %v stdout%d loads%d", err, out.Len(), *loads)
			}
		})
	}
}

func TestRunProfileExportSelectedPairOffline(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		path := cliPreviewFile(t)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := strings.Replace(string(original), "**Preferred:** fake", "**Preferred:** exact-authored-model", 1)
		if !explicit {
			source = strings.Split(source, "## Examples")[0]
		}
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		testsPath := strings.TrimSuffix(path, ".md") + ".test.md"
		tests := "---\nschemaVersion: 1\n---\n```yaml agent-test\nid: external\ninput: PRIVATE INPUT\nassertions: [{id: text, type: contains, value: PRIVATE OPERAND}]\n```\n"
		if err := os.WriteFile(testsPath, []byte(tests), 0600); err != nil {
			t.Fatal(err)
		}
		args := []string{"package", "run-profile", "export", path}
		override := ""
		if explicit {
			override = testsPath
			args = append(args, "--tests", testsPath)
		}
		home := filepath.Join(t.TempDir(), "absent-state")
		t.Setenv("KMX_HOME", home)
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		runner := &cliPreviewFake{}
		deps.fileRunner = runner
		deps.newCopilot = func() copilotAccess { t.Fatal("offline export accessed auth"); return nil }
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
		want, err := agentfile.ExportSelectedRunProfile(path, override)
		if err != nil || !bytes.Equal(out.Bytes(), want) || diagnostics.Len() != 0 || *loads != 0 || runner.calls != 0 {
			t.Fatal("not a canonical offline paired export", err)
		}
		var got struct {
			SchemaVersion int
			Examples      []struct{ ID string }
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.SchemaVersion != 2 || len(got.Examples) != 1 || got.Examples[0].ID != "external" {
			t.Fatalf("lost selected external cases: %+v / %v", got, err)
		}
		for filename, before := range map[string]string{path: source, testsPath: tests} {
			after, err := os.ReadFile(filename)
			if err != nil || string(after) != before {
				t.Fatal("export changed source", err)
			}
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("export touched state", err)
		}
	}
}

func TestRunProfileExportPairFailureWritesNothing(t *testing.T) {
	for _, mode := range []string{"missing", "directory", "symlink", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			path := cliPreviewFile(t)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			testsPath := filepath.Join(filepath.Dir(path), "selected.md")
			want := "Tests"
			switch mode {
			case "directory":
				testsPath = filepath.Dir(path)
			case "symlink":
				if err := os.Symlink(path, testsPath); err != nil {
					t.Skip(err)
				}
			case "oversize":
				source = []byte(strings.Replace(strings.Split(string(source), "## Examples")[0], "Greet the input.", strings.Repeat("<", 240000), 1))
				if err := os.WriteFile(path, source, 0600); err != nil {
					t.Fatal(err)
				}
				var tests strings.Builder
				tests.WriteString("---\nschemaVersion: 1\n---\n")
				for i := 0; i < 15; i++ {
					fmt.Fprintf(&tests, "```yaml agent-test\nid: case-%d\ninput: '%s'\nassertions: [{id: text, type: contains, value: ok}]\n```\n", i, strings.Repeat("<", 16000))
				}
				if err := os.WriteFile(testsPath, []byte(tests.String()), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := agentfile.LoadPair(path, testsPath); err != nil {
					t.Fatal("oversize fixture must parse", err)
				}
				want = "cannot encode bounded run profile"
			}
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			deps.newCopilot = func() copilotAccess { t.Fatal("offline refusal accessed auth"); return nil }
			err = execute([]string{"package", "run-profile", "export", path, "--tests", testsPath}, deps)
			if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 || *loads != 0 || diagnostics.Len() != 0 {
				t.Fatalf("failure leaked output or loaded config: %v / %q / %q", err, out.String(), diagnostics.String())
			}
		})
	}
}

func TestRunProfileExportRefusesBeforeConfiguration(t *testing.T) {
	path := cliPreviewFile(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing file", []string{"package", "run-profile", "export"}, "arg"},
		{"extra file", []string{"package", "run-profile", "export", path, "PRIVATE_EXPORT_CANARY"}, "arg"},
		{"context prefix", []string{"--context", "PRIVATE_EXPORT_CANARY", "package", "run-profile", "export", path}, "--context"},
		{"context suffix", []string{"package", "run-profile", "export", path, "--context", "PRIVATE_EXPORT_CANARY"}, "--context"},
		{"engine", []string{"package", "run-profile", "export", path, "--container-engine", "PRIVATE_EXPORT_CANARY"}, "--container-engine"},
		{"model override", []string{"package", "run-profile", "export", path, "--model", "PRIVATE_EXPORT_CANARY"}, "unknown flag: --model"},
		{"output file", []string{"package", "run-profile", "export", path, "--out", "PRIVATE_EXPORT_CANARY"}, "unknown flag: --out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			runner := &cliPreviewFake{}
			deps.fileRunner = runner
			err := execute(tc.args, deps)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 || *loads != 0 || runner.calls != 0 {
				t.Fatalf("refusal=%v loads=%d calls=%d output=%q", err, *loads, runner.calls, out.String())
			}
			if strings.Contains(err.Error()+diagnostics.String(), "PRIVATE_EXPORT_CANARY") {
				t.Fatal("argument value leaked")
			}
		})
	}
	if err := os.WriteFile(path, []byte("# Display only\nPRIVATE_EXPORT_CANARY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	err := execute([]string{"package", "run-profile", "export", path}, deps)
	var refusal *agentfile.Error
	if !errors.As(err, &refusal) || out.Len() != 0 || *loads != 0 || strings.Contains(err.Error(), "PRIVATE_EXPORT_CANARY") {
		t.Fatal("display-only source exported or parser diagnostic lost", err)
	}
}

func TestRunProfileExportHelpCompletionInert(t *testing.T) {
	for _, args := range [][]string{
		{"package", "run-profile"}, {"package", "run-profile", "--help"}, {"package", "run-profile", "export", "--help"},
		{"__complete", "package", "run-profile", ""}, {"__complete", "package", "run-profile", "export", ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "absent-state")
			t.Setenv("KMX_HOME", home)
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			runner := &cliPreviewFake{}
			deps.fileRunner = runner
			if err := execute(args, deps); err != nil || *loads != 0 || runner.calls != 0 {
				t.Fatal("help/completion performed execution", err)
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatal("help/completion touched state", err)
			}
			if args[0] == "run-profile" && len(args) == 3 {
				for _, word := range []string{"offline", "stdout", "content", "not_run", "not a package"} {
					if !strings.Contains(out.String(), word) {
						t.Errorf("help missing %q", word)
					}
				}
			}
		})
	}
}

func TestRunProfileExportOutputFailureIsNotSuccess(t *testing.T) {
	for _, short := range []bool{false, true} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		deps.stdout = runProfileFailWriter{short: short}
		err := execute([]string{"package", "run-profile", "export", cliPreviewFile(t)}, deps)
		if err == nil || !strings.Contains(err.Error(), "write run profile") || strings.Contains(err.Error()+diagnostics.String(), "PRIVATE_WRITER_DETAIL") || *loads != 0 {
			t.Fatal("output failure reported success or leaked writer detail", err)
		}
	}
}

type runProfileFailWriter struct{ short bool }

func (w runProfileFailWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("PRIVATE_WRITER_DETAIL")
}
