package agentfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
func TestLoadRefusesErasedSymlinkTraversal(t *testing.T) {
	root := realTemp(t)
	local, remote := filepath.Join(root, "local"), filepath.Join(root, "remote")
	for _, dir := range []string{local, filepath.Join(remote, "deep")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for dir, source := range map[string]string{local: sample(t), remote: strings.Replace(sample(t), "# Greeter", "# Remote", 1)} {
		if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Even without symlinks, parent-traversal spellings must refuse before cleaning.
	traversal := filepath.Join(remote, "deep") + string(filepath.Separator) + ".." + string(filepath.Separator) + "a.md"
	var refusal *Error
	if a, err := Load(traversal); a != nil || !errors.As(err, &refusal) || refusal.Field != "File" || !strings.Contains(refusal.Problem, "parent traversal") {
		t.Fatalf("parent traversal was not refused: %v", err)
	}
	if volume := filepath.VolumeName(root); volume != "" {
		if a, err := Load(volume + ".." + string(filepath.Separator) + "a.md"); a != nil || !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "parent traversal") {
			t.Fatalf("drive-relative parent traversal was not refused: %v", err)
		}
	}
	link := filepath.Join(local, "link")
	if err := os.Symlink(filepath.Join(remote, "deep"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	// filepath.Join would erase the component the regression needs to preserve.
	traversal = link + string(filepath.Separator) + ".." + string(filepath.Separator) + "a.md"
	if a, err := Load(traversal); a != nil || !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "parent traversal") {
		t.Fatalf("erased symlink traversal was not refused: %v", err)
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
