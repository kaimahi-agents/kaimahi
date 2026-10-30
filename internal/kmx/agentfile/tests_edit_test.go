package agentfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestApplyTestsEditsPreservesSources(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n"} {
		t.Run(strings.ReplaceAll(eol, "\n", "LF"), func(t *testing.T) {
			source := strings.ReplaceAll("---\nschemaVersion: 1\n---\n\nProse stays.\n\n```yaml agent-test\nid: first\ninput: old\nassertions: [{id: a, type: contains, value: hi}]\nmocks:\n  tools:\n    - name: lookup\n      mode: recorded\n      description: lookup\n      parameters: {type: object}\n      calls:\n        - arguments: {}\n          result: {large: 9007199254740992, exponent: 1.2300e+4}\n```\n\nAfter first.\n\n~~~yaml agent-test\nid: second\ninput: keep\nassertions: [{id: a, type: contains, value: hi}]\n~~~\n\nTrailing prose.\n", "\n", eol)
			if _, err := ParseTests("x.agent.test.md", []byte(source)); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			input := "new\ninput"
			got, err := ApplyTestsEdits([]byte(source), []ExampleEdit{{BeforeID: "first", Input: &input}})
			if err != nil {
				t.Fatal(err)
			}
			details := TestsRecordingDetails(got)
			if len(details) != 1 || !strings.Contains(details[0].Text, "9007199254740992") || !strings.Contains(details[0].Text, "1.2300e+4") {
				t.Fatalf("lossy recording display: %+v", details)
			}
			parsed, err := ParseTests("x.agent.test.md", got)
			if err != nil || parsed.Examples[0].Input != input {
				t.Fatalf("parse=%v error=%v", parsed, err)
			}
			for _, exact := range []string{"result: {large: 9007199254740992, exponent: 1.2300e+4}", "After first.", "~~~yaml agent-test" + eol + "id: second" + eol + "input: keep", "Trailing prose."} {
				if !bytes.Contains(got, []byte(exact)) {
					t.Fatalf("lost exact %q: %s", exact, got)
				}
			}
			added := "hello"
			got, err = ApplyTestsEdits(got, []ExampleEdit{{BeforeID: "second", Delete: true}, {ID: "third", Input: &added, Assertions: json.RawMessage(`[{"id":"a","type":"contains","value":"hi"}]`), Tools: []ToolModeEdit{{Name: "lookup", Mode: "recorded"}}}})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err = ParseTests("x.agent.test.md", got)
			if err != nil || len(parsed.Examples) != 2 || parsed.Examples[1].ID != "third" {
				t.Fatalf("parse=%v error=%v source=%s", parsed, err, got)
			}
			if !bytes.Contains(got, []byte("After first.")) || !bytes.Contains(got, []byte("Trailing prose.")) {
				t.Fatal("delete changed prose")
			}
			got, err = ApplyTestsEdits(got, []ExampleEdit{{BeforeID: "first", Delete: true}, {BeforeID: "third", Delete: true}})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err = ParseTests("x.agent.test.md", got)
			if err != nil || len(parsed.Examples) != 0 || !bytes.Contains(got, []byte("schemaVersion: 1")) {
				t.Fatalf("not present zero-test suite: %s %v", got, err)
			}
			if eol == "\r\n" && strings.Contains(strings.ReplaceAll(string(got), "\r\n", ""), "\n") {
				t.Fatal("introduced LF")
			}
		})
	}
}

func TestApplyTestsEditsValidatesWholeBatch(t *testing.T) {
	fence := func(id string) string {
		return "```yaml agent-test\nid: " + id + "\ninput: x\nassertions: [{id: a, type: contains, value: x}]\n```\n"
	}
	source := "---\nschemaVersion: 1\n---\n"
	for i := 0; i < MaxExamples; i++ {
		source += fence(fmt.Sprintf("case-%d", i))
	}
	input := "x"
	got, err := ApplyTestsEdits([]byte(source), []ExampleEdit{{ID: "replacement", Input: &input, Assertions: json.RawMessage(`[{"id":"a","type":"contains","value":"x"}]`)}, {BeforeID: "case-127", Delete: true}})
	if err != nil {
		t.Fatalf("valid final 128 cases refused: %v", err)
	}
	parsed, err := ParseTests("x.agent.test.md", got)
	if err != nil || len(parsed.Examples) != MaxExamples || parsed.Examples[MaxExamples-1].ID != "replacement" {
		t.Fatalf("wrong final replacement: %v %v", parsed, err)
	}
	source = "---\nschemaVersion: 1\n---\n" + fence("one") + fence("two")
	got, err = ApplyTestsEdits([]byte(source), []ExampleEdit{{BeforeID: "one", ID: "two"}, {BeforeID: "two", ID: "one"}})
	if err != nil {
		t.Fatalf("valid final IDs refused: %v", err)
	}
	parsed, err = ParseTests("x.agent.test.md", got)
	if err != nil || parsed.Examples[0].ID != "two" || parsed.Examples[1].ID != "one" {
		t.Fatal("batch changed order", err)
	}
}

func TestApplyTestsEditsAbsentAndEndOfFile(t *testing.T) {
	input := "é 🙂"
	add := ExampleEdit{ID: "new", Input: &input, Assertions: json.RawMessage(`[{"id":"a","type":"contains","value":"é"}]`)}
	seeded, err := ApplyTestsEdits(nil, []ExampleEdit{add})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTests("x.agent.test.md", seeded)
	if err != nil || len(parsed.Examples) != 1 || parsed.Examples[0].Input != input {
		t.Fatalf("seed %s: %v", seeded, err)
	}
	for _, suffix := range []string{"", "\n", "\n\n", "\n\nProse after.\n"} {
		source := bytes.TrimRight(seeded, "\n")
		source = append(bytes.Clone(source), []byte(suffix)...)
		got, err := ApplyTestsEdits(source, []ExampleEdit{{BeforeID: "new", Delete: true}})
		if err != nil {
			t.Fatal(err)
		}
		p, err := ParseTests("x.agent.test.md", got)
		if err != nil || len(p.Examples) != 0 || strings.Contains(string(got), "agent-test") {
			t.Fatalf("delete EOF %q: %s %v", suffix, got, err)
		}
		if strings.Contains(suffix, "Prose after.") && !bytes.Contains(got, []byte("Prose after.\n")) {
			t.Fatal("deleted prose")
		}
	}
}

func TestApplyTestsEditsRefusesWholeCandidate(t *testing.T) {
	source := []byte("---\nschemaVersion: 1\n---\n\n```yaml agent-test\nid: first\ninput: old\nassertions: [{id: a, type: contains, value: hi}]\n```\n")
	input := "changed"
	for name, edits := range map[string][]ExampleEdit{
		"missing":            {{BeforeID: "first", Input: &input}, {BeforeID: "missing", Delete: true}},
		"duplicate":          {{BeforeID: "first", Input: &input}, {BeforeID: "first", Input: &input}},
		"delete fields":      {{BeforeID: "first", Delete: true, Input: &input}},
		"invalid assertions": {{BeforeID: "first", Assertions: json.RawMessage(`[{"id":"a","type":"not-a-check"}]`)}},
		"duplicate new id":   {{ID: "first", Input: &input, Assertions: json.RawMessage(`[{"id":"a","type":"contains","value":"x"}]`)}},
		"incomplete new":     {{ID: "new", Input: &input}},
		"empty":              {},
	} {
		t.Run(name, func(t *testing.T) {
			before := bytes.Clone(source)
			got, err := ApplyTestsEdits(source, edits)
			if err == nil || got != nil {
				t.Fatalf("accepted %s: %s", name, got)
			}
			if !bytes.Equal(source, before) {
				t.Fatal("mutated caller bytes")
			}
		})
	}
}
