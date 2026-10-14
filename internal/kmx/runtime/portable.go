package runtime

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// rejectPortableKeyHazards recurses through every node in the document — at
// every nesting level, including inside sequences — and refuses the ways an
// authored mapping can mean something other than what it literally says.
//
//  1. A repeated key. yaml.v3 otherwise silently keeps the last occurrence,
//     which would let a document quietly say two different things about the
//     same field.
//  2. A merge key ("<<"). yaml.v3 resolves it before the strict decode ever
//     sees the mapping, so the merged-in keys are never written where they
//     take effect: a merge can supply a field this mapping also states — the
//     duplicate this walk exists to catch — or supply one the closed schema
//     does not model, with neither gate able to see it.
//  3. A plain alias. The same argument without the merge: the bytes where a
//     field takes effect are not what it means, and nothing here validates
//     an expanded alias, so none is accepted.
//  4. A key that is not a plain string name, or contains control characters.
//     Such a key is unsafe to name in an error or use as a path component.
func rejectPortableKeyHazards(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.AliasNode:
		return fmt.Errorf("YAML alias at %s (line %d): every field must be stated where it applies, not copied in from an anchor", portablePathOrRoot(path), node.Line)
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode, valueNode := node.Content[i], node.Content[i+1]
			if keyNode.Kind == yaml.AliasNode {
				return fmt.Errorf("YAML alias used as a key at %s (line %d): every field must be stated where it applies, not copied in from an anchor", portablePathOrRoot(path), keyNode.Line)
			}
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" ||
				strings.IndexFunc(keyNode.Value, unicode.IsControl) >= 0 {
				return fmt.Errorf("key at %s (line %d) must be a plain name", portablePathOrRoot(path), keyNode.Line)
			}
			key := keyNode.Value
			location := key
			if path != "" {
				location = path + "." + key
			}
			if key == portableMergeKey || keyNode.Tag == "!!merge" {
				return fmt.Errorf("merge key %q at %s (line %d): every field must be stated where it applies, not merged in from an anchor", portableMergeKey, location, keyNode.Line)
			}
			if seen[key] {
				return fmt.Errorf("duplicate key %q at %s (line %d)", key, location, keyNode.Line)
			}
			seen[key] = true
			if err := rejectPortableKeyHazards(valueNode, location); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := rejectPortableKeyHazards(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func portablePathOrRoot(path string) string {
	if path == "" {
		return "the document root"
	}
	return path
}

// Scan decoded scalar keys and values before any YAML validation can echo them.
// Binary scalars are scanned both as authored and after base64 decoding.
func refusePortableDecodedSecretShapes(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		if err := refusePortableSecretShape(node.Value); err != nil {
			return err
		}
		// A "!!binary" scalar's Value is still the base64 text; yaml.v3
		// resolves it into the bytes it encodes, and those bytes are what a
		// later error would print. A payload that does not decode is left
		// to the decoder to refuse — it carries no assembled value to echo.
		if node.Tag == "!!binary" {
			if decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(node.Value), "")); err == nil {
				if err := refusePortableSecretShape(string(decoded)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, child := range node.Content {
		if err := refusePortableDecodedSecretShapes(child); err != nil {
			return err
		}
	}
	return nil
}

func refusePortableSecretShape(value string) error {
	if shape := secretshapes.Match(value); shape != nil {
		return fmt.Errorf("refusing portable agent document containing something shaped like %s; reference a separately provisioned Secret, never a credential value", shape.What)
	}
	return nil
}

const PortableAPIVersion = "kmx.kaimahi.dev/v1alpha1"
const portableMergeKey = "<<"
