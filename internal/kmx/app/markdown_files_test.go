package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMarkdownDirectorySplitCompanionsImportAndDrift(t *testing.T) {
	t.Setenv("KMX_HOME", t.TempDir())
	root := t.TempDir()
	path := filepath.Join(root, "weather.agent.md")
	writeFile(t, path, strings.Replace(markdownTestAgent, "tools: []", "tools: [go_version]", 1))
	spec := CLIToolSpec{Description: "Read Go version", Command: "go", Args: []CLIToolArg{{Literal: "version"}}, Parameters: map[string]CLIToolParameter{}}
	spec.Execution.Timeout = "5s"
	spec.Execution.MaxOutputBytes = 1024
	spec.Execution.Approval = "granted-scope"
	leaf, _ := yaml.Marshal(markdownToolsFile{SchemaVersion: 1, Tools: map[string]CLIToolSpec{"go_version": spec}})
	writeFile(t, filepath.Join(root, "tools", "go.yaml"), string(leaf))
	writeFile(t, MarkdownToolsPath(path), "schemaVersion: 1\ntools: {}\nfiles: [tools/go.yaml]\n")
	writeFile(t, MarkdownTestsPath(path), "---\nschemaVersion: 1\nfiles: [tests/empty.tests.md]\n---\n# Tests\n")
	writeFile(t, filepath.Join(root, "tests", "empty.tests.md"), emptyMarkdownTests)
	a, err := ReadMarkdownAgent(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path != path || len(a.CLITools) != 1 || len(a.ToolSources) != 2 || len(a.TestSources) != 2 {
		t.Fatalf("bad closure: %+v", a)
	}
	destination := filepath.Join(t.TempDir(), "imported")
	saved, err := ImportMarkdownDirectory(t.Context(), root, destination)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadMarkdownAgent(saved)
	if err != nil {
		t.Fatal(err)
	}
	if a.ToolsRevision != b.ToolsRevision || a.TestsRevision != b.TestsRevision || !bytes.Equal(a.Source, b.Source) {
		t.Fatal("copy changed identities")
	}
	writeFile(t, filepath.Join(root, "tools", "go.yaml"), string(leaf)+"# changed\n")
	if err := a.CheckCurrent(); err == nil {
		t.Fatal("tool-only drift ignored")
	}
	writeFile(t, MarkdownToolsPath(path), "schemaVersion: 1\ntools: {}\nfiles: [../escape.yaml]\n")
	if _, err := ReadMarkdownAgent(path); err == nil {
		t.Fatal("escaping reference accepted")
	}
}

func TestMarkdownToolOnlyEditRequiresApprovalAndChangesTestIdentity(t *testing.T) {
	path, bin := cliFixture(t)
	// This fixture currently supplies its spec inline; move it into the new file.
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.Split(cliToolAgent, "\n## Tool bindings")[0] + "\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	doc := markdownToolsFile{SchemaVersion: 1, Tools: map[string]CLIToolSpec{"get_cluster": a.CLITools[0]}}
	b, _ := yaml.Marshal(doc)
	writeFile(t, MarkdownToolsPath(path), string(b))
	fakeTool(t, bin, "fixture-cli", `printf 'fixture 2.4\n'`)
	p, err := PrepareCLIToolApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyCLIToolApproval(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	before, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachApprovedTools(before); err != nil {
		t.Fatal(err)
	}
	lease, _, err := beginAgentInstance(before, "")
	if err != nil {
		t.Fatal(err)
	}
	defer lease.finish(nil)
	writeFile(t, MarkdownToolsPath(path), string(b)+"# edit\n")
	after, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || before.PairRevision == after.PairRevision {
		t.Fatal("tool edit identity wrong")
	}
	if err := attachApprovedTools(after); err == nil {
		t.Fatal("old approval accepted tool change")
	}
	if err := ApplyCLIToolApproval(t.Context(), &p); err == nil {
		t.Fatal("stale plan applied")
	}
	frozen, err := loadAgentVersion(lease.record)
	if err != nil || len(frozen.CLITools) != 1 || frozen.ToolsRevision != before.ToolsRevision {
		t.Fatal("cached instance did not preserve approved tool snapshot", err)
	}
}

func TestMarkdownDirectoryAmbiguityAndOptionalCompanions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.md")
	writeFile(t, path, "Just say hello.\n")
	a, err := ReadMarkdownAgent(root)
	if err != nil || len(a.Examples) != 0 {
		t.Fatal(err)
	}
	writeFile(t, MarkdownToolsPath(path), emptyMarkdownTools)
	if err := a.CheckCurrent(); err == nil {
		t.Fatal("new companion did not invalidate snapshot")
	}
	writeFile(t, filepath.Join(root, "second.agent.md"), "Say hi")
	if _, err := ReadMarkdownAgent(root); err == nil {
		t.Fatal("ambiguous directory selected")
	}
}
