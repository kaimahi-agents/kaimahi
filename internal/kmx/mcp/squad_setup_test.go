package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The init child simulates only the external dependency. All orchestration,
// validation, rooted reads and publication execute production code.
func squadInitTestChild() int {
	if len(os.Args) != 4 || os.Args[2] != "--no-workflows" || os.Args[3] != "--no-vscode-default" {
		return 9
	}
	root, _ := os.Getwd()
	exe, _ := os.Executable()
	if os.Getenv("SQUAD_NO_PERSONAL") != "1" || os.Getenv("SQUAD_STANDALONE_HOME") != filepath.Dir(exe) || os.Getenv("GIT_WORK_TREE") != root || os.Getenv("GIT_CEILING_DIRECTORIES") != filepath.Dir(root) || os.Getenv("GIT_CONFIG_KEY_0") != "remote.origin.url" || os.Getenv("GIT_CONFIG_VALUE_0") != "" {
		return 8
	}
	os.WriteFile(".init-called", []byte("once"), 0600)
	os.Mkdir(".squad", 0755)
	os.WriteFile(".squad/config.json", []byte(`{"version":1}`), 0644)
	mode, _ := os.ReadFile(".fixture-mode")
	if string(mode) == "partial-init" {
		return 3
	}
	if string(mode) == "zero-init" {
		return 0
	}
	os.WriteFile(".squad/team.md", []byte("# Squad Team\n\n## Members\n\n| Name | Role | Charter | Status |\n|------|------|---------|--------|\n"), 0644)
	os.WriteFile(".squad/routing.md", []byte("# Routing\n"), 0644)
	for _, kind := range []string{"catalog", "fallback"} {
		b, _ := os.ReadFile(".fixture-" + kind)
		os.WriteFile(".squad/.fixture-"+kind, b, 0644)
	}
	mcp := `{"mcpServers":{"squad_state":{"command":"squad","args":["state-mcp"]}}}`
	if string(mode) == "npx-init" {
		mcp = `{"mcpServers":{"squad_state":{"command":"npx","args":["state-mcp"]}}}`
	}
	os.WriteFile(".mcp.json", []byte(mcp), 0644)
	if string(mode) == "output-init" {
		fmt.Print(strings.Repeat("x", 33<<10))
	}
	return 0
}
func squadSetupRoot(t *testing.T) (string, SquadProgram) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"catalog", "fallback"} {
		if err := os.WriteFile(filepath.Join(root, ".fixture-"+kind), enrichFixture(t, kind), 0644); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "squad"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	program := filepath.Join(t.TempDir(), name)
	// Windows locks all hardlinks of the running test executable against deletion.
	// Other platforms can avoid copying the large fixture executable per case.
	linked := false
	if runtime.GOOS != "windows" {
		linked = os.Link(exe, program) == nil
	}
	if !linked {
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, b, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root, SquadProgram{Path: program, Release: "1.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}
func TestSquadSetupCreateInitializesOnlyOnApprovedOperation(t *testing.T) {
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := os.Stat(filepath.Join(root, ".squad")); !os.IsNotExist(err) {
		t.Fatal("startup performed setup")
	}
	got, err := c.Create(context.Background(), SquadPlan{Lead: squadLead(), Specialists: []SquadMember{enrichMember()}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"setup":"initialized"`) || len(got.Members) != 2 || got.Members[0].State != "enriched" || got.Members[1].State != "enriched" || len(got.ChangedPaths) == 0 {
		t.Fatalf("lost setup/member observations: %s", b)
	}
	os.Remove(filepath.Join(root, ".init-called"))
	other := enrichMember()
	other.ID = "another"
	if _, err := c.Create(context.Background(), SquadPlan{Lead: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".init-called")); !os.IsNotExist(err) {
		t.Fatal("initialized project was reinitialized")
	}
}
func TestSquadSetupPreflightAndPartialFailure(t *testing.T) {
	for _, kind := range []string{"bad-member", "duplicate", "prior-mcp", "partial-init", "zero-init", "npx-init", "output-init"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadSetupRoot(t)
			if kind == "prior-mcp" {
				os.WriteFile(filepath.Join(root, ".mcp.json"), []byte("prior config"), 0644)
			}
			if strings.HasSuffix(kind, "init") {
				os.WriteFile(filepath.Join(root, ".fixture-mode"), []byte(kind), 0644)
			}
			c, err := OpenSquadSetup(root, p)
			plan := SquadPlan{Lead: squadLead()}
			if kind == "bad-member" {
				m := enrichMember()
				m.Role = "--force"
				plan.Specialists = []SquadMember{m}
			}
			if kind == "duplicate" {
				plan.Specialists = []SquadMember{squadLead()}
			}
			var result SquadResult
			if err == nil {
				defer c.Close()
				result, err = c.Create(context.Background(), plan)
			}
			if err == nil {
				t.Fatal("unsafe/partial setup accepted")
			}
			if _, err := os.Stat(filepath.Join(root, ".squad/agents/leader")); !os.IsNotExist(err) {
				t.Fatal("cast ran after setup refusal/failure")
			}
			_, called := os.Stat(filepath.Join(root, ".init-called"))
			if kind == "output-init" && result.Setup != "unknown" {
				t.Fatalf("zero-exit uncertain init mislabeled: %s", result.Setup)
			}
			if kind == "partial-init" {
				if e, ok := err.(*Error); !ok || e.Code != "squad_setup_failed" {
					t.Fatalf("setup failure mislabeled as cast failure: %v", err)
				}
			}
			if strings.HasSuffix(kind, "init") {
				if called != nil || !result.EffectsUnknown || len(result.ChangedPaths) == 0 {
					t.Fatal("lost partial native setup effects")
				}
			} else if !os.IsNotExist(called) {
				t.Fatal("invalid request caused init")
			}
		})
	}
}
