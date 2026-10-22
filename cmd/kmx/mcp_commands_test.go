package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStartupRefusalsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"mcp"}, {"mcp", "--root", "."}, {"mcp", "--root", t.TempDir(), "--context", "private-context"}, {"mcp", "--root", t.TempDir(), "--container-engine", "docker"}, {"mcp", "--root", t.TempDir(), "--provider", "compatible"}, {"mcp", "--root", t.TempDir(), "--endpoint", "http://private.invalid"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute(args, deps); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if *loads != 0 || out.Len() != 0 {
			t.Fatalf("startup side effects: %d %s", *loads, out.String())
		}
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"mcp", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), "--root") {
		t.Fatal("help is not inert/root explicit")
	}
}
func TestMCPCommandGracefulCancellation(t *testing.T) {
	sr, cw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cr, sw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	defer cw.Close()
	defer cr.Close()
	defer sw.Close()
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.stdin, deps.stdout = sr, sw
	serverCtx, stop := context.WithCancel(t.Context())
	defer stop()
	root := newRootCommand(&commandState{deps: deps})
	root.SetArgs([]string{"mcp", "--root", t.TempDir()})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(serverCtx) }()
	clientCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "shutdown-test", Version: "1"}, nil)
	cs, err := client.Connect(clientCtx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("intentional shutdown reported failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestMCPCommandSharedOperationParity(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	sr, cw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cr, sw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer sr.Close()
	defer cw.Close()
	defer cr.Close()
	defer sw.Close()
	var ignored, diag bytes.Buffer
	deps, loads := testDependencies(&ignored, &diag)
	deps.stdin = sr
	deps.stdout = sw
	root := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- execute([]string{"mcp", "--root", root}, deps) }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "cli-boundary-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	source, err := os.ReadFile(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the production dependency, including the fake route's instruction
	// interpreter, rather than substituting a scripted passing runner.
	source = bytes.Replace(source, []byte("Greet the input."), []byte("Reply with exactly `PRIVATE OPERAND`."), 1)
	call := func(name string, args map[string]any) *sdk.CallToolResult {
		t.Helper()
		r, err := cs.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	created := call("create", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "content": string(source)})
	if created.IsError {
		t.Fatalf("create: %+v", created)
	}
	for _, op := range []string{"run", "test"} {
		result := call(op, map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "caseId": "greeting"})
		if result.IsError {
			t.Fatalf("preview: %+v", result)
		}
		var out, cliDiag bytes.Buffer
		cliDeps, _ := testDependencies(&out, &cliDiag)
		if err := execute([]string{op, filepath.Join(root, "greeter.agent.md"), "--case", "greeting", "-o", "json"}, cliDeps); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(result.StructuredContent)
		var cliResult, mcpResult any
		if err := json.Unmarshal(out.Bytes(), &cliResult); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &mcpResult); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cliResult, mcpResult) {
			t.Fatalf("CLI/MCP differ: %s vs %s", out.String(), b)
		}
	}
	// Real instruction edit/fail/fix/pass loop over the offline fake model route;
	// this is not real-model or Sessions execution qualification.
	var previous struct{ File struct{ Revision string } }
	b, _ := json.Marshal(created.StructuredContent)
	if err := json.Unmarshal(b, &previous); err != nil {
		t.Fatal(err)
	}
	broken := call("edit", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "expectedRevision": previous.File.Revision, "content": strings.Replace(string(source), "`PRIVATE OPERAND`", "`different response`", 1)})
	if broken.IsError {
		t.Fatal("edit refused")
	}
	var changed struct{ File struct{ Revision string } }
	b, _ = json.Marshal(broken.StructuredContent)
	if err := json.Unmarshal(b, &changed); err != nil {
		t.Fatal(err)
	}
	failed := call("test", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"})
	b, _ = json.Marshal(failed.StructuredContent)
	var r app.FilePreviewResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if failed.IsError || r.State != "complete" || r.ReasonCode != "assertion_failed" || r.FullPass || r.File.Revision != changed.File.Revision {
		t.Fatalf("instruction regression hidden: %+v", r)
	}
	summary := failed.Content[0].(*sdk.TextContent).Text
	if !strings.Contains(summary, "greeting") || !strings.Contains(summary, "no journal") || strings.Contains(summary, "PRIVATE OPERAND") || strings.Contains(summary, "different response") || strings.Contains(summary, "schemaVersion") {
		t.Fatalf("failure summary lost or leaked content: %s", summary)
	}
	fixed := call("edit", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md", "expectedRevision": changed.File.Revision, "content": string(source)})
	if fixed.IsError {
		t.Fatal("fix refused")
	}
	passed := call("test", map[string]any{"schemaVersion": 1, "path": "greeter.agent.md"})
	b, _ = json.Marshal(passed.StructuredContent)
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if passed.IsError || !r.FullPass || r.File.Revision != previous.File.Revision || r.File.Revision == changed.File.Revision || r.Runner != "preview runner (no journal)" {
		t.Fatalf("full pass/revision/runner label lost: %+v", r)
	}
	if *loads != 0 || diag.Len() != 0 {
		t.Fatalf("protocol config/logs: %d %s", *loads, diag.String())
	}
	cs.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stdio server did not stop")
	}
}
