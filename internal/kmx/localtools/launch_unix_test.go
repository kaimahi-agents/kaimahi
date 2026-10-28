//go:build unix

package localtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
	"go.yaml.in/yaml/v3"
)

func launchFixture(t *testing.T, mode string) (string, map[string]any) {
	t.Helper()
	cfg := stdioFixture(t, mode)
	// Checked folder reads intentionally refuse symlink ancestors; macOS's
	// temporary root may use /var as an alias for /private/var.
	folder, err := filepath.EvalSymlinks(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(folder, "agent.agent.md")
	doc := map[string]any{"version": 1, "mcpServers": map[string]any{"notes": map[string]any{"command": cfg.Command, "args": cfg.Args, "env": map[string]string{"STDIO_FROZEN": "FROZEN_ENV"}, "tools": map[string]string{"ping": "remote_ping"}}}, "liveToolServers": []string{"notes"}}
	return file, doc
}
func writeLaunchFixture(t *testing.T, file string, doc map[string]any) {
	t.Helper()
	raw, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStdioSelectionLaunchAndSnapshot(t *testing.T) {
	file, doc := launchFixture(t, "structured")
	writeLaunchFixture(t, file, doc)
	owner := NewStdioInvocation(t.Context(), file, ServerSelection{})
	counts := map[string]int{}
	owner.lookup = func(key string) (string, bool) {
		counts[key]++
		if key == "FROZEN_ENV" {
			return "frozen-before-start", true
		}
		return "/usr/bin:/bin", true
	}
	host, err := owner.Resolve(t.Context(), []string{"ping"}, []ToolSetting{{Name: "ping", State: Enabled}})
	if err != nil || host == nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	owner.lookup = func(string) (string, bool) { t.Fatal("lookup after freeze"); return "", false }
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "kmx.yaml"), []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "one", Tool: "ping", Args: map[string]any{}, IdempotencyKey: "once", Mediation: api.MediationControllerMediated})
	if err != nil || result.Output["ok"] != true || counts["FROZEN_ENV"] != 1 || counts["PATH"] != 1 {
		t.Fatalf("frozen call: %+v %v %v", result, err, counts)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStdioSelectionCombinesHostsAndMemoizesRefs(t *testing.T) {
	file, doc := launchFixture(t, "structured")
	cfg := stdioFixture(t, "structured")
	doc["mcpServers"].(map[string]any)["second"] = map[string]any{"command": cfg.Command, "args": cfg.Args, "env": map[string]string{"STDIO_FROZEN": "FROZEN_ENV"}, "tools": map[string]string{"lookup": "remote_ping"}}
	doc["liveToolServers"] = []string{"notes", "second"}
	writeLaunchFixture(t, file, doc)
	owner := NewStdioInvocation(t.Context(), file, ServerSelection{})
	count := 0
	owner.lookup = func(key string) (string, bool) {
		if key == "FROZEN_ENV" {
			count++
			return "frozen-before-start", true
		}
		return "/usr/bin:/bin", true
	}
	host, err := owner.Resolve(t.Context(), []string{"ping", "lookup"}, []ToolSetting{{Name: "ping", State: Enabled}, {Name: "lookup", State: Enabled}})
	t.Cleanup(func() { _ = owner.Close() })
	if err != nil || len(host.Definitions()) != 2 || count != 1 {
		t.Fatalf("composite: %v count%d", err, count)
	}
	for _, name := range []string{"ping", "lookup"} {
		result, err := host.Execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: name, Tool: name, Args: map[string]any{}, IdempotencyKey: name, Mediation: api.MediationControllerMediated})
		if err != nil || result.Output["ok"] != true {
			t.Fatalf("composite call: %+v %v", result, err)
		}
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStdioSelectionNoUnneededWork(t *testing.T) {
	for _, tc := range []struct {
		selection ServerSelection
		names     []string
		state     ToolState
	}{
		{ServerSelection{Clear: true}, []string{"ping"}, Enabled},
		{ServerSelection{}, []string{"ping"}, Disabled},
		{ServerSelection{}, nil, Enabled},
	} {
		owner := NewStdioInvocation(t.Context(), "not-read.agent.md", tc.selection)
		owner.read = func(context.Context, string) ([]byte, error) { t.Fatal("read without live need"); return nil, nil }
		owner.lookup = func(string) (string, bool) { t.Fatal("env without live need"); return "", false }
		host, err := owner.Resolve(t.Context(), tc.names, []ToolSetting{{Name: "ping", State: tc.state}})
		if err != nil || host != nil || owner.Close() != nil {
			t.Fatal("unneeded resolution", err)
		}
	}
	file, doc := launchFixture(t, "structured")
	writeLaunchFixture(t, file, doc)
	owner := NewStdioInvocation(t.Context(), file, ServerSelection{Override: true})
	owner.lookup = func(string) (string, bool) { t.Fatal("unselected env lookup"); return "", false }
	if host, err := owner.Resolve(t.Context(), []string{"ping"}, []ToolSetting{{Name: "ping", State: Enabled}}); err != nil || host != nil {
		t.Fatal(err)
	}
}

func TestStdioSelectionPreflightAndPartialCleanup(t *testing.T) {
	for _, mode := range []string{"missing-env", "changed-settings", "overlap", "secret-arg", "cross-secret-arg", "cross-secret-command", "partial-start"} {
		t.Run(mode, func(t *testing.T) {
			file, doc := launchFixture(t, "structured")
			servers := doc["mcpServers"].(map[string]any)
			settings := []ToolSetting{{Name: "ping", State: Enabled}}
			if mode == "changed-settings" {
				doc["tools"] = map[string]string{"ping": "Disabled"}
			}
			if mode == "overlap" || mode == "partial-start" || mode == "secret-arg" || mode == "cross-secret-arg" || mode == "cross-secret-command" {
				tool := "ping"
				if mode == "partial-start" || mode == "secret-arg" || mode == "cross-secret-arg" || mode == "cross-secret-command" {
					tool = "lookup"
					settings = append(settings, ToolSetting{Name: tool, State: Enabled})
				}
				servers["second"] = map[string]any{"command": "/missing-mcp-executable", "tools": map[string]string{tool: "remote_ping"}}
				if mode == "secret-arg" || mode == "cross-secret-arg" {
					cfg := stdioFixture(t, "structured")
					servers["second"] = map[string]any{"command": cfg.Command, "args": append(cfg.Args, "frozen-before-start"), "env": map[string]string{"STDIO_FROZEN": "FROZEN_ENV"}, "tools": map[string]string{"lookup": "remote_ping"}}
				}
				if mode == "cross-secret-arg" {
					delete(servers["second"].(map[string]any), "env")
				}
				if mode == "cross-secret-command" {
					cfg := stdioFixture(t, "structured")
					command := filepath.Join(filepath.Dir(file), "frozen-before-start")
					if err := os.Link(cfg.Command, command); err != nil {
						t.Fatal(err)
					}
					servers["second"] = map[string]any{"command": command, "args": cfg.Args, "tools": map[string]string{"lookup": "remote_ping"}}
				}
				doc["liveToolServers"] = []string{"notes", "second"}
			}
			writeLaunchFixture(t, file, doc)
			owner := NewStdioInvocation(t.Context(), file, ServerSelection{})
			t.Cleanup(func() { _ = owner.Close() })
			owner.lookup = func(key string) (string, bool) {
				if mode == "missing-env" {
					return "", false
				}
				return "frozen-before-start", true
			}
			if _, err := owner.Resolve(t.Context(), []string{"ping", "lookup"}, settings); err == nil || strings.Contains(err.Error(), "frozen-before-start") {
				t.Fatal("unsafe/accepted config", err)
			}
			_ = owner.Close()
			for _, host := range owner.owned {
				if host.process.cmd.ProcessState == nil {
					t.Fatal("partial startup leaked root")
				}
			}
			if mode != "partial-start" && len(owner.owned) > 0 {
				t.Fatal("started before full config/env validation")
			}
		})
	}
}
