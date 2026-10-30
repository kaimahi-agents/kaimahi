package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	copilot "github.com/github/copilot-sdk/go"
)

const markdownTestAgent = "---\nname: hello\ndescription: Greet people\ntools: []\n---\nGreet the user.\n"
const markdownTestCases = "---\nschemaVersion: 1\n---\n# Tests\n```yaml agent-test\nid: sam\ninput: My name is Sam\nassertions:\n  - id: greeting\n    type: contains\n    value: Hello\n```\n"

func mdFixture(t *testing.T) *MarkdownAgent {
	t.Helper()
	t.Setenv("KMX_HOME", t.TempDir())
	root := t.TempDir()
	if err := InitMarkdownAgent(root, "hello"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "hello", "hello.agent.md")
	if err := os.WriteFile(path, []byte(markdownTestAgent), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MarkdownTestsPath(path), []byte(markdownTestCases), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := ReadMarkdownAgent(path)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestMarkdownParsingAndOfflineValidation(t *testing.T) {
	for name, raw := range map[string]string{"native": markdownTestAgent, "crlf": strings.ReplaceAll(markdownTestAgent, "\n", "\r\n")} {
		t.Run(name, func(t *testing.T) {
			a, err := parseMarkdownAgent("path with spaces/hello.agent.md", []byte(raw))
			if err != nil || a.Description != "Greet people" || a.Instructions != "Greet the user." {
				t.Fatalf("%+v %v", a, err)
			}
		})
	}
	for name, raw := range map[string]string{
		"duplicate":          strings.Replace(markdownTestAgent, "name: hello", "name: hello\nname: other", 1),
		"nested-model":       strings.Replace(markdownTestAgent, "tools: []", "model: {provider: copilot, id: auto}\ntools: []", 1),
		"empty-instructions": strings.Replace(markdownTestAgent, "Greet the user.", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseMarkdownAgent("bad.agent.md", []byte(raw))
			var d *MarkdownDiagnostic
			if !errors.As(err, &d) {
				t.Fatalf("expected diagnostic: %v", err)
			}
		})
	}
	a := mdFixture(t)
	if len(a.Examples) != 1 || a.TestsRevision == "" || a.PairRevision == a.Revision {
		t.Fatal("pair not loaded")
	}
	for name, raw := range map[string]string{
		"bad-fence":    strings.Replace(markdownTestCases, "yaml agent-test", "yaml", 1),
		"bad-regex":    strings.Replace(strings.Replace(markdownTestCases, "type: contains", "type: regex", 1), "value: Hello", "pattern: '['", 1),
		"duplicate-id": markdownTestCases + strings.Split(markdownTestCases, "# Tests\n")[1],
		"no-checks":    strings.Split(markdownTestCases, "assertions:")[0] + "```\n",
		"unknown":      strings.Replace(markdownTestCases, "id: sam", "id: sam\nrunz: 2", 1),
		"null-check":   strings.Replace(markdownTestCases, "  - id: greeting\n    type: contains\n    value: Hello", "  - null", 1),
	} {
		t.Run(name, func(t *testing.T) {
			copy := *a
			copy.Examples = nil
			if err := parseMarkdownTests(&copy, []byte(raw)); err == nil {
				t.Fatal("invalid tests accepted")
			}
		})
	}
}
func TestMarkdownDiscoveryAndSpecialFiles(t *testing.T) {
	a := mdFixture(t)
	dir := filepath.Dir(a.Path)
	root := filepath.Dir(filepath.Dir(dir))
	if err := os.Symlink(a.Path, filepath.Join(dir, "link.agent.md")); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo.agent.md")
	if err := markdownTestFIFO(fifo); err != nil {
		t.Fatal(err)
	}
	paths, err := DiscoverMarkdownAgents(root)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	for _, p := range []string{fifo, filepath.Join(dir, "link.agent.md")} {
		if _, err := ReadMarkdownAgent(p); err == nil {
			t.Fatal("special file accepted")
		}
	}
}

type mdFakeExecutor struct {
	calls, starts, resets int
	answer                string
	hook                  func()
	err                   error
}

func (e *mdFakeExecutor) Start(context.Context) (string, error) {
	e.starts++
	return "test-runtime", nil
}
func (e *mdFakeExecutor) Answer(_ context.Context, a *MarkdownAgent, input string, mocks []MarkdownMock) (markdownAnswer, error) {
	e.calls++
	if strings.Contains(a.Instructions, "assertions:") {
		panic("test leaked into prompt")
	}
	if e.hook != nil {
		e.hook()
	}
	return markdownAnswer{Text: e.answer, Model: "test-model", Calls: map[string]int{}}, e.err
}
func (e *mdFakeExecutor) Reset() { e.resets++ }
func (e *mdFakeExecutor) Close() {}
func TestMarkdownSnapshotsPartialHistoryAndStale(t *testing.T) {
	a := mdFixture(t)
	e := &mdFakeExecutor{answer: "Hello, Sam!"}
	s := NewMarkdownSession(io.Discard, io.Discard)
	s.Provider = "copilot"
	s.executor = e
	r, err := s.Evaluate(t.Context(), a, "sam", nil)
	if err != nil || r.Status != "partial" || e.calls != 1 {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	r, err = s.Evaluate(t.Context(), a, "", nil)
	if err != nil || r.Status != "passed" {
		t.Fatal(err)
	}
	rows, err := LoadMarkdownHistory(a)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	e.hook = func() {
		if err := os.WriteFile(a.Path, append(bytes.Clone(a.Source), '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err = s.Evaluate(t.Context(), a, "", nil)
	if err != nil || !r.Stale {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	before := e.starts
	if _, err = s.Evaluate(t.Context(), a, "", nil); err == nil || e.starts != before {
		t.Fatal("stale inputs reached provider")
	}
}
func TestMarkdownNoTestsInvalidSelectorAndOutput(t *testing.T) {
	a := mdFixture(t)
	e := &mdFakeExecutor{answer: "Hello"}
	s := NewMarkdownSession(io.Discard, io.Discard)
	s.Provider = "copilot"
	s.executor = e
	if _, err := s.Evaluate(t.Context(), a, "missing", nil); err == nil || e.starts != 0 {
		t.Fatal("unknown selector reached provider")
	}
	a.Examples = nil
	if _, err := s.Evaluate(t.Context(), a, "", nil); err == nil || e.starts != 0 {
		t.Fatal("empty suite passed")
	}
}
func TestMarkdownMockDispatchChecksActualCalls(t *testing.T) {
	a, err := ReadMarkdownAgent("../../../.github/agents/weather.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	m := a.Mocks["wellington-weather"][0]
	d := &markdownMockDispatch{counts: map[string]int{}}
	h := d.handler(m)
	result, err := h(copilot.ToolInvocation{Arguments: map[string]any{"city": "Wellington"}})
	if err != nil || !strings.Contains(result.TextResultForLLM, "rainy") || d.counts[m.Name] != 1 {
		t.Fatal("mock did not execute")
	}
	_, _ = h(copilot.ToolInvocation{Arguments: map[string]any{"city": "Wellington"}})
	if d.failure == "" {
		t.Fatal("extra call passed")
	}
	d = &markdownMockDispatch{counts: map[string]int{}}
	_, _ = d.handler(m)(copilot.ToolInvocation{Arguments: map[string]any{"city": "Paris"}})
	if d.failure == "" {
		t.Fatal("wrong arguments passed")
	}
}
func TestMarkdownPreferencesRememberChoice(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	dir := t.TempDir()
	fakeTool(t, dir, "copilot", "exit 99")
	t.Setenv("PATH", dir)
	var out bytes.Buffer
	yes, err := SetupMarkdownProvider(strings.NewReader("\n"), &out, false)
	if err != nil || !yes {
		t.Fatal(err)
	}
	out.Reset()
	yes, err = SetupMarkdownProvider(strings.NewReader("n\n"), &out, false)
	if err != nil || !yes || out.Len() != 0 {
		t.Fatal("remembered choice prompted")
	}
	if err = SetMarkdownProvider("declined"); err != nil {
		t.Fatal(err)
	}
	yes, err = SetupMarkdownProvider(strings.NewReader("\n"), &out, false)
	if err != nil || yes {
		t.Fatal("decline forgotten")
	}
	if err = SetMarkdownProvider(""); err != nil {
		t.Fatal(err)
	}
	_, err = SetupMarkdownProvider(strings.NewReader(""), &out, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ReadMarkdownPreferences()
	if err != nil || p.LocalAgents.CopilotChoice != "" {
		t.Fatal("EOF saved choice")
	}
}

func TestMarkdownPreferencesSaveInExistingConfigDirectory(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KMX_HOME", home)
	dir := t.TempDir()
	fakeTool(t, dir, "copilot", "exit 99")
	t.Setenv("PATH", dir)
	var first, second bytes.Buffer
	accepted, err := SetupMarkdownProvider(strings.NewReader("\n"), &first, false)
	if err != nil || !accepted {
		t.Fatalf("save: %v %s", err, &first)
	}
	info, err := os.Stat(filepath.Join(home, "preferences.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private preference not saved: %v", err)
	}
	accepted, err = SetupMarkdownProvider(strings.NewReader(""), &second, false)
	if err != nil || !accepted || second.Len() != 0 {
		t.Fatalf("repeat prompt: %v %s", err, &second)
	}
	dirInfo, _ := os.Stat(home)
	if dirInfo.Mode().Perm() != 0755 {
		t.Fatal("changed existing directory permissions")
	}
}
func TestMarkdownEditorParsingAndLateEvents(t *testing.T) {
	args, err := mdSplitArgs(`"/path with spaces/editor" --wait`)
	if err != nil || len(args) != 2 || args[0] != "/path with spaces/editor" {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if _, err = mdSplitArgs(`"broken`); err == nil {
		t.Fatal("accepted bad quotes")
	}
	a := mdFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := NewMarkdownSession(io.Discard, io.Discard)
	m := markdownTUI{chatTimelineModel: newChatTimeline(a.Name, "local", "", false), session: s, loaded: a, disk: a, ctx: ctx, cancel: cancel, generation: 2}
	next, _ := m.Update(mdDone{generation: 1, result: &MarkdownTestResult{Status: "passed"}})
	if next.(markdownTUI).result != nil {
		t.Fatal("old completion changed state")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 12})
	_ = next.(markdownTUI).View()
}

func TestMarkdownHeaderSeparatesStateDefinitionAndTools(t *testing.T) {
	a := mdFixture(t)
	s := NewMarkdownSession(io.Discard, io.Discard)
	m := markdownTUI{chatTimelineModel: newChatTimeline(a.Name, "local", "", false), session: s, loaded: a, disk: a}
	m.refreshHeader()
	fields := map[string]string{}
	for _, f := range m.fields {
		fields[f.Label] = f.Value
	}
	if fields["Tools"] != "none" || fields["Definition"] != mdShort(a.Revision) || fields["Status"] != "stopped" || fields["Tests"] != "UNTESTED" {
		t.Fatalf("header=%v", fields)
	}
}

func TestMarkdownHistoryRetentionAndTestOnlyChanges(t *testing.T) {
	a := mdFixture(t)
	for i := 0; i < 12; i++ {
		r := MarkdownTestResult{SchemaVersion: 1, ID: fmt.Sprint(i), RunKey: "key", PairDigest: a.PairRevision, Adapter: markdownAdapterVersion, Status: "passed"}
		if err := saveMarkdownResult(a, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := LoadMarkdownHistory(a)
	if err != nil || len(rows) != 10 || rows[0].ID != "2" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	before, _ := os.Stat(a.TestsPath)
	raw := strings.Replace(string(a.TestSource), "My name is Sam", "My name is Bob", 1)
	if err := os.WriteFile(a.TestsPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(a.TestsPath, before.ModTime(), before.ModTime())
	next, err := ReadMarkdownAgent(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if next.Revision != a.Revision || next.TestsRevision == a.TestsRevision || next.PairRevision == a.PairRevision {
		t.Fatal("test-only same-size/same-mtime change not identified")
	}
	if status := MarkdownStatus(next, &rows[9]); !strings.HasPrefix(status, "CHANGED") {
		t.Fatal(status)
	}
	path, _ := markdownHistoryPath(a)
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMarkdownHistory(a); err == nil {
		t.Fatal("corrupt history accepted")
	}
}

func TestMarkdownMissingPairAndOrphans(t *testing.T) {
	a := mdFixture(t)
	if err := os.Remove(a.TestsPath); err != nil {
		t.Fatal(err)
	}
	next, err := LoadMarkdownAgent(a.Path, a.TestsPath)
	if err != nil || len(next.Examples) != 0 {
		t.Fatalf("missing conventional pair: %v", err)
	}
	if _, err := LoadMarkdownAgent(a.Path, filepath.Join(t.TempDir(), "missing.agent.test.md")); err == nil {
		t.Fatal("explicit missing test accepted")
	}
	orphan := filepath.Join(filepath.Dir(a.TestsPath), "orphan.tests.md")
	if err := os.WriteFile(orphan, []byte(markdownTestCases), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(a.Path)
	paths, err := MarkdownOrphanTests(root)
	if err != nil || len(paths) != 1 {
		t.Fatalf("orphans=%v err=%v", paths, err)
	}
}
