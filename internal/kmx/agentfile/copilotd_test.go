package agentfile

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCopilotdPairNativeNames(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "reader.agent.md")
	tests := filepath.Join(dir, "reader.agent.test.md")
	raw := "---\r\nname: Reader\r\ntools: [bash, view, glob, task]\r\n---\r\nRead files.\r\n"
	suite := testsHeader + "```yaml agent-test\nid: read\ninput: read\nassertions: [{id: bash, type: toolCalled, tool: bash}, {id: task, type: toolNotCalled, tool: task}]\n```\n"
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tests, []byte(suite), 0600); err != nil {
		t.Fatal(err)
	}
	pair, err := LoadCopilotdPair(file, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pair.Agent.Tools, []string{"bash", "view", "glob", "task"}) || pair.Tests.Suite.Examples[0].Assertions[0].Tool != "bash" {
		t.Fatalf("lost native names: %+v", pair)
	}
	if pair.Agent.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) || string(pair.Tests.Suite.Source) != suite {
		t.Fatal("lost raw bytes")
	}
	if _, err := LoadPair(file, ""); err == nil {
		t.Fatal("embedded loader mapped native bash")
	}
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	old, err := LoadPair(file, "")
	if err != nil || !slices.Equal(old.Agent.Tools, []string{"view"}) {
		t.Fatalf("embedded mapping changed: %+v %v", old, err)
	}
	for _, header := range []string{"", "tools: []\n"} {
		if err := os.WriteFile(file, []byte("---\n"+header+"---\nRead.\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tests, []byte(suite), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCopilotdPair(file, ""); err == nil || !strings.Contains(err.Error(), "declared tool") {
			t.Fatalf("undeclared assertion accepted: %v", err)
		}
	}
}
