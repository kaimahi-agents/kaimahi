package agentfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func pairWrite(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}
func pairLoad(t *testing.T, agent, tests string) *Pair {
	t.Helper()
	p, err := LoadPair(agent, tests)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func pairRefusal(t *testing.T, agent, tests, field, reason string) {
	t.Helper()
	p, err := LoadPair(agent, tests)
	var e *Error
	if p != nil || !errors.As(err, &e) || e.Field != field || !strings.Contains(e.Problem, reason) {
		t.Fatalf("want %s/%s refusal, got %v %v", field, reason, p, err)
	}
}

func TestCompanionPath(t *testing.T) {
	for _, tt := range []struct {
		in, out string
		ok      bool
	}{
		{"greeter.agent.md", "greeter.agent.test.md", true},
		{".github/agents/greeter.agent.md", ".github/agent-tests/greeter.agent.test.md", true},
		{"repo/.github/agents/greeter.agent.md", "repo/.github/agent-tests/greeter.agent.test.md", true},
		{"repo/agents/greeter.agent.md", "repo/agents/greeter.agent.test.md", true},
		{"repo/.github/agents/deep/greeter.agent.md", "repo/.github/agents/deep/greeter.agent.test.md", true},
		{"repo/other.github/agents/greeter.agent.md", "repo/other.github/agents/greeter.agent.test.md", true},
		{"charter.md", "", false}, {"greeter.agent.test.md", "", false}, {".agent.md", "", false}, {"", "", false},
	} {
		got, ok := CompanionPath(filepath.FromSlash(tt.in))
		if got != filepath.FromSlash(tt.out) || ok != tt.ok {
			t.Fatalf("%q: %q %t", tt.in, got, ok)
		}
	}
}

func TestComposePairSelectionAndNonAuthority(t *testing.T) {
	root := realTemp(t)
	agentPath := filepath.Join(root, "g.agent.md")
	testsPath := filepath.Join(root, "g.agent.test.md")
	agent, err := Parse(agentPath, []byte("# Greeter\nSay hello.\n"))
	if err != nil {
		t.Fatal(err)
	}
	before := *agent
	makePair := func(path string, explicit, present bool, source string) *Pair {
		t.Helper()
		p, err := ComposePair(agentPath, agent, TestsSelection{Path: path, Key: "untrusted", Explicit: explicit}, []byte(source), present)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := makePair(testsPath, false, true, testsStarter)
	explicit := makePair(testsPath, true, true, testsStarter)
	other := makePair(filepath.Join(root, "other.agent.test.md"), true, true, testsStarter)
	if p.Agent != agent || !reflect.DeepEqual(before, *agent) || len(agent.Examples) != 0 {
		t.Fatal("composition changed original agent")
	}
	if p.Tests.State != "external" || p.Tests.Suite == nil || p.Tests.Revision != p.Tests.Suite.Revision || p.Tests.Selection.Key == "" || p.Tests.Selection.Key == "untrusted" {
		t.Fatalf("bad external source: %+v", p.Tests)
	}
	if p.SnapshotRevision != explicit.SnapshotRevision || p.Tests.Selection.Key != explicit.Tests.Selection.Key {
		t.Fatal("explicit/default intent changed value identity")
	}
	if p.SnapshotRevision == other.SnapshotRevision || p.Tests.Selection.Key == other.Tests.Selection.Key {
		t.Fatal("different target with equal bytes collapsed")
	}
	if p.SameSnapshot(explicit) || p.SameSnapshot(p) || p.SameSnapshot(nil) {
		t.Fatal("pure composition claimed checked observations")
	}
	changed := makePair(testsPath, false, true, strings.Replace(testsStarter, "input: Ada", "input: Grace", 1))
	absent := makePair(testsPath, false, false, "")
	empty := makePair(testsPath, false, true, testsHeader)
	if absent.Tests.State != "absent" || absent.Tests.Revision != "" || absent.Tests.Suite != nil || empty.Tests.State != "external" || len(empty.Tests.Suite.Examples) != 0 {
		t.Fatal("absence collapsed with present empty")
	}
	for _, q := range []*Pair{changed, absent, empty} {
		if p.SnapshotRevision == q.SnapshotRevision {
			t.Fatal("tests-only state/change not bound")
		}
	}
	moved, err := ComposePair(filepath.Join(root, "moved.agent.md"), agent, p.Tests.Selection, []byte(testsStarter), true)
	if err != nil || moved.SnapshotRevision == p.SnapshotRevision {
		t.Fatalf("agent binding absent: %v", err)
	}
	changedAgent := *agent
	changedAgent.Revision = "sha256:other"
	changedPair, err := ComposePair(agentPath, &changedAgent, p.Tests.Selection, []byte(testsStarter), true)
	if err != nil || changedPair.SnapshotRevision == p.SnapshotRevision {
		t.Fatalf("agent revision absent: %v", err)
	}
}

func TestComposePairRefusesUncheckedBindings(t *testing.T) {
	root := realTemp(t)
	agentPath := filepath.Join(root, "g.agent.md")
	a, err := Parse(agentPath, []byte("Say hello."))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, agent, tests string
		explicit, present  bool
		reason             string
	}{
		{"relative agent", "g.agent.md", "", false, false, "absolute"},
		{"relative tests", agentPath, "g.agent.test.md", true, true, "absolute"},
		{"self", agentPath, agentPath, true, true, "different"},
		{"tests as agent", filepath.Join(root, "g.agent.test.md"), "", false, false, "agent file"},
		{"charter override", filepath.Join(root, "charter.md"), filepath.Join(root, "g.agent.test.md"), true, true, "supported"},
		{"explicit missing", agentPath, filepath.Join(root, "g.agent.test.md"), true, false, "existing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ComposePair(tt.agent, a, TestsSelection{Path: tt.tests, Explicit: tt.explicit}, []byte(testsStarter), tt.present)
			var e *Error
			if p != nil || !errors.As(err, &e) || e.Field != "Tests" || !strings.Contains(e.Problem, tt.reason) {
				t.Fatalf("wrong refusal: %v", err)
			}
		})
	}
}

func TestPairInlineConflictAndExplicitIsolation(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, "g.agent.md")
	tests := filepath.Join(root, "g.agent.test.md")
	for _, inline := range []string{"examples: []", "examples:\n  - id: inline\n    input: Ada\n    assertions: [{id: a, type: contains, value: Ada}]"} {
		source := "# Greeter\nSay hello.\n\n## Examples\n```yaml\n" + inline + "\n```\n"
		pairWrite(t, path, source)
		p := pairLoad(t, path, "")
		if p.Tests.State != "inline" || p.Tests.Revision != p.Agent.Revision || p.Tests.Suite == nil || !reflect.DeepEqual(p.Tests.Suite.Examples, p.Agent.Examples) {
			t.Fatalf("lost inline source: %+v", p.Tests)
		}
		a := p.Agent
		before := *a
		pairWrite(t, tests, testsStarter)
		pairRefusal(t, path, "", "Tests", "inline")
		p = pairLoad(t, path, tests)
		if p.Tests.State != "external" || !reflect.DeepEqual(*a, before) || p.Agent.Revision != a.Revision || p.Agent.Instructions != a.Instructions || !reflect.DeepEqual(p.Agent.Examples, a.Examples) {
			t.Fatal("override mutated inline agent")
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != source {
			t.Fatalf("rewrote source: %v", err)
		}
		if err := os.Remove(tests); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPairExternalToolValidation(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, "g.agent.md")
	tests := filepath.Join(root, "g.agent.test.md")
	for _, payload := range []string{
		"id: a\ninput: Ada\nassertions: [{id: a, type: toolCalled, tool: read}]\n",
		"id: a\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: [{name: read, mode: recorded, calls: []}]}\n",
	} {
		pairWrite(t, tests, testsHeader+"```yaml agent-test\n"+payload+"```\n")
		for _, source := range []string{"Say hello.\n", "---\nname: greeter\n---\nSay hello.\n"} {
			pairWrite(t, path, source)
			pairRefusal(t, path, "", "Examples", "declared tool")
			_, err := LoadPair(path, "")
			var e *Error
			if !errors.As(err, &e) || e.Location.File != tests {
				t.Fatalf("tool error attributed to wrong source: %v", err)
			}
		}
		pairWrite(t, path, "Say hello.\n\n## Tools\n- read\n")
		p := pairLoad(t, path, "")
		if len(p.Agent.Examples) != 0 || len(p.Tests.Suite.Examples) != 1 {
			t.Fatal("external examples merged into agent")
		}
	}
	// Omitted judgeModel keeps canonical semantics, unlike native Squad.
	pairWrite(t, tests, testsHeader+"```yaml agent-test\nid: a\ninput: Ada\nassertions: [{id: a, type: llmJudge, rubric: Be nice, minScore: 50, judgeVersion: v1}]\n```\n")
	p := pairLoad(t, path, "")
	if p.Tests.Suite.Examples[0].Assertions[0].JudgeModel != "" {
		t.Fatal("validation pin leaked into authored assertion")
	}
}

func TestLoadPairRefusesAgentAsTestsThroughHardlink(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			root := realTemp(t)
			agentPath := filepath.Join(root, "g.agent.md")
			pairWrite(t, agentPath, testsStarter)
			testsPath, _ := CompanionPath(agentPath)
			override := ""
			if explicit {
				testsPath = filepath.Join(root, "override.md")
				override = testsPath
			}
			if err := os.Link(agentPath, testsPath); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}
			pairRefusal(t, agentPath, override, "Tests", "different agent and tests files")
		})
	}
}

func TestLoadPairC3AndSelection(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, ".github", "agents", "g.agent.md")
	target := filepath.Join(root, ".github", "agent-tests", "g.agent.test.md")
	pairWrite(t, path, "# Greeter\nSay hello.\n")
	// A beside candidate must not act as fallback for the repo rule.
	pairWrite(t, filepath.Join(filepath.Dir(path), "g.agent.test.md"), testsStarter)
	absent := pairLoad(t, path, "")
	if absent.Tests.State != "absent" || absent.Tests.Selection.Path != target {
		t.Fatal("wrong C3 or fallback")
	}
	pairWrite(t, target, testsStarter)
	p := pairLoad(t, path, "")
	again := pairLoad(t, path, target)
	if !p.SameSnapshot(again) || p.SnapshotRevision != again.SnapshotRevision {
		t.Fatal("unchanged/default explicit reload differs")
	}
	if absent.SameSnapshot(p) || absent.SnapshotRevision == p.SnapshotRevision {
		t.Fatal("appearance unchanged")
	}
	// Explicit relative paths are relative to cwd, not the agent's parent.
	t.Chdir(root)
	pairWrite(t, filepath.Join(root, "override.md"), testsStarter)
	override := pairLoad(t, path, "override.md")
	if override.Tests.Selection.Path != filepath.Join(root, "override.md") || override.SnapshotRevision == p.SnapshotRevision {
		t.Fatal("explicit target not bound to cwd")
	}
	pairRefusal(t, path, "missing.md", "Tests", "existing")
	pairWrite(t, filepath.Join(root, "charter.md"), "Say hello.\n")
	none := pairLoad(t, filepath.Join(root, "charter.md"), "")
	if none.Tests.State != "none" || !none.SameSnapshot(pairLoad(t, filepath.Join(root, "charter.md"), "")) {
		t.Fatalf("invented charter companion or lost checked observation: %+v", none.Tests)
	}
	inlinePath := filepath.Join(root, "inline.md")
	pairWrite(t, inlinePath, "Say hello.\n\n## Examples\n```yaml\nexamples: []\n```\n")
	if inline := pairLoad(t, inlinePath, ""); inline.Tests.State != "inline" || inline.Tests.Suite == nil || len(inline.Tests.Suite.Examples) != 0 {
		t.Fatal("unsupported no-override lost existing structured inline truth")
	}
	pairRefusal(t, filepath.Join(root, "charter.md"), target, "Tests", "supported")
	pairRefusal(t, target, "", "Tests", "agent file")
	pairRefusal(t, path, path, "Tests", "different")
}

func TestLoadPairMissingParentsAndUnsafePaths(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, ".github", "agents", "g.agent.md")
	pairWrite(t, path, "Say hello.\n")
	p := pairLoad(t, path, "")
	again := pairLoad(t, path, "")
	if p.Tests.State != "absent" || !p.SameSnapshot(again) {
		t.Fatal("missing parent not observed")
	}
	// ENOTDIR may not be treated as absence on the default candidate.
	blocker := filepath.Join(root, ".github", "agent-tests")
	pairWrite(t, blocker, "not a directory")
	pairRefusal(t, path, "", "Tests", "directory")
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	// Reject all existing ancestors even beneath a missing intermediate parent.
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, blocker); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	pairRefusal(t, path, "", "Tests", "symlinks or reparse")
	pairRefusal(t, path, filepath.Join(blocker, "missing", "test.md"), "Tests", "symlinks or reparse")
	pairRefusal(t, path, blocker+string(filepath.Separator)+".."+string(filepath.Separator)+"test.md", "Tests", "parent traversal")
	pairRefusal(t, filepath.Dir(path)+string(filepath.Separator)+".."+string(filepath.Separator)+"agents/g.agent.md", "", "File", "parent traversal")
}

func TestLoadPairRegularBoundedSources(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, "g.agent.md")
	tests := filepath.Join(root, "g.agent.test.md")
	pairWrite(t, path, "Say hello.\n")
	if err := os.Mkdir(tests, 0700); err != nil {
		t.Fatal(err)
	}
	pairRefusal(t, path, "", "Tests", "regular")
	if err := os.Remove(tests); err != nil {
		t.Fatal(err)
	}
	pairWrite(t, tests, strings.Repeat("x", MaxFileBytes+1))
	pairRefusal(t, path, "", "Tests", "256 KiB")
	pairWrite(t, tests, testsHeader+"```yaml agent-test\ninvalid\n```\n")
	pairRefusal(t, path, "", "Examples", "mapping")
	pairWrite(t, tests, testsStarter)
	link := filepath.Join(root, "link.md")
	if err := os.Symlink(tests, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	pairRefusal(t, path, link, "Tests", "symlinks or reparse")
}

func TestLoadPairSameByteReplacements(t *testing.T) {
	for _, replace := range []string{"agent", "tests", "agent-parent", "tests-parent", "absent-parent"} {
		t.Run(replace, func(t *testing.T) {
			root := realTemp(t)
			agentDir := filepath.Join(root, "agents")
			testsDir := filepath.Join(root, "tests")
			path := filepath.Join(agentDir, "g.agent.md")
			target := filepath.Join(testsDir, "g.agent.test.md")
			pairWrite(t, path, "Say hello.\n")
			pairWrite(t, target, testsStarter)
			override := target
			if replace == "absent-parent" {
				path = filepath.Join(root, ".github", "agents", "g.agent.md")
				pairWrite(t, path, "Say hello.\n")
				override = ""
			}
			p := pairLoad(t, path, override)
			if !p.SameSnapshot(pairLoad(t, path, override)) {
				t.Fatal("unchanged reload not same")
			}
			switch replace {
			case "agent", "tests":
				file := path
				data := "Say hello.\n"
				if replace == "tests" {
					file, data = target, testsStarter
				}
				if err := os.Rename(file, file+".old"); err != nil {
					t.Fatal(err)
				}
				pairWrite(t, file, data)
			case "agent-parent", "tests-parent":
				dir := agentDir
				if replace == "tests-parent" {
					dir = testsDir
				}
				if err := os.Rename(dir, dir+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				leaf := "g.agent.md"
				if replace == "tests-parent" {
					leaf = "g.agent.test.md"
				}
				// Move the original inode back: only the parent identity changes.
				if err := os.Rename(filepath.Join(dir+".old", leaf), filepath.Join(dir, leaf)); err != nil {
					t.Fatal(err)
				}
			case "absent-parent":
				anchor := filepath.Join(root, ".github")
				if err := os.Rename(anchor, anchor+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(anchor, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(anchor+".old", "agents"), filepath.Join(anchor, "agents")); err != nil {
					t.Fatal(err)
				}
			}
			q := pairLoad(t, path, override)
			if p.SnapshotRevision != q.SnapshotRevision {
				t.Fatal("same-byte/binding identity changed")
			}
			if p.SameSnapshot(q) || q.SameSnapshot(p) {
				t.Fatal("same-byte replacement passed checked identity")
			}
		})
	}
}

func TestLoadPairLeavesLegacyLoadUnchanged(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, "g.agent.md")
	pairWrite(t, path, sample(t))
	original, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	pairWrite(t, filepath.Join(root, "g.agent.test.md"), "invalid companion")
	after, err := Load(path)
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Fatalf("legacy Load discovered companion: %v", err)
	}
	pairRefusal(t, path, "", "Tests", "inline")
}

func TestPairReadFinalStabilityChecks(t *testing.T) {
	for _, change := range []string{"file", "parent", "missing-appeared", "missing-anchor"} {
		t.Run(change, func(t *testing.T) {
			root := realTemp(t)
			path := filepath.Join(root, "parent", "suite.md")
			pairWrite(t, path, testsStarter)
			missing := strings.HasPrefix(change, "missing")
			if missing {
				path = filepath.Join(root, "parent", "missing", "suite.md")
			}
			_, observation, err := readPairSource(path, "Tests", missing)
			if err != nil {
				t.Fatal(err)
			}
			if err := observation.check("Tests"); err != nil {
				t.Fatalf("unchanged source refused: %v", err)
			}
			switch change {
			case "file":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				pairWrite(t, path, testsStarter)
			case "parent", "missing-anchor":
				parent := filepath.Join(root, "parent")
				if err := os.Rename(parent, parent+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if !missing {
					if err := os.Rename(filepath.Join(parent+".old", "suite.md"), path); err != nil {
						t.Fatal(err)
					}
				}
			case "missing-appeared":
				pairWrite(t, path, testsStarter)
			}
			var refusal *Error
			err = observation.check("Tests")
			if !errors.As(err, &refusal) || refusal.Field != "Tests" || !strings.Contains(refusal.Problem, "changed while reading") {
				t.Fatalf("wrong final stability refusal: %v", err)
			}
		})
	}
}

func TestLoadPairHardlinkBindingsRemainDistinct(t *testing.T) {
	root := realTemp(t)
	path := filepath.Join(root, "g.agent.md")
	target := filepath.Join(root, "g.agent.test.md")
	other := filepath.Join(root, "other.md")
	pairWrite(t, path, "Say hello.\n")
	pairWrite(t, target, testsStarter)
	if err := os.Link(target, other); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	p := pairLoad(t, path, target)
	q := pairLoad(t, path, other)
	if p.SnapshotRevision == q.SnapshotRevision || p.Tests.Selection.Key == q.Tests.Selection.Key || p.SameSnapshot(q) {
		t.Fatal("hardlink aliases collapsed distinct publication paths")
	}
	for _, p := range []*Pair{p, q} {
		if !strings.HasPrefix(p.SnapshotRevision, "sha256:") || len(p.SnapshotRevision) != 71 {
			t.Fatalf("bad opaque revision: %s", fmt.Sprint(p.SnapshotRevision))
		}
	}
}
