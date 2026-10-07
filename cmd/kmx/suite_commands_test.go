package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite"
)

func TestSuiteValidateMinimalLayout(t *testing.T) {
	fixture := copySuiteFixture(t, filepath.Join("..", "..", "internal", "kmx", "agentsuite", "testdata", "minimal"))
	for _, tc := range []struct {
		name string
		args []string
		json bool
	}{
		{name: "text", args: []string{"suite", "validate", fixture}},
		{name: "json", args: []string{"suite", "validate", fixture, "--output", "json"}, json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(tc.args, deps); err != nil {
				t.Fatalf("execute(%v) error = %v\n%s", tc.args, err, diagnostics.String())
			}
			if *loads != 0 {
				t.Fatalf("offline suite validation loaded operational config %d time(s)", *loads)
			}
			if !tc.json {
				want := "AgentSuite minimal: conformant (agents=1 toolProviders=0 compositions=1 toolProviderCompositions=0 capabilities=none)\n"
				if out.String() != want {
					t.Fatalf("text output = %q, want %q", out.String(), want)
				}
				return
			}
			var report agentsuite.Report
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatalf("decode JSON output: %v\n%s", err, out.String())
			}
			if report.Name != "minimal" || report.Agents != 1 || report.ToolProviders != 0 || report.Compositions != 1 {
				t.Fatalf("unexpected JSON report: %+v", report)
			}
			if strings.TrimSpace(diagnostics.String()) != "" {
				t.Fatalf("unexpected diagnostics: %s", diagnostics.String())
			}
		})
	}
}

func copySuiteFixture(t *testing.T, source string) string {
	t.Helper()
	destination := t.TempDir()
	if err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}
