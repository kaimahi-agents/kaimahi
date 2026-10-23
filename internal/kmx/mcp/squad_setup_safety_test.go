package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSquadSetupRejectsStaticAliasesAndPriorTargetsBeforeInit(t *testing.T) {
	for _, kind := range []string{"git-pointer", "github-alias", "attributes-alias", "ignore-hardlink", "prior-skills", "prior-copilot", "partial-squad"} {
		t.Run(kind, func(t *testing.T) {
			root, p := squadSetupRoot(t)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			os.WriteFile(sentinel, []byte("unchanged"), 0644)
			switch kind {
			case "git-pointer":
				os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+outside+"\n"), 0644)
			case "github-alias":
				if err := os.Symlink(outside, filepath.Join(root, ".github")); err != nil {
					t.Skip("symlink unavailable")
				}
			case "attributes-alias":
				if err := os.Symlink(sentinel, filepath.Join(root, ".gitattributes")); err != nil {
					t.Skip("symlink unavailable")
				}
			case "ignore-hardlink":
				if err := os.Link(sentinel, filepath.Join(root, ".gitignore")); err != nil {
					t.Skip("hardlink unavailable")
				}
			case "prior-skills":
				os.MkdirAll(filepath.Join(root, ".github/skills"), 0755)
			case "prior-copilot":
				os.Mkdir(filepath.Join(root, ".copilot"), 0755)
			case "partial-squad":
				os.Mkdir(filepath.Join(root, ".squad"), 0755)
			}
			c, err := OpenSquadSetup(root, p)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if _, err := c.Create(context.Background(), SquadPlan{Lead: squadLead()}); err == nil {
				t.Fatal("unsafe setup was accepted")
			}
			if _, err := os.Stat(filepath.Join(root, ".init-called")); !os.IsNotExist(err) {
				t.Fatal("init ran before root preflight")
			}
			b, _ := os.ReadFile(sentinel)
			if string(b) != "unchanged" {
				t.Fatal("modified alias target")
			}
		})
	}
}
func TestSquadSetupRequiresNativeStandaloneLauncherBasename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows qualified executable suffix is covered by constructor tests")
	}
	for _, name := range []string{"Squad", "squad.exe"} {
		t.Run(name, func(t *testing.T) {
			root, p := squadSetupRoot(t)
			next := filepath.Join(filepath.Dir(p.Path), name)
			if err := os.Rename(p.Path, next); err != nil {
				t.Fatal(err)
			}
			p.Path = next
			c, err := OpenSquadSetup(root, p)
			if err == nil {
				c.Close()
				t.Fatal("native SDK launcher discovery could fall back to npx")
			}
		})
	}
}
func TestSquadSetupGitCannotDiscoverAncestorRepository(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	// macOS temp roots can contain /var aliases; production intentionally refuses them.
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(git, "init", "--quiet", parent).Run(); err != nil {
		t.Fatal(err)
	}
	_, p := squadSetupRoot(t)
	root := filepath.Join(parent, "nested")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cmd := exec.Command(git, "rev-parse", "--absolute-git-dir")
	cmd.Dir = root
	cmd.Env = append(squadEnvironment(), c.setupEnvironment()...)
	if output, err := cmd.Output(); err == nil {
		t.Fatalf("native Git still discovered ancestor repository: %s", output)
	}
}
func TestSquadSetupCancellationBeforeEffects(t *testing.T) {
	root, p := squadSetupRoot(t)
	c, err := OpenSquadSetup(root, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Create(ctx, SquadPlan{Lead: squadLead()}); err == nil {
		t.Fatal("cancelled create accepted")
	}
	if _, err := os.Stat(filepath.Join(root, ".init-called")); !os.IsNotExist(err) {
		t.Fatal("cancelled request ran init")
	}
}
