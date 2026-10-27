package agentfile

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Canvas edits and saved recordings land in a file people edit by hand.
func TestCanvasEditsWriteBlockYAML(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		source := strings.ReplaceAll(strings.Replace(sample(t), "## Examples", "## Tools\n- read\n\n## Examples", 1), "\n", eol)
		input := "Two PRs\n#101 Add export"
		got, err := ApplyExampleEdits([]byte(source), []ExampleEdit{
			{BeforeID: "greeting", Assertions: json.RawMessage(`[{"id":"greeting-text","type":"contains","value":"Hello, Ada"},{"id":"check-1","type":"notContains","value":"#202"}]`), Tools: []ToolModeEdit{{Name: "read", Mode: "live"}}},
			{ID: "two-merged-PRs", Input: &input, Assertions: json.RawMessage(`[{"id":"says","type":"contains","value":"yes"},{"id":"calls","type":"toolCalled","tool":"read"}]`), Tools: []ToolModeEdit{{Name: "read", Mode: "recorded"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, err = ApplyRecordings(got, []Recording{readRecording("two-merged-PRs", map[string]any{"rows": []any{json.Number("9007199254740992")}, "note": "42"})}); err != nil {
			t.Fatal(err)
		}
		examples := string(got[bytes.Index(got, []byte("## Examples")):])
		if strings.Contains(examples, `{"`) || strings.Contains(examples, `[{`) || strings.Count(examples, "\n") != strings.Count(examples, eol) {
			t.Fatalf("not block YAML with %q line endings:\n%s", eol, examples)
		}
		for _, want := range []string{"id: check-1", "- name: read" + eol, "mode: live", "- id: two-merged-PRs", "description: Read rows", "9007199254740992", `note: "42"`} {
			if !strings.Contains(examples, want) {
				t.Fatalf("missing %q:\n%s", want, examples)
			}
		}
		a, err := Parse("block.agent.md", got)
		if err != nil {
			t.Fatal(err)
		}
		first, added := a.Examples[0], a.Examples[len(a.Examples)-1]
		if len(first.Assertions) != 2 || first.Assertions[1].Value != "#202" || first.Mocks.Tools[0].Mode != "live" ||
			added.ID != "two-merged-PRs" || added.Input != input || len(added.Assertions) != 2 || added.Assertions[1].Tool != "read" ||
			len(added.Mocks.Tools[0].Calls) != 1 || added.Mocks.Tools[0].Description != "Read rows" {
			t.Fatalf("meaning changed: %+v / %+v", first, added)
		}
	}
}
