package localtools

import (
	"strings"
	"testing"
)

const launchYAML = `version: 1
mcpServers:
  notes:
    command: node
    args: [server.js, ""]
    env: {AUTH: NOTES_TOKEN}
    tools: {ping: remote_ping}
  other:
    command: other
    tools: {lookup: remote_lookup}
liveToolServers: [notes]
`

func TestLaunchConfigSelection(t *testing.T) {
	for _, tc := range []struct {
		sel  ServerSelection
		want string
	}{
		{ServerSelection{}, "notes"},
		{ServerSelection{Names: []string{"other"}, Override: true}, "other"},
		{ServerSelection{Names: []string{"missing"}, Override: true, Clear: true}, ""},
	} {
		defs, err := parseLaunchConfig([]byte(launchYAML), tc.sel)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if len(defs) != 0 {
				t.Fatal("clear selected a server")
			}
		} else if len(defs) != 1 || defs[0].name != tc.want {
			t.Fatalf("selection: %+v", defs)
		}
	}
	defs, err := parseLaunchConfig([]byte(launchYAML), ServerSelection{})
	if err != nil || defs[0].env["AUTH"] != "NOTES_TOKEN" || len(defs[0].args) != 2 || defs[0].tools["ping"] != "remote_ping" {
		t.Fatalf("schema: %+v %v", defs, err)
	}
	for _, raw := range []string{"version: 1\nmcpServers: nonsense\n", "version: 1\n"} {
		defs, err := parseLaunchConfig([]byte(raw), ServerSelection{})
		if err != nil || len(defs) != 0 {
			t.Fatal("unselected definitions had effects", err)
		}
	}
}

func TestLaunchConfigRejectsAmbiguousOrInvalid(t *testing.T) {
	for _, raw := range []string{
		strings.Replace(launchYAML, "command: node", "command: 1", 1),
		strings.Replace(launchYAML, "args: [server.js, \"\"]", "args: [true]", 1),
		strings.Replace(launchYAML, "AUTH: NOTES_TOKEN", "AUTH: literal-value", 1),
		strings.Replace(launchYAML, "command: node", "command: node\n    shell: true", 1),
		strings.Replace(launchYAML, "tools: {ping: remote_ping}", "tools: {}", 1),
		strings.Replace(launchYAML, "[notes]", "[missing]", 1),
		strings.Replace(launchYAML, "[notes]", "[notes, notes]", 1),
		strings.Replace(launchYAML, "command: node", "command: node\n    command: other", 1),
		strings.Replace(launchYAML, "command: node", "command: &cmd node", 1),
		strings.Replace(launchYAML, "[notes]", "[true]", 1),
	} {
		if _, err := parseLaunchConfig([]byte(raw), ServerSelection{}); err == nil {
			t.Fatalf("accepted invalid launch schema: %s", raw)
		}
	}
}
