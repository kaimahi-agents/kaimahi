package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The fixed cast argv is handled only in this test binary, never production.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "cast" {
		os.Exit(squadTestChild())
	}
	os.Exit(m.Run())
}
func squadTestChild() int {
	if len(os.Args) != 6 || os.Args[2] != "--name" || os.Args[4] != "--role" {
		return 9
	}
	id, role := os.Args[3], os.Args[5]
	if b, _ := os.ReadFile(".squad/.fixture-mode"); string(b) == "fail" {
		return 2
	}
	if os.Getenv("SQUAD_NO_PERSONAL") != "1" || os.Getenv("SQUAD_TEAM_ROOT") != "" || os.Getenv("NODE_OPTIONS") != "" || os.Getenv("GITHUB_TOKEN") != "" {
		return 8
	}
	stdin := make([]byte, 1)
	if n, _ := os.Stdin.Read(stdin); n != 0 {
		return 7
	}
	kind, display := "fallback", "Greeting specialist"
	if role == "lead" {
		kind, display = "catalog", "Lead / Architect"
	}
	raw, err := os.ReadFile(".squad/.fixture-" + kind)
	if err != nil {
		return 6
	}
	old := "greeter"
	if kind == "catalog" {
		old = "leader"
	}
	raw = bytes.ReplaceAll(raw, []byte(old), []byte(id))
	dir := filepath.Join(".squad", "agents", id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 6
	}
	if err := os.WriteFile(filepath.Join(dir, "charter.md"), raw, 0644); err != nil {
		return 6
	}
	os.WriteFile(filepath.Join(dir, "history.md"), []byte("native history\n"), 0644)
	mode, _ := os.ReadFile(".squad/.fixture-mode")
	if string(mode) == "partial" {
		return 3
	}
	if string(mode) == "unregistered-success" || string(mode) == "unregistered-overflow" {
		if string(mode) == "unregistered-overflow" {
			fmt.Print(strings.Repeat("unverified native output", 5000))
		}
		return 0
	}
	if string(mode) == "cancel" {
		time.Sleep(60 * time.Second)
		return 4
	}
	f, err := os.OpenFile(".squad/team.md", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return 6
	}
	fmt.Fprintf(f, "| %s | %s | `.squad/agents/%s/charter.md` | active |\n", id, display, id)
	f.Close()
	f, err = os.OpenFile(".squad/routing.md", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return 6
	}
	fmt.Fprintf(f, "| %s | %s | — |\n", display, id)
	f.Close()
	var reg struct {
		Schema      string                       `json:"schema"`
		Version     int                          `json:"schema_version"`
		Revision    int                          `json:"revision"`
		Transaction string                       `json:"transaction_id"`
		Agents      map[string]map[string]string `json:"agents"`
	}
	b, _ := os.ReadFile(".squad/casting/registry.json")
	json.Unmarshal(b, &reg)
	if reg.Agents == nil {
		reg.Agents = map[string]map[string]string{}
	}
	reg.Agents[id] = map[string]string{"display_name": id, "role": display, "status": "active"}
	reg.Schema = "squad-agent-provenance/v1"
	reg.Version = 1
	reg.Revision++
	reg.Transaction = fmt.Sprintf("test-transaction-%d", reg.Revision)
	b, _ = json.Marshal(reg)
	os.MkdirAll(".squad/casting", 0755)
	os.WriteFile(".squad/casting/registry.json", b, 0644)
	history, _ := json.Marshal(map[string]any{"assignment_cast_snapshots": map[string]any{}, "universe_usage_history": []any{}, "transaction_id": reg.Transaction, "registry_revision": reg.Revision})
	os.WriteFile(".squad/casting/history.json", history, 0644)
	manifest, _ := json.Marshal(map[string]any{"version": 1, "transaction_id": reg.Transaction, "registry_revision": reg.Revision, "registry_sha256": fmt.Sprintf("%x", sha256.Sum256(b)), "history_sha256": fmt.Sprintf("%x", sha256.Sum256(history))})
	os.WriteFile(".squad/casting/registry-history.commit.json", manifest, 0644)
	if string(mode) == "effects-unknown" {
		os.WriteFile(".squad/unknown-output", bytes.Repeat([]byte("x"), squadTreeBudget+1), 0644)
	}
	if string(mode) == "overflow" {
		fmt.Print(strings.Repeat("private native output", 5000))
	}
	return 0
}
func squadTestRoot(t *testing.T) (string, SquadProgram) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".squad"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"config.json": []byte(`{"version":1}`), "team.md": []byte("# Team\n"), "routing.md": []byte("# Routing\n"), ".fixture-catalog": enrichFixture(t, "catalog"), ".fixture-fallback": enrichFixture(t, "fallback")} {
		if err := os.WriteFile(filepath.Join(root, ".squad", name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return root, SquadProgram{Path: program, Release: "1.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}
func squadLead() SquadMember {
	m := enrichMember()
	m.ID = "leader"
	m.Role = "lead"
	m.DisplayRole = "Lead / Architect"
	return m
}

func TestSquadCastEnrichFixedProcessAndPartialBatch(t *testing.T) {
	root, program := squadTestRoot(t)
	t.Setenv("SQUAD_TEAM_ROOT", t.TempDir())
	t.Setenv("NODE_OPTIONS", "--invalid-option")
	t.Setenv("GITHUB_TOKEN", "not-a-real-token")
	c, err := OpenSquad(root, program)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got, err := c.Create(context.Background(), SquadPlan{Lead: squadLead(), Specialists: []SquadMember{enrichMember()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || got.Members[0].State != "enriched" || got.Members[1].State != "enriched" || got.EffectsUnknown || len(got.ChangedPaths) == 0 {
		t.Fatalf("incomplete result: %+v", got)
	}
	for _, row := range got.Members {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(row.File.Path)))
		if err != nil {
			t.Fatal(err)
		}
		if revision(b) != row.File.Revision || !bytes.Contains(b, []byte("## Task Instructions")) {
			t.Fatal("reported revision was not published")
		}
	}
	// A second call cannot adopt or silently re-cast the existing members.
	_, err = c.Create(context.Background(), SquadPlan{Lead: squadLead()})
	if err == nil {
		t.Fatal("prior member adopted")
	}
}

func TestSquadCastPreflightRefusesPriorIdentityAndRedirects(t *testing.T) {
	for _, kind := range []string{"remote", "external", "unknown-config", "legacy", "directory", "roster", "routing", "registry", "transaction"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadTestRoot(t)
			squad := filepath.Join(root, ".squad")
			switch kind {
			case "remote":
				os.WriteFile(filepath.Join(squad, "config.json"), []byte(`{"version":1,"teamRoot":"../outside"}`), 0644)
			case "external":
				os.WriteFile(filepath.Join(squad, "config.json"), []byte(`{"version":1,"stateLocation":"external"}`), 0644)
			case "unknown-config":
				os.WriteFile(filepath.Join(squad, "config.json"), []byte(`{"version":1,"unknown":true}`), 0644)
			case "legacy":
				os.Mkdir(filepath.Join(root, ".ai-team"), 0755)
			case "directory":
				os.MkdirAll(filepath.Join(squad, "agents/leader"), 0755)
				os.WriteFile(filepath.Join(squad, "agents/leader/history.md"), []byte("prior history"), 0644)
			case "roster":
				os.WriteFile(filepath.Join(squad, "team.md"), []byte("prior `.squad/agents/leader/charter.md` reference"), 0644)
			case "routing":
				os.WriteFile(filepath.Join(squad, "routing.md"), []byte("prior | leader | route"), 0644)
			case "registry":
				os.MkdirAll(filepath.Join(squad, "casting"), 0755)
				os.WriteFile(filepath.Join(squad, "casting/registry.json"), []byte(`{"agents":{"leader":{"status":"retired"}}}`), 0644)
			case "transaction":
				os.MkdirAll(filepath.Join(squad, "casting"), 0755)
				os.WriteFile(filepath.Join(squad, "casting/registry-history.transaction.json"), []byte("pending native recovery"), 0644)
			}
			c, err := OpenSquad(root, p)
			if err == nil {
				defer c.Close()
				_, err = c.Create(context.Background(), SquadPlan{Lead: squadLead()})
			}
			if err == nil {
				t.Fatal("unsafe root/prior identity accepted")
			}
			if _, err := os.Stat(filepath.Join(squad, "agents/leader/charter.md")); !os.IsNotExist(err) {
				t.Fatal("cast ran before preflight refusal")
			}
		})
	}
}

func TestSquadCastFailureKeepsObservedEffects(t *testing.T) {
	for _, mode := range []string{"partial", "overflow", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			root, p := squadTestRoot(t)
			os.WriteFile(filepath.Join(root, ".squad/.fixture-mode"), []byte(mode), 0644)
			c, err := OpenSquad(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				go func() {
					for i := 0; i < 200; i++ {
						if _, err := os.Stat(filepath.Join(root, ".squad/agents/leader/charter.md")); err == nil {
							cancel()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
					cancel()
				}()
			}
			got, err := c.Create(ctx, SquadPlan{Lead: squadLead(), Specialists: []SquadMember{enrichMember()}})
			if err == nil || got.Members[0].State == "enriched" || got.Members[1].State != "not_attempted" || len(got.ChangedPaths) == 0 {
				t.Fatalf("lost partial effects: %+v %v", got, err)
			}
			b, readErr := os.ReadFile(filepath.Join(root, ".squad/agents/leader/charter.md"))
			if readErr != nil || bytes.Contains(b, []byte("## Task Instructions")) {
				t.Fatal("partial starter removed or enriched")
			}
		})
	}
}
