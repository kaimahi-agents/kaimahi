package agentfile

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrOrkaSourceUnsupported classifies native sources that cannot enter a selected
// reader. errors.As also exposes the existing safe field/location diagnostic.
var ErrOrkaSourceUnsupported = errors.New("unsupported Orka source")

func unsupportedOrka(file, field, problem string) error {
	return fmt.Errorf("%w: %w", ErrOrkaSourceUnsupported, refuse(file, 1, 1, field, problem))
}

func frontmatterEnd(lines []string) int {
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			return i
		}
	}
	return 0
}

// Native sources contain exactly one document; preview preserves the legacy
// first-document projection but still refuses native markers in later documents.
func decodeFrontmatter(file, source string) (*yaml.Node, map[string]*yaml.Node, *Error) {
	decoder := yaml.NewDecoder(strings.NewReader(source))
	doc, fields, err := decodeFrontmatterRoot(file, decoder)
	if err != nil {
		return nil, nil, err
	}
	var extra yaml.Node
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, nil, refuse(file, 2, 1, "Frontmatter", "provide exactly one YAML document")
	}
	return doc, fields, nil
}

func decodePreviewFrontmatter(file, source string) (*yaml.Node, map[string]*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(source))
	doc, fields, err := decodeFrontmatterRoot(file, decoder)
	if err != nil {
		return nil, nil, err
	}
	next := doc
	for {
		if hasOrkaMarker(next) {
			return nil, nil, unsupportedOrka(file, "kmx", "native package source cannot enter preview; use the native package path")
		}
		next = &yaml.Node{}
		// Legacy yaml.Unmarshal ignored content after the first document, including
		// malformed tails. It remains ignored unless a decoded document is native.
		if decoder.Decode(next) != nil {
			break
		}
	}
	return doc, fields, nil
}

// Shared root decoding does not project fields or apply native-only schema rules.
func decodeFrontmatterRoot(file string, decoder *yaml.Decoder) (*yaml.Node, map[string]*yaml.Node, *Error) {
	var doc yaml.Node
	err := decoder.Decode(&doc)
	if errors.Is(err, io.EOF) {
		return &doc, map[string]*yaml.Node{}, nil
	}
	if err != nil {
		return nil, nil, refuse(file, 2, 1, "Frontmatter", "provide valid YAML between the --- lines")
	}
	if doc.Kind == 0 || len(doc.Content) == 1 && doc.Content[0].Kind == yaml.ScalarNode && doc.Content[0].Tag == "!!null" {
		return &doc, map[string]*yaml.Node{}, nil
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, refuse(file, 2, 1, "Frontmatter", "use a YAML mapping of settings")
	}
	fields := map[string]*yaml.Node{}
	root := doc.Content[0]
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if _, dup := fields[key]; dup {
			return nil, nil, refuse(file, 1+root.Content[i].Line, 1, "Frontmatter", "remove duplicate settings")
		}
		fields[key] = root.Content[i+1]
	}
	return &doc, fields, nil
}

// Inspect only root/merged mappings: an unrelated nested kmx key is still prose
// metadata. Following merge aliases must not permit cycles or unbounded revisits.
func hasOrkaMarker(root *yaml.Node) bool {
	seen := map[*yaml.Node]bool{}
	var visit func(*yaml.Node) bool
	visit = func(n *yaml.Node) bool {
		if n == nil || seen[n] {
			return false
		}
		seen[n] = true
		switch n.Kind {
		case yaml.AliasNode:
			return visit(n.Alias)
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, child := range n.Content {
				if visit(child) {
					return true
				}
			}
		case yaml.MappingNode:
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if orkaMarkerKey(key) {
					return true
				}
				if key.Tag == "!!merge" && visit(n.Content[i+1]) {
					return true
				}
			}
		}
		return false
	}
	return visit(root)
}

// YAML aliases and binary scalars can name the same decoded root key. Decode
// only a scalar key, never a referenced mapping or arbitrary metadata subtree.
func orkaMarkerKey(key *yaml.Node) bool {
	seen := map[*yaml.Node]bool{}
	for key != nil && key.Kind == yaml.AliasNode {
		if seen[key] {
			return false
		}
		seen[key] = true
		key = key.Alias
	}
	if key == nil || key.Kind != yaml.ScalarNode {
		return false
	}
	var value string
	return key.Decode(&value) == nil && value == "kmx"
}
