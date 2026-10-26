package localtools

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func settingsRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFolderToolSettingsDefaultsAndExactFolder(t *testing.T) {
	root := settingsRoot(t)
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
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != body {
		t.Fatal("reader changed unrelated keys/comments")
	}
}

func TestFolderToolSettingsRejectAmbiguousAndInvalidFiles(t *testing.T) {
	for _, body := range []string{"", "[one]", "tools: {ping: Ask}", "tools: {ping: false}", "tools: {ping: Disabled, ping: Enabled}", "tools: null", "tools: {bad.name: Disabled}", "schemaVersion: 2", "tools: &settings {ping: Disabled}\ncopy: *settings", "other: {<<: {ping: Disabled}}", "tools: {ping: Disabled}\n---\nother: x", "other: {same: one, same: two}", "other: " + strings.Repeat("x", 256*1024)} {
		t.Run(body[:min(len(body), 28)], func(t *testing.T) {
			root := settingsRoot(t)
			if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	root := settingsRoot(t)
	target := filepath.Join(root, "other.yaml")
	if err := os.WriteFile(target, []byte("tools: {ping: Disabled}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "kmx.yaml")); err == nil {
		if _, err := LoadSettings(t.Context(), filepath.Join(root, "agent.agent.md"), []string{"ping"}); err == nil {
			t.Fatal("linked config accepted")
		}
	}
}

func TestSettingsCopyAndDisabledResultAreDetached(t *testing.T) {
	states := []ToolSetting{{Name: "lookup", State: Enabled}, {Name: "ping", State: Disabled}}
	frozen, err := CopySettings(states, trustedDefinitions())
	if err != nil {
		t.Fatal(err)
	}
	states[1].State = Enabled
	if !IsDisabled(frozen, "ping") {
		t.Fatal("caller mutation changed the frozen policy")
	}
	result := DisabledResult("c")
	if result.ID != "c" || result.Output["message"] != "This tool is disabled for this test." {
		t.Fatal("invalid disabled answer")
	}
	result.Output["status"] = "changed"
	if DisabledResult("d").Output["status"] != "disabled" {
		t.Fatal("shared mutable answer")
	}
}
