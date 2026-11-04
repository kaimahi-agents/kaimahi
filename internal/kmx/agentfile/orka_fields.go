package agentfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

type orkaParser struct{ file string }

func (p orkaParser) fail(field, problem string) error { return refuse(p.file, 1, 1, field, problem) }
func (p orkaParser) text(n *yaml.Node, field string, required bool) (string, error) {
	if n == nil && !required {
		return "", nil
	}
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || required && n.Value == "" {
		return "", p.fail(field, "provide the required string value")
	}
	return n.Value, nil
}

func orkaMapping(n *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	for i := 0; i < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return out
}

func (p orkaParser) mapping(n *yaml.Node, field string, allowed ...string) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, p.fail(field, "use the documented native mapping")
	}
	fields := orkaMapping(n)
	if unknownOrkaFields(fields, allowed...) {
		return nil, p.fail(field, "use only the documented native fields")
	}
	return fields, nil
}

func checkOrkaTree(file string, root *yaml.Node) error {
	nodes := 0
	location := func(n *yaml.Node) Location { return Location{file, n.Line + 1, n.Column} }
	if err := checkExamplesYAML(root, location, "Frontmatter", &nodes); err != nil {
		return err
	}
	var visit func(*yaml.Node) error
	visit = func(n *yaml.Node) error {
		if n.Kind == yaml.ScalarNode && (strings.IndexFunc(n.Value, unsupportedControl) >= 0 || secretshapes.Match(n.Value) != nil) {
			return refuse(file, n.Line+1, n.Column, "Frontmatter", "remove unsupported controls or credential-shaped decoded content")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
					return refuse(file, key.Line+1, key.Column, "Frontmatter", "use unique string mapping keys")
				}
				seen[key.Value] = true
			}
		}
		for _, child := range n.Content {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root)
}

func (p orkaParser) reference(n *yaml.Node, field string) (*OrkaReference, error) {
	fields, err := p.mapping(n, field, "name", "key")
	if err != nil {
		return nil, err
	}
	name, err := p.text(fields["name"], field, true)
	if err != nil {
		return nil, err
	}
	key, err := p.text(fields["key"], field, true)
	if err != nil {
		return nil, err
	}
	if scaffold.ValidateObjectName(name) != nil || scaffold.ValidateOrkaSecretKey(key) != nil {
		return nil, p.fail(field, "provide a valid ConfigMap name and data key")
	}
	return &OrkaReference{Name: name, Key: key}, nil
}

func (p orkaParser) soul(n *yaml.Node) (*OrkaSoul, error) {
	fields, err := p.mapping(n, "kmx.soul", "inline", "configMapRef", "digest")
	if err != nil {
		return nil, err
	}
	inline, ref := fields["inline"], fields["configMapRef"]
	if (inline == nil) == (ref == nil) {
		return nil, p.fail("kmx.soul", "select exactly one inline or ConfigMap source")
	}
	soul := &OrkaSoul{}
	if inline != nil {
		soul.Inline, err = p.text(inline, "kmx.soul.inline", true)
		if err != nil {
			return nil, err
		}
		if len(soul.Inline) > 8192 {
			return nil, p.fail("kmx.soul.inline", "keep inline Soul within 8192 UTF-8 bytes")
		}
	} else {
		soul.ConfigMapRef, err = p.reference(ref, "kmx.soul.configMapRef")
		if err != nil {
			return nil, err
		}
	}
	if fields["digest"] != nil || ref != nil {
		soul.Digest, err = p.text(fields["digest"], "kmx.soul.digest", true)
		if err != nil {
			return nil, err
		}
		value := strings.TrimPrefix(soul.Digest, "sha256:")
		_, err := hex.DecodeString(value)
		if !strings.HasPrefix(soul.Digest, "sha256:") || len(value) != 64 || strings.ToLower(value) != value || err != nil {
			return nil, p.fail("kmx.soul.digest", "provide an exact lowercase sha256 digest")
		}
		if inline != nil && soul.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(soul.Inline))) {
			return nil, p.fail("kmx.soul.digest", "match the exact decoded inline Soul bytes")
		}
	}
	return soul, nil
}

func (p orkaParser) skills(n *yaml.Node) ([]OrkaSkill, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, p.fail("kmx.skills", "use an ordered list of Skill references")
	}
	out := make([]OrkaSkill, 0, len(n.Content))
	seen := map[string]bool{}
	for _, item := range n.Content {
		fields, err := p.mapping(item, "kmx.skills", "name", "configMapRef")
		if err != nil {
			return nil, err
		}
		if (fields["name"] == nil) == (fields["configMapRef"] == nil) {
			return nil, p.fail("kmx.skills", "select exactly one named Skill or ConfigMap reference")
		}
		var skill OrkaSkill
		var identity string
		if fields["name"] != nil {
			skill.Name, err = p.text(fields["name"], "kmx.skills.name", true)
			if err != nil {
				return nil, err
			}
			if scaffold.ValidateObjectName(skill.Name) != nil {
				return nil, p.fail("kmx.skills.name", "provide a valid Skill resource name")
			}
			identity = "name/" + skill.Name
		} else {
			skill.ConfigMapRef, err = p.reference(fields["configMapRef"], "kmx.skills.configMapRef")
			if err != nil {
				return nil, err
			}
			identity = "configMap/" + skill.ConfigMapRef.Name + "/" + skill.ConfigMapRef.Key
		}
		if seen[identity] {
			return nil, p.fail("kmx.skills", "remove duplicate Skill references")
		}
		seen[identity] = true
		out = append(out, skill)
	}
	return out, nil
}
