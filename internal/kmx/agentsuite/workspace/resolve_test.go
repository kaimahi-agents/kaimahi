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

// Descriptor-only fixtures make accidental member reads or preparation fail.
func TestResolveWorkspace(t *testing.T) {
	root, selected := t.TempDir(), t.TempDir()
	nearest := filepath.Join(root, "nearest")
	start := filepath.Join(nearest, "repository", "deep")
	if err := os.MkdirAll(start, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(nearest, "repository", ".git", "config"), "git boundary\n")
	writeFile(t, filepath.Join(root, "workspace.yaml"), resolverMarker)
	writeFile(t, filepath.Join(selected, "workspace.yaml"), resolverMarker)
	relative, err := filepath.Rel(start, selected)
	if err != nil {
		t.Fatal(err)
	}
	credential := "ghp_" + strings.Repeat("x", 36)
	for _, tc := range []struct {
		name, start, override, want, wantErr, marker string
	}{
		{"nearest", start, "", nearest, "", resolverMarker},
		{"cross Git boundary", start, "", root, "", "missing"},
		{"absolute override", start, selected, selected, "", "broken: [\n"},
		{"relative override", start, relative, selected, "", "broken: [\n"},
		{"absolute override ignores start", "\x00invalid", selected, selected, "", resolverMarker},
		{"missing exact marker", start, start, "", "not found", resolverMarker},
		{"missing exact directory", start, filepath.Join(root, "absent"), "", "not found", resolverMarker},
		{"no discovery", t.TempDir(), "", "", "not found", resolverMarker},
		{"malformed nearest", start, "", "", "invalid workspace descriptor", "broken: [\n"},
		{"malformed exact", start, nearest, "", "invalid workspace descriptor", "broken: [\n"},
		{"unsupported", start, "", "", "unsupported workspace", strings.Replace(resolverMarker, "schemaVersion: 1", "schemaVersion: 2", 1)},
		{"member traversal", start, "", "", "workspace member path", strings.Replace(resolverMarker, "agents/greeter/member.yaml", "../member.yaml", 1)},
		{"private parse error", start, "", "", "invalid workspace descriptor", resolverMarker + "\\u0067" + credential[1:] + ": unused\n"},
		{"oversized", start, "", "", "at most 1 MiB", strings.Repeat("x", (1<<20)+1)},
		{"unreadable", start, "", "", "permission denied", "unreadable"},
		{"directory", start, "", "", "regular file", "directory"},
		{"symlink", start, "", "", "symlink", "symlink"},
		{"FIFO", start, "", "", "regular file", "FIFO"},
		{"empty start", "", "", "", "path is required", resolverMarker},
		{"file start", filepath.Join(root, "workspace.yaml"), "", "", "directory", resolverMarker},
		{"credential start", filepath.Join(root, credential), "", "", "credential-shaped", resolverMarker},
		{"credential override", start, credential, "", "credential-shaped", resolverMarker},
		{"invalid Unicode", "\xff", "", "", "invalid Unicode", resolverMarker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(nearest, "workspace.yaml")
			if err := os.RemoveAll(marker); err != nil {
				t.Fatal(err)
			}
			var setupErr error
			switch tc.marker {
			case "missing":
			case "directory":
				setupErr = os.Mkdir(marker, 0700)
			case "symlink":
				setupErr = os.Symlink(filepath.Join(selected, "workspace.yaml"), marker)
			case "FIFO":
				tool, err := exec.LookPath("mkfifo")
				if err != nil {
					t.Skip("mkfifo unavailable")
				}
				setupErr = exec.Command(tool, marker).Run()
			case "unreadable":
				writeFile(t, marker, resolverMarker)
				setupErr = os.Chmod(marker, 0)
				t.Cleanup(func() { _ = os.Chmod(marker, 0600) })
				if _, err := os.ReadFile(marker); setupErr == nil && err == nil {
					t.Skip("process can read mode-000 marker")
				}
			default:
				writeFile(t, marker, tc.marker)
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			got, err := workspace.Resolve(tc.start, tc.override)
			if tc.wantErr == "" {
				if err != nil || got != tc.want || !filepath.IsAbs(got) || string(readFile(t, filepath.Join(got, "workspace.yaml"))) != resolverMarker {
					t.Fatalf("resolution = %q, %v; want %q without source changes", got, err, tc.want)
				}
			} else if got != "" || err == nil || !strings.Contains(err.Error(), tc.wantErr) || errors.Is(err, workspace.ErrNotFound) != (tc.wantErr == "not found") || strings.Contains(err.Error(), credential) {
				t.Fatalf("resolution = %q, %v; want private %q error without fallback", got, err, tc.wantErr)
			}
		})
	}
}
