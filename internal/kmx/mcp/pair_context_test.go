//go:build linux || darwin

package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func pairContextRead(t *testing.T, cs *sdk.ClientSession, path, tests string) string {
	t.Helper()
	r, err := cs.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: sourcesURI(path, tests)})
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Kind, SourceVersion string }
	if json.Unmarshal([]byte(r.Contents[0].Text), &d) != nil || d.Kind != "kmx.agent-sources" || d.SourceVersion == "" {
		t.Fatalf("missing checked context: %s", r.Contents[0].Text)
	}
	return d.SourceVersion
}

func TestPairContextSelectedAliasCannotRetarget(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, "", base)
	agent := filepath.Join(root, "a.agent.md")
	tests := filepath.Join(root, "checks.md")
	other := filepath.Join(root, "other.md")
	alias := filepath.Join(t.TempDir(), "selected.md")
	for path, data := range map[string]string{agent: "Prompt\n", tests: historyTests, other: historyTests} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(tests, alias); err != nil {
		t.Fatal(err)
	}
	bound, err := f.bindPair(t.Context(), agent, alias)
	if err != nil {
		t.Fatal(err)
	}
	defer bound.close()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatal(err)
	}
	if err := bound.check(t.Context()); err == nil {
		t.Fatal("retargeted selected alias retained authority")
	}
}

func TestPairContextAbsentDirectoryAndEviction(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, "", base)
	agent := filepath.Join(root, ".github", "agents", "a.agent.md")
	if _, err := f.Create(t.Context(), agent, []byte("Prompt\n")); err != nil {
		t.Fatal(err)
	}
	bound, err := f.bindPair(t.Context(), agent, "")
	if err != nil {
		t.Fatal(err)
	}
	if bound.absent == nil || bound.absent.name != "agent-tests" {
		t.Fatal("missing nearest ancestor guard")
	}
	target, _ := agentfile.DefaultTestsPath(agent)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := bound.check(t.Context()); err == nil {
		t.Fatal("adopted newly appearing default directory")
	}
	bound.close()
	var first string
	for i := 0; i < 33; i++ {
		suite := filepath.Join(root, fmt.Sprintf("suite-%d.md", i))
		if err := os.WriteFile(suite, []byte(historyTests), 0600); err != nil {
			t.Fatal(err)
		}
		b, err := f.bindPair(t.Context(), agent, suite)
		if err != nil {
			t.Fatal(err)
		}
		token := b.sourceVersion()
		b.close()
		if i == 0 {
			first = token
		}
	}
	b, err := f.bindPair(t.Context(), agent, filepath.Join(root, "suite-0.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if len(f.pairContexts) != 32 || b.requireVersion(first) == nil {
		t.Fatal("evicted context retained authority")
	}
	if b.sourceVersion() == first {
		t.Fatal("refresh revived expired token")
	}
}

func TestPairContextObservedVersions(t *testing.T) {
	base, root := historyPaths(t)
	f := historyFiles(t, root, base)
	if _, err := f.Create(t.Context(), "a.agent.md", []byte("Prompt\n")); err != nil {
		t.Fatal(err)
	}
	tests := filepath.Join(root, "a.agent.test.md")
	if err := os.WriteFile(tests, []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	cs := historyClient(t, f)
	first := pairContextRead(t, cs, "a.agent.md", "")
	if next := pairContextRead(t, cs, "a.agent.md", ""); next != first {
		t.Fatal("unchanged context rotated")
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if next := pairContextRead(t, cs, "a.agent.md", ""); next != first {
		t.Fatal("unrelated parent timestamp invalidated present sources")
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte(historyTests), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, tests); err != nil {
		t.Fatal(err)
	}
	if next := pairContextRead(t, cs, "a.agent.md", ""); next == first {
		t.Fatal("equal bytes concealed replaced tests object")
	}
}
