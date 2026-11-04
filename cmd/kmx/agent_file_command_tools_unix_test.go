//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"go.yaml.in/yaml/v3"
)

func commandToolFixture(t *testing.T, state string) (string, string) {
	t.Helper()
	file := cliPreviewFile(t)
	source := "# Command recorder\n## Identity\n- **ID:** command-recorder\n- **Purpose:** Run a fixed check.\n## Accountable Responsibilities\nCall the tool `check`. Reply with exactly `done`.\n## Non-Responsibilities\nNo other effects.\n## Collaboration and Review Authority\nNone.\n## Model\n- **Preferred:** fake\n## Tools\n- check\n## Examples\n```yaml\nexamples:\n  - id: sample\n    input: hello\n    mocks:\n      tools:\n        - name: check\n          mode: recorded\n          calls: []\n    assertions:\n      - id: calls\n        type: toolCalled\n        tool: check\n```\n"
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	doc := map[string]any{
		"tools": map[string]string{"check": state},
		"commandTools": map[string]any{"check": map[string]any{
			"command": program,
			"args":    []string{"-test.run=^TestCLICommandToolHelperProcess$", "--", marker},
			"env":     map[string]string{"GO_WANT_COMMAND_TOOL_HELPER": "HOST_COMMAND_TOOL_HELPER"},
		}},
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOST_COMMAND_TOOL_HELPER", "yes")
	t.Setenv("COMMAND_TOOL_AMBIENT", "must-not-inherit")
	t.Setenv("KMX_HOME", privateCommandHome(t))
	return file, marker
}

func TestFileCLICommandToolCaptureReplayAndDisable(t *testing.T) {
	for _, state := range []string{"Enabled", "Disabled"} {
		t.Run(state, func(t *testing.T) {
			file, marker := commandToolFixture(t, state)
			var out, diag bytes.Buffer
			deps, loads := testDependencies(&out, &diag)
			deps.fileRunner = productionDependencies().fileRunner
			err := execute([]string{"test", file, "-o", "json", "--no-mcp-servers"}, deps)
			if state == "Disabled" {
				var commandErr *fileCommandError
				if !errors.As(err, &commandErr) || commandErr.exit != 1 {
					t.Fatalf("disabled result: %v\n%s\n%s", err, out.String(), diag.String())
				}
				if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
					t.Fatal("disabled command started")
				}
				return
			}
			if err != nil || *loads != 0 {
				t.Fatalf("live command: %v\n%s\n%s", err, out.String(), diag.String())
			}
			var result app.FilePreviewResult
			if json.Unmarshal(out.Bytes(), &result) != nil || !result.FullPass || result.SavedRevision == "" || result.Examples[0].Tools[0].Source != "live" {
				t.Fatalf("capture: %+v", result)
			}
			if raw, readErr := os.ReadFile(marker); readErr != nil || string(raw) != "ran" {
				t.Fatalf("command did not execute: %q %v", raw, readErr)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			// Replay does not parse launch details, resolve environment or start a process.
			if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), []byte("tools: {check: Enabled}\ncommandTools: false\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			diag.Reset()
			if err := execute([]string{"test", file, "-o", "json"}, deps); err != nil {
				t.Fatal("offline replay", err, out.String(), diag.String())
			}
			if json.Unmarshal(out.Bytes(), &result) != nil || !result.FullPass || result.Examples[0].Tools[0].Source != "recorded" {
				t.Fatalf("replay: %+v", result)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatal("replay started command")
			}
		})
	}
}

func TestCLICommandToolHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_COMMAND_TOOL_HELPER") != "yes" {
		return
	}
	if os.Getenv("COMMAND_TOOL_AMBIENT") != "" {
		os.Exit(90)
	}
	marker := os.Args[len(os.Args)-1]
	if !strings.HasSuffix(marker, "started") || os.WriteFile(marker, []byte("ran"), 0600) != nil {
		os.Exit(91)
	}
	_, _ = os.Stdout.WriteString("checked\n")
	os.Exit(0)
}
