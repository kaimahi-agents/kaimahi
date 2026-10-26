package agentfile

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRecordingDetailsPreservesNumberLexemes(t *testing.T) {
	source := blockWant(recordingSource(t, pendingTool), "\n")
	source = strings.Replace(source, `"complete":true`, `"large":9007199254740992,"decimal":1.0`, 1)
	if _, err := Parse("details.agent.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
	details := RecordingDetails([]byte(source))
	if len(details) != 1 || !strings.Contains(details[0].Text, "9007199254740992") || !strings.Contains(details[0].Text, "1.0") {
		t.Fatalf("numeric display changed %+v", details)
	}
}

func TestExamplePatchPreservesFixtureBytes(t *testing.T) {
	source := recordingSource(t, pendingTool)
	saved, err := ApplyRecordings([]byte(source), []Recording{readRecording("greeting", nil)})
	if err != nil {
		t.Fatal(err)
	}
	for _, eol := range []string{"\n", "\r\n"} {
		original := strings.ReplaceAll(string(saved), "\n", eol)
		input := "a different input é 🙂"
		got, err := ApplyExampleEdits([]byte(original), []ExampleEdit{{BeforeID: "greeting", Input: &input}})
		if err != nil {
			t.Fatal(err)
		}
		a, err := Parse("test.agent.md", got)
		if err != nil || a.Examples[0].Input != input {
			t.Fatalf("%v", err)
		}
		oldFixture := original[strings.Index(original, "    mocks:"):strings.Index(original, "    assertions:")]
		if !bytes.Contains(got, []byte(oldFixture)) {
			t.Fatalf("fixture changed: %s", got)
		}
		if !bytes.Contains(got, []byte("\"a different input é 🙂\"")) {
			t.Fatal("input not patched")
		}
	}
}

func TestExamplePatchLayoutsAndComments(t *testing.T) {
	for _, entry := range []string{pendingTool, `{name: read, mode: recorded, calls: []}`} {
		source := recordingSource(t, entry)
		source = strings.Replace(source, "    mocks:", "    # retained fixture comment é\n    mocks:", 1)
		value := "changed"
		got, err := ApplyExampleEdits([]byte(source), []ExampleEdit{{BeforeID: "greeting", Input: &value}})
		if err != nil || !bytes.Contains(got, []byte("# retained fixture comment é")) {
			t.Fatalf("lost comment: %v\n%s", err, got)
		}
	}
	source := recordingSource(t, pendingTool)
	a, _ := Parse("test.agent.md", []byte(source))
	input := a.Examples[0].Input
	// A flow example exercises scalar delimiters separately from flow tool rows.
	start := strings.Index(source, "  - id:")
	end := strings.LastIndex(source, "```")
	flow := source[:start] + `  - {id: greeting, input: ` + string(mustJSON(t, input)) + `, assertions: [{id: word, type: contains, value: hello}], mocks: {tools: [{name: read, mode: recorded, calls: []}]}}` + "\n" + source[end:]
	value := "changed"
	if _, err := ApplyExampleEdits([]byte(flow), []ExampleEdit{{BeforeID: "greeting", Input: &value}}); err != nil {
		t.Fatal("flow input:", err)
	}
}

func TestExamplePatchFlowLastScalarAndAdjacentDeletes(t *testing.T) {
	source := recordingSource(t, pendingTool)
	start := strings.Index(source, "examples:")
	end := strings.LastIndex(source, "```")
	example := func(id string) string {
		return `{id: ` + id + `, assertions: [{id: word, type: contains, value: hello}], input: Ada}`
	}
	input := "changed"
	for _, body := range []string{"examples:\n  - " + example("a") + "\n", "examples: [" + example("a") + ", " + example("b") + ", " + example("c") + "]\n"} {
		raw := []byte(source[:start] + body + source[end:])
		got, err := ApplyExampleEdits(raw, []ExampleEdit{{BeforeID: "a", Input: &input}})
		if err != nil {
			t.Fatal("last flow scalar", err)
		}
		a, _ := Parse("test.agent.md", got)
		if a.Examples[0].Input != input {
			t.Fatal("wrong input")
		}
		if len(a.Examples) == 3 {
			got, err = ApplyExampleEdits(raw, []ExampleEdit{{BeforeID: "b", Delete: true}, {BeforeID: "c", Delete: true}})
			if err != nil {
				t.Fatal("adjacent flow delete", err)
			}
			a, _ = Parse("test.agent.md", got)
			if len(a.Examples) != 1 || a.Examples[0].ID != "a" {
				t.Fatal("wrong retained test")
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExamplePatchModesAndReset(t *testing.T) {
	source := recordingSource(t, pendingTool)
	saved, err := ApplyRecordings([]byte(source), []Recording{readRecording("greeting", nil)})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []ToolModeEdit{{Name: "read", Mode: "recorded"}, {Name: "read", Mode: "recorded", Reset: true}, {Name: "read", Mode: "live"}} {
		got, err := ApplyExampleEdits(saved, []ExampleEdit{{BeforeID: "greeting", Tools: []ToolModeEdit{change}}})
		if err != nil {
			t.Fatal(err)
		}
		a, err := Parse("test.agent.md", got)
		if err != nil {
			t.Fatal(err)
		}
		tool := a.Examples[0].Mocks.Tools[0]
		if tool.Mode != change.Mode || len(tool.Calls) != map[bool]int{true: 0, false: 1}[change.Reset || change.Mode == "live"] {
			t.Fatalf("tool %+v", tool)
		}
		if !change.Reset && change.Mode == "recorded" && !bytes.Equal(saved, got) {
			t.Fatal("no-op changed bytes")
		}
	}
}

func TestExamplePatchDeleteAllAndEmpty(t *testing.T) {
	source := []byte(recordingSource(t, pendingTool))
	input := "second"
	source, err := ApplyExampleEdits(source, []ExampleEdit{{ID: "second", Input: &input, Assertions: json.RawMessage(`[{"id":"a","type":"contains","value":"hi"}]`)}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyExampleEdits(source, []ExampleEdit{{BeforeID: "greeting", Delete: true}, {BeforeID: "second", Delete: true}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Parse("test.agent.md", got)
	if err != nil || len(a.Examples) != 0 {
		t.Fatalf("%v", err)
	}
	got, err = ApplyExampleEdits(got, []ExampleEdit{{ID: "new", Input: &input, Assertions: json.RawMessage(`[{"id":"a","type":"contains","value":"hi"}]`)}})
	if err != nil {
		t.Fatal(err)
	}
	a, err = Parse("test.agent.md", got)
	if err != nil || len(a.Examples) != 1 {
		t.Fatalf("%v", err)
	}
}

func TestExamplePatchReplaceLast(t *testing.T) {
	for _, addSecond := range []bool{false, true} {
		source := []byte(recordingSource(t, pendingTool))
		input := "replacement"
		assertions := json.RawMessage(`[{"id":"a","type":"contains","value":"hi"}]`)
		if addSecond {
			var err error
			source, err = ApplyExampleEdits(source, []ExampleEdit{{ID: "second", Input: &input, Assertions: assertions}})
			if err != nil {
				t.Fatal(err)
			}
		}
		last := "greeting"
		if addSecond {
			last = "second"
		}
		got, err := ApplyExampleEdits(source, []ExampleEdit{{BeforeID: last, Delete: true}, {ID: "replacement", Input: &input, Assertions: assertions}})
		if err != nil {
			t.Fatal(err)
		}
		a, err := Parse("test.agent.md", got)
		if err != nil || a.Examples[len(a.Examples)-1].ID != "replacement" {
			t.Fatalf("%v", err)
		}
	}
}

func TestExamplePatchCreateAndRefusal(t *testing.T) {
	source := []byte(recordingSource(t, pendingTool))
	input := "new input"
	assertions := json.RawMessage(`[{"id":"called","type":"toolCalled","tool":"read"}]`)
	got, err := ApplyExampleEdits(source, []ExampleEdit{{ID: "second", Input: &input, Assertions: assertions, Tools: []ToolModeEdit{{Name: "read", Mode: "recorded"}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Parse("test.agent.md", got)
	if err != nil || len(a.Examples) != 2 {
		t.Fatalf("%v", err)
	}
	removed, err := ApplyExampleEdits(got, []ExampleEdit{{BeforeID: "second", Delete: true}})
	if err != nil {
		t.Fatal(err)
	}
	a, err = Parse("test.agent.md", removed)
	if err != nil || len(a.Examples) != 1 {
		t.Fatalf("%v", err)
	}
	for _, edits := range [][]ExampleEdit{
		{{BeforeID: "missing", Input: &input}},
		{{BeforeID: "greeting", Tools: []ToolModeEdit{{Name: "read", Mode: "invalid"}}}},
		{{BeforeID: "greeting", Input: &input}, {BeforeID: "missing", Delete: true}},
		{{BeforeID: "greeting", Input: &input}, {BeforeID: "greeting", Delete: true}},
	} {
		if b, err := ApplyExampleEdits(source, edits); err == nil || b != nil {
			t.Fatal("invalid patch produced bytes")
		}
	}
}
