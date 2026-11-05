package agentfile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
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

func composeNative(t *testing.T, agentPath string, agent []byte, selection TestsSelection, tests []byte, present bool) *OrkaSourcePair {
	t.Helper()
	p, err := ComposeOrkaSourcePair(agentPath, agent, selection, tests, present)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("composition returned neither a pair nor an error")
	}
	return p
}

func wantOrkaRevision(raw string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(raw))) }

// A composition from caller-held bytes must agree with the checked loader on
// every source-derived field, while never inheriting its filesystem observations.
func TestComposeOrkaSourcePairMatchesCheckedLoad(t *testing.T) {
	raw := testsHeader + "# Kept prose\n" + testFence("z-last") + testFence("a-first")
	for _, relative := range []string{"reader.agent.md", ".github/agents/reader.agent.md"} {
		root := realTemp(t)
		agent := filepath.Join(root, filepath.FromSlash(relative))
		pairWrite(t, agent, orkaExample)
		target, _ := DefaultTestsPath(agent)
		for _, present := range []bool{false, true} {
			if present {
				pairWrite(t, target, raw)
			}
			loaded := nativePair(t, agent, "")
			// A caller-supplied Key is never trusted; it is recomputed from the path.
			selection := TestsSelection{Path: target, Key: "sha256:forged"}
			var testsBytes []byte
			if present {
				testsBytes = []byte(raw)
			}
			p := composeNative(t, agent, []byte(orkaExample), selection, testsBytes, present)
			if p.AgentPath != loaded.AgentPath || p.Agent.Revision != wantOrkaRevision(orkaExample) || p.Tests.State != loaded.Tests.State ||
				p.Tests.Revision != loaded.Tests.Revision || p.Tests.Selection != loaded.Tests.Selection || p.Tests.Selection.Key == "sha256:forged" ||
				p.SnapshotRevision != loaded.SnapshotRevision || !reflect.DeepEqual(p.Unsupported, loaded.Unsupported) {
				t.Fatalf("present=%v composition diverged from checked load: %#v vs %#v", present, p, loaded)
			}
			if present != (p.Tests.Suite != nil) || (present && !bytes.Equal(p.Tests.Suite.Source, []byte(raw))) {
				t.Fatalf("present=%v lost or invented tests suite", present)
			}
			if p.SameSourceVersion(loaded) || loaded.SameSourceVersion(p) || p.SameSourceVersion(p) {
				t.Fatalf("present=%v pure composition satisfied checked currentness", present)
			}
		}
	}
}

func TestComposeOrkaSourcePairRetainsExactLFAndCRLFBytes(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	lf := testsHeader + testFence("case")
	crlfAgent, crlfTests := strings.ReplaceAll(orkaExample, "\n", "\r\n"), strings.ReplaceAll(lf, "\n", "\r\n")
	var revisions []string
	for _, c := range []struct{ agent, tests string }{{orkaExample, lf}, {crlfAgent, crlfTests}} {
		p := composeNative(t, agent, []byte(c.agent), TestsSelection{Path: tests}, []byte(c.tests), true)
		if string(p.Agent.Source) != c.agent || string(p.Tests.Suite.Source) != c.tests ||
			p.Agent.Revision != wantOrkaRevision(c.agent) || p.Tests.Revision != wantOrkaRevision(c.tests) {
			t.Fatalf("source bytes or revision normalized: %q", c.agent)
		}
		revisions = append(revisions, p.SnapshotRevision)
	}
	if revisions[0] == revisions[1] {
		t.Fatal("CRLF originals share the LF revision")
	}
}

func TestComposeOrkaSourcePairKeepsOrderedAuthoredExecutionContent(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	raw := testsHeader + "```yaml agent-test\nid: First\ninput: Ada\nmocks: {tools: [{name: ping, mode: live}]}\nassertions:\n- {id: one, type: contains, value: Ada}\n- {id: two, type: toolNotCalled, tool: ping}\n- {id: three, type: llmJudge, rubric: Say hello, minScore: 80, judgeVersion: v1}\n```\n" + testFence("second")
	p := composeNative(t, agent, []byte(orkaExample), TestsSelection{Path: tests}, []byte(raw), true)
	want := []OrkaUnsupported{{"Tests[0].Mocks", "unrepresented"}, {"Tests[0].Assertions[1]", "not_yet_supported"}, {"Tests[0].Assertions[2]", "unrepresented"}}
	examples := p.Tests.Suite.Examples
	if !reflect.DeepEqual(p.Unsupported, want) || len(examples) != 2 || examples[0].ID != "First" || examples[1].ID != "second" ||
		examples[0].Mocks == nil || len(examples[0].Assertions) != 3 || examples[0].Assertions[0].ID != "one" || examples[0].Assertions[2].ID != "three" {
		t.Fatalf("authorable content lost, reordered or relabelled: %#v", p.Unsupported)
	}
}

func TestComposeOrkaSourcePairReportsAbsentAndEmptyTests(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	want := []OrkaUnsupported{{"Tests", "unrepresented"}}
	absent := composeNative(t, agent, []byte(orkaExample), TestsSelection{Path: tests}, nil, false)
	if absent.Tests.State != "absent" || absent.Tests.Suite != nil || absent.Tests.Revision != "" || !reflect.DeepEqual(absent.Unsupported, want) {
		t.Fatalf("absent companion not reported: %#v", absent.Tests)
	}
	empty := composeNative(t, agent, []byte(orkaExample), TestsSelection{Path: tests}, []byte(testsHeader), true)
	if empty.Tests.State != "external" || len(empty.Tests.Suite.Examples) != 0 || !reflect.DeepEqual(empty.Unsupported, want) {
		t.Fatalf("empty companion implied executable cases: %#v", empty.Tests)
	}
	if empty.SnapshotRevision == absent.SnapshotRevision {
		t.Fatal("empty and absent companions share identity")
	}
}

func TestComposeOrkaSourcePairSelectionRules(t *testing.T) {
	root := realTemp(t)
	sep := string(filepath.Separator)
	agent := filepath.Join(root, ".github", "agents", "reader.agent.md")
	standard := filepath.Join(root, ".github", "agent-tests", "reader.agent.test.md")
	alternate := filepath.Join(root, "alternate.md")
	raw := []byte(testsStarter)
	implicit := composeNative(t, agent, []byte(orkaExample), TestsSelection{Path: standard}, raw, true)
	explicit := composeNative(t, agent, []byte(orkaExample), TestsSelection{Path: standard, Explicit: true}, raw, true)
	if implicit.SnapshotRevision != explicit.SnapshotRevision || implicit.Tests.Selection.Explicit || !explicit.Tests.Selection.Explicit {
		t.Fatal("explicit intent must be recorded without entering source identity")
	}
	other := composeNative(t, agent, []byte(orkaExample), TestsSelection{Path: alternate, Explicit: true}, raw, true)
	if other.SnapshotRevision == explicit.SnapshotRevision || other.Tests.Selection.Key == explicit.Tests.Selection.Key {
		t.Fatal("selected path omitted from identity")
	}
	unclean := composeNative(t, root+sep+".github"+sep+"."+sep+"agents"+sep+"reader.agent.md", []byte(orkaExample), TestsSelection{Path: root + sep + ".github" + sep + "agent-tests" + sep + "." + sep + "reader.agent.test.md"}, raw, true)
	if unclean.AgentPath != agent || unclean.Tests.Selection.Path != standard || unclean.SnapshotRevision != implicit.SnapshotRevision {
		t.Fatal("equivalent spellings produced different identities")
	}
}

func TestComposeOrkaSourcePairRejectsInvalidTuples(t *testing.T) {
	root := realTemp(t)
	sep := string(filepath.Separator)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	valid := []byte(testsStarter)
	for _, c := range []struct {
		name      string
		agentPath string
		agent     []byte
		selection TestsSelection
		tests     []byte
		present   bool
		unsupport bool
	}{
		{"wrong leaf", filepath.Join(root, "reader.md"), []byte(orkaExample), TestsSelection{Path: tests}, valid, true, true},
		{"empty NAME", filepath.Join(root, ".agent.md"), []byte(orkaExample), TestsSelection{Path: tests}, valid, true, true},
		{"tests leaf as agent", filepath.Join(root, "reader.agent.test.md"), []byte(orkaExample), TestsSelection{Path: tests}, valid, true, true},
		{"relative agent", "reader.agent.md", []byte(orkaExample), TestsSelection{Path: tests}, valid, true, false},
		{"agent traversal", root + sep + "x" + sep + ".." + sep + "reader.agent.md", []byte(orkaExample), TestsSelection{Path: tests}, valid, true, false},
		{"relative tests", agent, []byte(orkaExample), TestsSelection{Path: "reader.agent.test.md", Explicit: true}, valid, true, false},
		{"tests traversal", agent, []byte(orkaExample), TestsSelection{Path: root + sep + "x" + sep + ".." + sep + "t.md", Explicit: true}, valid, true, false},
		{"identical roles", agent, []byte(orkaExample), TestsSelection{Path: agent, Explicit: true}, valid, true, false},
		{"identical after dot cleaning", agent, []byte(orkaExample), TestsSelection{Path: root + sep + "." + sep + "reader.agent.md", Explicit: true}, valid, true, false},
		{"no selected path", agent, []byte(orkaExample), TestsSelection{}, nil, false, false},
		{"default names another file", agent, []byte(orkaExample), TestsSelection{Path: filepath.Join(root, "other.test.md")}, valid, true, false},
		{"explicit but absent", agent, []byte(orkaExample), TestsSelection{Path: tests, Explicit: true}, nil, false, false},
		{"case-fold test IDs", agent, []byte(orkaExample), TestsSelection{Path: tests}, []byte(testsHeader + testFence("case") + testFence("CASE")), true, false},
		{"unknown test field", agent, []byte(orkaExample), TestsSelection{Path: tests}, []byte(strings.Replace(testsStarter, "input: Ada", "input: Ada\nunknown: value", 1)), true, false},
		{"not a native agent", agent, []byte("# plain prompt\n"), TestsSelection{Path: tests}, valid, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := ComposeOrkaSourcePair(c.agentPath, c.agent, c.selection, c.tests, c.present)
			if err == nil || p != nil {
				t.Fatalf("accepted invalid tuple: %#v", p)
			}
			if errors.Is(err, ErrOrkaSourceUnsupported) != c.unsupport {
				t.Fatalf("wrong diagnostic class: %v", err)
			}
		})
	}
}

// The returned pair owns its bytes: later caller mutation changes neither source
// text nor anything derived from it.
func TestComposeOrkaSourcePairCopiesCallerBuffers(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	agentBytes, testsBytes := []byte(orkaExample), []byte(testsStarter)
	p := composeNative(t, agent, agentBytes, TestsSelection{Path: tests}, testsBytes, true)
	for i := range agentBytes {
		agentBytes[i] = 'X'
	}
	for i := range testsBytes {
		testsBytes[i] = 'X'
	}
	if string(p.Agent.Source) != orkaExample || string(p.Tests.Suite.Source) != testsStarter {
		t.Fatal("pair aliases caller buffers")
	}
}

func TestComposeOrkaSourcePairKeepsAuthoredAgentExtensionsAndOwnsUnsupportedList(t *testing.T) {
	root := realTemp(t)
	agent := filepath.Join(root, "reader.agent.md")
	tests := filepath.Join(root, "reader.agent.test.md")
	source := strings.Replace(orkaExample, "model: model/v1\n", "model: model/v1\ncolor: blue\n", 1)
	p := composeNative(t, agent, []byte(source), TestsSelection{Path: tests}, []byte(testsStarter), true)
	want := []OrkaUnsupported{{"Frontmatter", "unrepresented"}}
	if !reflect.DeepEqual(p.Unsupported, want) || string(p.Agent.Source) != source {
		t.Fatalf("authorable agent content dropped: %#v", p.Unsupported)
	}
	p.Unsupported[0].Field = "Changed"
	if p.Agent.Unsupported[0].Field != "Frontmatter" {
		t.Fatal("pair shares unsupported storage with its parsed agent")
	}
}
