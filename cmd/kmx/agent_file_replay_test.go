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
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileCLIReplayAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, mock, state, source string
		wantExit                  int
		wantNote                  bool
	}{
		{"pending", "          mode: recorded\n          calls: []\n", "Enabled", "unavailable", 0, true},
		{"no mock", "", "Enabled", "unavailable", 0, true},
		{"disabled", "          mode: recorded\n          calls: []\n", "Disabled", "unavailable", 1, false},
		{"recorded", "          mode: recorded\n          description: Saved ping\n          parameters: {type: object}\n          calls: [{arguments: {}, result: {value: private-saved-result}}]\n", "Enabled", "recorded", 0, false},
		{"mismatch", "          mode: recorded\n          description: Saved ping\n          parameters: {type: object}\n          calls: [{arguments: {different: true}, result: {value: private-saved-result}}]\n", "Enabled", "", 2, false},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				file := cliPreviewFile(t)
				mocks := ""
				if tc.mock != "" {
					mocks = "    mocks:\n      tools:\n        - name: ping\n" + tc.mock
				}
				source := "# Recorder\n## Identity\n- **ID:** recorder\n- **Purpose:** Record.\n## Accountable Responsibilities\nCall the tool `ping`. Reply with exactly `done`.\n## Non-Responsibilities\nNo effects.\n## Collaboration and Review Authority\nNone.\n## Model\n- **Preferred:** fake\n## Tools\n- ping\n## Examples\n```yaml\nexamples:\n  - id: sample\n    input: hello\n" + mocks + "    assertions:\n      - id: calls\n        type: toolCalled\n        tool: ping\n```\n"
				if err := os.WriteFile(file, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				folder := filepath.Dir(file)
				if err := os.WriteFile(filepath.Join(folder, "kmx.yaml"), []byte("tools: {ping: "+tc.state+"}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("KMX_HOME", privateCommandHome(t))
				summary := filepath.Join(folder, "summary.md")
				t.Setenv("GITHUB_STEP_SUMMARY", summary)
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				deps.fileRunner = app.NewLocalFileRunner()
				args := []string{"test", file, "-o", format}
				if format == "json" {
					args = append(args, "--github-summary")
				}
				err := execute(args, deps)
				exit := 0
				if err != nil {
					var commandError *fileCommandError
					if !errors.As(err, &commandError) {
						t.Fatalf("unexpected error: %v", err)
					}
					exit = commandError.exit
				}
				if exit != tc.wantExit || *loads != 0 {
					t.Fatalf("exit=%d want=%d config loads=%d: %v\n%s\n%s", exit, tc.wantExit, *loads, err, out.String(), diag.String())
				}
				if format == "text" {
					if got := strings.Contains(out.String(), "No recording; tool unavailable."); got != tc.wantNote {
						t.Fatalf("availability=%v want=%v:\n%s", got, tc.wantNote, out.String())
					}
				} else {
					var result app.FilePreviewResult
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if tc.name == "mismatch" {
						if result.ReasonCode != "recording_mismatch" || result.FullPass {
							t.Fatalf("mismatch lost: %+v", result)
						}
					} else {
						tool := result.Examples[0].Tools[0]
						wantCalls := 1
						if tc.state == "Disabled" {
							wantCalls = 0
						}
						if tool.Source != tc.source || tool.CallCount != wantCalls || result.SavedRevision != "" || tool.RecordingSaved || result.FullPass != (tc.wantExit == 0) || string(result.ToolSettings[0].State) != tc.state {
							t.Fatalf("source/count/verdict/capture changed: %+v", result)
						}
						if tc.wantExit == 0 && result.Verdict != runtime.EvaluationPass {
							t.Fatal("request-based Calls no longer passes")
						}
					}
					markdown, err := os.ReadFile(summary)
					if err != nil {
						t.Fatal(err)
					}
					if got := bytes.Contains(markdown, []byte("Unavailable tools have no recording or live source")); got != tc.wantNote {
						t.Fatalf("summary availability=%v want=%v:\n%s", got, tc.wantNote, markdown)
					}
					if bytes.Contains(markdown, []byte("private-saved-result")) || bytes.Contains(out.Bytes(), []byte("private-saved-result")) {
						t.Fatal("report leaked saved content")
					}
				}
				if got, err := os.ReadFile(file); err != nil || string(got) != source {
					t.Fatalf("replay/unavailable changed source: %v", err)
				}
			})
		}
	}
}
