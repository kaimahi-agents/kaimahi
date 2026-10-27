package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestCopilotFileCLI(t *testing.T) {
	const notice = "Copilot gives this agent all tools; these tests run without tools. Add tools to choose which."
	for _, tools := range []string{"", "tools: []\n"} {
		for _, format := range []string{"text", "json"} {
			t.Run(tools+format, func(t *testing.T) {
				path := cliPreviewFile(t)
				source := "---\nname: Greeter\ndescription: PRIVATE DESCRIPTION\nmodel: fake\n" + tools + "future: {keep: 'PRIVATE METADATA'}\n---\nSay hello.\n\n## Examples\n```yaml\nexamples:\n  - id: greeting\n    input: PRIVATE INPUT\n    assertions:\n      - id: text\n        type: contains\n        value: PRIVATE OPERAND\n```\n"
				if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
				var out, diag bytes.Buffer
				deps, loads := testDependencies(&out, &diag)
				runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
				deps.fileRunner = runner
				if err := execute([]string{"test", path, "-o", format}, deps); err != nil {
					t.Fatal(err, out.String())
				}
				if runner.calls != 1 || *loads != 0 || len(runner.request.Agent.Tools) != 0 || strings.Contains(runner.request.Agent.Instructions, "future:") {
					t.Fatal("incorrect preview mapping")
				}
				if strings.Contains(out.String(), "PRIVATE") {
					t.Fatal("source leaked into safe output")
				}
				if strings.Contains(out.String(), notice) != (tools == "") {
					t.Fatalf("notice mismatch: %s", out.String())
				}
				if format == "json" && !json.Valid(out.Bytes()) {
					t.Fatal("invalid JSON")
				}
				saved, err := os.ReadFile(path)
				if err != nil || string(saved) != source {
					t.Fatal("Test rewrote source", err)
				}
			})
		}
	}
}
