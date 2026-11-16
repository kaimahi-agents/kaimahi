package localagent

import (
	"errors"
	"io"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/modelaccess"
	"go.yaml.in/yaml/v3"
)

// ModelRequirements combines resident-only authored requirements with stronger
// invocation requirements. An invocation cannot weaken authored minima.
func ModelRequirements(pair *agentfile.Pair, requested modelaccess.Requirements) (modelaccess.Requirements, error) {
	if pair == nil || pair.Agent == nil || requested.Validate() != nil {
		return requested, errors.New("invalid model requirements")
	}
	var fields map[string]yaml.Node
	if yaml.Unmarshal([]byte(pair.Agent.Frontmatter), &fields) != nil {
		return requested, errors.New("invalid model requirements frontmatter")
	}
	if node, ok := fields["modelRequirements"]; ok {
		raw, err := yaml.Marshal(&node)
		if err != nil || node.Kind != yaml.MappingNode {
			return requested, errors.New("modelRequirements must be a mapping")
		}
		var authored modelaccess.Requirements
		d := yaml.NewDecoder(strings.NewReader(string(raw)))
		d.KnownFields(true)
		if d.Decode(&authored) != nil || d.Decode(new(any)) != io.EOF || authored.Validate() != nil {
			return requested, errors.New("invalid modelRequirements; use contextTokens, outputTokens and reasoning")
		}
		requested.ContextTokens = max(requested.ContextTokens, authored.ContextTokens)
		requested.OutputTokens = max(requested.OutputTokens, authored.OutputTokens)
		requested.Reasoning = requested.Reasoning || authored.Reasoning
	}
	return requested, requested.Validate()
}
