package agentfile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func TestRecordingFieldsRetainModelVisibleOutput(t *testing.T) {
	a, err := Parse("greeter.agent.md", []byte(recordingSource(t, recordedTool)))
	if err != nil {
		t.Fatal(err)
	}
	tool := a.Examples[0].Mocks.Tools[0]
	if tool.Name != "read" || tool.Mode != "recorded" || tool.Description != "Read rows" || tool.Parameters["type"] != "object" || len(tool.Calls) != 1 {
		t.Fatal("fixture definition dropped")
	}
	if !reflect.DeepEqual(tool.Calls[0].Arguments, map[string]any{"query": "release"}) {
		t.Fatal("saved arguments changed")
	}
	want := map[string]any{"rows": []any{map[string]any{"number": float64(42), "title": "Fix"}}, "complete": true}
	if !reflect.DeepEqual(tool.Calls[0].Result, want) {
		t.Fatalf("not model-visible output: %#v", tool.Calls[0].Result)
	}
	if strings.Contains(a.Instructions, "Read rows") || strings.Contains(a.Instructions, "## Tools") {
		t.Fatal("fixtures entered instructions")
	}
}

// Dropping nested copies lets execution mutate frozen source data, locations or
// authored score pointers even when the caller only edits its returned examples.
func TestBindJudgeModelsDetachesFixtures(t *testing.T) {
	a, err := Parse("greeter.agent.md", []byte(recordingSource(t, recordedTool)))
	if err != nil {
		t.Fatal(err)
	}
	score := 80
	a.Examples[0].Assertions = append(a.Examples[0].Assertions, runtime.EvaluationAssertion{ID: "judge", Type: "llmJudge", MinScore: &score})
	a.Examples[0].Mocks.Tools[0].Calls[0].Arguments["nested"] = []any{map[string]any{"value": "original"}}
	before, _ := json.Marshal(a.Examples)
	copy := BindJudgeModels(a.Examples, "execution-model")
	if copy[0].Assertions[1].JudgeModel != "execution-model" {
		t.Fatal("judge model not bound")
	}
	copy[0].AssertionLocations[0].Line = 0
	*copy[0].Assertions[1].MinScore = 1
	tool := &copy[0].Mocks.Tools[0]
	tool.Parameters["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number"
	tool.Calls[0].Arguments["nested"].([]any)[0].(map[string]any)["value"] = "changed"
	tool.Calls[0].Result["rows"].([]any)[0].(map[string]any)["title"] = "Changed"
	tool.Calls = append(tool.Calls, MockCall{})
	tool.Name = "changed"
	copy[0].Mocks.Tools = append(copy[0].Mocks.Tools, MockTool{})
	after, _ := json.Marshal(a.Examples)
	if string(before) != string(after) {
		t.Fatal("returned execution fixture aliases frozen source")
	}
}

func TestCloneExamplesPreservesNilAndDetachedContent(t *testing.T) {
	if CloneExamples(nil) != nil {
		t.Fatal("nil examples changed")
	}
	empty := CloneExamples([]Example{})
	if empty == nil || len(empty) != 0 {
		t.Fatal("explicit empty examples changed")
	}
	a, err := Parse("greeter.agent.md", []byte(recordingSource(t, recordedTool)))
	if err != nil {
		t.Fatal(err)
	}
	cloned := CloneExamples(a.Examples)
	if !reflect.DeepEqual(cloned, a.Examples) {
		t.Fatal("clone changed authored content or locations")
	}
	cloned[0].Mocks.Tools[0].Calls[0].Result["rows"].([]any)[0].(map[string]any)["title"] = "Changed"
	if a.Examples[0].Mocks.Tools[0].Calls[0].Result["rows"].([]any)[0].(map[string]any)["title"] != "Fix" {
		t.Fatal("clone aliases original result")
	}
	for _, entry := range []string{"name: read\n        mode: live", "name: read\n        mode: recorded\n        calls: []"} {
		a, err := Parse("greeter.agent.md", []byte(recordingSource(t, entry)))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(CloneExamples(a.Examples), a.Examples) {
			t.Fatal("clone changed nil/empty fields")
		}
	}
}

func TestRecordingJSONTypesAndOfflineLocalReferences(t *testing.T) {
	base := recordingSource(t, recordedTool)
	for _, replacement := range []string{
		"{type: object, definitions: {query: {type: string}}, properties: {query: {'$ref': '#/definitions/query'}}, required: [query], additionalProperties: false}",
		"{type: object}",
	} {
		source := strings.Replace(base, "{type: object, properties: {query: {type: string}}, required: [query], additionalProperties: false}", replacement, 1)
		if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
			t.Fatal("offline local schema refused:", err)
		}
	}
	source := strings.Replace(base, "number: 42, title: Fix", "number: 9007199254740992, title: '2026-10-25'", 1)
	source = strings.Replace(source, "complete: true", "complete: null, text: on, fraction: 0.1", 1)
	a, err := Parse("greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	result := a.Examples[0].Mocks.Tools[0].Calls[0].Result
	if result["complete"] != nil || result["text"] != "on" || result["fraction"] != float64(0.1) {
		t.Fatal("JSON types changed")
	}
	for _, row := range []struct{ old, replacement string }{
		{"mocks:", "Mocks:"},
		{"name: read", "Name: read"},
		{"arguments:", "Arguments:"},
		{"result:", "Result:"},
		{"name: read", "name: null"},
		{"mode: recorded", "mode: null"},
		{"number: 42", "number: .nan"},
		{"number: 42", "number: !!float 1e999"},
	} {
		if _, err := Parse("greeter.agent.md", []byte(strings.Replace(base, row.old, row.replacement, 1))); err == nil {
			t.Fatalf("accepted invalid exact key/type/number: %s", row.replacement)
		}
	}
	for _, kind := range []string{"toolCalled", "toolNotCalled"} {
		source := strings.Replace(sample(t), "type: contains\n        value: Hello, Ada", "type: "+kind+"\n        tool: missing", 1)
		if _, err := Parse("greeter.agent.md", []byte(source)); err == nil {
			t.Fatal("undeclared tool check accepted")
		}
	}
}
