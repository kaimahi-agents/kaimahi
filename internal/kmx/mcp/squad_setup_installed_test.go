package mcp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSquadInstalledSetupNestedProjectStaysLocal(t *testing.T) {
	program := os.Getenv("KMX_TEST_SQUAD_EXECUTABLE")
	if program == "" {
		t.Skip("requires separately qualified standalone Squad1.1.0")
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "--quiet", parent).Run(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "nested")
	os.Mkdir(root, 0755)
	c, err := OpenSquadSetup(root, SquadProgram{Path: program, Release: "1.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(parent, ".github")); !os.IsNotExist(err) {
		t.Fatal("native setup escaped to ancestor Git root")
	}
	if _, err := os.Stat(filepath.Join(root, ".github/agents/squad.agent.md")); err != nil {
		t.Fatal("native capability not rooted locally")
	}
}
func TestSquadInstalledSetupCreate(t *testing.T) {
	program := os.Getenv("KMX_TEST_SQUAD_EXECUTABLE")
	if program == "" {
		t.Skip("requires separately qualified standalone Squad1.1.0")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	if err := exec.Command("git", "init", "--quiet", root).Run(); err != nil {
		t.Fatal(err)
	}
	// A command-scope origin override must stop native platform introspection.
	if err := exec.Command("git", "-C", root, "config", "remote.origin.url", "https://dev.azure.com/example/project/_git/example").Run(); err != nil {
		t.Fatal(err)
	}
	c, err := OpenSquadSetup(root, SquadProgram{Path: program, Release: "1.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lead, greeter := squadLead(), enrichMember()
	lead.Model = "fake"
	greeter.Model = "fake"
	got, err := c.Create(context.Background(), SquadPlan{Lead: lead, Specialists: []SquadMember{greeter}})
	if err != nil {
		t.Fatalf("integrated native setup failed: %+v %v", got, err)
	}
	if got.Setup != "initialized" || got.EffectsUnknown || len(got.Members) != 2 || got.Members[1].State != "enriched" {
		t.Fatalf("wrong native lifecycle: %+v", got)
	}
	config, err := os.ReadFile(filepath.Join(root, ".squad/config.json"))
	if err != nil || bytes.Contains(config, []byte("platform")) {
		t.Fatal("native setup performed origin platform discovery")
	}
	for i, name := range []string{"catalog", "fallback"} {
		m := lead
		if i == 1 {
			m = greeter
		}
		want, err := enrichSquad(m, enrichFixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(root, got.Members[i].File.Path))
		if err != nil || !bytes.Equal(raw, want) {
			t.Fatal("native fixture source not preserved")
		}
		d, err := c.Member(context.Background(), m.ID)
		if err != nil || !d.Enriched || d.Profile.Role != m.DisplayRole {
			t.Fatalf("actual native mapping not reconstructed: %v", err)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("native setup touched private HOME/global footprint")
	}
	t.Logf("actual init→cast→enrich: %d members; %d retained changed paths; no model or dispatch", len(got.Members), len(got.ChangedPaths))
}
