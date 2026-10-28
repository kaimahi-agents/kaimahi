package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localpreview"
)

func TestFilePairOptionRefusalStillWritesSummary(t *testing.T) {
	file, _ := cliPairFiles(t, false)
	summary := filepath.Join(t.TempDir(), "step.md")
	t.Setenv("GITHUB_STEP_SUMMARY", summary)
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	err := execute([]string{"test", file, "--model", "bad model!", "-o", "json", "--github-summary"}, deps)
	if err == nil || exitCode(err) != 2 || strings.Contains(err.Error(), "cannot write GitHub") {
		t.Fatalf("option refusal status replaced by summary failure: %v", err)
	}
	data, readErr := os.ReadFile(summary)
	if readErr != nil || !bytes.Contains(data, []byte("UNKNOWN")) || bytes.Contains(data, []byte(file)) {
		t.Fatalf("missing/unsafe refusal summary: %s %v", data, readErr)
	}
}

func TestFilePairExplainUsesCurrentTestsSelection(t *testing.T) {
	t.Setenv("KMX_HOME", privateCommandHome(t))
	file, tests := cliPairFiles(t, false)
	failing := strings.Replace(cliPairTests, "value: Hello from pair", "value: not the answer", 1)
	if err := os.WriteFile(tests, []byte(failing), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	deps.fileRunner = localpreview.NewRunner(localpreview.RunnerOptions{JournalDir: func() (string, error) { return privateCommandHome(t), nil }})
	if err := execute([]string{"test", file, "-o", "json"}, deps); err == nil || exitCode(err) != 1 {
		t.Fatalf("failing pair: %v %s", err, out.String())
	}
	var result app.FilePreviewResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Retention.Status != "retained" || len(result.Retention.Diagnostics) != 1 {
		t.Fatalf("failure not retained: %v %s", err, out.String())
	}
	id := result.Retention.Diagnostics[0].ID
	explain := func(selector, want string) {
		t.Helper()
		out.Reset()
		diag.Reset()
		args := []string{"test", "explain", id, "--file", file}
		if selector != "" {
			args = append(args, "--tests", selector)
		}
		if err := execute(args, deps); err != nil || !strings.Contains(out.String(), " · "+want+"\n") {
			t.Fatalf("currentness %s: %v %s", want, err, out.String())
		}
	}
	explain("", "current")
	override := filepath.Join(t.TempDir(), "same bytes.md")
	if err := os.WriteFile(override, []byte(failing), 0600); err != nil {
		t.Fatal(err)
	}
	explain(override, "historical")
	if err := os.WriteFile(tests, []byte(strings.Replace(failing, "input: Ada", "input: Bea", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	explain("", "historical")
	if *loads != 0 {
		t.Fatal("pair execution/Explain loaded cluster configuration")
	}
}
