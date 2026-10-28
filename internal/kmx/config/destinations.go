package config

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/localtools"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
	"oras.land/oras-go/v2/registry"
)

// Destinations contains operational selectors only; it is not suite source.
// Resolve once and pass copied concrete values to later operations.
type Destinations struct {
	Version           int                       `yaml:"version"`
	EvalTarget        string                    `yaml:"evalTarget"`
	PublishRepository string                    `yaml:"publishRepository"`
	Targets           map[string]Destination    `yaml:"targets"`
	Registries        map[string]RegistryPolicy `yaml:"registries"`
	// Tools coexists with folder test settings; destination selection ignores it.
	Tools map[string]string `yaml:"tools"`
}
type Destination struct {
	Context          string `yaml:"context" json:"context"`
	Namespace        string `yaml:"namespace" json:"namespace,omitempty"`
	Runtime          string `yaml:"runtime" json:"runtime,omitempty"`
	Inference        string `yaml:"inference" json:"inference,omitempty"`
	Bindings         string `yaml:"bindings" json:"bindings,omitempty"`
	EvaluationTarget string `yaml:"evaluationTarget" json:"evaluationTarget,omitempty"`
}
type RegistryPolicy struct {
	PlainHTTP bool `yaml:"plainHTTP"`
}

var ErrDestinationsInvalid = errors.New("kmx.yaml: invalid operational config")
var ErrDestinationUnknown = errors.New("kmx.yaml: named destination not found")

const maxDestinationsBytes = 64 << 10
const maxDestinationsEntries = 128

var destinationName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$`)

// Validate scalar tags before decoding: yaml's ordinary string decoding would
// otherwise coerce booleans/numbers and silently accept nulls and merge aliases.
func validDestinationNode(n *yaml.Node, depth int, key string) bool {
	if depth > 8 || n.Alias != nil || n.Anchor != "" {
		return false
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" || len(n.Content) > 2*maxDestinationsEntries {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || !destinationText(k.Value) || k.Value == "<<" || seen[k.Value] {
				return false
			}
			seen[k.Value] = true
			if depth == 0 && (k.Value == "mcpServers" || k.Value == "liveToolServers") {
				if !validIgnoredMCPNode(n.Content[i+1], depth+1) {
					return false
				}
				continue
			}
			if depth == 1 && key == "tools" {
				v := n.Content[i+1]
				if !localtools.ValidToolName(k.Value) || v.Kind != yaml.ScalarNode || v.Tag != "!!str" || localtools.ToolState(v.Value) != localtools.Enabled && localtools.ToolState(v.Value) != localtools.Disabled {
					return false
				}
				continue
			}
			if !validDestinationNode(n.Content[i+1], depth+1, k.Value) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		if key == "version" {
			return n.Tag == "!!int" && n.Value == "1"
		}
		if key == "plainHTTP" {
			return n.Tag == "!!bool" && (n.Value == "true" || n.Value == "false")
		}
		return n.Tag == "!!str" && destinationText(n.Value)
	default:
		return false
	}
}

// Launch semantics belong to the tool consumer; ambiguity is rejected even
// when this destination consumer discards the subtree before strict decoding.
func validIgnoredMCPNode(n *yaml.Node, depth int) bool {
	if n == nil || depth > 8 || n.Alias != nil || n.Anchor != "" {
		return false
	}
	switch n.Kind {
	case yaml.MappingNode:
		if n.Tag != "!!map" || len(n.Content) > 2*maxDestinationsEntries || len(n.Content)%2 != 0 {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
				return false
			}
			seen[key.Value] = true
		}
	case yaml.SequenceNode:
		if n.Tag != "!!seq" || len(n.Content) > maxDestinationsEntries {
			return false
		}
	case yaml.ScalarNode:
	default:
		return false
	}
	for _, child := range n.Content {
		if !validIgnoredMCPNode(child, depth+1) {
			return false
		}
	}
	return true
}

func destinationText(s string) bool {
	return s != "" && len(s) <= 1024 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0 && secretshapes.Match(s) == nil
}
func validDestinationHost(host string) bool {
	if !destinationText(host) || strings.ContainsAny(host, "/@?#\\") || strings.Contains(host, "://") {
		return false
	}
	ref, err := registry.ParseReference(host + "/kmx")
	return err == nil && ref.Registry == host && ref.Reference == ""
}
func (d *Destinations) Target(name string) (Destination, error) {
	if d != nil {
		if target, ok := d.Targets[name]; ok {
			return target, nil
		}
	}
	return Destination{}, ErrDestinationUnknown
}
func (d *Destinations) RegistryHTTP(reference string) bool {
	if d == nil {
		return false
	}
	ref, err := registry.ParseReference(reference)
	return err == nil && d.Registries[ref.Registry].PlainHTTP
}
