package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
)

func TestFileMCPRunnerDoesNotChangeOtherPaths(t *testing.T) {
	for _, mode := range []string{"no CLI intent", "fixed binding", "tool free"} {
		base := &cliPreviewFake{}
		runner := &fileMCPRunner{base: base, owner: func(context.Context, string, localtools.ServerSelection) fileMCPOwner {
			t.Fatal("non-CLI/legacy path constructed launcher")
			return nil
		}}
		ctx := t.Context()
		req := agentpreview.Request{Agent: &agentfile.Agent{Tools: []string{"ping"}}}
		if mode != "no CLI intent" {
			ctx = context.WithValue(ctx, fileMCPIntentKey{}, fileMCPIntent{file: "not-read.agent.md"})
		}
		if mode == "fixed binding" {
			req.ToolBindings = "not-read-binding"
		}
		if mode == "tool free" {
			req.Agent.Tools = nil
		}
		if _, err := runner.Run(ctx, req); err != nil || base.calls != 1 {
			t.Fatalf("base path changed: %s %v", mode, err)
		}
	}
}

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
