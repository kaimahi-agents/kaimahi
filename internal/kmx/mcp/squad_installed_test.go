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

// Opt-in installed-dependency qualification. Init is a separately selected test
// setup action, outside SquadCreator; production only executes fixed cast calls.
func TestSquadInstalledCastEnrich(t *testing.T) {
	program := os.Getenv("KMX_TEST_SQUAD_EXECUTABLE")
	if program == "" {
		t.Skip("set an explicitly qualified Squad1.1.0 executable")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))
	git := exec.Command("git", "init", "--quiet", root)
	if err := git.Run(); err != nil {
		t.Fatal("test project initialization failed")
	}
	init := exec.Command(program, "init", "--no-workflows", "--no-vscode-default")
	init.Dir = root
	init.Env = squadEnvironment()
	if err := init.Run(); err != nil {
		t.Fatal("separately approved native init failed")
	}
	c, err := OpenSquad(root, SquadProgram{Path: program, Release: "1.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lead, greeter := squadLead(), enrichMember()
	lead.Model = "fake"
	greeter.Model = "fake"
	result, err := c.Create(context.Background(), SquadPlan{Lead: lead, Specialists: []SquadMember{greeter}})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectsUnknown || len(result.Members) != 2 {
		t.Fatal("uncertain real native outcome")
	}
	for i, row := range result.Members {
		if row.State != "enriched" {
			t.Fatal("real cast was not enriched")
		}
		kind := "catalog"
		member := lead
		if i == 1 {
			kind = "fallback"
			member = greeter
		}
		want, err := enrichSquad(member, enrichFixture(t, kind))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(row.File.Path)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("real native content differs from qualified byte-preserving enrichment")
		}
	}
	// No personal/global state should be bootstrapped as an incidental cast.
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("native setup/cast touched private home outside project")
	}
	before, _ := os.ReadFile(filepath.Join(root, ".squad/agents/leader/charter.md"))
	if _, err := c.Create(context.Background(), SquadPlan{Lead: lead}); err == nil {
		t.Fatal("repeat cast adopted a registered member")
	}
	after, _ := os.ReadFile(filepath.Join(root, ".squad/agents/leader/charter.md"))
	if !bytes.Equal(before, after) {
		t.Fatal("repeat changed the member")
	}
	t.Logf("actual native cast/enrich:2members; %dretained changed paths; no member execution or digest claim", len(result.ChangedPaths))
}
