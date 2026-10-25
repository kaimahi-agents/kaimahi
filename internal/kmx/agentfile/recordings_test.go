package agentfile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const recordedTool = `name: read
        mode: recorded
        description: Read rows
        parameters: {type: object, properties: {query: {type: string}}, required: [query], additionalProperties: false}
        calls:
          - arguments: {query: release}
            result: {rows: [{number: 42, title: Fix}], complete: true}`

func recordingSource(t *testing.T, tools string) string {
	t.Helper()
	source := strings.Replace(sample(t), "## Examples", "## Tools\n- read\n- other\n\n## Examples", 1)
	return strings.Replace(source, "    assertions:", "    mocks:\n      tools:\n      - "+tools+"\n    assertions:", 1)
}

// Removing explicit modes, fixture retention or declared call checks breaks these
// accepted files. They must remain parser data, not live executor authority.
func TestOptionalMocksNotListedAsRequired(t *testing.T) {
	source := strings.Split(sample(t), "## Examples")[0] + "## Examples\n```yaml\nexamples:\n  - id: missing-input\n    assertions: []\n```\n"
	_, err := Parse("missing.agent.md", []byte(source))
	if err == nil || strings.Contains(err.Error(), "mocks") {
		t.Fatalf("optional field listed as required: %v", err)
	}
}

func TestRecordingsParseModes(t *testing.T) {
	for _, entry := range []string{recordedTool, "name: read\n        mode: live", "name: read\n        mode: recorded\n        calls: []"} {
		t.Run(strings.ReplaceAll(entry, "\n", " ")[:20], func(t *testing.T) {
			source := recordingSource(t, entry)
			if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
				t.Fatalf("valid explicit mode refused: %v", err)
			}
		})
	}
	for _, check := range []string{"toolCalled", "toolNotCalled"} {
		source := strings.Replace(recordingSource(t, recordedTool), "type: contains\n        value: Hello, Ada", "type: "+check+"\n        tool: read", 1)
		if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
			t.Fatalf("declared %s refused: %v", check, err)
		}
		// A check does not require any saved data.
		source = strings.Replace(sample(t), "## Examples", "## Tools\n- read\n\n## Examples", 1)
		source = strings.Replace(source, "type: contains\n        value: Hello, Ada", "type: "+check+"\n        tool: read", 1)
		if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
			t.Fatalf("unconfigured %s refused: %v", check, err)
		}
	}
}

func TestRecordingsClosedFieldsAndModes(t *testing.T) {
	base := recordingSource(t, recordedTool)
	for _, row := range []struct{ name, old, replacement string }{
		{"unknown mocks key", "    mocks:", "    mocks:\n      PRIVATE_FIXTURE_CANARY: true"},
		{"missing tools", "      tools:", "      notTools:"},
		{"null tools", "      tools:\n      - " + recordedTool, "      tools: null"},
		{"unknown tool field", "        mode: recorded", "        mode: recorded\n        PRIVATE_FIXTURE_CANARY: true"},
		{"duplicate tool field", "        mode: recorded", "        mode: recorded\n        mode: recorded"},
		{"missing name", "name: read", "mode: recorded"},
		{"numeric name", "name: read", "name: 123"},
		{"boolean name", "name: read", "name: true"},
		{"invalid name", "name: read", "name: server.read"},
		{"undeclared name", "name: read", "name: missing"},
		{"omitted mode", "        mode: recorded\n", ""},
		{"unknown mode", "mode: recorded", "mode: auto"},
		{"missing calls", "        calls:\n          - arguments: {query: release}\n            result: {rows: [{number: 42, title: Fix}], complete: true}", ""},
		{"null calls", "calls:\n          - arguments: {query: release}\n            result: {rows: [{number: 42, title: Fix}], complete: true}", "calls: null"},
		{"missing description", "        description: Read rows\n", ""},
		{"numeric description", "description: Read rows", "description: 123"},
		{"blank description", "description: Read rows", "description: ''"},
		{"missing schema", "        parameters: {type: object, properties: {query: {type: string}}, required: [query], additionalProperties: false}\n", ""},
		{"null schema", "parameters: {type: object, properties: {query: {type: string}}, required: [query], additionalProperties: false}", "parameters: null"},
		{"invalid schema", "{type: string}", "{type: invalid}"},
		{"external reference", "{type: string}", "{'$ref': 'https://example.invalid/schema'}"},
		{"file reference", "{type: string}", "{'$ref': 'file:///private/schema'}"},
		{"schema mismatch", "arguments: {query: release}", "arguments: {query: 42}"},
		{"extra argument", "arguments: {query: release}", "arguments: {query: release, extra: true}"},
		{"missing argument", "arguments: {query: release}", "arguments: {}"},
		{"null arguments", "arguments: {query: release}", "arguments: null"},
		{"list arguments", "arguments: {query: release}", "arguments: []"},
		{"unknown call field", "arguments: {query: release}", "PRIVATE_FIXTURE_CANARY: true\n            arguments: {query: release}"},
		{"missing result", "            result: {rows: [{number: 42, title: Fix}], complete: true}", ""},
		{"null result", "result: {rows: [{number: 42, title: Fix}], complete: true}", "result: null"},
		{"scalar result", "result: {rows: [{number: 42, title: Fix}], complete: true}", "result: failure"},
		{"list result", "result: {rows: [{number: 42, title: Fix}], complete: true}", "result: []"},
		{"two calls", "            result: {rows: [{number: 42, title: Fix}], complete: true}", "            result: {}\n          - arguments: {query: release}\n            result: {}"},
		{"duplicate names", "    assertions:", "      - name: read\n        mode: live\n    assertions:"},
		{"live schema", "mode: recorded", "mode: live"},
		{"numeric result key", "complete: true", "1: true"},
		{"duplicate result key", "complete: true", "complete: true, complete: false"},
		{"duplicate schema key", "type: string", "type: string, type: number"},
		{"inexact integer", "number: 42", "number: 9007199254740993"},
		{"inexact decimal", "number: 42", "number: 0.123456789012345678901"},
		{"nonfinite", "number: 42", "number: .inf"},
		{"timestamp", "title: Fix", "title: 2026-10-25"},
		{"deep result", "title: Fix", "title: {nested: value}"},
		{"escaped credential value", "title: Fix", `title: "\u0067hp_` + strings.Repeat("a", 36) + `"`},
		{"escaped credential key", "complete: true", `"\u0067hp_` + strings.Repeat("a", 36) + `": true`},
	} {
		t.Run(row.name, func(t *testing.T) {
			source := strings.Replace(base, row.old, row.replacement, 1)
			if source == base {
				t.Fatal("test replacement did not match")
			}
			a, err := Parse("greeter.agent.md", []byte(source))
			var refusal *Error
			if a != nil || !errors.As(err, &refusal) || strings.Contains(err.Error(), "PRIVATE_FIXTURE_CANARY") {
				t.Fatalf("invalid fixture accepted or unsafe refusal: %v", err)
			}
		})
	}
	for _, entry := range []string{
		"name: read\n        mode: live\n        calls: []",
		"name: read\n        mode: live\n        description: ''",
		"name: read\n        mode: recorded\n        calls: []\n        parameters: {}",
		"name: read\n        mode: recorded\n        calls: []\n        description: Read",
	} {
		if _, err := Parse("greeter.agent.md", []byte(recordingSource(t, entry))); err == nil {
			t.Fatalf("invalid mode fields accepted: %s", entry)
		}
	}
}

func TestRecordingsBounds(t *testing.T) {
	base := recordingSource(t, recordedTool)
	for _, row := range []struct {
		name, old, replacement string
		valid                  bool
	}{
		{"arguments at limit", "query: release", "query: " + strings.Repeat("a", 8180), true},
		{"arguments over limit", "query: release", "query: " + strings.Repeat("a", 8181), false},
		{"result at limit", "{rows: [{number: 42, title: Fix}], complete: true}", "{a: '" + strings.Repeat("a", 16384) + "', b: '" + strings.Repeat("a", 16369) + "'}", true},
		{"result over limit", "{rows: [{number: 42, title: Fix}], complete: true}", "{a: '" + strings.Repeat("a", 16384) + "', b: '" + strings.Repeat("a", 16370) + "'}", false},
		{"description over limit", "description: Read rows", "description: " + strings.Repeat("a", 4097), false},
		{"scalar over limit", "title: Fix", "title: " + strings.Repeat("a", 16385), false},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := Parse("greeter.agent.md", []byte(strings.Replace(base, row.old, row.replacement, 1)))
			if (err == nil) != row.valid {
				t.Fatalf("valid=%t, err=%v", row.valid, err)
			}
		})
	}
	// The total definition budget is per example, not per individual tool.
	var declarations, entries strings.Builder
	for i := 0; i < 17; i++ {
		fmt.Fprintf(&declarations, "- tool_%d\n", i)
		fmt.Fprintf(&entries, "      - name: tool_%d\n        mode: recorded\n        description: %s\n        parameters: {type: object}\n        calls: [{arguments: {}, result: {}}]\n", i, strings.Repeat("a", 4096))
	}
	source := strings.Replace(base, "- read\n- other\n", declarations.String(), 1)
	source = strings.Replace(source, "      - "+recordedTool+"\n", entries.String(), 1)
	if _, err := Parse("greeter.agent.md", []byte(source)); err == nil {
		t.Fatal("aggregate oversized definitions accepted")
	}
}

func TestSquadRejectsCanonicalFixturesAndToolChecks(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	source := string(enrichedSquad(raw))
	for _, bad := range []string{
		strings.Replace(source, "    assertions:", "    mocks: {tools: []}\n    assertions:", 1),
		strings.Replace(source, "type: contains\n        value: Hello, Ada", "type: toolCalled\n        tool: read", 1),
		strings.Replace(source, "type: contains\n        value: Hello, Ada", "type: toolNotCalled\n        tool: read", 1),
	} {
		if _, err := ParseSquad(filename, []byte(bad), profile); err == nil {
			t.Fatal("native Squad accepted tool semantics")
		}
	}
}

func TestExportRefusesRecordingSemantics(t *testing.T) {
	for _, source := range []string{
		recordingSource(t, recordedTool),
		recordingSource(t, "name: read\n        mode: live"),
		strings.Replace(sample(t), "    assertions:", "    mocks: {tools: []}\n    assertions:", 1),
		strings.Replace(strings.Replace(sample(t), "## Examples", "## Tools\n- read\n\n## Examples", 1), "type: contains\n        value: Hello, Ada", "type: toolCalled\n        tool: read", 1),
	} {
		if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
			t.Fatalf("refusal fixture must parse: %v", err)
		}
		data, err := ExportRunProfile(exportSource(t, source))
		if err == nil || data != nil {
			t.Fatal("lossy export accepted fixture/check semantics")
		}
	}
}
