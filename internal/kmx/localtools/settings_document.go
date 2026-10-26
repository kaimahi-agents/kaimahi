package localtools

import (
	"bytes"
	"io"
	"slices"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
	"oras.land/oras-go/v2/registry"
)

// ParseSettings is the shared bounded byte contract for folder settings reads/writes.
// Nil bytes mean absence. A present empty or ambiguous document is invalid.
func ParseSettings(raw []byte, names []string) ([]ToolSetting, error) {
	_, states, err := settingsDocument(raw, names)
	return states, err
}
func settingsDocument(raw []byte, names []string) (*yaml.Node, []ToolSetting, error) {
	states := make([]ToolSetting, len(names))
	seen := map[string]bool{}
	for i, name := range names {
		if !ValidToolName(name) || seen[name] || len(names) > 32 {
			return nil, nil, ErrSettings
		}
		states[i] = ToolSetting{Name: name, State: Enabled}
		seen[name] = true
	}
	doc := &yaml.Node{}
	if raw == nil {
		return doc, states, nil
	}
	if len(raw) > 256*1024 || secretshapes.Match(string(raw)) != nil {
		return nil, nil, ErrSettings
	}
	var extra yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(raw))
	if d.Decode(doc) != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || !settingsNode(doc.Content[0], 0) || d.Decode(&extra) != io.EOF {
		return nil, nil, ErrSettings
	}
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i].Value, root.Content[i+1]
		switch key {
		case "schemaVersion":
			var version int
			if value.Tag != "!!int" || value.Decode(&version) != nil || version != 1 {
				return nil, nil, ErrSettings
			}
		case "publishRepository":
			ref, e := registry.ParseReference(value.Value)
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || e != nil || ref.Reference != "" {
				return nil, nil, ErrSettings
			}
		case "tools":
			if value.Kind != yaml.MappingNode {
				return nil, nil, ErrSettings
			}
			overrides := map[string]ToolState{}
			for j := 0; j < len(value.Content); j += 2 {
				name, state := value.Content[j].Value, value.Content[j+1]
				if !ValidToolName(name) || state.Kind != yaml.ScalarNode || state.Tag != "!!str" || ToolState(state.Value) != Enabled && ToolState(state.Value) != Disabled {
					return nil, nil, ErrSettings
				}
				overrides[name] = ToolState(state.Value)
			}
			for j := range states {
				if state, ok := overrides[states[j].Name]; ok {
					states[j].State = state
				}
			}
		}
	}
	return doc, states, nil
}

// ChangeToolState merges one declared name. No-ops preserve every byte and absence.
// A changed document preserves YAML nodes/comments/order; layout may normalize.
func ChangeToolState(raw []byte, names []string, name string, state ToolState) ([]byte, []ToolSetting, bool, error) {
	if !slices.Contains(names, name) || state != Enabled && state != Disabled {
		return nil, nil, false, ErrSettings
	}
	doc, states, err := settingsDocument(raw, names)
	if err != nil {
		return nil, nil, false, err
	}
	for _, s := range states {
		if s.Name == name && s.State == state {
			return raw, states, false, nil
		}
	}
	if raw == nil {
		if yaml.Unmarshal([]byte("version: 1\ntools: {}\n"), doc) != nil {
			return nil, nil, false, ErrSettings
		}
	}
	root := doc.Content[0]
	var tools *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "tools" {
			tools = root.Content[i+1]
		}
	}
	scalar := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	if tools == nil {
		tools = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, scalar("tools"), tools)
	}
	var target *yaml.Node
	for i := 0; i < len(tools.Content); i += 2 {
		if tools.Content[i].Value == name {
			target = tools.Content[i+1]
		}
	}
	if target == nil {
		tools.Content = append(tools.Content, scalar(name), scalar(string(state)))
	} else {
		target.Value = string(state)
	}
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, nil, false, ErrSettings
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, false, ErrSettings
	}
	updated, err := ParseSettings(b.Bytes(), names)
	for i := range states {
		if states[i].Name == name {
			states[i].State = state
		}
	}
	if err != nil || !slices.Equal(updated, states) {
		return nil, nil, false, ErrSettings
	}
	return b.Bytes(), updated, true, nil
}

func settingsNode(n *yaml.Node, depth int) bool {
	if n == nil || depth > 32 || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return false
	}
	if n.Kind == yaml.MappingNode {
		if n.Tag != "!!map" || len(n.Content)%2 != 0 {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return false
			}
			seen[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if !settingsNode(child, depth+1) {
			return false
		}
	}
	return true
}
