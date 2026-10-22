package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

func observedSquadFile(t *testing.T) (*Files, SquadMember, []byte, sourceIdentity) {
	t.Helper()
	root := t.TempDir()
	m := enrichMember()
	p := squadPath(m)
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0755); err != nil {
		t.Fatal(err)
	}
	raw := enrichFixture(t, "fallback")
	if err := os.WriteFile(filepath.Join(root, p), raw, 0640); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(root, func(p string, b []byte) error { _, err := agentfile.ParseSquad(p, b, squadProfile(m)); return err })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	parent, err := f.parent(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	file, _, info, err := readTarget(parent, "charter.md")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	dir, err := parent.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	return f, m, raw, sourceIdentity{file: info, parent: dir}
}

func TestSquadObservedPublication(t *testing.T) {
	f, m, raw, identity := observedSquadFile(t)
	b, err := enrichSquad(m, raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.editObserved(context.Background(), squadPath(m), revision(raw), b, &identity)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != revision(b) {
		t.Fatal("published a different revision")
	}
}

// Revision equality must not turn another inode or member directory into the
// new starter observed by this operation.
func TestSquadObservedPublicationRefusesSubstitution(t *testing.T) {
	for _, kind := range []string{"file", "parent", "root"} {
		t.Run(kind, func(t *testing.T) {
			f, m, raw, identity := observedSquadFile(t)
			p := filepath.Join(f.root.Name(), filepath.FromSlash(squadPath(m)))
			switch kind {
			case "file":
				if err := os.WriteFile(p+".replacement", raw, 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(p+".replacement", p); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.Rename(filepath.Dir(p), filepath.Dir(p)+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, raw, 0640); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := os.Rename(f.root.Name(), f.root.Name()+"-old"); err != nil {
					t.Skip("host cannot rename an open root")
				}
				t.Cleanup(func() { os.RemoveAll(f.root.Name() + "-old") })
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, raw, 0640); err != nil {
					t.Fatal(err)
				}
			}
			b, err := enrichSquad(m, raw)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.editObserved(context.Background(), squadPath(m), revision(raw), b, &identity)
			var code string
			if e, ok := err.(*Error); ok {
				code = e.Code
			}
			if code != "stale_revision" {
				t.Fatalf("substitution not refused: %v", err)
			}
		})
	}
}
