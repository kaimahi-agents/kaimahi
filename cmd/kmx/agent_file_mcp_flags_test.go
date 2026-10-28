package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestFileMCPFlagsHelpAndIntake(t *testing.T) {
	for _, op := range []string{"run", "test"} {
		var out, diag bytes.Buffer
		deps, _ := testDependencies(&out, &diag)
		if err := execute([]string{op, "--help"}, deps); err != nil || !strings.Contains(out.String(), "--mcp-server") || !strings.Contains(out.String(), "--no-mcp-servers") {
			t.Fatal("selector help", err, out.String())
		}
		for _, args := range [][]string{{op, "--mcp-server", "notes"}, {op, "--no-mcp-servers"}, {op, "missing.agent.md", "--mcp-server", ""}} {
			if err := execute(args, deps); err == nil {
				t.Fatalf("invalid selector intake: %v", args)
			}
		}
	}
}
