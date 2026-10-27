package agentfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
)

func sample(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile("testdata/greeter.agent.md")
	if err != nil {
		t.Fatal(err)
	}
	// Git may check out Markdown as CRLF on Windows. Test edits use LF;
	// TestParseCanonical independently generates and checks both encodings.
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestParseCanonical(t *testing.T) {
	source := sample(t)
	for _, newline := range []string{"\n", "\r\n"} {
		data := []byte(strings.ReplaceAll(source, "\n", newline))
		a, err := Parse("a path/greeter.agent.md", data)
		if err != nil {
			t.Fatal(err)
		}
		if a.Identity.ID != "greeter" || a.Identity.Purpose != "Return a short greeting for the supplied name." || a.Title != "Greeter — Greeting specialist" || a.Model != "fake" {
			t.Fatalf("wrong metadata: %+v", a)
		}
		if a.Instructions != strings.Split(source, "## Examples")[0] {
			t.Fatal("instructions changed or include examples")
		}
		if a.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) {
			t.Fatal("revision is not exact bytes")
		}
		if len(a.Examples) != 1 || a.Examples[0].ID != "greeting" || a.Examples[0].Input != "Ada" {
			t.Fatalf("wrong examples: %+v", a.Examples)
		}
		e := a.Examples[0]
		if e.Location.Line != 22 || e.Location.Column != 5 || e.AssertionLocations[0].Line != 25 {
			t.Fatalf("wrong source locations: %+v", e)
		}
		if a.Locations["Model.Preferred"].Line != 17 || a.Locations["Identity.ID"].Line != 4 {
			t.Fatal(a.Locations)
		}
		if err := runtime.ValidateEvaluationAssertions(e.Assertions); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOrderedExamplesAndAssertionSemantics(t *testing.T) {
	source := sample(t)
	source = strings.Replace(source, "        value: Hello, Ada\n", "        value: Hello, Ada\n      - id: absent\n        type: notContains\n        value: Goodbye\n      - id: shape\n        type: regex\n        pattern: '^Hello, '\n      - id: judge\n        type: llmJudge\n        rubric: Is this a greeting?\n        minScore: 80\n        judgeModel: exact-model\n        judgeVersion: v1\n  - id: second\n    input: Grace\n    assertions:\n      - id: greeting-text\n        type: contains\n        value: Grace\n", 1)
	a, err := Parse("greeter.agent.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Examples) != 2 || a.Examples[0].ID != "greeting" || a.Examples[1].ID != "second" || len(a.Examples[0].Assertions) != 4 || a.Examples[0].Assertions[3].Type != "llmJudge" {
		t.Fatalf("order/definitions lost: %+v", a.Examples)
	}
}

func TestParseRefusals(t *testing.T) {
	original := sample(t)
	replace := func(old, value string) string { return strings.Replace(original, old, value, 1) }
	rows := []struct{ name, source, field string }{
		{"blank prompt", "\n", "Instructions"},
		{"missing purpose", replace("- **Purpose:** Return a short greeting for the supplied name.", ""), "Identity.Purpose"},
		{"blank id", replace("**ID:** greeter", "**ID:** "), "Identity.ID"},
		{"unsafe id", replace("**ID:** greeter", "**ID:** ../greeter"), "Identity.ID"},
		{"missing title", replace("# Greeter — Greeting specialist", ""), "Title"},
		{"unclosed frontmatter", "---\nname: greeter\n" + original, "Frontmatter"},
		{"missing preferred", replace("- **Preferred:** fake", ""), "Model.Preferred"},
		{"blank model", replace("**Preferred:** fake", "**Preferred:** "), "Model.Preferred"},
		{"auto model", replace("**Preferred:** fake", "**Preferred:** auto"), "Model.Preferred"},
		{"wildcard model", replace("**Preferred:** fake", "**Preferred:** gpt-*"), "Model.Preferred"},
		{"endpoint model", replace("**Preferred:** fake", "**Preferred:** https://example.invalid"), "Model.Preferred"},
		{"model fallback", replace("**Preferred:** fake", "**Preferred:** fake\n- **Fallback:** other"), "Model"},
		{"duplicate identity", replace("## Model", "## Identity\n- **ID:** other\n\n## Model"), "Identity"},
		{"duplicate field", replace("**ID:** greeter", "**ID:** greeter\n- **ID:** other"), "Identity"},
		{"empty responsibility", replace("Reply with exactly `Hello, ` followed by the input name.", ""), "Accountable Responsibilities"},
		{"unknown section", replace("## Model", "## Tools\nexec: shell\n\n## Model"), "Sections"},
		{"trailing section", original + "\n## Model\n", "Examples"},
		{"trailing text", original + "\nignored instructions\n", "Examples"},
		{"unclosed fence", strings.TrimSuffix(original, "```\n"), "Examples"},
		{"mismatched fence", strings.TrimSuffix(original, "```\n") + "~~~\n", "Examples"},
		{"no yaml fence", replace("```yaml", "```json"), "Examples"},
		{"null examples", strings.Split(original, "```yaml")[0] + "```yaml\nexamples:\n```\n", "Examples"},
		{"mapping examples", strings.Split(original, "```yaml")[0] + "```yaml\nexamples: {}\n```\n", "Examples"},
		{"empty list with text", strings.Split(original, "```yaml")[0] + "```yaml\nexamples: []\n```\nextra\n", "Examples"},
		{"bad yaml", replace("examples:", "examples: ["), "Examples"},
		{"second document", replace("examples:", "---\n---\nexamples:"), "Examples"},
		{"unknown field", replace("input: Ada", "input: Ada\n    private-canary: no"), "Examples"},
		{"duplicate key", replace("input: Ada", "input: Ada\n    input: Grace"), "Examples"},
		{"null example", strings.Split(original, "```yaml")[0] + "```yaml\nexamples: [null]\n```\n", "Examples"},
		{"numeric input", replace("input: Ada", "input: 123"), "Examples"},
		{"blank input", replace("input: Ada", "input: '  '"), "Examples"},
		{"missing input", replace("    input: Ada\n", ""), "Examples"},
		{"empty assertions", replace("assertions:\n      - id: greeting-text\n        type: contains\n        value: Hello, Ada", "assertions: []"), "Examples"},
		{"null assertion", replace("      - id: greeting-text\n        type: contains\n        value: Hello, Ada", "      - null"), "Examples"},
		{"duplicate example id", replace("```\n", "  - id: greeting\n    input: Grace\n    assertions: [{id: check, type: contains, value: Grace}]\n```\n"), "Examples"},
		{"blank example id", replace("id: greeting\n", "id: ''\n"), "Examples"},
		{"unsafe example id", replace("id: greeting\n", "id: '../bad'\n"), "Examples"},
		{"unknown assertion key", replace("value: Hello, Ada", "value: Hello, Ada\n        private-canary: no"), "Examples"},
		{"duplicate assertion id", strings.ReplaceAll(replace("        value: Hello, Ada", "        value: Hello, Ada\n      - id: greeting-text\n        type: contains\n        value: hello"), "greeting-text", "private-canary"), "Examples"},
		{"tool assertion", replace("type: contains\n        value: Hello, Ada", "type: toolCalled\n        tool: shell"), "Examples"},
		{"tool field", replace("value: Hello, Ada", "value: Hello, Ada\n        tool: shell"), "Examples"},
		{"bad regex", replace("type: contains\n        value: Hello, Ada", "type: regex\n        pattern: '[private-canary'"), "Examples"},
		{"judge incomplete", replace("type: contains\n        value: Hello, Ada", "type: llmJudge\n        rubric: hello"), "Examples"},
		{"anchor", replace("input: Ada", "input: &name Ada"), "Examples"},
		{"undefined alias", replace("input: Ada", "input: *name"), "Examples"},
		{"custom tag", replace("input: Ada", "input: !exec Ada"), "Examples"},
		{"merge", replace("input: Ada", "input: Ada\n    <<: {input: other}"), "Examples"},
		{"oversize file", strings.Repeat(" ", 256*1024+1), "File"},
		{"oversize input", replace("input: Ada", "input: "+strings.Repeat("a", 16*1024+1)), "Examples"},
		{"too deep", replace("input: Ada", "input: "+strings.Repeat("[", 14)+"a"+strings.Repeat("]", 14)), "Examples"},
		{"invalid utf8", original + string([]byte{0xff}), "File"},
		{"bare CR", replace("input: Ada", "input: Ada\rignored"), "File"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			a, err := Parse("test.agent.md", []byte(row.source))
			var refusal *Error
			if err == nil || a != nil || !errors.As(err, &refusal) {
				t.Fatalf("expected typed refusal and nil result, got %+v %v", a, err)
			}
			if !strings.Contains(refusal.Field, row.field) || refusal.Location.File != "test.agent.md" || refusal.Location.Line < 1 || refusal.Location.Column < 1 {
				t.Fatalf("bad refusal: %+v", refusal)
			}
			if strings.Contains(err.Error(), "private-canary") {
				t.Fatalf("error echoes source: %v", err)
			}
		})
	}
	// A charter that names Identity but gives no ID or Purpose still reports both.
	bare := strings.Replace(strings.Replace(original, "- **ID:** greeter\n", "", 1), "- **Purpose:** Return a short greeting for the supplied name.\n", "", 1)
	a, err := Parse("legacy.md", []byte(bare))
	if a != nil || err == nil {
		t.Fatal("charter without ID and Purpose accepted")
	}
	for _, field := range []string{"Identity.ID", "Identity.Purpose"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("missing field not named: %s: %v", field, err)
		}
	}
}

func TestFencedHeadingsAreNotSections(t *testing.T) {
	source := strings.Replace(sample(t), "Reply with exactly `Hello, ` followed by the input name.", "Reply with a greeting.\n\n```text\n## Examples\n## Tools\n```", 1)
	a, err := Parse("a.md", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Instructions, "## Tools") || len(a.Examples) != 1 {
		t.Fatal("fenced heading split instructions")
	}
	source = strings.ReplaceAll(sample(t), "```", "  ~~~~")
	if _, err := Parse("a.md", []byte(source)); err != nil {
		t.Fatal(err)
	}
}

func TestRefusalLocation(t *testing.T) {
	source := strings.Replace(sample(t), "input: Ada", "input: 123", 1)
	for _, sep := range []string{"\n", "\r\n"} {
		_, err := Parse("a.md", []byte(strings.ReplaceAll(source, "\n", sep)))
		var e *Error
		if !errors.As(err, &e) || e.Location.Line != 23 || e.Location.Column != 12 {
			t.Fatalf("bad location: %+v", e)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(sample(f)))
	f.Add([]byte(strings.Split(sample(f), "```yaml")[0] + "```yaml\nexamples: []\n```\n"))
	f.Add([]byte("# Legacy\n"))
	f.Add([]byte(strings.Replace(sample(f), "Reply with exactly `Hello, ` followed by the input name.", "Reply with a greeting.\n\n```text\n## Examples\n## Tools\n```", 1)))
	f.Fuzz(func(t *testing.T, data []byte) {
		a, err := Parse("fuzz.agent.md", data)
		if err != nil && a != nil {
			t.Fatal("partial success")
		}
		if err == nil {
			if a == nil {
				t.Fatal("invalid success")
			}
			lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			line, ok := a.Locations["Examples"]
			if !ok {
				if len(a.Examples) != 0 || strings.TrimSpace(a.Instructions) == "" {
					t.Fatal("tests without a location, or a blank prompt")
				}
				return
			}
			if line.Line < 1 || line.Line > len(lines) || lines[line.Line-1] != "## Examples" {
				t.Fatal("invalid Examples location")
			}
			if _, charter := a.Locations["Identity"]; charter && a.Instructions != strings.Join(lines[:line.Line-1], "\n")+"\n" {
				t.Fatal("instructions differ from the source prefix before Examples")
			}
		}
	})
}

// An explicit empty sequence is a saved zero-test charter: Parse/Load/Inspect
// accept the same bytes and revision without inventing examples.
func TestParseZeroExamplesRoundTrip(t *testing.T) {
	source := strings.Split(sample(t), "```yaml")[0] + "```yaml\nexamples: []\n```\n"
	a, err := Parse("greeter.agent.md", []byte(source))
	if err != nil || a == nil || len(a.Examples) != 0 || a.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(source))) {
		t.Fatalf("zero examples refused or changed: %+v / %v", a, err)
	}
	path := filepath.Join(realTemp(t), "greeter.agent.md")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil || len(loaded.Examples) != 0 || loaded.Revision != a.Revision || loaded.Instructions != a.Instructions {
		t.Fatalf("zero-example load round trip: %+v / %v", loaded, err)
	}
	if inspected, err := Inspect("greeter.agent.md", []byte(source)); err != nil || len(inspected.Examples) != 0 {
		t.Fatalf("inspection refused zero examples: %v", err)
	}
}
