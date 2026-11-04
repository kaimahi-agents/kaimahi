package agentfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func nativePair(t *testing.T, agent, tests string) *OrkaSourcePair {
	t.Helper()
	p, err := LoadOrkaPair(agent, tests)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOrkaPairSelectionAndSourceIdentity(t *testing.T) {
	for _, relative := range []string{"reader.agent.md", ".github/agents/reader.agent.md", ".github/agents/deep/reader.agent.md"} {
		root := realTemp(t)
		agent := filepath.Join(root, filepath.FromSlash(relative))
		pairWrite(t, agent, orkaExample)
		absent := nativePair(t, agent, "")
		if absent.Tests.State != "absent" || absent.Tests.Suite != nil || absent.SnapshotRevision == absent.Agent.Revision {
			t.Fatal("wrong absent identity")
		}
		if len(absent.Unsupported) != 1 || absent.Unsupported[0] != (OrkaUnsupported{"Tests", "unrepresented"}) {
			t.Fatal("absent tests implied representability")
		}
		target := filepath.Join(filepath.Dir(agent), "reader.agent.test.md")
		if relative == ".github/agents/reader.agent.md" {
			target = filepath.Join(root, ".github", "agent-tests", "reader.agent.test.md")
		}
		raw := testsHeader + "# Kept prose\n```text\nunrelated\n```\n" + testFence("z-last") + testFence("a-first")
		pairWrite(t, target, raw)
		p := nativePair(t, agent, "")
		if p.Tests.State != "external" || p.Tests.Selection.Path != target || p.Tests.Selection.Key == "" || !bytes.Equal(p.Tests.Suite.Source, []byte(raw)) || len(p.Unsupported) != 0 {
			t.Fatal("lost selected source")
		}
		examples := p.Tests.Suite.Examples
		if examples[0].ID != "z-last" || examples[1].ID != "a-first" || examples[0].Assertions[0].ID != "a" || examples[0].Assertions[0].Value != "Ada" {
			t.Fatal("ordered assertions changed")
		}
		if strings.Contains(p.Agent.Instructions, "Kept prose") || strings.Contains(p.Agent.Instructions, "assertions:") {
			t.Fatal("companion entered instructions")
		}
		if !p.SameSourceVersion(nativePair(t, agent, "")) || !p.SameSourceVersion(nativePair(t, agent, target)) {
			t.Fatal("unchanged selected files stale")
		}
		if p.SnapshotRevision == absent.SnapshotRevision {
			t.Fatal("new companion did not change revision")
		}
		alternate := filepath.Join(root, "alternate.md")
		pairWrite(t, alternate, raw)
		selected := nativePair(t, agent, alternate)
		if selected.SnapshotRevision == p.SnapshotRevision || !selected.Tests.Selection.Explicit {
			t.Fatal("override selection omitted from identity")
		}
	}
}

func TestOrkaPairUnsupportedExecutionContentIsNotDropped(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	pairWrite(t, agent, orkaExample)
	tests := filepath.Join(root, "reader.agent.test.md")
	raw := testsHeader + "```yaml agent-test\nid: private-case-canary\ninput: Ada\nmocks: {tools: [{name: ping, mode: live}]}\nassertions:\n- {id: first, type: toolCalled, tool: ping}\n- {id: second, type: llmJudge, rubric: Say hello, minScore: 80, judgeVersion: v1}\n```\n"
	pairWrite(t, tests, raw)
	p := nativePair(t, agent, "")
	want := []OrkaUnsupported{{"Tests[0].Mocks", "unrepresented"}, {"Tests[0].Assertions[0]", "not_yet_supported"}, {"Tests[0].Assertions[1]", "unrepresented"}}
	if !reflect.DeepEqual(p.Unsupported, want) || p.Tests.Suite.Examples[0].Mocks == nil || p.Tests.Suite.Examples[0].Assertions[1].JudgeModel != "" || string(p.Tests.Suite.Source) != raw {
		t.Fatalf("execution content lost or relabelled: %#v", p.Unsupported)
	}
	pairWrite(t, tests, testsHeader)
	if p := nativePair(t, agent, ""); len(p.Unsupported) != 1 || p.Unsupported[0].Field != "Tests" {
		t.Fatal("empty tests implied execution support")
	}
}

func TestOrkaPairRejectsInvalidInputs(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	pairWrite(t, agent, orkaExample)
	if _, err := LoadOrkaPair(agent, filepath.Join(root, "missing.md")); err == nil {
		t.Fatal("missing explicit tests treated as absent")
	}
	if _, err := LoadOrkaPair(agent, agent); err == nil {
		t.Fatal("same source used for both roles")
	}
	for _, raw := range []string{testsHeader + testFence("case") + testFence("CASE"), strings.Replace(testsStarter, "input: Ada", "input: Ada\nunknown: value", 1)} {
		pairWrite(t, tests, raw)
		if _, err := LoadOrkaPair(agent, ""); err == nil {
			t.Fatal("invalid native test inventory accepted")
		}
	}
	for _, name := range []string{"reader.md", "charter.md", "reader.agent.test.md", ".agent.md"} {
		path := filepath.Join(root, name)
		pairWrite(t, path, orkaExample)
		if _, err := LoadOrkaPair(path, ""); !errors.Is(err, ErrOrkaSourceUnsupported) {
			t.Fatalf("wrong agent filename accepted: %v", err)
		}
	}
	pairWrite(t, agent, orkaExample+strings.Repeat("x", MaxFileBytes))
	if _, err := LoadOrkaPair(agent, ""); err == nil {
		t.Fatal("oversize source accepted")
	}
	pairWrite(t, agent, orkaExample)
	pairWrite(t, tests, strings.Repeat("x", MaxFileBytes+1))
	if _, err := LoadOrkaPair(agent, ""); err == nil {
		t.Fatal("oversize tests accepted")
	}
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tests, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrkaPair(agent, ""); err == nil {
		t.Fatal("tests directory treated as absence")
	}
}

func TestOrkaPairReplacementAndPureValuesHaveNoAuthority(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	pairWrite(t, agent, orkaExample)
	pairWrite(t, tests, testsStarter)
	for _, path := range []string{agent, tests} {
		before := nativePair(t, agent, "")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := filepath.Join(root, "replacement")
		pairWrite(t, replacement, string(raw))
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		after := nativePair(t, agent, "")
		if before.SnapshotRevision != after.SnapshotRevision || before.SameSourceVersion(after) {
			t.Fatal("equal bytes concealed replacement")
		}
	}
	before := nativePair(t, agent, "")
	pairWrite(t, tests, testsStarter+"\nChanged prose.\n")
	if before.SameSourceVersion(nativePair(t, agent, "")) {
		t.Fatal("changed tests current")
	}
	pure := &OrkaSourcePair{AgentPath: before.AgentPath, Agent: before.Agent, Tests: before.Tests, SnapshotRevision: before.SnapshotRevision}
	if pure.SameSourceVersion(before) || pure.SameSourceVersion(pure) || (*OrkaSourcePair)(nil).SameSourceVersion(before) {
		t.Fatal("pure values established filesystem authority")
	}
}
