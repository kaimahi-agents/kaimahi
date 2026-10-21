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
