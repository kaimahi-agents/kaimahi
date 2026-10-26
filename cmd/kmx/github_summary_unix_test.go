//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubSummaryRefusesLinkedOrNonregularSink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "summary.md")
	if err := os.WriteFile(regular, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "summary-link.md")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, root} {
		t.Setenv("GITHUB_STEP_SUMMARY", path)
		if file, err := openGitHubSummary(); err == nil || file != nil {
			t.Fatalf("unsafe summary sink %q accepted: %v", path, err)
		}
	}
	missing := filepath.Join(root, "created-summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", missing)
	file, err := openGitHubSummary()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(missing)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("created summary = %v, %v", info, err)
	}
}
