package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFileToolBindingsExplicitAndInert(t *testing.T) {
	file := cliPreviewFile(t)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, bytes.Replace(raw, []byte("## Examples"), []byte("## Tools\n- github_list_merged_prs\n\n## Examples"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"run", "test"} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		f := &cliPreviewFake{verdict: runtime.EvaluationPass}
		deps.fileRunner = f
		if err = execute([]string{op, file, "--tool-bindings", "host-binding.json"}, deps); err != nil || f.calls != 1 || f.request.ToolBindings != "host-binding.json" || *loads != 0 {
			t.Fatalf("wiring: %v calls=%d", err, f.calls)
		}
		out.Reset()
		diag.Reset()
		deps.fileRunner = app.NewLocalFileRunner()
		if err = execute([]string{op, file}, deps); err == nil || !strings.Contains(out.String(), "tools") || *loads != 0 {
			t.Fatal("missing provider did not refuse", err, out.String())
		}
	}
	for _, args := range [][]string{{"test", "--tool-bindings", "host-binding.json"}, {"run", "--tool-bindings", "host-binding.json"}, {"test", file, "--tool-bindings", ""}, {"test", "--help"}, {"run", "--help"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		f := &cliPreviewFake{}
		deps.fileRunner = f
		err = execute(args, deps)
		help := args[len(args)-1] == "--help"
		if help != (err == nil) || f.calls != 0 || *loads != 0 {
			t.Fatalf("inert flag %v err=%v", args, err)
		}
	}
}
