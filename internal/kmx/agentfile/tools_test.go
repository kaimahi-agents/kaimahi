package agentfile

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestToolsCanonicalAndLegacy(t *testing.T) {
	source := sample(t)
	for _, newline := range []string{"\n", "\r\n"} {
		for _, tools := range []bool{false, true} {
			input := source
			if tools {
				input = strings.Replace(input, "## Examples", "## Tools\n- github_list_merged_prs\n\n## Examples", 1)
			}
			data := []byte(strings.ReplaceAll(input, "\n", newline))
			a, err := Parse("greeter.agent.md", data)
			if err != nil {
				t.Fatal(err)
			}
			if a.Instructions != strings.Split(source, "## Examples")[0] {
				t.Fatal("tool-free instruction bytes changed or declaration leaked")
			}
			if a.Revision != fmt.Sprintf("sha256:%x", sha256.Sum256(data)) {
				t.Fatal("revision lost exact source bytes")
			}
			var want []string
			if tools {
				want = []string{"github_list_merged_prs"}
			}
			if !reflect.DeepEqual(a.Tools, want) {
				t.Fatalf("tools = %v", a.Tools)
			}
		}
	}
}

func TestToolsRefusals(t *testing.T) {
	source := sample(t)
	for _, body := range []string{"", "- unknown", "- github_list_merged_prs\n- github_list_merged_prs", "github_list_merged_prs", " - github_list_merged_prs", "- github_list_merged_prs ", "```\n- github_list_merged_prs\n```", "- github_list_merged_prs\nextra"} {
		t.Run(body, func(t *testing.T) {
			input := strings.Replace(source, "## Examples", "## Tools\n"+body+"\n\n## Examples", 1)
			if _, err := Parse("greeter.agent.md", []byte(input)); err == nil {
				t.Fatal("accepted malformed declaration")
			}
		})
	}
	for _, input := range []string{
		strings.Replace(source, "## Model", "## Tools\n- github_list_merged_prs\n\n## Model", 1),
		source + "\n## Tools\n- github_list_merged_prs\n",
		strings.Replace(source, "## Examples", "## Tools\n- github_list_merged_prs\n\n## Tools\n- github_list_merged_prs\n\n## Examples", 1),
		strings.Replace(source, "## Examples", "## Tools\n- github_list_merged_prs\n", 1),
	} {
		if _, err := Parse("greeter.agent.md", []byte(input)); err == nil {
			t.Fatal("accepted misplaced/repeated Tools or missing Examples")
		}
	}
}

func TestToolsNativeSquadRemainsToolFree(t *testing.T) {
	raw, profile, filename := squadFixture(t, false)
	data := enrichedSquad(raw)
	if _, err := ParseSquad(filename, data, profile); err != nil {
		t.Fatal(err)
	}
	input := strings.Replace(string(data), "## Examples", "## Tools\n- github_list_merged_prs\n\n## Examples", 1)
	if _, err := ParseSquad(filename, []byte(input), profile); err == nil {
		t.Fatal("native Squad accepted tools")
	}
}
