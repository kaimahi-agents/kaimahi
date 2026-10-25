package agentfile

import (
	"fmt"
	"strings"
	"testing"
)

func TestToolsNamesOnlyBoundedSet(t *testing.T) {
	source := sample(t)
	for _, count := range []int{1, 2, 32, 33} {
		names := ""
		for i := 0; i < count; i++ {
			names += fmt.Sprintf("- tool_%d\n", i)
		}
		a, err := Parse("greeter.agent.md", []byte(strings.Replace(source, "## Examples", "## Tools\n"+names+"\n## Examples", 1)))
		if count <= 32 {
			if err != nil || len(a.Tools) != count {
				t.Fatalf("%d names: %v", count, err)
			}
		} else if err == nil {
			t.Fatal("accepted 33 declarations")
		}
	}
	for _, name := range []string{"server.read", "tool extra", "tool: {}", "https://example.test", "tool/token", "tool\n- tool", strings.Repeat("x", 65)} {
		if _, err := Parse("greeter.agent.md", []byte(strings.Replace(source, "## Examples", "## Tools\n- "+name+"\n\n## Examples", 1))); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
