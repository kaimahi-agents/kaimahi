package agentfile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExampleAndAssertionBounds(t *testing.T) {
	prefix := strings.Split(sample(t), "```yaml")[0] + "```yaml\nexamples:\n"
	for _, count := range []int{128, 129} {
		var body strings.Builder
		body.WriteString(prefix)
		for i := range count {
			fmt.Fprintf(&body, "  - id: case-%d\n    input: Ada\n    assertions: [{id: text, type: contains, value: Hello}]\n", i)
		}
		body.WriteString("```\n")
		a, err := Parse("a.md", []byte(body.String()))
		if count == 128 {
			if err != nil || len(a.Examples) != 128 {
				t.Fatalf("maximum examples refused: %v", err)
			}
		} else if err == nil {
			t.Fatal("too many examples accepted")
		}
	}
	for _, count := range []int{64, 65} {
		var body strings.Builder
		body.WriteString(prefix)
		body.WriteString("  - id: case\n    input: Ada\n    assertions:\n")
		for i := range count {
			fmt.Fprintf(&body, "      - {id: text-%d, type: contains, value: Hello}\n", i)
		}
		body.WriteString("```\n")
		a, err := Parse("a.md", []byte(body.String()))
		if count == 64 {
			if err != nil || len(a.Examples[0].Assertions) != 64 {
				t.Fatalf("maximum assertions refused: %v", err)
			}
		} else if err == nil {
			t.Fatal("too many assertions accepted")
		}
	}
}
func TestYAMLNodeBudget(t *testing.T) {
	source := strings.Split(sample(t), "```yaml")[0] + "```yaml\nexamples: [" + strings.Repeat("0,", 32768) + "0]\n```\n"
	_, err := Parse("a.md", []byte(source))
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Problem, "reduce YAML complexity") {
		t.Fatalf("node budget not enforced before schema decode: %+v", e)
	}
}

func TestExactByteLimits(t *testing.T) {
	source := sample(t)
	input := strings.Replace(source, "input: Ada", "input: "+strings.Repeat("a", 16384), 1)
	if _, err := Parse("a.md", []byte(input)); err != nil {
		t.Fatal("maximum input refused:", err)
	}
	source = strings.Replace(source, "Reply with exactly", strings.Repeat("a", MaxFileBytes-len(source))+"Reply with exactly", 1)
	if len(source) != MaxFileBytes {
		t.Fatal("invalid test fixture")
	}
	if _, err := Parse("a.md", []byte(source)); err != nil {
		t.Fatal("maximum file refused:", err)
	}
	if _, err := Parse("a.md", []byte(source+" ")); err == nil {
		t.Fatal("oversize file accepted")
	}
}
