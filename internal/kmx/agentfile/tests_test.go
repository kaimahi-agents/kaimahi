package agentfile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const testsStarter = "---\nschemaVersion: 1\n---\n\n# greeter tests\n\n```yaml agent-test\nid: greeting\ninput: Ada\nassertions:\n  - id: greeting-text\n    type: contains\n    value: Ada\n```\n"
const testsHeader = "---\nschemaVersion: 1\n---\n"

func testFence(id string) string {
	return "```yaml agent-test\nid: " + id + "\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\n```\n"
}

func testsRefusal(t *testing.T, source, field, reason string) *Error {
	t.Helper()
	suite, err := ParseTests("greeter.agent.test.md", []byte(source))
	var e *Error
	if suite != nil || !errors.As(err, &e) || e.Field != field || !strings.Contains(e.Problem, reason) || e.Location.File != "greeter.agent.test.md" {
		t.Fatalf("want %s/%s typed refusal, got suite=%v err=%v", field, reason, suite, err)
	}
	return e
}

func TestParseTestsStarterSourceAndLocations(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		data := []byte(strings.ReplaceAll(testsStarter, "\n", eol))
		suite, err := ParseTests("greeter.agent.test.md", data)
		if err != nil {
			t.Fatal(err)
		}
		if string(suite.Source) != string(data) || suite.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) {
			t.Fatal("raw source/revision changed")
		}
		if len(suite.Examples) != 1 || suite.Examples[0].ID != "greeting" || suite.Examples[0].Input != "Ada" || suite.Examples[0].Assertions[0].Value != "Ada" {
			t.Fatalf("starter lost content: %+v", suite.Examples)
		}
		e := suite.Examples[0]
		if e.Location != (Location{"greeter.agent.test.md", 8, 1}) || e.AssertionLocations[0] != (Location{"greeter.agent.test.md", 11, 5}) {
			t.Fatalf("wrong source locations: %+v", e)
		}
		data[0] = '!'
		if suite.Source[0] != '-' {
			t.Fatal("suite aliases caller bytes")
		}
	}
}

func TestParseTestsRefusesIndentedAndListSampleTests(t *testing.T) {
	listSample := testsHeader + "- ~~~text\n  ```yaml agent-test\n  id: sample\n  input: Ada\n  assertions: [{id: a, type: contains, value: Ada}]\n  ```\n  ~~~\n- ~~~text\n  x\n  ~~~\n"
	testsRefusal(t, listSample, "Tests", "canonical yaml agent-test fence")
	for _, indent := range []string{" ", "  ", "   "} {
		testsRefusal(t, testsHeader+indent+testFence("sample"), "Tests", "canonical yaml agent-test fence")
	}
}

func TestParseTestsProseAndOrderedFences(t *testing.T) {
	source := testsHeader + "# Notes\nUse the inline spelling ```yaml agent-test``` for a fence.\n\n```text\nignored\n```\n" + testFence("first") + "\nBetween tests.\n" + testFence("second")
	suite, err := ParseTests("suite.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Examples) != 2 || suite.Examples[0].ID != "first" || suite.Examples[1].ID != "second" {
		t.Fatalf("lost order: %+v", suite.Examples)
	}
	for _, source := range []string{testsHeader, testsHeader + "Just prose.\n", testsHeader + "````text\n```yaml agent-test\nnot YAML\n```\n````\n"} {
		suite, err := ParseTests("empty.md", []byte(source))
		if err != nil || suite == nil || len(suite.Examples) != 0 || suite.Examples == nil {
			t.Fatalf("present empty suite: %+v %v", suite, err)
		}
	}
}

func TestParseTestsStrictSyntax(t *testing.T) {
	cases := []struct{ name, source, field, reason string }{
		{"no header", testFence("a"), "Frontmatter", "schemaVersion"},
		{"unclosed header", "---\nschemaVersion: 1\n", "Frontmatter", "close"},
		{"wrong version", strings.Replace(testsStarter, "Version: 1", "Version: 2", 1), "Frontmatter", "integer 1"},
		{"string version", strings.Replace(testsStarter, "Version: 1", "Version: '1'", 1), "Frontmatter", "integer 1"},
		{"unknown header", strings.Replace(testsStarter, "schemaVersion: 1", "schemaVersion: 1\nother: true", 1), "Frontmatter", "only schemaVersion"},
		{"duplicate header", strings.Replace(testsStarter, "schemaVersion: 1", "schemaVersion: 1\nschemaVersion: 1", 1), "Frontmatter", "only schemaVersion"},
		{"multidoc header", "---\nschemaVersion: 1\n...\nschemaVersion: 1\n---\n", "Frontmatter", "one YAML document"},
		{"empty header", "---\n---\n", "Frontmatter", "only schemaVersion"},
		{"null version", "---\nschemaVersion: null\n---\n", "Frontmatter", "integer 1"},
		{"anchor header", "---\nschemaVersion: &version 1\n---\n", "Frontmatter", "anchors and aliases"},
		{"alias header", "---\nschemaVersion: &version 1\nother: *version\n---\n", "Frontmatter", "anchors and aliases"},
		{"tag header", "---\nschemaVersion: !custom 1\n---\n", "Frontmatter", "documented YAML types"},
		{"unknown case", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: Ada\nother: true", 1), "Examples", "unsupported fields"},
		{"duplicate case field", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: Ada\ninput: Ada", 1), "Examples", "duplicate YAML fields"},
		{"file operand", testsHeader + strings.Replace(testFence("a"), "input: Ada", "inputFile: input.txt", 1), "Examples", "unsupported fields"},
		{"alternate case schema", testsHeader + strings.Replace(testFence("a"), "assertions:", "checks:", 1), "Examples", "unsupported fields"},
		{"alternate root", testsHeader + "```yaml agent-test\nexamples: []\n```\n", "Examples", "unsupported fields"},
		{"duplicate IDs", testsHeader + testFence("a") + testFence("a"), "Examples", "unique example IDs"},
		{"sequence not mapping", testsHeader + "```yaml agent-test\n- a\n```\n", "Examples", "mapping"},
		{"multidoc", testsHeader + strings.Replace(testFence("a"), "\n```\n", "\n---\nid: b\n```\n", 1), "Examples", "one YAML document"},
		{"anchor", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: &name Ada", 1), "Examples", "anchors and aliases"},
		{"alias", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: *name", 1), "Examples", "valid YAML"},
		{"tag", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: !secret Ada", 1), "Examples", "documented YAML types"},
		{"unclosed", testsHeader + "```yaml agent-test\nid: a\n", "Tests", "close"},
		{"unclosed prose", testsHeader + "```text\nhello\n", "Tests", "close"},
		{"HTML hidden", testsHeader + "<!--\n" + testFence("a") + "-->\n", "Tests", "HTML comments"},
		{"big scalar", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: "+strings.Repeat("x", MaxInputBytes+1), 1), "Examples", "16 KiB"},
		{"credential", testsHeader + strings.Replace(testFence("a"), "input: Ada", "input: ghp_"+strings.Repeat("a", 36), 1), "File", "credential-shaped"},
		{"bytes", strings.Repeat("x", MaxFileBytes+1), "File", "256 KiB"},
		{"UTF8", testsHeader + string([]byte{0xff}), "File", "UTF-8"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) { testsRefusal(t, tt.source, tt.field, tt.reason) })
	}
	for _, info := range []string{"yml agent-test", "yaml Agent-Test", "yaml agent-tests", "yaml agent-test extra", "yaml\tagent-test", "yaml  agent-test", "agent-test", "yaml {agent-test}"} {
		t.Run(info, func(t *testing.T) {
			testsRefusal(t, testsHeader+strings.Replace(testFence("a"), "yaml agent-test", info, 1), "Tests", "yaml agent-test")
		})
	}
	for _, prefix := range []string{"    ", "\t", "> ", "- "} {
		t.Run(fmt.Sprintf("indent-%q", prefix), func(t *testing.T) { testsRefusal(t, testsHeader+prefix+testFence("a"), "Tests", "yaml agent-test") })
	}
}

func TestParseTestsSecondFenceByteLocations(t *testing.T) {
	source := testsHeader + testFence("first") + "Prose é.\n```yaml agent-test\n{input: é, id: first, assertions: [{id: a, type: contains, value: Ada}]}\n```\n"
	for _, eol := range []string{"\n", "\r\n"} {
		e := testsRefusal(t, strings.ReplaceAll(source, "\n", eol), "Examples", "unique example IDs")
		if e.Location.Line != 11 || e.Location.Column != 17 {
			t.Fatalf("wrong second fence byte location: %+v", e.Location)
		}
	}
}

func TestParseTestsGlobalCaseAndNodeLimits(t *testing.T) {
	for _, count := range []int{128, 129} {
		source := testsHeader
		for i := range count {
			source += testFence(fmt.Sprintf("case-%d", i))
		}
		suite, err := ParseTests("cases.md", []byte(source))
		if count == 128 {
			if err != nil || len(suite.Examples) != 128 {
				t.Fatalf("128 cases: %v", err)
			}
		} else {
			testsRefusal(t, source, "Examples", "0–128")
		}
	}
	// Four header nodes + four wrapper nodes + three (38+N)-node cases.
	// 3*10882 array elements put the combined suite at exactly 32768 nodes.
	for _, extra := range []int{0, 1} {
		source := testsHeader
		for i := range 3 {
			n := 10882
			if i == 2 {
				n += extra
			}
			source += "```yaml agent-test\nid: case-" + fmt.Sprint(i) + "\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: [{name: read, mode: recorded, description: Read, parameters: {type: object}, calls: [{arguments: {}, result: {rows: [" + strings.Repeat("0,", n-1) + "0]}}]}]}\n```\n"
		}
		if extra == 0 {
			if _, err := ParseTests("boundary.md", []byte(source)); err != nil {
				t.Fatalf("32768 nodes refused: %v", err)
			}
		} else {
			testsRefusal(t, source, "Examples", "32768")
		}
	}
}

func TestParseTestsStructuralBoundaryAndDecodedMocks(t *testing.T) {
	for _, extra := range []bool{false, true} {
		r := readRecording("greeting", nil)
		property := map[string]any{"type": "string"}
		levels := 2
		if extra {
			levels = 3
		}
		for range levels {
			property = map[string]any{"type": "array", "items": property}
		}
		r.Tool.Parameters = map[string]any{"type": "object", "properties": map[string]any{"x": property}}
		raw, _ := json.Marshal(r.Tool)
		source := testsHeader + "```yaml agent-test\nid: a\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: [" + string(raw) + "]}\n```\n"
		if extra {
			testsRefusal(t, source, "Examples", "12 levels")
		} else if _, err := ParseTests("boundary.md", []byte(source)); err != nil {
			t.Fatalf("structural depth12 refused: %v", err)
		}
	}
	credential := `"ghp_` + strings.Repeat(`\u0061`, 36) + `"`
	source := testsHeader + "```yaml agent-test\nid: a\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: [{name: read, mode: recorded, description: Read, parameters: {type: object}, calls: [{arguments: {}, result: {text: " + credential + "}}]}]}\n```\n"
	testsRefusal(t, source, "Examples", "no credentials")
	// Lookalike canonical paths inside payloads must not restart depth limits.
	r := readRecording("greeting", map[string]any{"examples": []any{map[string]any{"mocks": map[string]any{"tools": []any{map[string]any{"calls": []any{map[string]any{"result": payloadAtDepth(32, false)}}}}}}}})
	raw, _ := json.Marshal(r.Tool)
	testsRefusal(t, testsHeader+"```yaml agent-test\nid: a\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: ["+string(raw)+"]}\n```\n", "Examples", "32 recorded payload levels")
}

func TestParseTestsRecordingDepthParity(t *testing.T) {
	for _, field := range []string{"arguments", "result"} {
		for _, depth := range []int{32, 33} {
			r := readRecording("greeting", nil)
			r.Tool.Parameters = map[string]any{"type": "object"}
			if field == "arguments" {
				r.Tool.Calls[0].Arguments = payloadAtDepth(depth, false)
			} else {
				r.Tool.Calls[0].Result = payloadAtDepth(depth, false)
			}
			raw, err := json.Marshal(r.Tool)
			if err != nil {
				t.Fatal(err)
			}
			source := testsHeader + "```yaml agent-test\nid: greeting\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: [" + string(raw) + "]}\n```\n"
			if depth == 33 {
				testsRefusal(t, source, "Examples", "32 recorded payload levels")
			} else if _, err := ParseTests("depth.md", []byte(source)); err != nil {
				t.Fatalf("%s depth32: %v", field, err)
			}
		}
	}
	r := readRecording("greeting", nil)
	r.Tool.Parameters = map[string]any{"type": "object", "properties": map[string]any{"result": payloadAtDepth(10, false)}}
	raw, _ := json.Marshal(r.Tool)
	testsRefusal(t, testsHeader+"```yaml agent-test\nid: a\ninput: Ada\nassertions: [{id: a, type: contains, value: Ada}]\nmocks: {tools: ["+string(raw)+"]}\n```\n", "Examples", "12 levels")
	testsRefusal(t, "---\nschemaVersion: "+strings.Repeat("[", 14)+"1"+strings.Repeat("]", 14)+"\n---\n", "Frontmatter", "12 levels")
}
