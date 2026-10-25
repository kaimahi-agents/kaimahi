package localtools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-primitives/sessions/api"
	"github.com/agent-primitives/sessions/controller"
)

func TestFolderToolSettingsDefaultsAndExactFolder(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "agents")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte("tools: {ping: Disabled}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child, "one.agent.md")
	got, err := LoadSettings(t.Context(), file, []string{"ping"})
	if err != nil || !reflect.DeepEqual(got, []ToolSetting{{Name: "ping", State: Enabled}}) {
		t.Fatalf("ancestor applied: %+v %v", got, err)
	}
	body := "# keep me\npublishRepository: registry.example/team/agents\nother: {nested: [one, two]}\ntools: {ping: Disabled, another_agent: Enabled}\n"
	p := filepath.Join(child, "kmx.yaml")
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = LoadSettings(t.Context(), file, []string{"ping", "lookup"})
	if err != nil || !reflect.DeepEqual(got, []ToolSetting{{Name: "ping", State: Disabled}, {Name: "lookup", State: Enabled}}) {
		t.Fatalf("settings: %+v %v", got, err)
	}
	after, _ := os.ReadFile(p)
	if string(after) != body {
		t.Fatal("reader changed unrelated keys/comments")
	}
}

func TestFolderToolSettingsRejectAmbiguousAndInvalidFiles(t *testing.T) {
	for _, body := range []string{"", "[one]", "tools: {ping: Ask}", "tools: {ping: false}", "tools: {ping: Disabled, ping: Enabled}", "tools: null", "tools: {bad.name: Disabled}", "schemaVersion: 2", "tools: &settings {ping: Disabled}\ncopy: *settings", "other: {<<: {ping: Disabled}}", "tools: {ping: Disabled}\n---\nother: x", "other: {same: one, same: two}", "other: " + strings.Repeat("x", 256*1024)} {
		t.Run(body[:min(len(body), 28)], func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	root := t.TempDir()
	target := filepath.Join(root, "other.yaml")
	_ = os.WriteFile(target, []byte("tools: {ping: Disabled}"), 0600)
	if err := os.Symlink(target, filepath.Join(root, "kmx.yaml")); err == nil {
		if _, err := LoadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}); err == nil {
			t.Fatal("linked config accepted")
		}
	}
}

func TestDisabledHostReturnsAnswerWithoutLiveExecution(t *testing.T) {
	executions := 0
	host, err := NewTrustedHost(trustedDefinitions(), func(_ context.Context, _ controller.ToolCallContext, c api.ToolCall) (api.ToolResult, error) {
		executions++
		return api.ToolResult{ID: c.ID, Output: map[string]any{"ok": true}}, nil
	}, func(...string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	states := []ToolSetting{{Name: "lookup", State: Enabled}, {Name: "ping", State: Disabled}}
	wrapped, err := host.WithSettings(states)
	if err != nil {
		t.Fatal(err)
	}
	states[1].State = Enabled // Caller mutation cannot silently enable dispatch.
	result, err := wrapped.Execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "c", Tool: "ping", Mediation: api.MediationControllerMediated, IdempotencyKey: "e:c"})
	if err != nil || result.ID != "c" || result.Output["message"] != "This tool is disabled for this test." || executions != 0 {
		t.Fatalf("disabled ran live: %+v %v %d", result, err, executions)
	}
	_, err = wrapped.Execute(t.Context(), controller.ToolCallContext{SessionUID: "s"}, api.ToolCall{ID: "d", Tool: "lookup", Args: map[string]any{"query": "find"}, Mediation: api.MediationControllerMediated, IdempotencyKey: "e:d"})
	if err != nil || executions != 1 {
		t.Fatalf("enabled did not delegate: %v %d", err, executions)
	}
}
