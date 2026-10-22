package agentfile

import (
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

func TestAuthoringRefusalRepairs(t *testing.T) {
	source := sample(t)
	rows := []struct{ source, field, repair string }{
		{"\uFEFF" + source, "File", "remove the UTF-8 byte-order mark"},
		{strings.Replace(source, "## Identity", "## Identity \t", 1), "Sections", "trailing spaces or tabs"},
	}
	for _, escape := range []string{`\e`, `\0`, `\r`, `\u2028`, `\u2029`} {
		rows = append(rows, struct{ source, field, repair string }{strings.Replace(source, "input: Ada", `input: "Ada`+escape+`Name"`, 1), "Examples", "control characters"})
	}
	for _, row := range rows {
		a, err := Parse("a.md", []byte(row.source))
		var refusal *Error
		if a != nil || !errors.As(err, &refusal) || refusal.Field != row.field || !strings.Contains(refusal.Problem, row.repair) {
			t.Errorf("expected %s repair: %v", row.field, err)
		}
	}
	valid := strings.Replace(source, "input: Ada", `input: "Ada\nName\tX"`, 1)
	if _, err := Parse("a.md", []byte(valid)); err != nil {
		t.Fatal(err)
	}
}

func TestDecodedAssertionOperandControls(t *testing.T) {
	for _, row := range []struct{ name, assertion string }{
		{"pattern NUL", `type: regex
        pattern: "\0"`},
		{"pattern escape", `type: regex
        pattern: "a\eb"`},
		{"value line separator", `type: contains
        value: "Hello\u2028Ada"`},
		{"rubric line separator", `type: llmJudge
        rubric: "x\u2028y"
        minScore: 80
        judgeModel: exact-model
        judgeVersion: v1`},
	} {
		t.Run(row.name, func(t *testing.T) {
			source := strings.Replace(sample(t), "type: contains\n        value: Hello, Ada", row.assertion, 1)
			a, err := Parse("a.md", []byte(source))
			var refusal *Error
			if a != nil || !errors.As(err, &refusal) || refusal.Field != "Examples" || refusal.Location.Line != 25 || refusal.Problem != "remove control characters except newline/tab and use LF or CRLF line breaks" {
				t.Fatalf("expected located, redacted control-character repair: %v", err)
			}
		})
	}
	for _, assertion := range []string{
		`type: regex
        pattern: "Hello\nAda\tX"`,
		`type: llmJudge
        rubric: "Hello\nAda\tX"
        minScore: 80
        judgeModel: exact-model
        judgeVersion: v1`,
	} {
		source := strings.Replace(sample(t), "type: contains\n        value: Hello, Ada", assertion, 1)
		a, err := Parse("a.md", []byte(source))
		if err != nil {
			t.Fatalf("newline/tab operand refused: %v", err)
		}
		operand := a.Examples[0].Assertions[0]
		if operand.Pattern+operand.Rubric != "Hello\nAda\tX" {
			t.Fatal("newline/tab operand was changed")
		}
	}
}

func TestInstructionCredentialRefusalLocationAndRepair(t *testing.T) {
	for _, row := range []struct {
		name, old, prefix string
		line              int
	}{
		{"line start", "Reply with exactly `Hello, ` followed by the input name.", "", 8},
		{"inline", "Reply with exactly `Hello, ` followed by the input name.", "Key: ", 8},
		{"file start", "# Greeter — Greeting specialist", "# ", 1},
	} {
		t.Run(row.name, func(t *testing.T) {
			credential := strings.Repeat("a1b2", 8)
			source := strings.Replace(sample(t), row.old, row.prefix+credential, 1)
			for _, newline := range []string{"\n", "\r\n"} {
				a, err := Parse("a.md", []byte(strings.ReplaceAll(source, "\n", newline)))
				var refusal *Error
				if a != nil || !errors.As(err, &refusal) || refusal.Field != "File" || refusal.Location.Line != row.line || refusal.Location.Column != 1 {
					t.Fatalf("expected credential refusal on line %d: %v", row.line, err)
				}
				if !strings.Contains(refusal.Problem, "remove credential-shaped content") || strings.Contains(refusal.Problem, "Unicode") || strings.Contains(err.Error(), credential) {
					t.Fatal("credential refusal must give a redacted removal repair")
				}
			}
		})
	}
}

func TestWholeCharterRefusesCredentialComments(t *testing.T) {
	for _, shape := range secretshapes.All() {
		for _, row := range []struct {
			name, old, replacement string
			line                   int
		}{
			{"input comment", "input: Ada", "input: Ada # ", 23},
			{"assertion comment", "value: Hello, Ada", "value: Hello, Ada # ", 27},
			{"standalone comment", "examples:", "# ", 21},
		} {
			t.Run(shape.Name+"/"+row.name, func(t *testing.T) {
				replacement := row.replacement + shape.Example
				if row.name == "standalone comment" {
					replacement += "\nexamples:"
				}
				source := strings.Replace(sample(t), row.old, replacement, 1)
				for _, newline := range []string{"\n", "\r\n"} {
					a, err := Parse("a.md", []byte(strings.ReplaceAll(source, "\n", newline)))
					var refusal *Error
					if a != nil || !errors.As(err, &refusal) || refusal.Field != "File" || refusal.Location.Line != row.line || !strings.Contains(refusal.Problem, "remove credential-shaped content") {
						t.Fatalf("expected redacted credential repair on line %d", row.line)
					}
					if strings.Contains(err.Error(), shape.Example) {
						t.Fatal("credential echoed in refusal")
					}
				}
			})
		}
	}
}

func TestPreludeFencesRefuse(t *testing.T) {
	for _, fence := range []string{"```text", "~~~text", "   ````text"} {
		source := strings.Replace(sample(t), "## Identity", fence+"\n## Tools\n"+strings.TrimSuffix(fence, "text")+"\n## Identity", 1)
		a, err := Parse("a.md", []byte(source))
		var refusal *Error
		if a != nil || !errors.As(err, &refusal) || refusal.Field != "Sections" || refusal.Location.Line != 3 || !strings.Contains(refusal.Problem, "put instructions inside") {
			t.Fatalf("expected located prelude refusal: %v", err)
		}
	}
}

func TestTitleRoleIsOptional(t *testing.T) {
	for _, title := range []string{"Greeter", "Greeter —", "Greeter — Greeting specialist"} {
		source := strings.Replace(sample(t), "# Greeter — Greeting specialist", "# "+title, 1)
		a, err := Parse("a.md", []byte(source))
		if err != nil || a.Title != title {
			t.Fatalf("optional role refused or title changed: %v", err)
		}
	}
}

func TestInstructionPrefixRefusesKnownCredentials(t *testing.T) {
	for _, shape := range secretshapes.All() {
		for _, field := range []struct{ name, old, prefix string }{
			{"model", "**Preferred:** fake", "**Preferred:** "},
			{"purpose", "Return a short greeting for the supplied name.", ""},
			{"title", "# Greeter — Greeting specialist", "# "},
			{"responsibilities", "Reply with exactly `Hello, ` followed by the input name.", ""},
		} {
			t.Run(shape.Name+"/"+field.name, func(t *testing.T) {
				source := strings.Replace(sample(t), field.old, field.prefix+shape.Example, 1)
				a, err := Parse("a.md", []byte(source))
				var refusal *Error
				if a != nil || !errors.As(err, &refusal) || !strings.Contains(refusal.Problem, "credential-shaped content") {
					t.Fatal("known credential was not refused with a safe diagnostic")
				}
				if strings.Contains(err.Error(), shape.Example) {
					t.Fatal("credential echoed in refusal")
				}
			})
		}
	}
}
