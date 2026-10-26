package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileCommandsReadFolderToolSettings(t *testing.T) {
	for _, op := range []string{"run", "test"} {
		for _, state := range []string{"Disabled", "Ask"} {
			t.Run(op+state, func(t *testing.T) {
				file := cliPreviewFile(t)
				raw, _ := os.ReadFile(file)
				if err := os.WriteFile(file, []byte(strings.Replace(string(raw), "## Examples", "## Tools\n- ping\n## Examples", 1)), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), []byte("tools: {ping: "+state+"}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
				deps.fileRunner = fake
				args := []string{op, file, "-o", "json"}
				if op == "run" {
					args = append(args, "--prompt", "ad-hoc")
				}
				err := execute(args, deps)
				if *loads != 0 {
					t.Fatal("folder settings loaded operational config")
				}
				if state == "Ask" {
					if err == nil || fake.calls != 0 {
						t.Fatal("invalid settings reached runner")
					}
					return
				}
				var result app.FilePreviewResult
				if json.Unmarshal(out.Bytes(), &result) != nil {
					t.Fatal("missing result")
				}
				if err != nil || fake.calls != 1 || len(fake.request.ToolSettings) != 1 || fake.request.ToolSettings[0].State != localtools.Disabled || len(result.ToolSettings) != 1 || result.ToolSettings[0].State != localtools.Disabled {
					t.Fatalf("CLI bypassed folder settings: %+v %v", result, err)
				}
			})
		}
	}
}
