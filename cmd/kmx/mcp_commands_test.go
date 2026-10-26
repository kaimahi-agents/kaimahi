package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess/copilot"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPStartupRefusalsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"mcp", "--root", ""}, {"mcp", "--root", "."}, {"mcp", "--root", t.TempDir(), "--context", "private-context"}, {"mcp", "--root", t.TempDir(), "--container-engine", "docker"}, {"mcp", "--root", t.TempDir(), "--provider", "compatible"}, {"mcp", "--root", t.TempDir(), "--endpoint", "http://private.invalid"}} {
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
func TestMCPNativeSquadStillRequiresRoot(t *testing.T) {
	for _, flags := range [][]string{{"--squad-program", "/missing/squad"}, {"--squad-release", "1.1.0"}, {"--squad-program", "/missing/squad", "--squad-release", "1.1.0"}} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(append([]string{"mcp"}, flags...), deps)
		if err == nil || err.Error() != `required flag(s) "root" not set` || *loads != 0 || out.Len() != 0 {
			t.Fatalf("native root requirement changed: %v, loads=%d, out=%s", err, *loads, out.String())
		}
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

// Initialization and discovery without --root must not execute a model or load
// cluster settings. One server can then author files in unrelated directories.
func TestMCPCommandUnboundStartup(t *testing.T) {
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
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	catalog, catalogs := &cliCopilot{}, 0
	deps.newCopilot = func() copilotAccess { catalogs++; return catalog }
	deps.stdin, deps.stdout, deps.fileRunner = sr, sw, runner
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := newRootCommand(&commandState{deps: deps})
	cmd.SetArgs([]string{"mcp"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "unbound-startup-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		select {
		case startupErr := <-done:
			t.Fatalf("unbound startup: %v; connect: %v", startupErr, err)
		default:
			t.Fatal(err)
		}
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 5 || runner.calls != 0 || *loads != 0 {
		t.Fatalf("startup effects: tools=%+v, err=%v, calls=%d, loads=%d", tools, err, runner.calls, *loads)
	}
	source, err := os.ReadFile(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(t.TempDir(), "one folder", "Agent café.agent.md"), filepath.Join(t.TempDir(), "greeter.agent.md")} {
		result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 1, "path": p, "content": string(source)}})
		if err != nil || result.IsError {
			t.Fatalf("create: %+v, %v", result, err)
		}
		content, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(content, source) {
			t.Fatal("unbound create did not save the requested file", err)
		}
	}
	if runner.calls != 0 || *loads != 0 || diag.Len() != 0 || catalogs != 0 {
		t.Fatalf("authoring effects: calls=%d, loads=%d, diag=%s, catalogs=%d", runner.calls, *loads, diag.String(), catalogs)
	}
	models, err := cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: "kmx://models"})
	if err != nil || len(models.Contents) != 1 || !strings.Contains(models.Contents[0].Text, `"gpt-test"`) || catalogs != 1 || catalog.list != 1 {
		t.Fatalf("runtime model resource: %+v / %v", models, err)
	}
	path := filepath.Join(t.TempDir(), "claude.agent.md")
	claude := bytes.ReplaceAll(source, []byte("**Preferred:** fake"), []byte("**Preferred:** claude-haiku-5.5"))
	created, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "create", Arguments: map[string]any{"schemaVersion": 1, "path": path, "content": string(claude)}})
	if err != nil || created.IsError {
		t.Fatalf("create Claude agent: %+v, %v", created, err)
	}
	for _, operation := range []string{"run", "test"} {
		result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 1, "path": path}})
		want := modelaccess.Route{Provider: "copilot", Model: "claude-haiku-5.5", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
		if err != nil || result.IsError || runner.request.Route != want || *loads != 0 {
			t.Fatalf("default MCP route=%+v err=%v config=%d", runner.request.Route, err, *loads)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMCPCommandCopilotRoute(t *testing.T) {
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
	runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
	deps.stdin, deps.stdout, deps.fileRunner = sr, sw, runner
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	source = bytes.ReplaceAll(source, []byte("**Preferred:** fake"), []byte("**Preferred:** gpt-test"))
	if err := os.WriteFile(filepath.Join(root, "copilot.agent.md"), source, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := newRootCommand(&commandState{deps: deps})
	cmd.SetArgs([]string{"mcp", "--root", root, "--provider", "copilot"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "copilot-route-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if runner.calls != 0 {
		t.Fatal("MCP startup executed a model")
	}
	for _, operation := range []string{"run", "test"} {
		result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 1, "path": "copilot.agent.md"}})
		want := modelaccess.Route{Provider: "copilot", Model: "gpt-test", Endpoint: modelaccess.CopilotEndpoint, Auth: "copilot"}
		if err != nil || result.IsError || runner.request.Route != want || *loads != 0 {
			t.Fatalf("MCP route=%+v err=%v config=%d", runner.request.Route, err, *loads)
		}
	}
	runner.cause = errors.Join(copilot.ErrAuthRequired, errors.New("PRIVATE AUTH CAUSE"))
	for _, operation := range []string{"run", "test"} {
		result, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"schemaVersion": 1, "path": "copilot.agent.md"}})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(result)
		if err != nil || !result.IsError || !strings.Contains(string(data), "copilot_auth_required") || strings.Contains(string(data), "PRIVATE") {
			t.Fatalf("unsafe MCP auth failure: %s / %v", data, err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMCPCommandSharedOperationParity(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", filepath.Join(home, "state"))
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
		for _, result := range []any{cliResult, mcpResult} {
			doc := result.(map[string]any)
			duration, ok := doc["durationMs"].(float64)
			if !ok || duration < 0 {
				t.Fatalf("missing or invalid total duration: %+v", doc)
			}
			delete(doc, "durationMs")
			for _, row := range doc["examples"].([]any) {
				example := row.(map[string]any)
				duration, ok := example["durationMs"].(float64)
				if !ok || duration < 0 {
					t.Fatalf("missing or invalid test duration: %+v", example)
				}
				delete(example, "durationMs")
			}
		}
		if !reflect.DeepEqual(cliResult, mcpResult) {
			t.Fatalf("CLI/MCP differ: %s vs %s", out.String(), b)
		}
	}
	// Real instruction edit/fail/fix/pass loop over the offline fake model route;
	// this exercises embedded Sessions, not real-model execution qualification.
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
	if !strings.Contains(summary, "greeting") || strings.Contains(summary, "no journal") || strings.Contains(summary, "PRIVATE OPERAND") || strings.Contains(summary, "different response") || strings.Contains(summary, "schemaVersion") {
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
	if passed.IsError || !r.FullPass || r.File.Revision != previous.File.Revision || r.File.Revision == changed.File.Revision || r.Runner != "" {
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
