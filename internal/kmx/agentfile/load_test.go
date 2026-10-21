package agentfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func realTemp(t *testing.T) string {
	t.Helper()
	// macOS's system temp path can run through the /var symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestLoadStandaloneAndSquad(t *testing.T) {
	root := realTemp(t)
	source := []byte(sample(t))
	for _, relative := range []string{"a path/other-name.agent.md", ".squad/agents/greeter/charter.md"} {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, source, 0600); err != nil {
			t.Fatal(err)
		}
		a, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if a.Identity.ID != "greeter" {
			t.Fatal(a.Identity)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(source, after) {
			t.Fatal("Load changed the file")
		}
	}
	bad := filepath.Join(root, ".squad/agents/wrong/charter.md")
	if a, err := Parse(bad, source); err == nil || a != nil {
		t.Fatal("Squad ID mismatch accepted")
	}
	if a, err := Parse(`C:\repo\.squad\agents\wrong\charter.md`, source); err == nil || a != nil {
		t.Fatal("Windows Squad ID mismatch accepted")
	}
}
func TestLoadRefusesUnsafeFiles(t *testing.T) {
	root := realTemp(t)
	for _, name := range []string{"missing.md", "directory", "oversize.md", "invalid.md"} {
		path := filepath.Join(root, name)
		switch name {
		case "directory":
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		case "oversize.md":
			if err := os.WriteFile(path, bytes.Repeat([]byte{'a'}, MaxFileBytes+1), 0600); err != nil {
				t.Fatal(err)
			}
		case "invalid.md":
			if err := os.WriteFile(path, []byte("# legacy\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if a, err := Load(path); err == nil || a != nil {
			t.Fatalf("accepted unsafe path %s", name)
		}
	}
	if a, err := Load(""); err == nil || a != nil {
		t.Fatal("accepted blank filename")
	}
}
func TestLoadRefusesSymlinks(t *testing.T) {
	root := realTemp(t)
	source := filepath.Join(root, "source.md")
	if err := os.WriteFile(source, []byte(sample(t)), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if a, err := Load(link); err == nil || a != nil {
		t.Fatal("symlink accepted")
	}
	parent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(root, parent); err != nil {
		t.Fatal(err)
	}
	if a, err := Load(filepath.Join(parent, "source.md")); err == nil || a != nil {
		t.Fatal("parent symlink accepted")
	}
}
