package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	kmxmcp "github.com/kaimahi-agents/kaimahi/internal/kmx/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func historyCLIFiles(t *testing.T, root string) *kmxmcp.Files {
	t.Helper()
	f, err := kmxmcp.OpenFilesWithHistory(root, func(p string, b []byte) error { _, err := agentfile.Parse(p, b); return err }, config.LocalAgentVersionDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// seedHistory creates one agent and edits it so one retained snapshot exists.
func seedHistory(t *testing.T, root string) (path string, current []byte) {
	t.Helper()
	source, err := os.ReadFile(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	f := historyCLIFiles(t, root)
	saved, err := f.Create(t.Context(), "a.agent.md", source)
	if err != nil {
		t.Fatal(err)
	}
	next := bytes.ReplaceAll(source, []byte("Greet the input."), []byte("Greet the input warmly."))
	if _, err := f.Edit(t.Context(), saved.Path, saved.Revision, next); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, saved.Path), next
}

func historyFileSet(t *testing.T, home string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(home, "local-preview", "versions"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			files[p] = true
		}
		return nil
	})
	return files
}

func TestMCPClearHistoryIsScopedAndStartupFree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	rootA, rootB := t.TempDir(), t.TempDir()
	sourceA, currentA := seedHistory(t, rootA)
	sourceB, currentB := seedHistory(t, rootB)
	all := historyFileSet(t, home)
	if len(all) < 2 {
		t.Fatalf("history was not retained: %v", all)
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	deps.stdin = nil // no protocol stdin, runner, provider or Squad access
	if err := execute([]string{"mcp", "--root", rootA, "--clear-history"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), "history") {
		t.Fatalf("clear used config or was silent: loads=%d out=%q", *loads, out.String())
	}
	after := historyFileSet(t, home)
	if len(after) == 0 || len(after) >= len(all) {
		t.Fatalf("clear did not remove only one root's history: %d -> %d", len(all), len(after))
	}
	for p := range after {
		if !all[p] {
			t.Fatalf("clear created %s", p)
		}
	}
	for path, want := range map[string][]byte{sourceA: currentA, sourceB: currentB} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("current source changed: %s %v", path, err)
		}
	}
	// Clearing again is a harmless no-op, and the other root can still be cleared.
	out.Reset()
	if err := execute([]string{"mcp", "--root", rootA, "--clear-history"}, deps); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"mcp", "--root", rootB, "--clear-history"}, deps); err != nil {
		t.Fatal(err)
	}
	if left := historyFileSet(t, home); len(left) != 0 {
		t.Fatalf("history left: %v", left)
	}
}

func TestMCPClearHistoryMissingHistoryIsInert(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	t.Setenv("KMX_HOME", home)
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	deps.stdin = nil
	if err := execute([]string{"mcp", "--root", t.TempDir(), "--clear-history"}, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) || *loads != 0 {
		t.Fatalf("missing-history clear created state or loaded config: %v %d", err, *loads)
	}
}

func TestMCPClearHistoryRefusals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	root := t.TempDir()
	source, current := seedHistory(t, root)
	before := historyFileSet(t, home)
	missing := filepath.Join(root, "missing")
	for _, args := range [][]string{
		{"mcp", "--clear-history"},
		{"mcp", "--root", root, "--clear-history", "--provider", "copilot"},
		{"mcp", "--root", root, "--clear-history", "--endpoint", "http://private.invalid"},
		{"mcp", "--root", root, "--clear-history", "--squad-program", "/private/squad"},
		{"mcp", "--root", root, "--clear-history", "--squad-release", "1.1.0"},
		{"mcp", "--root", root, "--clear-history", "--context", "private-context"},
		{"mcp", "--root", root, "--clear-history", "--container-engine", "docker"},
		{"mcp", "--root", ".", "--clear-history"},
		{"mcp", "--root", missing, "--clear-history"},
		{"mcp", "--root", source, "--clear-history"},
	} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute(args, deps)
		if err == nil || *loads != 0 || out.Len() != 0 || strings.Contains(err.Error(), "private") {
			t.Fatalf("accepted or leaked %v: %v %d %q", args, err, *loads, out.String())
		}
	}
	if !reflect.DeepEqual(before, historyFileSet(t, home)) {
		t.Fatal("refused clear changed history")
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, current) {
		t.Fatalf("refused clear changed source: %v", err)
	}
}

func TestMCPClearHistoryUnsafeStoreFailsSafely(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	root := t.TempDir()
	source, current := seedHistory(t, root)
	versions := filepath.Join(home, "local-preview", "versions")
	moved := versions + ".real"
	if err := os.Rename(versions, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, versions); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	err := execute([]string{"mcp", "--root", root, "--clear-history"}, deps)
	if err == nil || out.Len() != 0 || strings.Contains(err.Error(), home) || strings.Contains(err.Error(), "--clear-history") || !strings.Contains(err.Error(), "stop MCP") {
		t.Fatalf("unsafe history clear has missing/circular recovery advice: %v %q", err, out.String())
	}
	if got, readErr := os.ReadFile(source); readErr != nil || !bytes.Equal(got, current) {
		t.Fatalf("unsafe clear changed source: %v", readErr)
	}
	if err := os.Remove(versions); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, versions); err != nil {
		t.Fatal(err)
	}
	if left := historyFileSet(t, home); len(left) == 0 {
		t.Fatal("unsafe clear removed history through a link")
	}
}

func TestMCPClearHistoryReportsConfirmationFailureAfterClear(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	root := t.TempDir()
	seedHistory(t, root)
	var out, diag bytes.Buffer
	deps, _ := testDependencies(&out, &diag)
	deps.stdout = previewBrokenWriter{}
	err := execute([]string{"mcp", "--root", root, "--clear-history"}, deps)
	if err == nil || !strings.Contains(err.Error(), "was cleared") {
		t.Fatalf("lost acknowledged clear/output failure: %v", err)
	}
	if len(historyFileSet(t, home)) != 0 {
		t.Fatal("output error did not describe acknowledged clear")
	}
}

func TestMCPHelpDocumentsClearHistory(t *testing.T) {
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"mcp", "--help"}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), "--clear-history") {
		t.Fatal("help does not document inert --clear-history")
	}
}

func TestMCPStartupRetainsHistoryAndDiscoversVersionResource(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	t.Setenv("KMX_HOME", home)
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
	deps.stdin, deps.stdout = sr, sw
	p := filepath.Join(t.TempDir(), "Repo One", "Agent café.agent.md")
	source, err := os.ReadFile(cliPreviewFile(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := newRootCommand(&commandState{deps: deps})
	cmd.SetArgs([]string{"mcp"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "history-test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: cr, Writer: cw}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("startup created history state: %v", err)
	}
	templates, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tpl := range templates.ResourceTemplates {
		found[tpl.Name] = true
	}
	if !found["agent-version"] || !found["agent-view"] {
		t.Fatalf("missing old or version resource: %v", found)
	}
	call := func(args map[string]any, name string) map[string]any {
		r, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %+v", name, err, r)
		}
		b, _ := json.Marshal(r.StructuredContent)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		return v["file"].(map[string]any)
	}
	created := call(map[string]any{"schemaVersion": 1, "path": p, "content": string(source)}, "create")
	edited := call(map[string]any{"schemaVersion": 1, "path": p, "expectedRevision": created["revision"], "content": strings.Replace(string(source), "Greet the input.", "Greet warmly.", 1)}, "edit")
	if h, _ := edited["history"].(map[string]any); h["status"] != "retained" || edited["path"] != p || *loads != 0 {
		t.Fatalf("edit history = %v loads=%d", edited["history"], *loads)
	}
	cancel()
	<-done
}
