package agentfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestRefuseCredentialShapedExampleInput(t *testing.T) {
	value := secretshapes.All()[0].Example
	source := strings.Replace(sample(t), "input: Ada", "input: '"+value+"'", 1)
	_, err := Parse("a.md", []byte(source))
	var e *Error
	if !errors.As(err, &e) || e.Location.Line != 23 || strings.Contains(err.Error(), value) {
		t.Fatal("credential input was accepted or disclosed")
	}
}
func TestSquadPathNormalization(t *testing.T) {
	source := []byte(sample(t))
	root := realTemp(t)
	for _, name := range []string{".squad/agents/wrong/./charter.md", ".SQUAD/agents/wrong/charter.md", ".squad/agents/wrong/../wrong/charter.md"} {
		// Do not use filepath.Join here: the parser must clean the spelling.
		path := root + string(filepath.Separator) + name
		if a, err := Parse(path, source); err == nil || a != nil {
			t.Fatalf("accepted wrong Squad ID: %s", name)
		}
	}
	dir := filepath.Join(root, ".squad/agents/wrong")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if a, err := Parse("charter.md", source); err == nil || a != nil {
		t.Fatal("relative basename bypassed Squad ID")
	}
}
func TestUnicodeAndTrailingTextLocations(t *testing.T) {
	source := strings.Replace(sample(t), "    input: Ada", "    input: éé\n    unsupported: x", 1)
	_, err := Parse("a.md", []byte(source))
	var e *Error
	if !errors.As(err, &e) || e.Location.Line != 24 || e.Location.Column != 5 {
		t.Fatalf("wrong location: %+v", e)
	}
	source = strings.Replace(sample(t), "    input: Ada", "    input: 'Ada\u2028Grace'", 1)
	if _, err := Parse("a.md", []byte(source)); err == nil {
		t.Fatal("Unicode line separator accepted")
	}
	source = strings.Replace(sample(t), "    input: Ada", "    input: 'Ada\u2029Grace'", 1)
	if _, err := Parse("a.md", []byte(source)); err == nil {
		t.Fatal("Unicode paragraph separator accepted")
	}
	source = sample(t) + "\n\n\nlate\n"
	_, err = Parse("a.md", []byte(source))
	if !errors.As(err, &e) || e.Location.Line != 32 {
		t.Fatalf("wrong trailing-text line: %+v", e)
	}
	source = strings.Split(sample(t), "```yaml")[0] + "```yaml\nexamples: [{id: greeting, input: éé, unsupported: x, assertions: []}]\n```\n"
	_, err = Parse("a.md", []byte(source))
	// Derive the byte offset from the original line, not YAML's rune column.
	column := strings.Index(strings.Split(source, "\n")[20], "unsupported") + 1
	if !errors.As(err, &e) || e.Location.Line != 21 || e.Location.Column != column {
		t.Fatalf("wrong UTF-8 byte column, expected %d: %+v", column, e)
	}
}
func TestSpecificStructuralRefusals(t *testing.T) {
	source := sample(t)
	rows := []struct {
		old, new, problem string
		line              int
	}{
		{"assertions:\n      - id: greeting-text\n        type: contains\n        value: Hello, Ada", "assertions: []", "provide 1–64 assertions", 24},
		{"input: Ada", "input: *undefined", "provide valid YAML", 21},
		{"input: Ada", "input: &name Ada", "remove YAML anchors and aliases", 23},
	}
	for _, row := range rows {
		_, err := Parse("a.md", []byte(strings.Replace(source, row.old, row.new, 1)))
		var e *Error
		if !errors.As(err, &e) || e.Location.Line != row.line || !strings.Contains(e.Problem, row.problem) {
			t.Fatalf("wrong refusal for %q: %+v", row.new, e)
		}
	}
}

func TestBlockScalarAtEndOfExamples(t *testing.T) {
	prefix := strings.Split(sample(t), "```yaml")[0] + "```yaml\nexamples:\n  - id: greeting\n"
	inputLast := "    assertions: [{id: text, type: contains, value: Hello}]\n    input: |\n      Ada\n```\n"
	valueLast := "    input: Ada\n    assertions:\n      - id: text\n        type: contains\n        value: |\n          Hello\n```\n"
	for _, newline := range []string{"\n", "\r\n"} {
		t.Run(newline, func(t *testing.T) {
			a, err := Parse("a.md", []byte(strings.ReplaceAll(prefix+inputLast, "\n", newline)))
			if err != nil {
				t.Fatal(err)
			}
			if a.Examples[0].Input != "Ada\n" {
				t.Errorf("lost authored block-scalar newline: %q", a.Examples[0].Input)
			}
			var refusal *Error
			a, err = Parse("a.md", []byte(strings.ReplaceAll(prefix+valueLast, "\n", newline)))
			if a != nil || !errors.As(err, &refusal) || refusal.Field != "Examples" || !strings.Contains(refusal.Problem, "valid assertion") {
				t.Fatalf("multiline assertion bypassed shared validation: %v", err)
			}
			// Explicit strip chomping still authors a valid single-line value.
			if _, err := Parse("a.md", []byte(strings.Replace(prefix+valueLast, "value: |", "value: |-", 1))); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHTMLCommentInTitleRefuses(t *testing.T) {
	source := strings.Replace(sample(t), "# Greeter — Greeting specialist", "# Greeter <!--", 1)
	a, err := Parse("a.md", []byte(source))
	var refusal *Error
	if a != nil || !errors.As(err, &refusal) || refusal.Location.Line != 1 || !strings.Contains(refusal.Problem, "HTML comments") {
		t.Fatalf("title comment bypassed guard: %v", err)
	}
}

func TestHTMLCommentRefusalOutsideFences(t *testing.T) {
	source := strings.Replace(sample(t), "Reply with exactly", "<!--\n```\n-->\n## Tools\n```\nReply with exactly", 1)
	if a, err := Parse("a.md", []byte(source)); err == nil || a != nil {
		t.Fatal("HTML comment changed fence interpretation")
	}
}
