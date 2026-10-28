package localtools

import (
	"errors"
	"strings"

	"go.yaml.in/yaml/v3"
)

var ErrServerSelection = errors.New("MCP server selection or launch configuration is unavailable")

// ServerSelection is CLI host intent, not agent content. Clear wins over both
// explicit names and the folder's saved selection.
type ServerSelection struct {
	Names           []string
	Override, Clear bool
}

type launchDefinition struct {
	name, command string
	args          []string
	env, tools    map[string]string
}

func parseLaunchConfig(raw []byte, selection ServerSelection) ([]launchDefinition, error) {
	doc, _, err := settingsDocument(raw, nil)
	if err != nil {
		return nil, ErrServerSelection
	}
	root := map[string]*yaml.Node{}
	if len(doc.Content) > 0 {
		for i := 0; i < len(doc.Content[0].Content); i += 2 {
			root[doc.Content[0].Content[i].Value] = doc.Content[0].Content[i+1]
		}
	}
	if selection.Clear {
		return nil, nil
	}
	names := selection.Names
	if !selection.Override {
		names = nil
		if n := root["liveToolServers"]; n != nil {
			if n.Kind != yaml.SequenceNode || n.Tag != "!!seq" {
				return nil, ErrServerSelection
			}
			for _, v := range n.Content {
				if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
					return nil, ErrServerSelection
				}
				names = append(names, v.Value)
			}
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	servers := root["mcpServers"]
	if servers == nil || servers.Kind != yaml.MappingNode || servers.Tag != "!!map" || len(servers.Content) > 64 {
		return nil, ErrServerSelection
	}
	seen := map[string]bool{}
	var selected []launchDefinition
	text := func(n *yaml.Node, empty bool) bool {
		return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!str" && len(n.Value) <= 8192 && !strings.ContainsRune(n.Value, 0) && (empty || strings.TrimSpace(n.Value) != "")
	}
	for _, name := range names {
		if !ValidToolName(name) || seen[name] || len(names) > 32 {
			return nil, ErrServerSelection
		}
		seen[name] = true
		var entry *yaml.Node
		for i := 0; i < len(servers.Content); i += 2 {
			if servers.Content[i].Value == name {
				entry = servers.Content[i+1]
			}
		}
		if entry == nil || entry.Kind != yaml.MappingNode {
			return nil, ErrServerSelection
		}
		def := launchDefinition{name: name, env: map[string]string{}, tools: map[string]string{}}
		for i := 0; i < len(entry.Content); i += 2 {
			key, n := entry.Content[i].Value, entry.Content[i+1]
			switch key {
			case "command":
				if !text(n, false) {
					return nil, ErrServerSelection
				}
				def.command = n.Value
			case "args":
				if n.Kind != yaml.SequenceNode || n.Tag != "!!seq" || len(n.Content) > 128 {
					return nil, ErrServerSelection
				}
				for _, v := range n.Content {
					if !text(v, true) {
						return nil, ErrServerSelection
					}
					def.args = append(def.args, v.Value)
				}
			case "env", "tools":
				if n.Kind != yaml.MappingNode || n.Tag != "!!map" || len(n.Content) > 128 {
					return nil, ErrServerSelection
				}
				for j := 0; j < len(n.Content); j += 2 {
					k, v := n.Content[j].Value, n.Content[j+1]
					if !text(v, false) {
						return nil, ErrServerSelection
					}
					if key == "env" {
						if !envID.MatchString(k) || !envID.MatchString(v.Value) {
							return nil, ErrServerSelection
						}
						def.env[k] = v.Value
					} else {
						if !ValidToolName(k) || len(v.Value) > 128 || strings.TrimSpace(v.Value) != v.Value {
							return nil, ErrServerSelection
						}
						def.tools[k] = v.Value
					}
				}
			default:
				return nil, ErrServerSelection
			}
		}
		if def.command == "" || len(def.tools) == 0 || len(def.tools) > 32 {
			return nil, ErrServerSelection
		}
		selected = append(selected, def)
	}
	return selected, nil
}
