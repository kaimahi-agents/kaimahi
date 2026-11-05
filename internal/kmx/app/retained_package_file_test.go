package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

const retainedAgentFixture = "---\nkmx:\n  schemaVersion: 1\n  runtime: orka\nname: greeter\ndescription: A greeting.\nmodel: qwen-3.5-2b\n---\nSay hello.\n"

func retainedFileFixture(t *testing.T, n int) (string, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	agent, tests := filepath.Join(root, "greeter.agent.md"), filepath.Join(root, "greeter.agent.test.md")
	raw := "---\nschemaVersion: 1\n---\n# Original companion\n"
	for i := 0; i < n; i++ {
		raw += fmt.Sprintf("```yaml agent-test\nid: case-%03d\ninput: Ada\nassertions:\n- {id: exact, type: contains, value: Ada}\n- {id: judge, type: llmJudge, rubric: Say hello, minScore: 80, judgeModel: qwen-3.5-2b, judgeVersion: v1}\n```\n", i)
	}
	for p, s := range map[string]string{agent: retainedAgentFixture, tests: raw} {
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(s, "\n", "\r\n")), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pair, err := agentfile.LoadOrkaPair(agent, "")
	if err != nil {
		t.Fatal(err)
	}
	return agent, tests, pair.SnapshotRevision
}

func TestRetainedFileProjection(t *testing.T) {
	agent, tests, r := retainedFileFixture(t, 128)
	s, err := loadRetainedFile(t.Context(), agent, "", r)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(agent)
	b, _ := os.ReadFile(tests)
	m := s.input
	if !bytes.Equal(m.Sources.Agent, a) || !bytes.Equal(m.Sources.Tests, b) || m.Sources.Format != "kmx.orka-agent-markdown" || m.Sources.SchemaVersion != 1 {
		t.Fatal("lost exact originals")
	}
	if m.SuiteName != "greeter" || m.MemberID != "greeter" || m.Model != "qwen-3.5-2b" || m.Protocol != "openai-compatible" || m.Runtime.ID != "orka" || m.Description != "A greeting." || string(m.Instructions) != "Say hello.\n" || len(m.Cases) != 128 {
		t.Fatal("projection changed native fields")
	}
	for i, c := range m.Cases {
		if c.ID != fmt.Sprintf("case-%03d", i) || c.Input != "Ada" || len(c.Assertions) != 2 || c.Assertions[0].ID != "exact" || c.Assertions[1].ID != "judge" || *c.Assertions[1].MinScore != 80 || c.Runs != nil || c.MinPasses != nil {
			t.Fatalf("case %d changed", i)
		}
	}
	s.pair.Agent.Source[0] = 'X'
	s.pair.Tests.Suite.Source[0] = 'X'
	*s.pair.Tests.Suite.Examples[0].Assertions[1].MinScore = 1
	if m.Sources.Agent[0] != '-' || m.Sources.Tests[0] != '-' || *m.Cases[0].Assertions[1].MinScore != 80 {
		t.Fatal("projection aliases mutable pair")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	compiled, err := workspace.CompileMapped(t.Context(), m, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedFileSelectionModeAndPackageIdentity(t *testing.T) {
	agent, tests, r := retainedFileFixture(t, 1)
	conventional, err := loadRetainedFile(t.Context(), agent, "", r)
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := loadRetainedFile(t.Context(), agent, tests, r)
	if err != nil {
		t.Fatal(err)
	}
	if conventional.binding.Explicit || !explicit.binding.Explicit || explicit.binding.Revision != r || conventional.binding.Tests != tests {
		t.Fatal("selection mode/revision changed")
	}
	var digest, f string
	for _, s := range []*retainedFileSnapshot{conventional, explicit} {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0700); err != nil {
			t.Fatal(err)
		}
		p, err := workspace.CompileMapped(t.Context(), s.input, parent)
		if err != nil {
			t.Fatal(err)
		}
		d := p.Descriptor()
		fp := p.Fingerprint()
		if digest != "" && (d.Digest.String() != digest || fp != f) {
			t.Fatal("mode entered S/F")
		}
		digest, f = d.Digest.String(), fp
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetainedFileFreshness(t *testing.T) {
	for _, kind := range []string{"agent-bytes", "tests-bytes", "same-agent", "same-tests", "parent", "missing-tests"} {
		t.Run(kind, func(t *testing.T) {
			agent, tests, r := retainedFileFixture(t, 1)
			s, err := loadRetainedFile(t.Context(), agent, "", r)
			if err != nil {
				t.Fatal(err)
			}
			path := agent
			if strings.Contains(kind, "tests") {
				path = tests
			}
			raw, _ := os.ReadFile(path)
			switch kind {
			case "agent-bytes", "tests-bytes":
				raw = append(raw, []byte("\r\n# changed\r\n")...)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "same-agent", "same-tests":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-tests":
				if err := os.Remove(tests); err != nil {
					t.Fatal(err)
				}
			case "parent":
				root := filepath.Dir(agent)
				if err := os.Rename(root, root+".old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(root + ".old") })
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				for _, p := range []string{agent, tests} {
					b, err := os.ReadFile(filepath.Join(root+".old", filepath.Base(p)))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, b, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.recheck(t.Context()); !errors.Is(err, errRetainedSourceChanged) {
				t.Fatalf("freshness refusal = %v", err)
			}
		})
	}
	agent, _, r := retainedFileFixture(t, 1)
	for _, expected := range []string{"", "sha256:" + strings.Repeat("0", 64)} {
		if _, err := loadRetainedFile(t.Context(), agent, "", expected); !errors.Is(err, errRetainedSourceChanged) {
			t.Fatalf("expected R = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadRetainedFile(ctx, agent, "", r); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
}

func TestRetainedFileRejectsUnrepresented(t *testing.T) {
	for _, kind := range []string{"absent", "explicit-missing", "unknown", "soul", "skills", "tools", "mock", "tool-assertion", "agent-comment", "tests-comment"} {
		t.Run(kind, func(t *testing.T) {
			agent, tests, _ := retainedFileFixture(t, 1)
			a, _ := os.ReadFile(agent)
			b, _ := os.ReadFile(tests)
			override := ""
			switch kind {
			case "absent":
				_ = os.Remove(tests)
			case "explicit-missing":
				override = tests + ".missing"
			case "unknown":
				a = bytes.Replace(a, []byte("description:"), []byte("extra: value\r\ndescription:"), 1)
			case "soul":
				a = bytes.Replace(a, []byte("  runtime: orka"), []byte("  runtime: orka\r\n  soul: {inline: Be kind.}"), 1)
			case "skills":
				a = bytes.Replace(a, []byte("  runtime: orka"), []byte("  runtime: orka\r\n  skills: [{name: reader, configMapRef: {name: reader, key: SKILL.md}}]"), 1)
			case "tools":
				a = bytes.Replace(a, []byte("  runtime: orka"), []byte("  runtime: orka\r\n  tools: [{name: fetch}]"), 1)
			case "mock":
				b = bytes.Replace(b, []byte("input: Ada"), []byte("input: Ada\r\nmocks: {tools: [{name: ping, mode: live}]}"), 1)
			case "tool-assertion":
				b = bytes.Replace(b, []byte("type: contains, value: Ada"), []byte("type: toolCalled, tool: ping"), 1)
			case "agent-comment":
				a = append(a, []byte("\r\n<!-- "+"sk-"+strings.Repeat("A", 48)+" -->\r\n")...)
			case "tests-comment":
				b = append(b, []byte("\r\n<!-- "+"sk-"+strings.Repeat("A", 48)+" -->\r\n")...)
			}
			if kind != "absent" {
				if err := os.WriteFile(tests, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(agent, a, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := agentfile.LoadOrkaPair(agent, override)
			if err != nil {
				if _, err := loadRetainedFile(t.Context(), agent, override, "unused"); err == nil {
					t.Fatal("parser refusal disappeared")
				}
				return
			}
			if _, err := loadRetainedFile(t.Context(), agent, override, p.SnapshotRevision); err == nil {
				t.Fatal("unrepresented input accepted")
			}
		})
	}
}

func TestRetainedFilePureTupleHasNoObservations(t *testing.T) {
	agent, _, r := retainedFileFixture(t, 1)
	s, err := loadRetainedFile(t.Context(), agent, "", r)
	if err != nil {
		t.Fatal(err)
	}
	p := s.pair
	p, err = agentfile.ComposeOrkaSourcePair(p.AgentPath, p.Agent.Source, p.Tests.Selection, p.Tests.Suite.Source, true)
	if err != nil {
		t.Fatal(err)
	}
	s.pair = p
	if err := s.recheck(t.Context()); !errors.Is(err, errRetainedSourceChanged) {
		t.Fatalf("pure tuple became authority: %v", err)
	}
}
