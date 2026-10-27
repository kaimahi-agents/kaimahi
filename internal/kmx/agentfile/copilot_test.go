package agentfile

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const copilotExamples = "## Examples\n\n```yaml\nexamples:\n  - id: reply\n    input: Ada\n    assertions:\n      - id: text\n        type: contains\n        value: Ada\n```\n"

// This is GitHub's documented "Testing specialist" sample, verbatim.
const githubTestSpecialist = `---
name: test-specialist
description: Focuses on test coverage, quality, and testing best practices without modifying production code
---

You are a testing specialist focused on improving code quality through comprehensive testing. Your responsibilities:

- Analyze existing tests and identify coverage gaps
- Write unit tests, integration tests, and end-to-end tests following best practices
- Review test quality and suggest improvements for maintainability
- Ensure tests are isolated, deterministic, and well-documented
- Focus only on test files and avoid modifying production code unless specifically requested

Always include clear test descriptions and use appropriate testing patterns for the language and framework.
`

func parseBoth(t *testing.T, name, source string) *Agent {
	t.Helper()
	var first *Agent
	for _, parse := range []func(string, []byte) (*Agent, error){Parse, Inspect} {
		a, err := parse(name, []byte(source))
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = a
		} else if !reflect.DeepEqual(first, a) {
			t.Fatal("Parse and Inspect disagree")
		}
	}
	return first
}

func TestCopilotSampleOpensWithoutHeadings(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		data := strings.ReplaceAll(githubTestSpecialist, "\n", eol)
		a := parseBoth(t, "Test Specialist.agent.md", data)
		body := strings.SplitN(githubTestSpecialist, "---\n", 3)[2]
		if !a.HasFrontmatter || a.Frontmatter != strings.SplitN(githubTestSpecialist, "---\n", 3)[1] || a.Instructions != body {
			t.Fatalf("frontmatter/prompt split wrong: %q %q", a.Frontmatter, a.Instructions)
		}
		if a.Identity.ID != "test-specialist" || a.Identity.Purpose == "" || a.Title != "test-specialist" || a.Model != "" || len(a.Tools) != 0 || len(a.Examples) != 0 {
			t.Fatalf("wrong mapping: %+v", a)
		}
		if !a.CopilotToolsOmitted {
			t.Fatal("omitted tools not reported")
		}
		if a.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data))) {
			t.Fatal("revision is not exact bytes")
		}
		if _, ok := a.Locations["Examples"]; ok {
			t.Fatal("absent Examples has a location")
		}
	}
	if CopilotToolsNotice != "Copilot gives this agent all tools; these tests run without tools. Add tools to choose which." {
		t.Fatal(CopilotToolsNotice)
	}
}

func TestCopilotModelMapping(t *testing.T) {
	for model, want := range map[string]string{
		"gpt-4.1": "gpt-4.1", `"gpt-4.1"`: "gpt-4.1", "'claude-sonnet-4.5'": "claude-sonnet-4.5",
		"Claude Sonnet 4.5 (copilot)": "", "auto": "", "[gpt-5, claude-sonnet-4.5]": "", "": "", "4": "", "https://example.invalid/m": "", "gpt-*": "",
	} {
		source := "---\ndescription: d\nmodel: " + model + "\n---\nPrompt\n"
		a := parseBoth(t, "m.agent.md", source)
		if a.Model != want || !strings.Contains(a.Frontmatter, "model: "+model) {
			t.Errorf("model %q mapped to %q (frontmatter %q)", model, a.Model, a.Frontmatter)
		}
	}
}

func TestCopilotToolsMapping(t *testing.T) {
	rows := []struct {
		tools   string
		want    []string
		omitted bool
	}{
		{"", nil, true},
		{"tools: []\n", nil, false},
		{"tools: ['read', 'search', 'edit']\n", nil, false},
		{"tools: [Read, SEARCH, Grep, Glob, shell, Bash, powershell, execute, WebFetch, websearch, web, TodoWrite, todo, custom-agent, Task, agent, MultiEdit, Write, NotebookEdit, NotebookRead]\n", nil, false},
		{"tools: ['*']\n", nil, false},
		{"tools: ['github/*', 'azure.ext/tool', 'custom-mcp/tool-1']\n", nil, false},
		{"tools: ['tool-a', 'custom-mcp/tool-1', read, tool-b, tool-a]\n", []string{"tool-a", "tool-b"}, false},
		{"tools: read, edit\n", nil, false},
		{"tools: 'tool-a, custom-mcp/tool-1, Read ,tool-b,, tool-a'\n", []string{"tool-a", "tool-b"}, false},
		{"tools: tool-a\n", []string{"tool-a"}, false},
		{"tools: ''\n", nil, false},
		{"tools:\n", nil, false},
		{"tools: [1, {a: b}, '', 'has space']\n", nil, false},
	}
	for _, row := range rows {
		a := parseBoth(t, "t.agent.md", "---\ndescription: d\n"+row.tools+"---\nPrompt\n")
		if !reflect.DeepEqual(a.Tools, row.want) || a.CopilotToolsOmitted != row.omitted {
			t.Errorf("%q: tools=%v omitted=%t", row.tools, a.Tools, a.CopilotToolsOmitted)
		}
		if strings.TrimSuffix(row.tools, "\n") != "" && !strings.Contains(a.Frontmatter, strings.TrimSuffix(row.tools, "\n")) {
			t.Errorf("%q: header not preserved", row.tools)
		}
	}
}

func TestCopilotToolsDoNotTruncate(t *testing.T) {
	for _, count := range []int{32, 33} {
		names := make([]string, count)
		for i := range names {
			names[i] = fmt.Sprintf("tool%d", i)
		}
		source := "---\ndescription: Many tools\ntools: [" + strings.Join(names, ", ") + "]\n---\nPrompt\n"
		a := parseBoth(t, "many.agent.md", source)
		want := count
		if count > 32 {
			want = 0
		}
		if len(a.Tools) != want || !strings.Contains(a.Frontmatter, strings.Join(names, ", ")) {
			t.Fatalf("over-limit declarations must stay wholly unmapped, never truncated: got %d from %d", len(a.Tools), count)
		}
	}
}

func TestCopilotFreeFormPromptIsNotStructure(t *testing.T) {
	prompt := "# One\n\n<!-- a comment -->\n\n## Identity\n- **ID:** nope\n\n## Model\n- **Preferred:** nope\n\n## Tools\n- nope\n\n# Two\n\n## Examples\n\nProse about examples.\n\n```text\nunclosed prose\n"
	source := "---\ndescription: d\n---\n" + prompt
	a := parseBoth(t, "free.agent.md", source)
	if a.Instructions != prompt || a.Model != "" || len(a.Tools) != 0 || a.Identity.ID != "free" {
		t.Fatalf("prompt headings were interpreted: %+v", a)
	}
	if _, ok := a.Locations["Examples"]; ok || len(a.Examples) != 0 {
		t.Fatal("prose Examples became structure")
	}
	// A fenced lookalike, a non-final section and a non-yaml fence stay prompt.
	for _, tail := range []string{
		"## Examples\n\n```text\nexamples: []\n```\n",
		"## Examples\n\n```yaml\nnot: examples\n```\n",
		"## Examples\n\n```yaml\nexamples: []\n```\n\nmore prose\n",
		"## Examples\n\n```yaml\nexamples: []\n```\n## After\n",
		"```text\n## Examples\n\n```yaml\nexamples: []\n```\n```\n",
	} {
		a := parseBoth(t, "free.agent.md", "---\ndescription: d\n---\nPrompt\n\n"+tail)
		if len(a.Examples) != 0 || a.Instructions != "Prompt\n\n"+tail {
			t.Fatalf("%q treated as structured tests: %+v", tail, a)
		}
	}
}

func TestCopilotFinalExamplesAreTests(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		source := "---\ndescription: d\ntools: []\n---\nSay hi.\n\n## Not tests\n\n" + copilotExamples
		a := parseBoth(t, "x.agent.md", strings.ReplaceAll(source, "\n", eol))
		if a.Instructions != "Say hi.\n\n## Not tests\n\n" || len(a.Examples) != 1 || a.Examples[0].ID != "reply" {
			t.Fatalf("wrong split: %q %+v", a.Instructions, a.Examples)
		}
		if line := a.Locations["Examples"].Line; line != 9 || a.Examples[0].Location.Line != 13 {
			t.Fatalf("wrong locations: %+v", a.Locations)
		}
	}
	// A structured document is validated like any other tests, even when malformed.
	if _, err := Parse("x.agent.md", []byte("---\ndescription: d\n---\nHi\n\n## Examples\n\n```yaml\nexamples: [\n```\n")); err == nil {
		t.Fatal("malformed structured examples accepted")
	}
	// Tool checks still need declared tools.
	tool := strings.Replace(copilotExamples, "type: contains\n        value: Ada", "type: toolCalled\n        tool: lookup", 1)
	if _, err := Parse("x.agent.md", []byte("---\ndescription: d\n---\nHi\n\n"+tool)); err == nil {
		t.Fatal("undeclared tool check accepted")
	}
	if a, err := Parse("x.agent.md", []byte("---\ndescription: d\ntools: [lookup]\n---\nHi\n\n"+tool)); err != nil || len(a.Examples) != 1 {
		t.Fatalf("declared tool check refused: %v", err)
	}
}

func TestStructuredExamplesRootKeyForms(t *testing.T) {
	for _, document := range []string{"examples: []", "{examples: []}", "'examples': []", "\"examples\": []", "# note\nexamples:\n  []"} {
		source := "---\ndescription: d\n---\nPrompt\n\n## Examples\n\n```yaml\n" + document + "\n```\n"
		a := parseBoth(t, "x.agent.md", source)
		if _, ok := a.Locations["Examples"]; !ok || a.Instructions != "Prompt\n\n" {
			t.Errorf("%q not recognized as tests: %q", document, a.Instructions)
		}
	}
	for _, document := range []string{"- examples", "example: []", "other:\n  examples: []", "[examples]", "plain text"} {
		source := "---\ndescription: d\n---\nPrompt\n\n## Examples\n\n```yaml\n" + document + "\n```\n"
		if a := parseBoth(t, "x.agent.md", source); len(a.Locations) != 0 || !strings.Contains(a.Instructions, "```yaml") {
			t.Errorf("%q wrongly treated as tests", document)
		}
	}
}

func TestCopilotFrontmatterRefusals(t *testing.T) {
	for name, source := range map[string]string{
		"unclosed":      "---\ndescription: d\nPrompt\n",
		"bad yaml":      "---\ndescription: [\n---\nPrompt\n",
		"list root":     "---\n- a\n---\nPrompt\n",
		"duplicate key": "---\ntools: []\ntools: [a]\n---\nPrompt\n",
		"blank prompt":  "---\ndescription: d\n---\n\n  \n",
	} {
		a, err := Parse("x.agent.md", []byte(source))
		if a != nil || err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Unknown fields, comments, anchors and aliases are source only.
	source := "---\n# header comment\ndescription: d\nx-unknown: &a {k: [1, 2]}\ny: *a\nmcp-servers:\n  s: {command: run}\n---\nPrompt\n"
	if a := parseBoth(t, "x.agent.md", source); a.Frontmatter != strings.SplitN(source, "---\n", 3)[1] {
		t.Fatal("header not raw")
	}
}

func TestMinimalPromptOnlyAndFilenameIdentity(t *testing.T) {
	for file, id := range map[string]string{
		"x.agent.md": "x", "Code_Reviewer v2.agent.md": "code-reviewer-v2", "dir/9 Lives.agent.md": "agent-9-lives",
		"!!!.agent.md": "agent", "plain.md": "plain", strings.Repeat("a", 90) + ".agent.md": strings.Repeat("a", 63), "é.agent.md": "agent",
	} {
		a := parseBoth(t, file, "Just a prompt.\n")
		if a.Identity.ID != id || !agentID.MatchString(a.Identity.ID) || a.Instructions != "Just a prompt.\n" || a.Title != id || a.HasFrontmatter || a.CopilotToolsOmitted {
			t.Errorf("%s: %+v", file, a)
		}
	}
	if a := parseBoth(t, "x.agent.md", "---\ndescription: d\n---\nPrompt\n"); a.Title != "x" {
		t.Fatalf("frontmatter file without name or title: %q", a.Title)
	}
	a := parseBoth(t, "x.agent.md", "# Title\n\nPrompt\n\n# Another\n\nMore\n")
	if a.Title != "Title" || a.Instructions != "# Title\n\nPrompt\n\n# Another\n\nMore\n" || a.Locations["Title"].Line != 1 {
		t.Fatalf("%+v", a)
	}
	for name, source := range map[string]string{"blank": "\n\n", "blank frontmatter prompt": "---\n---\n\n"} {
		if _, err := Parse("x.agent.md", []byte(source)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestMinimalControls(t *testing.T) {
	source := "# Greeter\n\nSay hello.\n\n## Model\n- **Preferred:** fake\n\n## Tools\n- lookup\n\n## Other\nKept.\n\n" + copilotExamples
	a := parseBoth(t, "greeter.agent.md", source)
	if a.Model != "fake" || !reflect.DeepEqual(a.Tools, []string{"lookup"}) || len(a.Examples) != 1 || a.HasFrontmatter || a.CopilotToolsOmitted {
		t.Fatalf("%+v", a)
	}
	if a.Instructions != "# Greeter\n\nSay hello.\n\n## Other\nKept.\n\n" {
		t.Fatalf("controls leaked into the prompt: %q", a.Instructions)
	}
	for name, bad := range map[string]string{
		"auto":      strings.Replace(source, "fake", "auto", 1),
		"extra":     strings.Replace(source, "fake", "fake\n- **Fallback:** x", 1),
		"duplicate": strings.Replace(source, "## Other", "## Model\n- **Preferred:** fake\n\n## Other", 1),
		"no tools":  strings.Replace(source, "- lookup\n", "", 1),
		"bad tool":  strings.Replace(source, "- lookup", "- two words", 1),
	} {
		if _, err := Parse("greeter.agent.md", []byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A frontmatter file's headings are never controls.
	b := parseBoth(t, "g.agent.md", "---\ndescription: d\n---\n## Model\n- **Preferred:** fake\n")
	if b.Model != "" {
		t.Fatal("frontmatter file read a Model control")
	}
}

func TestPartialCharterHeadingsStayPrompt(t *testing.T) {
	source := strings.Replace(sample(t), "## Non-Responsibilities\nDo not call tools, read files or contact other agents.\n\n", "", 1)
	a := parseBoth(t, "greeter.agent.md", source)
	if a.Identity.ID != "greeter" || a.Model != "fake" || len(a.Examples) != 1 || !strings.Contains(a.Instructions, "## Identity") {
		t.Fatalf("%+v", a)
	}
}

func TestLegacyCharterWithoutExamples(t *testing.T) {
	source := strings.Split(sample(t), "## Examples")[0]
	a, err := Parse("greeter.agent.md", []byte(source))
	if err != nil || a.Instructions != source || len(a.Examples) != 0 {
		t.Fatalf("%v %+v", err, a)
	}
	if _, ok := a.Locations["Examples"]; ok {
		t.Fatal("absent Examples has a location")
	}
}
