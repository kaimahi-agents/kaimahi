package main

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if err := execute([]string{"run-profile", "export", path}, deps); err != nil {
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

func TestRunProfileExportRefusesBeforeConfiguration(t *testing.T) {
	path := cliPreviewFile(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing file", []string{"run-profile", "export"}, "arg"},
		{"extra file", []string{"run-profile", "export", path, "PRIVATE_EXPORT_CANARY"}, "arg"},
		{"context prefix", []string{"--context", "PRIVATE_EXPORT_CANARY", "run-profile", "export", path}, "--context"},
		{"context suffix", []string{"run-profile", "export", path, "--context", "PRIVATE_EXPORT_CANARY"}, "--context"},
		{"engine", []string{"run-profile", "export", path, "--container-engine", "PRIVATE_EXPORT_CANARY"}, "--container-engine"},
		{"model override", []string{"run-profile", "export", path, "--model", "PRIVATE_EXPORT_CANARY"}, "unknown flag: --model"},
		{"output file", []string{"run-profile", "export", path, "--out", "PRIVATE_EXPORT_CANARY"}, "unknown flag: --out"},
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
	err := execute([]string{"run-profile", "export", path}, deps)
	var refusal *agentfile.Error
	if !errors.As(err, &refusal) || out.Len() != 0 || *loads != 0 || strings.Contains(err.Error(), "PRIVATE_EXPORT_CANARY") {
		t.Fatal("display-only source exported or parser diagnostic lost", err)
	}
}

func TestRunProfileExportHelpCompletionInert(t *testing.T) {
	for _, args := range [][]string{
		{"run-profile"}, {"run-profile", "--help"}, {"run-profile", "export", "--help"},
		{"__complete", "run-profile", ""}, {"__complete", "run-profile", "export", ""},
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
		err := execute([]string{"run-profile", "export", cliPreviewFile(t)}, deps)
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
