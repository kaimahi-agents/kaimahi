package agentfile

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestInspectMinimalAndCanonical(t *testing.T) {
	canonical := sample(t)
	minimal := "# Greeter\n\nReply with exactly `Hello, ` followed by the input name.\n\n## Model\n- **Preferred:** fake\n\n## Examples" + strings.Split(canonical, "## Examples")[1]
	for _, source := range []string{minimal, canonical} {
		for _, ending := range []string{"\n", "\r\n"} {
			b := []byte(strings.ReplaceAll(source, "\n", ending))
			a, err := Inspect("greeter.agent.md", b)
			if err != nil {
				t.Fatal(err)
			}
			if a.Identity.ID != "greeter" || a.Model != "fake" || len(a.Examples) != 1 || a.Examples[0].Assertions[0].Value != "Hello, Ada" {
				t.Fatalf("wrong view: %+v", a)
			}
			if a.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(b)) || strings.Contains(a.Instructions, "examples:") {
				t.Fatal("view lost byte identity or included examples in instructions")
			}
		}
	}
	a, err := Inspect(".squad/agents/greeter/charter.md", []byte(canonical))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(".squad/agents/greeter/charter.md", []byte(canonical))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("canonical interpretation changed")
	}
	// Examples and the model are optional; a malformed control is still refused.
	if _, err := Inspect("greeter.agent.md", []byte(strings.Replace(minimal, "**Preferred:** fake", "**Preferred:** auto", 1))); err == nil {
		t.Fatal("invalid minimal model accepted")
	}
	for _, source := range []string{strings.Replace(minimal, "## Model", "## Models", 1), strings.Split(minimal, "## Examples")[0]} {
		if _, err := Parse("greeter.agent.md", []byte(source)); err != nil {
			t.Fatal(err)
		}
	}
}
