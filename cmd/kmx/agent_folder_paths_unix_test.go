//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestFolderTestUnsafeDiscoveryAndCandidates(t *testing.T) {
	for _, kind := range []string{"github-link", "agents-link", "file-link", "file-directory", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			dir := folderDirectory(t)
			folderAgent(t, dir, "valid.agent.md")
			target := t.TempDir()
			switch kind {
			case "github-link":
				if err := os.Symlink(target, filepath.Join(dir, ".github")); err != nil {
					t.Fatal(err)
				}
			case "agents-link":
				if err := os.Mkdir(filepath.Join(dir, ".github"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, ".github", "agents")); err != nil {
					t.Fatal(err)
				}
			case "file-link":
				file, _ := folderAgent(t, target, "outside.agent.md")
				if err := os.Symlink(file, filepath.Join(dir, "bad.agent.md")); err != nil {
					t.Fatal(err)
				}
			case "file-directory":
				if err := os.Mkdir(filepath.Join(dir, "bad.agent.md"), 0700); err != nil {
					t.Fatal(err)
				}
			case "unreadable":
				path := filepath.Join(dir, ".github", "agents")
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0700)
				if _, err := os.ReadDir(path); err == nil {
					t.Skip("permission denial unavailable")
				}
			}
			var out, diag bytes.Buffer
			deps, _ := testDependencies(&out, &diag)
			fake := &cliPreviewFake{verdict: runtime.EvaluationPass}
			deps.fileRunner = fake
			r := readFolderReport(t, &out, execute([]string{"test", "-o", "json"}, deps), 2)
			if kind == "file-link" || kind == "file-directory" {
				if fake.calls != 1 || r.Summary.Passed != 1 || r.Summary.CouldNotRun != 1 {
					t.Fatalf("unsafe candidate ignored or followed: calls%d counts%+v", fake.calls, r.Summary)
				}
			} else if fake.calls != 0 || r.ReasonCode != "discovery_failed" {
				t.Fatalf("unsafe discovery ignored: calls%d %+v", fake.calls, r)
			}
		})
	}
}
