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
	if _, err := Parse("greeter.agent.md", []byte(minimal)); err == nil {
		t.Fatal("read-only inspection must not enable minimal execution")
	}
	a, err := Inspect(".squad/agents/greeter/charter.md", []byte(canonical))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(".squad/agents/greeter/charter.md", []byte(canonical))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("canonical interpretation changed")
	}
	for _, source := range []string{strings.Replace(minimal, "## Model", "## Models", 1), strings.Replace(minimal, "**Preferred:** fake", "**Preferred:** auto", 1), strings.Replace(minimal, "Reply with exactly `Hello, ` followed by the input name.", "", 1), strings.Replace(minimal, "## Examples", "", 1)} {
		if _, err := Inspect("greeter.agent.md", []byte(source)); err == nil {
			t.Fatal("invalid minimal shape accepted")
		}
	}
}
