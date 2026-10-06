package krm

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// DecodeProvider strictly decodes and validates one Provider document and
// retains a copy of its exact bytes as its Source.
func DecodeProvider(data []byte) (*Provider, error) {
	p := &Provider{}
	if err := DecodeStrict(data, KindProvider, p); err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	p.source = clone(data)
	return p, nil
}

// DecodeTool strictly decodes and validates one Tool document and retains a
// copy of its exact bytes as its Source.
func DecodeTool(data []byte) (*Tool, error) {
	t := &Tool{}
	if err := DecodeStrict(data, KindTool, t); err != nil {
		return nil, err
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	t.source = clone(data)
	return t, nil
}

// DecodeAgent strictly decodes and validates one Agent document and retains
// a copy of its exact bytes as its Source.
func DecodeAgent(data []byte) (*Agent, error) {
	a := &Agent{}
	if err := DecodeStrict(data, KindAgent, a, rejectEmptyList("spec", "allowedAgents")); err != nil {
		return nil, err
	}
	if err := a.validate(); err != nil {
		return nil, err
	}
	a.source = clone(data)
	return a, nil
}

// DecodeStrict decodes exactly one YAML mapping of the given kind in the
// APIVersion group into out. It refuses credential shapes, invalid UTF-8,
// extra documents, duplicate keys, merge keys, aliases, non-string keys and
// unknown fields, the same refusals as the PortableAgent reader. Credential
// shapes are checked before any later error could quote the offending text.
// Each check runs on the parsed document before the typed decode.
func DecodeStrict(data []byte, kind string, out any, checks ...func(*yaml.Node) error) error {
	if err := refuseSecretShape(string(data)); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("document must be valid UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("document is empty or not valid YAML: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("file must contain exactly one YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return errors.New("document must be a single YAML mapping")
	}
	doc := root.Content[0]
	if err := refuseDecodedSecretShapes(doc); err != nil {
		return err
	}
	if err := rejectKeyHazards(doc, ""); err != nil {
		return err
	}
	if v := scalarAt(doc, "apiVersion"); v != APIVersion {
		return fmt.Errorf("apiVersion must be %q (found %q)", APIVersion, v)
	}
	if k := scalarAt(doc, "kind"); k != kind {
		return fmt.Errorf("kind must be %q (found %q)", kind, k)
	}
	for _, check := range checks {
		if err := check(doc); err != nil {
			return err
		}
	}
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	return strict.Decode(out)
}

// rejectKeyHazards refuses mappings that mean something other than what they
// literally say: aliases, merge keys, duplicate keys, and keys that are not
// plain names.
func rejectKeyHazards(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("YAML alias at %s (line %d): state every field where it applies", pathOrRoot(path), node.Line)
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if key.Kind == yaml.AliasNode {
				return fmt.Errorf("YAML alias used as a key at %s (line %d)", pathOrRoot(path), key.Line)
			}
			if key.Kind == yaml.ScalarNode && (key.Value == "<<" || key.Tag == "!!merge") {
				return fmt.Errorf("merge key at %s (line %d): state every field where it applies", pathOrRoot(path), key.Line)
			}
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || strings.IndexFunc(key.Value, unicode.IsControl) >= 0 {
				return fmt.Errorf("key at %s (line %d) must be a plain name", pathOrRoot(path), key.Line)
			}
			location := key.Value
			if path != "" {
				location = path + "." + key.Value
			}
			if seen[key.Value] {
				return fmt.Errorf("duplicate key %q at %s (line %d)", key.Value, location, key.Line)
			}
			seen[key.Value] = true
			if err := rejectKeyHazards(value, location); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := rejectKeyHazards(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// rejectEmptyList refuses an authored empty list, which decodes the same as
// an absent one but states something different.
func rejectEmptyList(path ...string) func(*yaml.Node) error {
	return func(doc *yaml.Node) error {
		if node := valueAt(doc, path...); node != nil && node.Kind == yaml.SequenceNode && len(node.Content) == 0 {
			return fmt.Errorf("%s must name at least one entry or be omitted", strings.Join(path, "."))
		}
		return nil
	}
}

func refuseDecodedSecretShapes(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if err := refuseSecretShape(node.Value); err != nil {
			return err
		}
		if node.Tag == "!!binary" {
			if decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(node.Value), "")); err == nil {
				return refuseSecretShape(string(decoded))
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := refuseDecodedSecretShapes(child); err != nil {
			return err
		}
	}
	return nil
}

func refuseSecretShape(value string) error {
	if shape := secretshapes.Match(value); shape != nil {
		return fmt.Errorf("refusing a document containing something shaped like %s; reference a separately provisioned Secret, never a credential value", shape.What)
	}
	return nil
}

func valueAt(node *yaml.Node, path ...string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode || len(path) == 0 {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == path[0] {
			if len(path) == 1 {
				return node.Content[i+1]
			}
			return valueAt(node.Content[i+1], path[1:]...)
		}
	}
	return nil
}

func scalarAt(node *yaml.Node, path ...string) string {
	if v := valueAt(node, path...); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func pathOrRoot(path string) string {
	if path == "" {
		return "the document root"
	}
	return path
}
