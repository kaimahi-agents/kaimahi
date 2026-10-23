package mcp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func createdSquadMember(t *testing.T) (string, SquadProgram, *SquadCreator) {
	t.Helper()
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()}); err != nil {
		c.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return root, p, c
}
func TestSquadMemberMappingSurvivesRestartWithoutKMXStore(t *testing.T) {
	root, p, _ := createdSquadMember(t)
	reopened, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	d, err := reopened.Member(context.Background(), "leader")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Enriched || d.Profile.MemberID != "leader" || d.Profile.Role != "Lead / Architect" || d.Profile.Release != "1.1.0" {
		t.Fatal("native mapping not reconstructed")
	}
	for _, name := range []string{".kmx", ".kmx-squad.json", "selected-members.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatal("created a kmx-owned mapping")
		}
	}
}
func TestSquadMemberInvalidNativeStateNeverGuesses(t *testing.T) {
	for _, kind := range []string{"registry", "history", "manifest", "roster", "roster-outside", "charter", "bare", "id", "registry.lock", "registry.lock.stale", "registry-history.transaction.json", "registry.json.tmp-interrupted"} {
		t.Run(kind, func(t *testing.T) {
			root, p, _ := createdSquadMember(t)
			switch kind {
			case "registry.lock", "registry.lock.stale", "registry-history.transaction.json", "registry.json.tmp-interrupted":
				if err := os.WriteFile(filepath.Join(root, ".squad/casting", kind), []byte("pending"), 0600); err != nil {
					t.Fatal(err)
				}
			case "registry":
				os.WriteFile(filepath.Join(root, ".squad/casting/registry.json"), []byte(`{"agents":{}}`), 0644)
			case "history":
				os.WriteFile(filepath.Join(root, ".squad/casting/history.json"), []byte(`{}`), 0644)
			case "manifest":
				os.Remove(filepath.Join(root, ".squad/casting/registry-history.commit.json"))
			case "roster":
				os.WriteFile(filepath.Join(root, ".squad/team.md"), []byte("# Team\n| leader | Wrong role | `.squad/agents/leader/charter.md` | active |\n"), 0644)
			case "roster-outside":
				os.WriteFile(filepath.Join(root, ".squad/team.md"), []byte("# Team\n## Notes\n| leader | Lead / Architect | `.squad/agents/leader/charter.md` | active |\n"), 0644)
			case "charter":
				b, _ := os.ReadFile(filepath.Join(root, ".squad/agents/leader/charter.md"))
				os.WriteFile(filepath.Join(root, ".squad/agents/leader/charter.md"), bytes.Replace(b, []byte("Lead / Architect"), []byte("Wrong role"), 1), 0644)
			case "bare":
				os.WriteFile(filepath.Join(root, ".squad/agents/leader/charter.md"), enrichFixture(t, "catalog"), 0644)
			}
			reopened, err := OpenSquadSetup(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			id := "leader"
			if kind == "id" {
				id = "../leader"
			}
			d, err := reopened.Member(context.Background(), id)
			if err == nil || d != nil {
				t.Fatal("invalid native state produced a profile")
			}
			if e, ok := err.(*Error); !ok || e.Code != "squad_not_setup" {
				t.Fatalf("missing safe setup classification: %v", err)
			}
		})
	}
}
func TestSquadMemberEditPreservesNativeBytesAndRefusesStale(t *testing.T) {
	_, _, c := createdSquadMember(t)
	d, err := c.Member(context.Background(), "leader")
	if err != nil {
		t.Fatal(err)
	}
	m := squadLead()
	m.TaskInstructions = "Reply with exactly `Goodbye, ` followed by the input name."
	m.Model = "fake"
	saved, err := c.EditMember(context.Background(), "leader", d.Revision, m)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.Member(context.Background(), "leader")
	if err != nil || next.Revision != saved.Revision {
		t.Fatal("native edit not published")
	}
	// The captured catalog has no final LF, so the original byte prefix ends at Voice.
	native := enrichFixture(t, "catalog")
	a := bytes.Index(native, []byte("- **Preferred:** auto"))
	if a < 0 {
		t.Fatal("missing native model fixture")
	}
	a += len("- **Preferred:** ")
	wantPrefix := append(append(append([]byte{}, native[:a]...), []byte("fake")...), native[a+len("auto"):]...)
	if !bytes.HasPrefix(next.Source(), wantPrefix) {
		t.Fatal("owned edit changed native content")
	}
	if _, err := c.EditMember(context.Background(), "leader", d.Revision, m); err == nil {
		t.Fatal("stale native overwrite accepted")
	} else if e, ok := err.(*Error); !ok || e.Code != "stale_revision" {
		t.Fatalf("stale edit received setup/enrichment recovery: %v", err)
	}
	m.TaskInstructions += "\n\n## Voice\nDo not replace native voice."
	if _, err := c.EditMember(context.Background(), "leader", next.Revision, m); err == nil {
		t.Fatal("invalid owned candidate accepted")
	} else if e, ok := err.(*Error); !ok || e.Code != "invalid_squad_member" {
		t.Fatalf("invalid edit mislabeled as partial native effects: %v", err)
	}
}
