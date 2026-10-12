package workspace_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const resolverMarker = "schemaVersion: 1\nname: greeter\nmember: agents/greeter/member.yaml\nbuildProfile: build-profiles/default.json\n"

// Descriptor-only fixtures catch accidental preparation or member/case reads.
func TestResolveNearestWorkspaceReadOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "workspace.yaml"), resolverMarker)
	nearest := filepath.Join(root, "nested")
	writeFile(t, filepath.Join(nearest, "workspace.yaml"), resolverMarker)
	start := filepath.Join(nearest, "repository", "deep")
	writeFile(t, filepath.Join(nearest, "repository", ".git", "config"), "git boundary\n")
	if err := os.MkdirAll(start, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(nearest, "workspace.yaml")
	before, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	got, err := workspace.Resolve(start, "")
	if err != nil || got != nearest || !filepath.IsAbs(got) {
		t.Fatalf("nearest discovery = %q, %v; want %q", got, err, nearest)
	}
	after, err := os.Stat(marker)
	if err != nil || before.Mode() != after.Mode() || before.ModTime() != after.ModTime() || string(readFile(t, marker)) != resolverMarker {
		t.Fatal("discovery mutated source", err)
	}
	entries, err := os.ReadDir(nearest)
	if err != nil || len(entries) != 2 {
		t.Fatalf("discovery wrote or prepared files: %v, %v", entries, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	got, err = workspace.Resolve(start, "")
	if err != nil || got != root {
		t.Fatalf("discovery stopped at Git boundary: %q, %v", got, err)
	}
}

func TestResolveExplicitWorkspaceDoesNotFallBack(t *testing.T) {
	start := t.TempDir()
	writeFile(t, filepath.Join(start, "workspace.yaml"), "broken: [\n")
	selected := filepath.Join(t.TempDir(), "selected")
	writeFile(t, filepath.Join(selected, "workspace.yaml"), resolverMarker)
	relative, err := filepath.Rel(start, selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, start, override string }{
		{"absolute override", start, selected},
		{"relative override", start, relative},
		{"unusable start with absolute override", "\x00invalid", selected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := workspace.Resolve(tc.start, tc.override)
			if err != nil || got != selected {
				t.Fatalf("explicit resolution = %q, %v; want %q", got, err, selected)
			}
		})
	}
	for _, missing := range []string{filepath.Join(selected, "missing"), t.TempDir()} {
		got, err := workspace.Resolve(start, missing)
		if got != "" || !errors.Is(err, workspace.ErrNotFound) {
			t.Fatalf("missing explicit workspace fell back: %q, %v", got, err)
		}
	}
	got, err := workspace.Resolve(t.TempDir(), "")
	if got != "" || !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("absent discovery = %q, %v", got, err)
	}
}

func TestResolveRefusesInvalidNearestMarker(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		setup      func(*testing.T, string)
	}{
		{"malformed", "invalid workspace descriptor", func(t *testing.T, p string) { writeFile(t, p, "broken: [\n") }},
		{"unsupported", "unsupported workspace", func(t *testing.T, p string) {
			writeFile(t, p, strings.Replace(resolverMarker, "schemaVersion: 1", "schemaVersion: 2", 1))
		}},
		{"unknown field", "invalid workspace descriptor", func(t *testing.T, p string) { writeFile(t, p, resolverMarker+"other: field\n") }},
		{"duplicate field", "invalid workspace descriptor", func(t *testing.T, p string) { writeFile(t, p, resolverMarker+"name: duplicate\n") }},
		{"multiple documents", "invalid workspace descriptor", func(t *testing.T, p string) { writeFile(t, p, resolverMarker+"---\n"+resolverMarker) }},
		{"member traversal", "workspace member path", func(t *testing.T, p string) {
			writeFile(t, p, strings.Replace(resolverMarker, "agents/greeter/member.yaml", "../outside/member.yaml", 1))
		}},
		{"empty member", "workspace member path", func(t *testing.T, p string) {
			writeFile(t, p, strings.Replace(resolverMarker, "agents/greeter/member.yaml", "''", 1))
		}},
		{"profile traversal", "unsupported workspace", func(t *testing.T, p string) {
			writeFile(t, p, strings.Replace(resolverMarker, "build-profiles/default.json", "../profile.json", 1))
		}},
		{"oversized", "at most 1 MiB", func(t *testing.T, p string) { writeFile(t, p, strings.Repeat("x", (1<<20)+1)) }},
		{"unreadable", "permission denied", func(t *testing.T, p string) {
			writeFile(t, p, resolverMarker)
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0600) })
			if _, err := os.ReadFile(p); err == nil {
				t.Skip("process can read mode-000 marker")
			}
		}},
		{"directory", "regular file", func(t *testing.T, p string) {
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", "symlink", func(t *testing.T, p string) {
			target := filepath.Join(t.TempDir(), "descriptor")
			writeFile(t, target, resolverMarker)
			if err := os.Symlink(target, p); err != nil {
				t.Skip(err)
			}
		}},
		{"dangling symlink", "symlink", func(t *testing.T, p string) {
			if err := os.Symlink("missing", p); err != nil {
				t.Skip(err)
			}
		}},
		{"FIFO", "regular file", func(t *testing.T, p string) {
			tool, err := exec.LookPath("mkfifo")
			if err != nil {
				t.Skip("mkfifo unavailable")
			}
			if err := exec.Command(tool, p).Run(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "workspace.yaml"), resolverMarker)
			nearest := filepath.Join(root, "nearest")
			start := filepath.Join(nearest, "deep")
			if err := os.MkdirAll(start, 0700); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, filepath.Join(nearest, "workspace.yaml"))
			for _, override := range []string{"", nearest} {
				got, err := workspace.Resolve(start, override)
				if got != "" || err == nil || errors.Is(err, workspace.ErrNotFound) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("invalid nearest marker became absence/fallback: %q, %v; want %q", got, err, tc.want)
				}
			}
		})
	}
}

func TestResolveRefusesUnsafePathsWithoutDisclosure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "workspace.yaml"), resolverMarker)
	file := filepath.Join(root, "file")
	writeFile(t, file, "not a directory")
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skip(err)
	}
	credential := "ghp_" + strings.Repeat("x", 36)
	for _, tc := range []struct{ start, override, want string }{
		{"", "", "path is required"},
		{file, "", "directory"},
		{root, file, "directory"},
		{link, "", "symlink"},
		{root, link, "symlink"},
		{filepath.Join(root, credential), "", "credential-shaped"},
		{root, credential, "credential-shaped"},
		{"\xff", "", "invalid Unicode"},
	} {
		got, err := workspace.Resolve(tc.start, tc.override)
		if got != "" || err == nil || errors.Is(err, workspace.ErrNotFound) || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), credential) {
			t.Fatalf("unsafe path resolution = %q, %v; want private %q error", got, err, tc.want)
		}
	}
	writeFile(t, filepath.Join(root, "workspace.yaml"), resolverMarker+"\\u0067"+credential[1:]+": unused\n")
	_, err := workspace.Resolve(root, "")
	if err == nil || errors.Is(err, workspace.ErrNotFound) || strings.Contains(err.Error(), credential) {
		t.Fatal("descriptor diagnostic disclosed or accepted credential-shaped content", err)
	}
}
