package convert

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// This allowlist is intentionally presence-sensitive: unsupported false, null,
// and empty values must fail before decoding could erase their presence.
type rule struct {
	kind     yaml.Kind
	fields   map[string]*rule
	required []string
	item     *rule
	extra    *rule
	values   []string
	empty    bool
	opaque   bool
}

var (
	text            = &rule{kind: yaml.ScalarNode, empty: true}
	word            = &rule{kind: yaml.ScalarNode}
	metadataRule    = object(map[string]*rule{"name": word, "namespace": word}, "name", "namespace")
	configMapSource = object(map[string]*rule{"type": choice("ConfigMap"), "name": word, "key": word}, "type", "name", "key")
	secretSource    = object(map[string]*rule{"type": choice("Secret"), "name": word, "key": word}, "type", "name", "key")
	remoteReference = object(map[string]*rule{
		"name": word, "namespace": word, "kind": choice("RemoteMCPServer"), "apiGroup": choice("kagent.dev"),
		"toolNames": list(word, false),
	}, "name", "kind", "apiGroup", "toolNames")
	resourceRules = map[string]*rule{
		"Agent": resourceRule("kagent.dev/v1alpha2", "spec", object(map[string]*rule{
			"type": choice("Declarative"), "description": text,
			"declarative": object(map[string]*rule{
				"modelConfig": word, "systemMessage": word, "systemMessageFrom": configMapSource,
				"tools": list(object(map[string]*rule{"type": choice("McpServer"), "mcpServer": remoteReference}, "type", "mcpServer"), false),
			}, "modelConfig", "tools"),
		}, "type", "declarative")),
		"ModelConfig": resourceRule("kagent.dev/v1alpha2", "spec", object(map[string]*rule{
			"provider": choice("OpenAI", "Anthropic"), "model": word, "apiKeySecret": word, "apiKeySecretKey": word,
			"openAI": object(map[string]*rule{"baseUrl": word}), "anthropic": object(map[string]*rule{"baseUrl": word}),
		}, "provider", "model", "apiKeySecret", "apiKeySecretKey")),
		"RemoteMCPServer": resourceRule("kagent.dev/v1alpha2", "spec", object(map[string]*rule{
			"description": text, "protocol": choice("STREAMABLE_HTTP"), "url": word,
			"headersFrom": list(object(map[string]*rule{"name": word, "valueFrom": secretSource}, "name", "valueFrom"), true),
		}, "description", "protocol", "url")),
		"ConfigMap": resourceRule("v1", "data", &rule{kind: yaml.MappingNode, extra: text}),
	}
	snapshotRule = &rule{kind: yaml.MappingNode, extra: object(map[string]*rule{
		"tools": list(object(map[string]*rule{
			"name": word, "description": text, "inputSchema": {kind: yaml.MappingNode, opaque: true},
		}, "name", "description", "inputSchema"), true),
	}, "tools")}
)

func object(fields map[string]*rule, required ...string) *rule {
	return &rule{kind: yaml.MappingNode, fields: fields, required: required}
}

func choice(values ...string) *rule { return &rule{kind: yaml.ScalarNode, values: values} }
func list(item *rule, empty bool) *rule {
	return &rule{kind: yaml.SequenceNode, item: item, empty: empty}
}
func resourceRule(version, body string, spec *rule) *rule {
	return object(map[string]*rule{"apiVersion": choice(version), "kind": word, "metadata": metadataRule, body: spec}, "apiVersion", "kind", "metadata", body)
}

func (r *rule) validate(n *yaml.Node, path string) error {
	if n.Kind != r.kind {
		return fmt.Errorf("%s: unsupported value shape", path)
	}
	if r.opaque {
		return nil
	} // JSON Schema is copied verbatim, not interpreted.
	switch r.kind {
	case yaml.ScalarNode:
		if n.Tag != "!!str" {
			return fmt.Errorf("%s: expected string", path)
		}
		if !r.empty && strings.TrimSpace(n.Value) == "" {
			return fmt.Errorf("%s: nonempty string required", path)
		}
		if len(r.values) > 0 {
			for _, value := range r.values {
				if n.Value == value {
					return nil
				}
			}
			return fmt.Errorf("%s: supported values: %s", path, strings.Join(r.values, ", "))
		}
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			child, ok := r.fields[key]
			if !ok {
				child = r.extra
			}
			if child == nil {
				return fmt.Errorf("%s.%s: unsupported field", path, key)
			}
			if err := child.validate(n.Content[i+1], path+"."+key); err != nil {
				return err
			}
		}
		for _, key := range r.required {
			if field(n, key) == nil {
				return fmt.Errorf("%s.%s: required field", path, key)
			}
		}
	case yaml.SequenceNode:
		if !r.empty && len(n.Content) == 0 {
			return fmt.Errorf("%s: explicit nonempty selection required", path)
		}
		for i, child := range n.Content {
			if err := r.item.validate(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// Inspect the raw tree, including opaque JSON Schema, before any decoding.
// Refusing anchors as well as aliases rules out hidden merge/default semantics.
func checkTree(n *yaml.Node, path string) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return fmt.Errorf("%s: YAML anchors and aliases are unsupported", path)
	}
	if n.Tag == "!!merge" {
		return fmt.Errorf("%s: YAML merge keys are unsupported", path)
	}
	if (n.Kind == yaml.MappingNode && n.Tag != "!!map") || (n.Kind == yaml.SequenceNode && n.Tag != "!!seq") || (n.Kind == yaml.ScalarNode && (n.Tag == "!!map" || n.Tag == "!!seq")) {
		return fmt.Errorf("%s: YAML tag does not match node kind", path)
	}
	switch n.Tag {
	case "!!map", "!!seq", "!!str", "!!bool", "!!null", "!!int", "!!float":
	default:
		return fmt.Errorf("%s: unsupported YAML tag", path)
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Value == "<<" {
				return fmt.Errorf("%s: YAML merge keys are unsupported", path)
			}
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("%s: mapping keys must be strings", path)
			}
			if err := checkTree(key, path); err != nil {
				return err
			}
			if seen[key.Value] {
				return fmt.Errorf("%s.%s: duplicate key", path, key.Value)
			}
			seen[key.Value] = true
			if err := checkTree(n.Content[i+1], path+"."+key.Value); err != nil {
				return err
			}
		}
	} else {
		for i, child := range n.Content {
			if err := checkTree(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func field(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func value(n *yaml.Node, key string) string {
	if child := field(n, key); child != nil {
		return child.Value
	}
	return ""
}
