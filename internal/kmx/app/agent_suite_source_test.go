package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitSuiteRepository(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	suite := filepath.Join(root, "suites", "minimal")
	copyTree(t, filepath.Join("..", "agentsuite", "testdata", "minimal"), suite)
	runGit(t, root, "init", "-q")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "suite")
	return root, suite
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSuiteSourceRevisionRecordsExactCommit(t *testing.T) {
	root, suite := gitSuiteRepository(t)
	runGit(t, root, "remote", "add", "origin", "https://github.com/example/suites.git")
	source, reason := suiteSourceRevision(context.Background(), suite)
	if source == nil {
		t.Fatalf("suiteSourceRevision() = nil, %q", reason)
	}
	if want := runGit(t, root, "rev-parse", "HEAD"); source.Commit != want {
		t.Fatalf("commit = %s, want %s", source.Commit, want)
	}
	if source.Path != "suites/minimal" || source.URI != "git+https://github.com/example/suites.git" {
		t.Fatalf("source = %+v", source)
	}
	t.Chdir(root)
	relative, reason := suiteSourceRevision(context.Background(), filepath.Join("suites", "minimal"))
	if relative == nil || *relative != *source {
		t.Fatalf("relative path: suiteSourceRevision() = %+v, %q; want %+v", relative, reason, source)
	}
}

func TestSuiteSourceRevisionRefusesAnythingPackingWouldNotReproduce(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(t *testing.T, root, suite string)
		want   string
	}{
		{"edited file", func(t *testing.T, _, suite string) {
			writeFile(t, filepath.Join(suite, "instructions", "writer.md"), "Write differently.\n")
		}, "differs from HEAD"},
		{"untracked file", func(t *testing.T, _, suite string) {
			writeFile(t, filepath.Join(suite, "notes.txt"), "x")
		}, "not in HEAD"},
		{"ignored file", func(t *testing.T, root, suite string) {
			writeFile(t, filepath.Join(root, ".git", "info", "exclude"), "*.log\n")
			writeFile(t, filepath.Join(suite, "debug.log"), "x")
		}, "not in HEAD"},
		{"deleted file", func(t *testing.T, _, suite string) {
			if err := os.Remove(filepath.Join(suite, "instructions", "writer.md")); err != nil {
				t.Fatal(err)
			}
		}, "missing from the directory"},
		{"mode change", func(t *testing.T, _, suite string) {
			if err := os.Chmod(filepath.Join(suite, "agentsuite.json"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "differs from HEAD"},
		{"empty directory", func(t *testing.T, _, suite string) {
			if err := os.Mkdir(filepath.Join(suite, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "empty directory"},
		{"line-ending attribute added after the files were written", func(t *testing.T, root, _ string) {
			writeFile(t, filepath.Join(root, ".gitattributes"), "*.md text eol=crlf\n")
			runGit(t, root, "add", ".gitattributes")
			runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "eol")
		}, "eol=crlf"},
		{"edit hidden by skip-worktree", func(t *testing.T, root, suite string) {
			runGit(t, root, "update-index", "--skip-worktree", "suites/minimal/agentsuite.json")
			writeFile(t, filepath.Join(suite, "agentsuite.json"), "{}")
		}, "differs from HEAD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, suite := gitSuiteRepository(t)
			test.change(t, root, suite)
			source, reason := suiteSourceRevision(context.Background(), suite)
			if source != nil || !strings.Contains(reason, test.want) {
				t.Fatalf("suiteSourceRevision() = %+v, %q; want refusal containing %q", source, reason, test.want)
			}
		})
	}
}

func TestSuiteSourceRevisionAllowsTextAuto(t *testing.T) {
	root, suite := gitSuiteRepository(t)
	writeFile(t, filepath.Join(root, ".gitattributes"), "* text=auto\n")
	runGit(t, root, "add", ".gitattributes")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "auto")
	if source, reason := suiteSourceRevision(context.Background(), suite); source == nil {
		t.Fatalf("text=auto refused: %q", reason)
	}
}

func TestSuiteSourceRevisionOutsideGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	if source, reason := suiteSourceRevision(context.Background(), t.TempDir()); source != nil || reason != "not in a Git repository" {
		t.Fatalf("suiteSourceRevision() = %+v, %q", source, reason)
	}
}

func TestPublicRepositoryURI(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/example/suites.git":           "git+https://github.com/example/suites.git",
		"git@github.com:example/suites.git":               "git+https://github.com/example/suites.git",
		"https://token@github.com/example/suites.git":     "",
		"https://user:secret@github.com/example/suites":   "",
		"https://github.com/example/suites.git?token=abc": "",
		"http://github.com/example/suites.git":            "",
		"/srv/git/suites.git":                             "",
		"ssh://git@github.com/example/suites.git":         "",
	} {
		if got := publicRepositoryURI(remote); got != want {
			t.Errorf("publicRepositoryURI(%q) = %q, want %q", remote, got, want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
