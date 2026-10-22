package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSquadWholeRequestPrecedesNativeEffects(t *testing.T) {
	for _, kind := range []string{"cancelled", "bad-specialist", "duplicate", "too-many"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadTestRoot(t)
			c, err := OpenSquad(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			plan := SquadPlan{Lead: squadLead()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "cancelled":
				cancel()
			case "bad-specialist":
				bad := enrichMember()
				bad.Role = "--force"
				plan.Specialists = []SquadMember{bad}
			case "duplicate":
				plan.Specialists = []SquadMember{squadLead()}
			case "too-many":
				plan.Specialists = make([]SquadMember, 8)
			}
			if _, err := c.Create(ctx, plan); err == nil {
				t.Fatal("unsafe request accepted")
			}
			if _, err := os.Stat(filepath.Join(root, ".squad/agents")); !os.IsNotExist(err) {
				t.Fatal("lead cast before whole-request validation")
			}
		})
	}
}

func TestSquadRejectsStaticSymlinkFootprint(t *testing.T) {
	for _, kind := range []string{"squad", "tree-file", "github", "typescript"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadTestRoot(t)
			outside := t.TempDir()
			switch kind {
			case "squad":
				if err := os.Rename(filepath.Join(root, ".squad"), filepath.Join(outside, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "moved"), filepath.Join(root, ".squad")); err != nil {
					t.Skip("host does not permit symlinks")
				}
			case "tree-file":
				os.WriteFile(filepath.Join(outside, "foreign"), []byte("foreign data"), 0644)
				if err := os.Symlink(filepath.Join(outside, "foreign"), filepath.Join(root, ".squad/foreign.md")); err != nil {
					t.Skip("host does not permit symlinks")
				}
			case "github":
				if err := os.Symlink(outside, filepath.Join(root, ".github")); err != nil {
					t.Skip("host does not permit symlinks")
				}
			case "typescript":
				os.WriteFile(filepath.Join(outside, "foreign"), []byte("foreign configuration"), 0644)
				if err := os.Symlink(filepath.Join(outside, "foreign"), filepath.Join(root, "squad.config.ts")); err != nil {
					t.Skip("host does not permit symlinks")
				}
			}
			c, err := OpenSquad(root, p)
			if err == nil {
				defer c.Close()
				_, err = c.Create(context.Background(), SquadPlan{Lead: squadLead()})
			}
			if err == nil {
				t.Fatal("redirected native footprint accepted")
			}
		})
	}
}

func TestSquadConfigUsesExactUniqueNativeKeys(t *testing.T) {
	for _, source := range []string{
		`{"version":1,"teamRoot":"../outside","TeamRoot":"."}`,
		`{"version":1,"teamRoot":"../outside","teamRoot":"."}`,
		`{"Version":1}`, `{"version":1,"TEAMROOT":"."}`, `{"version":1,"teamRoot":null}`,
	} {
		t.Run(source, func(t *testing.T) {
			root, p := squadTestRoot(t)
			os.WriteFile(filepath.Join(root, ".squad/config.json"), []byte(source), 0644)
			if c, err := OpenSquad(root, p); err == nil {
				c.Close()
				t.Fatal("non-native/ambiguous config keys accepted")
			}
		})
	}
}

func TestSquadRejectsBatchLaunchers(t *testing.T) {
	for _, ext := range []string{".cmd", ".bat", ".ps1"} {
		root, p := squadTestRoot(t)
		p.Path = filepath.Join(t.TempDir(), "squad"+ext)
		os.WriteFile(p.Path, []byte("not an executable qualified launcher"), 0755)
		if c, err := OpenSquad(root, p); err == nil {
			c.Close()
			t.Fatal("shell-associated launcher accepted")
		}
	}
}

func TestSquadCrossMemberRoleCollisionPrecedesEffects(t *testing.T) {
	root, p := squadTestRoot(t)
	c, err := OpenSquad(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	first := squadLead()
	first.DisplayRole = "greeter"
	if _, err := c.Create(context.Background(), SquadPlan{Lead: first, Specialists: []SquadMember{enrichMember()}}); err == nil {
		t.Fatal("cross-member identity/role collision accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".squad/agents")); !os.IsNotExist(err) {
		t.Fatal("native cast ran before cross-member refusal")
	}
}

func TestSquadCanonicalizesExcludedRoots(t *testing.T) {
	for _, kind := range []string{"home", "state", "personal", "config"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadTestRoot(t)
			alias := filepath.Join(t.TempDir(), "alias")
			target := root
			if kind == "config" {
				target = filepath.Dir(root)
			}
			if err := os.Symlink(target, alias); err != nil {
				t.Skip("host does not permit symlinks")
			}
			switch kind {
			case "home":
				t.Setenv("HOME", alias)
				t.Setenv("USERPROFILE", alias)
			case "state":
				t.Setenv("SQUAD_HOME", alias)
			case "personal":
				t.Setenv("SQUAD_PERSONAL_DIR", alias)
			case "config":
				// The selected project must be inside the physical squad state root.
				os.Mkdir(filepath.Join(target, "squad"), 0755)
				moved := filepath.Join(target, "squad", "project")
				if err := os.Rename(root, moved); err != nil {
					t.Fatal(err)
				}
				root = moved
				t.Setenv("XDG_CONFIG_HOME", alias)
				t.Setenv("APPDATA", alias)
				if runtime.GOOS == "darwin" {
					t.Skip("macOS config path is fixed beneath home")
				}
			}
			if c, err := OpenSquad(root, p); err == nil {
				c.Close()
				t.Fatal("aliased excluded root accepted")
			}
		})
	}
}

func TestSquadRefusesUnestablishedNativeStateBeforeCast(t *testing.T) {
	for _, kind := range []string{"typescript", "history", "registry", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadTestRoot(t)
			if kind == "typescript" {
				os.WriteFile(filepath.Join(root, "squad.config.ts"), []byte("export default { agents: [{ name: `leader` }] };"), 0644)
			} else {
				os.MkdirAll(filepath.Join(root, ".squad/casting"), 0755)
				name := map[string]string{"history": "history.json", "registry": "registry.json", "manifest": "registry-history.commit.json"}[kind]
				body := `{}`
				if kind == "registry" {
					body = `{"agents":{}}`
				}
				os.WriteFile(filepath.Join(root, ".squad/casting", name), []byte(body), 0644)
			}
			c, err := OpenSquad(root, p)
			if err == nil {
				defer c.Close()
				_, err = c.Create(context.Background(), SquadPlan{Lead: squadLead()})
			}
			if err == nil {
				t.Fatal("unsupported native state accepted")
			}
			if _, err := os.Stat(filepath.Join(root, ".squad/agents/leader")); !os.IsNotExist(err) {
				t.Fatal("native cast ran before state validation")
			}
		})
	}
}

func TestSquadCastingCommitTamperPrecedesFurtherNativeEffects(t *testing.T) {
	for _, name := range []string{"registry.json", "history.json", "registry-history.commit.json"} {
		t.Run(name, func(t *testing.T) {
			root, p := squadTestRoot(t)
			c, err := OpenSquad(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()}); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(root, ".squad/casting", name), []byte(`{}`), 0644)
			if _, err := c.Create(context.Background(), SquadPlan{Lead: enrichMember()}); err == nil {
				t.Fatal("inconsistent native commit accepted")
			}
			if _, err := os.Stat(filepath.Join(root, ".squad/agents/greeter")); !os.IsNotExist(err) {
				t.Fatal("native cast ran after commit mismatch")
			}
		})
	}
}

func TestSquadProgramAndRootContinuity(t *testing.T) {
	root, p := squadTestRoot(t)
	for _, change := range []func(*SquadProgram){func(p *SquadProgram) { p.Release = "next" }, func(p *SquadProgram) { p.GOOS = "unknown" }, func(p *SquadProgram) { p.Path = "relative-program" }} {
		bad := p
		change(&bad)
		if c, err := OpenSquad(root, bad); err == nil {
			c.Close()
			t.Fatal("unqualified program accepted")
		}
	}
	c, err := OpenSquad(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Skip("host does not rename an open root")
	}
	t.Cleanup(func() { os.RemoveAll(root + "-old") })
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()}); err == nil {
		t.Fatal("different named root accepted")
	}
}

func TestSquadUnknownEffectsDoNotUndoAcknowledgedPublication(t *testing.T) {
	root, p := squadTestRoot(t)
	os.WriteFile(filepath.Join(root, ".squad/.fixture-mode"), []byte("effects-unknown"), 0644)
	c, err := OpenSquad(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()})
	if err == nil || !result.EffectsUnknown || result.Members[0].State != "enriched" {
		t.Fatalf("unknown effects incorrectly reported: %+v %v", result, err)
	}
}

func TestSquadSuccessfulChildWithoutRegistrationIsUnknown(t *testing.T) {
	for _, mode := range []string{"unregistered-success", "unregistered-overflow"} {
		t.Run(mode, func(t *testing.T) {
			root, p := squadTestRoot(t)
			os.WriteFile(filepath.Join(root, ".squad/.fixture-mode"), []byte(mode), 0644)
			c, err := OpenSquad(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			result, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()})
			if err == nil || result.Members[0].State != "cast_unknown" || !result.EffectsUnknown {
				t.Fatalf("incorrect native completion state: %+v %v", result, err)
			}
		})
	}
}

func TestSquadPublicationErrorsRequireHumanInspection(t *testing.T) {
	for _, code := range []string{"stale_revision", "busy", "publication_failed"} {
		err := squadEnrichmentError(refusal(code))
		if !strings.Contains(err.Error(), "inspect") || strings.Contains(err.Error(), "try again") {
			t.Fatalf("unsafe native recovery advice: %v", err)
		}
	}
}

func TestSquadDoesNotExposeNativeOutput(t *testing.T) {
	root, p := squadTestRoot(t)
	os.WriteFile(filepath.Join(root, ".squad/.fixture-mode"), []byte("overflow"), 0644)
	c, err := OpenSquad(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()})
	if err == nil || result.Members[0].State != "registered_unenriched" {
		t.Fatalf("incorrect overflow status: %+v %v", result, err)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b)+err.Error(), "private native output") || strings.Contains(string(b), "Say Hello") {
		t.Fatal("authored or native content leaked")
	}
}
