package agentfile

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

const (
	pendingTool     = "name: read\n        mode: recorded\n        calls: []"
	readParamsJSON  = `{"additionalProperties":false,"properties":{"query":{"type":"string"}},"required":["query"],"type":"object"}`
	readCallJSON    = `{"arguments":{"query":"release"},"result":{"complete":true}}`
	readDefinitions = `"description": "Read rows"`
)

func readRecording(example string, result map[string]any) Recording {
	params := map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []any{"query"}, "additionalProperties": false}
	if result == nil {
		result = map[string]any{"complete": true}
	}
	return Recording{ExampleID: example, Tool: MockTool{Name: "read", Mode: "recorded", Description: "Read rows", Parameters: params,
		Calls: []MockCall{{Arguments: map[string]any{"query": "release"}, Result: result}}}}
}

// applyExact proves insertion-only output and that the reparsed fixture equals the request.
func applyExact(t *testing.T, source, want string, recordings ...Recording) {
	t.Helper()
	if source == want {
		t.Fatal("test expectation did not change the source")
	}
	got, err := ApplyRecordings([]byte(source), recordings)
	if err != nil || string(got) != want {
		t.Fatalf("err=%v\n got: %q\nwant: %q", err, got, want)
	}
	checkSaved(t, got, recordings)
}

func checkSaved(t *testing.T, data []byte, recordings []Recording) {
	t.Helper()
	a, err := Parse("greeter.agent.md", data)
	if err != nil {
		t.Fatalf("patched file does not parse: %v", err)
	}
	for _, r := range recordings {
		found := false
		for _, e := range a.Examples {
			for _, tool := range mocksOf(e) {
				if e.ID == r.ExampleID && tool.Name == r.Tool.Name {
					found = true
					got, _ := json.Marshal(tool)
					want, _ := json.Marshal(r.Tool)
					if string(got) != string(want) {
						t.Fatalf("saved fixture changed:\n got: %s\nwant: %s", got, want)
					}
				}
			}
		}
		if !found {
			t.Fatalf("recording %s/%s not saved", r.ExampleID, r.Tool.Name)
		}
	}
}

func mocksOf(e Example) []MockTool {
	if e.Mocks == nil {
		return nil
	}
	return e.Mocks.Tools
}

func refused(t *testing.T, source string, want error, recordings ...Recording) {
	t.Helper()
	got, err := ApplyRecordings([]byte(source), recordings)
	if got != nil || !errors.Is(err, want) {
		t.Fatalf("want %v and no bytes, got %v and %d bytes", want, err, len(got))
	}
}

func blockWant(source, eol string) string {
	return strings.Replace(source, "        calls: []", "        "+readDefinitions+eol+`        "parameters": `+readParamsJSON+eol+"        calls: ["+readCallJSON+"]", 1)
}

func TestApplyRecordingsBlockPreservesBytes(t *testing.T) {
	source := recordingSource(t, pendingTool)
	source = strings.Replace(source, "No delegated work.", "No delegated work.\n```\n<!-- not a comment -->\n## Examples\n    calls: []\n```", 1)
	source = strings.Replace(source, "Do not call tools", "Do not call tools # é 🙂 `calls: []`", 1)
	applyExact(t, source, blockWant(source, "\n"), readRecording("greeting", nil))
}

func TestApplyRecordingsFirstKeyLayout(t *testing.T) {
	source := recordingSource(t, "calls: [] # waiting\n        name: read\n        mode: recorded")
	want := strings.Replace(source, "      - calls: [] # waiting", "      - "+readDefinitions+"\n"+`        "parameters": `+readParamsJSON+"\n        calls: ["+readCallJSON+"] # waiting", 1)
	applyExact(t, source, want, readRecording("greeting", nil))
}

func TestApplyRecordingsLineEndings(t *testing.T) {
	lf := recordingSource(t, pendingTool)
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	applyExact(t, crlf, strings.ReplaceAll(blockWant(lf, "\n"), "\n", "\r\n"), readRecording("greeting", nil))
	// Mixed files are not normalized: each insertion follows its own target line.
	mixed := strings.Replace(crlf, "calls: []\r\n", "calls: []\n", 1)
	applyExact(t, mixed, blockWant(mixed, "\n"), readRecording("greeting", nil))
	mixed = strings.Replace(lf, "calls: []\n", "calls: []\r\n", 1)
	applyExact(t, mixed, blockWant(mixed, "\r\n"), readRecording("greeting", nil))
}

func TestApplyRecordingsFlowCommentsAndTags(t *testing.T) {
	for _, row := range []struct{ entry, old, new string }{
		{"{name: read, mode: recorded, calls: [ # waiting\n          ]}", "calls: [ # waiting",
			readDefinitions + `, "parameters": ` + readParamsJSON + ", calls: [" + readCallJSON + " # waiting"},
		{"!!map {name: read, mode: recorded, !!str calls: !!seq # tag\n          [ # [ ] waiting\n          ]}", "!!str calls: !!seq # tag\n          [",
			readDefinitions + `, "parameters": ` + readParamsJSON + ", !!str calls: !!seq # tag\n          [" + readCallJSON},
		{"name: read\n        mode: recorded\n        calls: !<tag:yaml.org,2002:seq> [ ]", "calls: !<tag:yaml.org,2002:seq> [",
			readDefinitions + "\n        \"parameters\": " + readParamsJSON + "\n        calls: !<tag:yaml.org,2002:seq> [" + readCallJSON},
	} {
		source := recordingSource(t, row.entry)
		applyExact(t, source, strings.Replace(source, row.old, row.new, 1), readRecording("greeting", nil))
	}
}

func TestApplyRecordingsUnicodeColumnsAndUnrelatedRecordings(t *testing.T) {
	source := recordingSource(t, recordedTool+" # keep")
	source = strings.Replace(source, "        value: Hello, Ada\n", "        value: Hello, Ada\n  - {id: unicode, input: \"Ādā 🙂 é\", mocks: {tools: [{name: read, mode: recorded, calls: []}]}, assertions: [{id: a, type: contains, value: x}]}\n  - id: second\n    input: B\n    mocks: {tools: [{name: other, mode: live}, {name: read, mode: recorded, calls: []}]}\n    assertions: [{id: a, type: contains, value: x}]\n", 1)
	want := strings.Replace(source, "{name: read, mode: recorded, calls: []}]}, assertions", "{name: read, mode: recorded, "+readDefinitions+`, "parameters": `+readParamsJSON+", calls: ["+readCallJSON+"]}]}, assertions", 1)
	want = strings.Replace(want, "{name: read, mode: recorded, calls: []}]}\n", "{name: read, mode: recorded, "+readDefinitions+`, "parameters": `+readParamsJSON+", calls: ["+readCallJSON+"]}]}\n", 1)
	applyExact(t, source, want, readRecording("unicode", nil), readRecording("second", nil))
}

func TestApplyRecordingsQuotesStringsAndKeys(t *testing.T) {
	result := map[string]any{"2026-10-25": "2026-10-25", `{"a": 1}`: "[1, 2] # not a comment", "yes": "no", "n": "0x10", "empty": "",
		"nested": map[string]any{"k: v": []any{"null", "~", "é🙂", "<&>", 1.5, nil, false}}, "!!int": "*alias"}
	source := recordingSource(t, pendingTool)
	got, err := ApplyRecordings([]byte(source), []Recording{readRecording("greeting", result)})
	if err != nil {
		t.Fatal(err)
	}
	checkSaved(t, got, []Recording{readRecording("greeting", result)})
}

func TestApplyRecordingsRefusesTargets(t *testing.T) {
	source := recordingSource(t, pendingTool)
	source = strings.Replace(source, "        value: Hello, Ada\n", "        value: Hello, Ada\n  - id: populated\n    input: B\n    mocks:\n      tools:\n      - "+recordedTool+"\n      - name: other\n        mode: live\n    assertions: [{id: a, type: contains, value: x}]\n  - id: plain\n    input: C\n    assertions: [{id: a, type: contains, value: x}]\n", 1)
	if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	good := readRecording("greeting", nil)
	for name, r := range map[string]Recording{"unknown example": readRecording("missing", nil), "no mocks": readRecording("plain", nil), "populated": readRecording("populated", nil),
		"live":        {ExampleID: "populated", Tool: MockTool{Name: "other", Mode: "recorded", Description: "x", Parameters: map[string]any{"type": "object"}, Calls: []MockCall{{map[string]any{}, map[string]any{}}}}},
		"absent tool": {ExampleID: "greeting", Tool: MockTool{Name: "other", Mode: "recorded", Description: "x", Parameters: map[string]any{"type": "object"}, Calls: []MockCall{{map[string]any{}, map[string]any{}}}}}} {
		t.Run(name, func(t *testing.T) { refused(t, source, ErrRecordingChanged, good, r) })
	}
	refused(t, strings.Replace(source, "## Identity", "## Identity\n## Identity", 1), ErrRecordingChanged, good)
	refused(t, source, ErrRecordingInvalid)
	refused(t, source, ErrRecordingInvalid, good, good)
	for name, change := range map[string]func(*MockTool){
		"live mode":         func(m *MockTool) { m.Mode = "live" },
		"no calls":          func(m *MockTool) { m.Calls = nil },
		"two calls":         func(m *MockTool) { m.Calls = append(m.Calls, m.Calls[0]) },
		"blank description": func(m *MockTool) { m.Description = " " },
		"missing schema":    func(m *MockTool) { m.Parameters = nil },
		"schema mismatch":   func(m *MockTool) { m.Calls[0].Arguments = map[string]any{"query": 42.0} },
		"nil result":        func(m *MockTool) { m.Calls[0].Result = nil },
		"nonfinite":         func(m *MockTool) { m.Calls[0].Result = map[string]any{"n": math.NaN()} },
		"inexact integer":   func(m *MockTool) { m.Calls[0].Result = map[string]any{"n": json.Number("9007199254740993")} },
		"separator":         func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "a\u2028b"} },
		"lossy text":        func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "a\xffb"} },
		"credential value":  func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": "ghp_" + strings.Repeat("a", 36)} },
		"credential key":    func(m *MockTool) { m.Calls[0].Result = map[string]any{"ghp_" + strings.Repeat("a", 36): true} },
		"scalar over bound": func(m *MockTool) { m.Calls[0].Result = map[string]any{"s": strings.Repeat("a", 16385)} },
		"result over bound": func(m *MockTool) {
			m.Calls[0].Result = map[string]any{"a": strings.Repeat("a", 16384), "b": strings.Repeat("a", 16384)}
		},
		"depth 13": func(m *MockTool) {
			m.Calls[0].Result = map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": 1.0}}}}
		},
		"definition too big": func(m *MockTool) { m.Description = strings.Repeat("a", 4097) },
	} {
		t.Run(name, func(t *testing.T) {
			r := readRecording("greeting", nil)
			change(&r.Tool)
			refused(t, source, ErrRecordingInvalid, r)
		})
	}
	depth12 := readRecording("greeting", map[string]any{"a": map[string]any{"b": map[string]any{"c": "x"}}})
	got, err := ApplyRecordings([]byte(source), []Recording{depth12})
	if err != nil {
		t.Fatalf("depth 12 refused: %v", err)
	}
	checkSaved(t, got, []Recording{depth12})
}
