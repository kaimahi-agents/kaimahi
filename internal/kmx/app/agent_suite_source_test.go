package app

import (
	"context"
	"crypto/sha1"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

func gitSuiteRepository(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	suite := filepath.Join(root, "suites", "minimal")
	copyTree(t, filepath.Join("..", "..", "..", "testdata", "agentsuite", "minimal"), suite)
	runGit(t, root, "init", "-q")
	// A commit may otherwise launch detached auto-maintenance that keeps writing
	// under .git after git exits and races t.TempDir cleanup.
	runGit(t, root, "config", "gc.auto", "0")
	runGit(t, root, "config", "maintenance.auto", "false")
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

func gitWorkspaceRepository(t *testing.T, nested bool) (string, string, *workspace.Prepared) {
	t.Helper()
	root := t.TempDir()
	source := root
	if nested {
		source = filepath.Join(root, "workspaces", "greeter")
	}
	input := filepath.Join(t.TempDir(), "input.md")
	writeFile(t, input, "Answer briefly.\n")
	if err := workspace.Create(context.Background(), source, workspace.CreateOptions{Name: "greeter", Model: "test-model", InstructionsFile: input}); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-q")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "workspace")
	prepared, err := workspace.Prepare(context.Background(), source, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	return root, source, prepared
}

func TestWorkspaceSourceRevisionUsesPreparedAuthoredInputs(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprintf("nested %t", nested), func(t *testing.T) {
			root, source, prepared := gitWorkspaceRepository(t, nested)
			writeFile(t, filepath.Join(source, "unrelated.txt"), "Not selected or distributed.\n")
			files, err := workspace.SourceFiles(context.Background(), source, prepared)
			if err != nil {
				t.Fatal(err)
			}
			options, warning := suitePushOptions(context.Background(), source, SuitePushProvenance{Enabled: true, Require: true, BuilderVersion: "v9.9.9"}, files)
			if warning != "" || options.Provenance == nil || !options.Provenance.Require || options.Provenance.BuilderVersion != "v9.9.9" {
				t.Fatalf("workspace push options: %+v, %q", options, warning)
			}
			revision := options.Provenance.Source
			if revision == nil || revision.Commit != runGit(t, root, "rev-parse", "HEAD") {
				t.Fatalf("prepared workspace revision: %+v, %q", revision, warning)
			}
			wantPath := "."
			if nested {
				wantPath = "workspaces/greeter"
			}
			if revision.Path != wantPath {
				t.Fatalf("source path = %q, want %q", revision.Path, wantPath)
			}
			writeFile(t, filepath.Join(source, "agents/greeter/instructions.md"), "Different authored source.\n")
			// Commit from the repository root without relying on source path spelling.
			runGit(t, root, "add", "-u")
			runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "changed source")
			if revision, _ := sourceRevision(context.Background(), source, files); revision != nil {
				t.Fatal("frozen candidate claimed the later source commit")
			}
			if _, err := workspace.SourceFiles(context.Background(), source, prepared); err == nil {
				t.Fatal("changed source reused old candidate inputs")
			}
		})
	}
}

func TestWorkspaceSourceRevisionAllowsNestedMemberNamedInstructions(t *testing.T) {
	root, source, _ := gitWorkspaceRepository(t, true)
	file := filepath.Join(source, "agents/greeter/member.yaml")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, strings.Replace(string(data), "instructions: instructions.md", "instructions: notes/member.yaml", 1))
	if err := os.Mkdir(filepath.Join(source, "agents/greeter/notes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(source, "agents/greeter/instructions.md"), filepath.Join(source, "agents/greeter/notes/member.yaml")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "nested instructions")
	prepared, err := workspace.Prepare(context.Background(), source, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := workspace.SourceFiles(context.Background(), source, prepared)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := parseLsTree([]byte(runGit(t, root, "ls-tree", "-r", "-z", "--full-tree", "HEAD")), "workspaces/greeter", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if reason := matchWorkspaceTree(context.Background(), inputs, committed, sha1.New); reason != "" {
			t.Fatalf("nested instructions confused member inventory: %s", reason)
		}
	}
}

func TestWorkspaceSourceRevisionIgnoresUnselectedGitlinks(t *testing.T) {
	root, source, prepared := gitWorkspaceRepository(t, false)
	commit := runGit(t, root, "rev-parse", "HEAD")
	runGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+commit+",vendor/library")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "unrelated submodule")
	inputs, err := workspace.SourceFiles(context.Background(), source, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if revision, reason := sourceRevision(context.Background(), source, inputs); revision == nil {
		t.Fatalf("unselected gitlink suppressed source commit: %s", reason)
	}
}

func TestWorkspaceSourceRevisionChecksHEADAttributesNotJustLiveOverrides(t *testing.T) {
	root, source, _ := gitWorkspaceRepository(t, true)
	writeFile(t, filepath.Join(root, ".gitattributes"), "*.md filter=custom\n")
	runGit(t, root, "add", ".gitattributes")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "rewriting attribute")
	writeFile(t, filepath.Join(root, ".gitattributes"), "*.md -filter\n")
	prepared, err := workspace.Prepare(context.Background(), source, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := workspace.SourceFiles(context.Background(), source, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if revision, reason := sourceRevision(context.Background(), source, files); revision != nil || !strings.Contains(reason, "attribute") {
		t.Fatalf("HEAD rewriting attribute masked by live override: %+v, %q", revision, reason)
	}
}

func TestWorkspaceSourceRevisionRefusesDeletedCommittedCase(t *testing.T) {
	root, source, _ := gitWorkspaceRepository(t, true)
	data, err := os.ReadFile(filepath.Join(source, "eval/example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "eval/second.yaml"), string(data))
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "second case")
	if err := os.Remove(filepath.Join(source, "eval/second.yaml")); err != nil {
		t.Fatal(err)
	}
	prepared, err := workspace.Prepare(context.Background(), source, filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := workspace.SourceFiles(context.Background(), source, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if revision, _ := sourceRevision(context.Background(), source, files); revision != nil {
		t.Fatal("missing selected case still claimed HEAD")
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

func TestSuiteSourceRevisionIgnoresInheritedRepositoryLocation(t *testing.T) {
	root, suite := gitSuiteRepository(t)
	wantCommit := runGit(t, root, "rev-parse", "HEAD")
	other, _ := gitSuiteRepository(t)
	writeFile(t, filepath.Join(other, "unrelated.txt"), "another repository\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "unrelated")
	if runGit(t, other, "rev-parse", "HEAD") == wantCommit {
		t.Fatal("fixture repositories must have different commits")
	}
	otherGit := filepath.Join(other, ".git")
	for _, test := range []struct {
		name string
		env  map[string]string
	}{
		{"hook repository", map[string]string{"GIT_DIR": otherGit, "GIT_WORK_TREE": root}},
		{"git directory", map[string]string{"GIT_DIR": otherGit}},
		{"work tree", map[string]string{"GIT_WORK_TREE": other}},
		{"index", map[string]string{"GIT_INDEX_FILE": filepath.Join(otherGit, "index")}},
		{"object directory", map[string]string{"GIT_OBJECT_DIRECTORY": filepath.Join(otherGit, "objects")}},
		{"alternate objects", map[string]string{"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(otherGit, "objects")}},
		{"common directory", map[string]string{"GIT_COMMON_DIR": otherGit}},
		{"namespace", map[string]string{"GIT_NAMESPACE": "other"}},
		{"shallow file", map[string]string{"GIT_SHALLOW_FILE": filepath.Join(otherGit, "missing-shallow")}},
		{"replacement refs", map[string]string{"GIT_REPLACE_REF_BASE": "refs/other/"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			source, reason := suiteSourceRevision(context.Background(), suite)
			if source == nil || source.Commit != wantCommit || source.Path != "suites/minimal" {
				t.Fatalf("suiteSourceRevision() = %+v, %q; want source commit %s", source, reason, wantCommit)
			}
		})
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
