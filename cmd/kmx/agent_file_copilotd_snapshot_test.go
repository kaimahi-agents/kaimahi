package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentpreview"
)

func snapshotGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func snapshotWrite(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func snapshotFixture(t *testing.T) (string, *agentfile.Pair) {
	t.Helper()
	root := t.TempDir()
	snapshotGit(t, root, "init", "-q")
	snapshotWrite(t, root, ".github/agents/reader.agent.md", "---\ntools: [view]\n---\nRead files.\n")
	snapshotWrite(t, root, "tracked", "original")
	snapshotWrite(t, root, "deleted", "delete me")
	snapshotWrite(t, root, ".gitignore", "ignored\n")
	snapshotGit(t, root, "add", ".")
	snapshotGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath="+os.DevNull, "commit", "-qm", "fixture")
	pair, err := agentfile.LoadCopilotdPair(filepath.Join(root, ".github/agents/reader.agent.md"), "")
	if err != nil {
		t.Fatal(err)
	}
	return root, pair
}
func TestCopilotdSnapshotWorkingFiles(t *testing.T) {
	root, pair := snapshotFixture(t)
	snapshotWrite(t, root, "tracked", "edited")
	snapshotWrite(t, root, "untracked", "new")
	snapshotWrite(t, root, "ignored", "secret ignored")
	if err := os.Remove(filepath.Join(root, "deleted")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "tracked"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	snap, err := makeCopilotdSnapshot(context.Background(), pair, func(...string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close(context.Background())
	for name, want := range map[string]string{"tracked": "edited", "untracked": "new", ".github/agents/reader.agent.md": "---\ntools: [view]\n---\nRead files.\n"} {
		got, err := os.ReadFile(filepath.Join(snap.Directory, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	for _, name := range []string{".git", "ignored", "deleted"} {
		if _, err := os.Lstat(filepath.Join(snap.Directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("copied %s: %v", name, err)
		}
	}
	link, err := os.Readlink(filepath.Join(snap.Directory, "link"))
	if err != nil || link != "tracked" {
		t.Fatalf("link: %s %v", link, err)
	}
	info, _ := os.Stat(filepath.Join(snap.Directory, "tracked"))
	if info.Mode().Perm() != 0700 {
		t.Fatal("lost executable bit")
	}
	info, _ = os.Stat(snap.Directory)
	if info.Mode().Perm() != 0700 {
		t.Fatal("snapshot not private")
	}
	if snap.GitRevision != snapshotGit(t, root, "rev-parse", "HEAD") || !strings.HasPrefix(snap.ContentRevision, "sha256:") {
		t.Fatalf("identity: %+v", snap)
	}
	snapshotWrite(t, snap.Directory, "tracked", "snapshot edit")
	got, _ := os.ReadFile(filepath.Join(root, "tracked"))
	if string(got) != "edited" {
		t.Fatal("snapshot mutated source")
	}
}
func TestCopilotdSnapshotStaleContext(t *testing.T) {
	for _, change := range []string{"bytes", "mode", "inventory", "pair", "during copy"} {
		t.Run(change, func(t *testing.T) {
			root, pair := snapshotFixture(t)
			factory := copilotdSnapshots(pair)
			first, err := factory(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(context.Background())
			check := func(...string) error { return nil }
			switch change {
			case "bytes":
				snapshotWrite(t, root, "tracked", "changed")
			case "mode":
				if err := os.Chmod(filepath.Join(root, "tracked"), 0700); err != nil {
					t.Fatal(err)
				}
			case "inventory":
				snapshotWrite(t, root, "added", "new")
			case "pair":
				snapshotWrite(t, root, ".github/agents/reader.agent.md", "Changed instructions.\n")
			case "during copy":
				check = func(values ...string) error {
					for _, s := range values {
						if s == "original" {
							snapshotWrite(t, root, "tracked", "mutated")
						}
					}
					return nil
				}
			}
			second, err := factory(context.Background(), check)
			if second.Directory != "" || !errors.Is(err, agentpreview.ErrSourceChanged) {
				t.Fatalf("stale source accepted: %+v %v", second, err)
			}
		})
	}
}
func TestCopilotdSnapshotOwnedCleanup(t *testing.T) {
	_, pair := snapshotFixture(t)
	snap, err := makeCopilotdSnapshot(context.Background(), pair, nil)
	if err != nil {
		t.Fatal(err)
	}
	moved := snap.Directory + "-moved"
	if err := os.Rename(snap.Directory, moved); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(moved)
	if err := os.Mkdir(snap.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(snap.Directory)
	snapshotWrite(t, snap.Directory, "unrelated", "keep")
	if err := snap.Close(context.Background()); err == nil {
		t.Fatal("accepted replacement root")
	}
	if got, err := os.ReadFile(filepath.Join(snap.Directory, "unrelated")); err != nil || string(got) != "keep" {
		t.Fatal("deleted unrelated root")
	}
}
func TestCopilotdSnapshotRefusals(t *testing.T) {
	for _, kind := range []string{"outside link", "absolute link", "ancestor link", "submodule", "submodule declaration", "missing git", "credential", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			root, pair := snapshotFixture(t)
			ctx := context.Background()
			check := func(...string) error { return nil }
			want := agentpreview.ErrSnapshotUnavailable
			switch kind {
			case "outside link":
				if err := os.Symlink("../outside", filepath.Join(root, "escape")); err != nil {
					t.Fatal(err)
				}
			case "absolute link":
				if err := os.Symlink(os.DevNull, filepath.Join(root, "escape")); err != nil {
					t.Fatal(err)
				}
			case "ancestor link":
				external := t.TempDir()
				snapshotWrite(t, external, "secret", "external")
				if err := os.Symlink(external, filepath.Join(root, "dir")); err != nil {
					t.Fatal(err)
				}
				snapshotGit(t, root, "update-index", "--add", "--cacheinfo", "100644,0000000000000000000000000000000000000001,dir/secret")
			case "submodule":
				snapshotGit(t, root, "update-index", "--add", "--cacheinfo", "160000,0000000000000000000000000000000000000001,sub")
				want = agentpreview.ErrSubmodulesUnsupported
			case "submodule declaration":
				snapshotWrite(t, root, ".gitmodules", "[submodule \"child\"]\npath = child\nurl = https://example.invalid/repo\n")
				want = agentpreview.ErrSubmodulesUnsupported
			case "missing git":
				t.Setenv("PATH", t.TempDir())
			case "credential":
				snapshotWrite(t, root, "credential", "captured-key")
				check = func(v ...string) error {
					for _, s := range v {
						if strings.Contains(s, "captured-key") {
							return agentpreview.ErrCredentialContent
						}
					}
					return nil
				}
				want = agentpreview.ErrCredentialContent
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			snap, err := makeCopilotdSnapshot(ctx, pair, check)
			if snap.Close != nil {
				defer snap.Close(context.Background())
			}
			if snap.Directory != "" || !errors.Is(err, want) {
				t.Fatalf("refusal: %+v %v want %v", snap, err, want)
			}
		})
	}
}
