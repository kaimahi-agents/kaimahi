package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownCommandsOfflineAndBeforeConfig(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	path, err := filepath.Abs("../../.github/agents/hello.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"cluster", "console", "--help"}, {"list", "--type", "md", "--directory", "../.."}, {"inspect", path}, {"test", "--help"},
		{"__complete", "chat", "--type", ""}, {"validate", path},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			var out, diagnostics bytes.Buffer
			deps, loads := testDependencies(&out, &diagnostics)
			if err := execute(args, deps); err != nil {
				t.Fatal(err)
			}
			if *loads != 0 {
				t.Fatal("loaded operational config")
			}
		})
	}
	for _, args := range [][]string{
		{"run", path, "--provider", "invalid"}, {"run", path, "--prompt", "hello", "--prompt-file", "-"},
		{"test", path, "--case", "missing"}, {"test", path, "--case="},
		{"inspect", path, "--context", "poison"}, {"inspect", path, "--container-engine", "docker"}, {"md", "list"},
	} {
		var out, diagnostics bytes.Buffer
		deps, loads := testDependencies(&out, &diagnostics)
		if err := execute(args, deps); err == nil {
			t.Fatalf("accepted %v", args)
		}
		if *loads != 0 {
			t.Fatal("loaded operational config")
		}
	}
}

func TestRegisteredAgentNameRoutesLocallyBeforeClusterDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	path := filepath.Join(t.TempDir(), "frogagent.agent.md")
	id := fmt.Sprintf("md-%x", sha256.Sum256([]byte(path)))
	dir := filepath.Join(home, "agents", "instances", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"schemaVersion": 1, "id": id, "name": "frogagent", "type": "md", "source": path, "version": "sha256:" + strings.Repeat("a", 64), "generation": 1, "state": "stopped"}
	raw, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"frogagent", id, strings.TrimPrefix(id, "md-")[:12]} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		err := execute([]string{"chat", selector}, deps)
		if err == nil || !strings.Contains(err.Error(), "is stopped") || *loads != 0 {
			t.Fatalf("selector=%s err=%v loads=%d", selector, err, *loads)
		}
	}
	for _, selector := range []string{id, strings.TrimPrefix(id, "md-")[:12]} {
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		if err := execute([]string{"inspect", selector, "-o", "json"}, deps); err != nil {
			t.Fatal(err)
		}
		if *loads != 0 || !strings.Contains(out.String(), id) {
			t.Fatalf("inspect: %s loads=%d", &out, *loads)
		}
	}
	var listOut, statusOut, diagnostics bytes.Buffer
	listDeps, _ := testDependencies(&listOut, &diagnostics)
	statusDeps, _ := testDependencies(&statusOut, &diagnostics)
	if err := execute([]string{"list", "--all", "-o", "json"}, listDeps); err != nil {
		t.Fatal(err)
	}
	if err := execute([]string{"list", "--all", "-o", "json"}, statusDeps); err != nil {
		t.Fatal(err)
	}
	if listOut.String() != statusOut.String() {
		t.Fatal("list/status aliases diverged")
	}
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	deps.loadConfig = func(string, string) (*config.Config, error) {
		*loads++
		return nil, fmt.Errorf("explicit Orka selection")
	}
	err := execute([]string{"chat", "frogagent", "--type", "orka"}, deps)
	if err == nil || !strings.Contains(err.Error(), "explicit Orka") || *loads != 1 {
		t.Fatalf("err=%v loads=%d", err, *loads)
	}
}

func TestMarkdownRedirectedBrowserListsWithoutCopilot(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	deps.stdin = in
	if err := execute([]string{"list", "--type", "md", "--definitions", "--directory", "../.."}, deps); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 || !strings.Contains(out.String(), "hello.agent.md") || strings.Contains(diagnostics.String(), "Copilot CLI found") {
		t.Fatalf("out=%s diagnostics=%s loads=%d", &out, &diagnostics, *loads)
	}
}

func TestTypedAgentNamesAndCreationStayOffline(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	var out, diagnostics bytes.Buffer
	deps, loads := testDependencies(&out, &diagnostics)
	for _, args := range [][]string{{"inspect", "hello", "--type", "md", "--directory", "../.."}, {"validate", "hello", "--directory", "../.."}} {
		if err := execute(args, deps); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	if err := execute([]string{"create", "greeting", "--type", "md", "--directory", root}, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "greeting", "greeting.agent.md")); err != nil {
		t.Fatal(err)
	}
	if *loads != 0 {
		t.Fatal("local typed route loaded cluster settings")
	}
}

func TestRunAndTestResolveStoppedShortIDToSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KMX_HOME", home)
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	var out, diag bytes.Buffer
	deps, loads := testDependencies(&out, &diag)
	if err := execute([]string{"create", "identity-test", "--type", "md", "--directory", root}, deps); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "identity-test", "identity-test.agent.md")
	id := fmt.Sprintf("md-%x", sha256.Sum256([]byte(source)))
	dir := filepath.Join(home, "agents", "instances", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "id": id, "name": "identity-test", "type": "md", "source": source, "version": "sha256:" + strings.Repeat("a", 64), "generation": 1, "state": "stopped"})
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	short := strings.TrimPrefix(id, "md-")[:12]
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"test", short, "--type", "md", "--case", "does-not-exist", "--provider", "copilot"}, "unknown test ID"}, {[]string{"run", short, "--type", "md", "--prompt", "", "--provider", "copilot"}, "nonempty prompt"}} {
		out.Reset()
		err := execute(tc.args, deps)
		if err == nil || !strings.Contains(err.Error(), tc.want) || *loads != 0 {
			t.Fatalf("%v: %v loads=%d", tc.args, err, *loads)
		}
	}
}

func TestMarkdownUpdateHintIncludesQuotedCommand(t *testing.T) {
	state := &commandState{deps: productionDependencies()}
	state.deps.executable = "/path with spaces/kmx"
	cmd := markdownOperation(state, "run")
	if err := cmd.ParseFlags([]string{"--model", "model-v2", "--tests", "/my tests/cases.md"}); err != nil {
		t.Fatal(err)
	}
	err := markdownUpdateHint(state, cmd, "/my agents/frog.agent.md", fmt.Errorf("changed"))
	want := "'/path with spaces/kmx' run '/my agents/frog.agent.md' --update --tests '/my tests/cases.md' --model model-v2"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("missing command: %v", err)
	}
}
