package localtools

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSettingsRevisionUsesExactReadBytes(t *testing.T) {
	root := settingsRoot(t)
	file := filepath.Join(root, "one.agent.md")
	snap, err := ReadSettingsSnapshot(t.Context(), file, []string{"ping"})
	if err != nil || snap.Revision != "absent" || !reflect.DeepEqual(snap.Settings, []ToolSetting{{Name: "ping", State: Enabled}}) {
		t.Fatalf("absent: %+v %v", snap, err)
	}
	for _, body := range []string{"tools: {ping: Disabled}\n", "# comment\ntools: {ping: Disabled}\n"} {
		if err := os.WriteFile(filepath.Join(root, "kmx.yaml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		next, err := ReadSettingsSnapshot(t.Context(), file, []string{"ping"})
		if err != nil || next.Revision == snap.Revision || !strings.HasPrefix(next.Revision, "sha256:") || next.Settings[0].State != Disabled {
			t.Fatalf("snapshot: %+v %v", next, err)
		}
		snap = next
	}
}

func TestSettingsMergePreservesUnrelatedNodesAndCheckedNoop(t *testing.T) {
	original := []byte("# heading\r\nversion: 1\r\nother: # note\r\n  nested: [one, two]\r\ntools:\r\n  other_agent: Disabled # retain\r\n  ping: Enabled\r\n")
	raw, states, changed, err := ChangeToolState(original, []string{"ping"}, "ping", Disabled)
	if err != nil || !changed || !reflect.DeepEqual(states, []ToolSetting{{Name: "ping", State: Disabled}}) {
		t.Fatalf("merge: %s %+v %v", raw, states, err)
	}
	for _, s := range []string{"# heading", "# note", "nested: [one, two]", "other_agent: Disabled # retain", "ping: Disabled"} {
		if !bytes.Contains(raw, []byte(s)) {
			t.Fatalf("lost %q: %s", s, raw)
		}
	}
	unchanged, _, changed, err := ChangeToolState(raw, []string{"ping"}, "ping", Disabled)
	if err != nil || changed || !bytes.Equal(unchanged, raw) {
		t.Fatal("no-op reformatted bytes")
	}
	enabled, _, changed, err := ChangeToolState(raw, []string{"ping"}, "ping", Enabled)
	if err != nil || !changed || !bytes.Contains(enabled, []byte("ping: Enabled")) {
		t.Fatal("Enabled key removed")
	}
	absent, _, changed, err := ChangeToolState(nil, []string{"ping"}, "ping", Enabled)
	if err != nil || changed || absent != nil {
		t.Fatal("default Enabled created a file")
	}
	created, _, changed, err := ChangeToolState(nil, []string{"ping"}, "ping", Disabled)
	if err != nil || !changed || !bytes.Contains(created, []byte("version: 1")) {
		t.Fatalf("creation: %s %v", created, err)
	}
	for _, body := range []string{"tools: {ping: Ask}", "tools: {ping: Enabled, ping: Disabled}", "tools: &x {ping: Enabled}\ncopy: *x", "", strings.Repeat("x", 256*1024+1)} {
		if _, _, _, err := ChangeToolState([]byte(body), []string{"ping"}, "ping", Disabled); err == nil {
			t.Fatalf("invalid input accepted %q", body[:min(len(body), 30)])
		}
	}
	for _, name := range []string{"undeclared", ""} {
		if _, _, _, err := ChangeToolState(nil, []string{"ping"}, name, Disabled); err == nil {
			t.Fatal("undeclared mutation")
		}
	}
	if _, _, _, err := ChangeToolState(nil, []string{"ping"}, "ping", ToolState("Ask")); err == nil {
		t.Fatal("Ask accepted")
	}
}
