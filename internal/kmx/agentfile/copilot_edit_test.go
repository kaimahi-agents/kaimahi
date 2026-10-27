package agentfile

import (
	"strconv"
	"strings"
	"testing"
)

func firstTest() ExampleEdit {
	input := "Ada"
	return ExampleEdit{ID: "reply", Input: &input, Assertions: []byte(`[{"id":"text","type":"contains","value":"Ada"}]`)}
}

func TestAddFirstTestAppendsOnlyExamples(t *testing.T) {
	for _, source := range []string{
		"---\n# keep\ndescription: d   \nx-unknown: [1, 2]\n---\nPrompt\n\n## Heading\n\n```text\nfenced\n```\n",
		"Plain prompt without a trailing newline",
		"---\ndescription: d\n---\nPrompt\n\n## Examples\n\nProse, not tests.\n",
	} {
		for _, eol := range []string{"\n", "\r\n"} {
			source := strings.ReplaceAll(source, "\n", eol)
			got, err := ApplyExampleEdits([]byte(source), []ExampleEdit{firstTest()})
			if err != nil {
				t.Fatalf("%q: %v", source, err)
			}
			if !strings.HasPrefix(string(got), source) && !strings.HasPrefix(string(got), source+eol) {
				t.Fatalf("prefix changed:\n%q\n%q", source, got)
			}
			a, err := Parse("x.agent.md", got)
			if err != nil || len(a.Examples) != 1 || a.Examples[0].ID != "reply" {
				t.Fatalf("%v %+v\n%s", err, a, got)
			}
			if strings.Contains(source, "\r\n") && strings.Count(string(got), "\r\n") != strings.Count(string(got), "\n") {
				t.Fatalf("mixed line endings: %q", got)
			}
		}
	}
	edit := firstTest()
	edit.ID = "kmx-first-test-placeholder"
	if got, err := ApplyExampleEdits([]byte("Prompt\n"), []ExampleEdit{edit}); err != nil {
		t.Fatalf("valid example ID was reserved internally: %v (%s)", err, got)
	}
	// An unclosed prompt fence would swallow the appended section: refuse, never rewrite.
	if _, err := ApplyExampleEdits([]byte("Prompt\n\n```text\nopen\n"), []ExampleEdit{firstTest()}); err == nil {
		t.Fatal("section appended inside an open fence")
	}
	// Editing the prompt of a file with no tests never adds the section.
	if _, err := ApplyExampleEdits([]byte("Prompt\n"), []ExampleEdit{{BeforeID: "x", Delete: true}}); err == nil {
		t.Fatal("edit of absent example accepted")
	}
	// Fixtures cannot be recorded before any test exists.
	if _, err := ApplyRecordings([]byte("Prompt\n"), []Recording{readRecording("reply", nil)}); err == nil {
		t.Fatal("recording applied to a file without Examples")
	}
	if got := RecordingDetails([]byte("---\ndescription: d\n---\nPrompt\n")); got != nil {
		t.Fatalf("details = %v", got)
	}
}

func TestAddFirstMaxExamples(t *testing.T) {
	edits := make([]ExampleEdit, MaxExamples)
	for i := range edits {
		edits[i] = firstTest()
		edits[i].ID = "test-" + strconv.Itoa(i)
	}
	got, err := ApplyExampleEdits([]byte("Prompt\n"), edits)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Parse("x.agent.md", got)
	if err != nil || len(a.Examples) != MaxExamples {
		t.Fatalf("first save lost the example limit: %v", err)
	}
}

func TestEditExistingExamplesInFreeFormFile(t *testing.T) {
	source := "---\ndescription: d\n---\nPrompt\n\n" + copilotExamples
	input := "Grace"
	got, err := ApplyExampleEdits([]byte(source), []ExampleEdit{{BeforeID: "reply", Input: &input}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "---\ndescription: d\n---\nPrompt\n\n## Examples") || !strings.Contains(string(got), "input: Grace") {
		t.Fatalf("%s", got)
	}
	if details := RecordingDetails([]byte(source)); len(details) != 0 {
		t.Fatalf("%v", details)
	}
}
