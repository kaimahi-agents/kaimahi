package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/app"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestCompanionPreviewKeepsProviderAndFileRevision(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "hello.agent.md")
	if err := os.WriteFile(file, []byte("---\nname: hello\nmodel: fake\ntools: []\n---\nSay hello.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var revision string
	for _, stage := range []string{"standalone", "empty", "tests", "directory", "edited"} {
		if stage == "empty" {
			if err := os.WriteFile(app.MarkdownToolsPath(file), []byte("schemaVersion: 1\ntools: {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "tests" || stage == "edited" {
			text := "---\nschemaVersion: 1\n---\n```yaml agent-test\nid: hello\ninput: Hi\nassertions:\n  - id: text\n    type: contains\n    value: Hello\n```\n"
			if stage == "edited" {
				text += "\nchanged commentary\n"
			}
			if err := os.WriteFile(app.MarkdownTestsPath(file), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
		}
		input := file
		if stage == "directory" {
			input = root
		}
		var out, diag bytes.Buffer
		deps, loads := testDependencies(&out, &diag)
		runner := &cliPreviewFake{verdict: runtime.EvaluationPass}
		deps.fileRunner = runner
		if err := execute([]string{"run", input, "Hi", "--provider", "fake", "-o", "json"}, deps); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		var result app.FilePreviewResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if revision == "" {
			revision = result.File.Revision
		}
		if result.File.Revision != revision || result.ModelRoute.Provider != "fake" || runner.calls != 1 || *loads != 0 {
			t.Fatalf("route changed at %s: %+v", stage, result)
		}
		if stage != "standalone" && result.SnapshotDigest == "" {
			t.Fatal("companion identity omitted")
		}
		if stage == "tests" || stage == "directory" {
			out.Reset()
			if err := execute([]string{"test", input, "--provider", "fake", "-o", "json"}, deps); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("KMX_HOME"), "agents", "instances")); !os.IsNotExist(err) {
		t.Fatal("preview created instance state")
	}
}
