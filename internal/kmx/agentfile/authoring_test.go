package agentfile

import (
	"errors"
	"strings"
	"testing"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

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
