package agentfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func titleSource(value string) string {
	return strings.Replace(testsStarter, "id: greeting\n", "id: greeting\ntitle: "+value+"\n", 1)
}

func TestParseTestTitles(t *testing.T) {
	for _, title := range []string{"Says which kubectl version it's for", "  é 🙂  ", strings.Repeat("🙂", 160)} {
		raw, _ := json.Marshal(title)
		suite, err := ParseTests("g.agent.test.md", []byte(titleSource(string(raw))))
		if err != nil {
			t.Fatalf("valid title rejected: %v", err)
		}
		if suite.Examples[0].Title != title {
			t.Fatalf("title lost: %q", suite.Examples[0].Title)
		}
	}
	for _, value := range []string{`""`, `"   "`, "null", "123", "true", "[name]", `"a\nb"`, `"a\tb"`, `"a\u001bb"`, `"a\u202eb"`, `"a\u200bb"`, `"a\u2028b"`, `"a\u2029b"`, `"` + strings.Repeat("é", 161) + `"`} {
		_, err := ParseTests("g.agent.test.md", []byte(titleSource(value)))
		var detail *Error
		if !errors.As(err, &detail) || !strings.Contains(strings.ToLower(detail.Problem), "title") {
			t.Errorf("invalid title %q needs a clear title error: %v", value, err)
		}
	}
	if _, err := ParseTests("g.agent.test.md", []byte(testsStarter)); err != nil {
		t.Fatal(err)
	}
}

func TestTestTitleDoesNotWidenInlineOrSquad(t *testing.T) {
	inline := "---\nname: greeter\nmodel: fake\ntools: []\n---\nSay hello.\n\n## Examples\n\n```yaml\nexamples:\n  - id: greeting\n    title: A name\n    input: Ada\n    assertions: [{id: check, type: contains, value: Ada}]\n```\n"
	if _, err := Parse("g.agent.md", []byte(inline)); err == nil {
		t.Fatal("inline parser accepted test title")
	}
	raw, profile, path := squadFixture(t, false)
	source := bytes.Replace(enrichedSquad(raw), []byte("  - id: greeting\n"), []byte("  - id: greeting\n    title: A name\n"), 1)
	if _, err := ParseSquad(path, source, profile); err == nil {
		t.Fatal("Squad parser accepted test title")
	}
}

func TestTestTitlePreservedByEditsAndRecordings(t *testing.T) {
	const titleLine = "title: '  Says hello 🙂  ' # keep this spelling\n"
	source := strings.Replace(testsStarter, "id: greeting\n", "id: greeting\n"+titleLine, 1)
	input := "new input"
	edited, err := ApplyTestsEdits([]byte(source), []ExampleEdit{{BeforeID: "greeting", ID: "renamed", Input: &input, Assertions: json.RawMessage(`[{"id":"new-check","type":"contains","value":"new"}]`)}})
	if err != nil || !bytes.Contains(edited, []byte(titleLine)) {
		t.Fatalf("edit lost title: %v\n%s", err, edited)
	}
	pending := strings.Replace(testsHeader+recordingTestFence("greeting", testPendingTool), "id: greeting\n", "id: greeting\n"+titleLine, 1)
	saved, err := ApplyTestRecordings([]byte(pending), []Recording{readRecording("greeting", nil)})
	if err != nil || !bytes.Contains(saved, []byte(titleLine)) {
		t.Fatalf("recording lost title: %v\n%s", err, saved)
	}
	before, err := ParseTests("g.agent.test.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseTests("g.agent.test.md", []byte(strings.Replace(source, "Says hello", "Another name", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(projectJSON(projectExamples(before.Examples)), projectJSON(projectExamples(after.Examples))) {
		t.Fatal("authored comparison ignored changed title")
	}
}
