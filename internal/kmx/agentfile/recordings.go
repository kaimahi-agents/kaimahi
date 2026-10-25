package agentfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/agent-primitives/sessions/api"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// Mocks is per-example fixture data, never authority to construct a live host.
type Mocks struct {
	Tools []MockTool `json:"tools"`
}
type MockTool struct {
	Name        string         `json:"name"`
	Mode        string         `json:"mode"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Calls       []MockCall     `json:"calls,omitempty"`
}
type MockCall struct {
	Arguments map[string]any `json:"arguments"`
	Result    map[string]any `json:"result"`
}

var errRecording = errors.New("invalid tool fixture")

func parseMocks(node *yaml.Node) (*Mocks, error) {
	// Convert before decoding the closed shape: YAML's permissive scalar-to-string
	// conversion and float parsing must not erase original types or precision.
	value, err := fixtureJSON(node)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errRecording
	}
	var mocks Mocks
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&mocks) != nil || mocks.Tools == nil || len(mocks.Tools) > 32 {
		return nil, errRecording
	}
	// JSON decoding alone accepts case-folded field names and cannot distinguish
	// absent from null/empty. Check exact field spelling and presence on the YAML.
	fields := func(n *yaml.Node, required int, names ...string) map[string]*yaml.Node {
		if n.Kind != yaml.MappingNode {
			return nil
		}
		values := map[string]*yaml.Node{}
		for i := 0; i < len(n.Content); i += 2 {
			values[n.Content[i].Value] = n.Content[i+1]
		}
		for key := range values {
			found := false
			for _, name := range names {
				found = found || key == name
			}
			if !found {
				return nil
			}
		}
		for _, name := range names[:required] {
			if values[name] == nil {
				return nil
			}
		}
		return values
	}
	root := fields(node, 1, "tools")
	if root == nil || root["tools"].Kind != yaml.SequenceNode {
		return nil, errRecording
	}
	seen := map[string]bool{}
	var definitions []api.ToolDefinition
	for i, tool := range mocks.Tools {
		n := root["tools"].Content[i]
		f := fields(n, 2, "name", "mode", "description", "parameters", "calls")
		if f == nil || !localtools.ValidToolName(tool.Name) || seen[tool.Name] {
			return nil, errRecording
		}
		seen[tool.Name] = true
		if tool.Mode == "live" {
			if len(f) != 2 {
				return nil, errRecording
			}
			continue
		}
		if tool.Mode != "recorded" || f["calls"] == nil || f["calls"].Kind != yaml.SequenceNode || len(tool.Calls) > 1 {
			return nil, errRecording
		}
		if len(tool.Calls) == 0 {
			if len(f) != 3 {
				return nil, errRecording
			}
			continue
		}
		if strings.TrimSpace(tool.Description) == "" || tool.Parameters == nil {
			return nil, errRecording
		}
		call := tool.Calls[0]
		if fields(f["calls"].Content[0], 2, "arguments", "result") == nil || call.Arguments == nil || call.Result == nil {
			return nil, errRecording
		}
		result, err := json.Marshal(call.Result)
		if err != nil || len(result) > 32*1024 {
			return nil, errRecording
		}
		definitions = append(definitions, api.ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: tool.Parameters})
	}
	if len(definitions) > 0 {
		definitions, err = localtools.CopyDefinitions(definitions)
		if err != nil {
			return nil, errRecording
		}
		for _, tool := range mocks.Tools {
			if len(tool.Calls) > 0 && localtools.ValidateArguments(definitions, tool.Name, tool.Calls[0].Arguments) != nil {
				return nil, errRecording
			}
		}
	}
	if localtools.CheckJSON(mocks, func(values ...string) error {
		for _, value := range values {
			if secretshapes.Match(value) != nil || strings.IndexFunc(value, unsupportedControl) >= 0 {
				return errRecording
			}
		}
		return nil
	}) != nil {
		return nil, errRecording
	}
	return &mocks, nil
}

// fixtureJSON keeps JSON types and exact numeric values, rejecting duplicate or
// non-string keys at every depth. The caller already enforced YAML tree bounds.
func fixtureJSON(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.MappingNode:
		value := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, errRecording
			}
			if _, exists := value[key.Value]; exists {
				return nil, errRecording
			}
			child, err := fixtureJSON(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			value[key.Value] = child
		}
		return value, nil
	case yaml.SequenceNode:
		value := make([]any, 0, len(n.Content))
		for _, node := range n.Content {
			child, err := fixtureJSON(node)
			if err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		return value, nil
	case yaml.ScalarNode:
		var value any
		if n.Tag == "!!int" || n.Tag == "!!float" {
			if !json.Valid([]byte(n.Value)) || !runtime.JSONNumbersExact([]byte(n.Value)) || json.Unmarshal([]byte(n.Value), &value) != nil {
				return nil, errRecording
			}
		} else if n.Tag == "!!str" || n.Tag == "!!bool" || n.Tag == "!!null" {
			if n.Decode(&value) != nil {
				return nil, errRecording
			}
		} else {
			return nil, errRecording
		}
		return value, nil
	}
	return nil, errRecording
}
