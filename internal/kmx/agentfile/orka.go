package agentfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/scaffold"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
	"go.yaml.in/yaml/v3"
)

// OrkaSource is authored source, never a preview Agent, safe log projection or
// execution/package authority. Source and Revision bind the original bytes.
type OrkaSource struct {
	Name, Description, Model, Protocol, Instructions, Revision string
	Source                                                     []byte
	Soul                                                       *OrkaSoul
	Skills                                                     []OrkaSkill
	Tools                                                      []json.RawMessage
	Unsupported                                                []OrkaUnsupported
}

type OrkaReference struct{ Name, Key string }
type OrkaSoul struct {
	Inline, Digest string
	ConfigMapRef   *OrkaReference
}
type OrkaSkill struct {
	Name         string
	ConfigMapRef *OrkaReference
}

// OrkaUnsupported names a canonical field, never an authored operand or ID.
// A retained unsupported field must not silently disappear during compilation.
type OrkaUnsupported struct{ Field, Reason string }

func orkaSourceName(filename string) bool {
	leaf := filepath.Base(filename)
	return strings.HasSuffix(leaf, ".agent.md") && leaf != ".agent.md"
}

func unknownOrkaFields(fields map[string]*yaml.Node, allowed ...string) bool {
	for key := range fields {
		if !slices.Contains(allowed, key) {
			return true
		}
	}
	return false
}

// ParseOrkaSource validates the explicit Orka profile without IO or execution.
func ParseOrkaSource(filename string, data []byte) (*OrkaSource, error) {
	if !orkaSourceName(filename) {
		return nil, unsupportedOrka(filename, "File", "select a NAME.agent.md native source")
	}
	text, err := checkedSourceText(filename, data)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(text, "\n")
	if !hasFrontmatter(lines) {
		return nil, unsupportedOrka(filename, "kmx", "select an explicit native Orka profile")
	}
	end := frontmatterEnd(lines)
	if end == 0 {
		return nil, refuse(filename, 1, 1, "Frontmatter", "close the YAML frontmatter with a --- line")
	}
	doc, fields, e := decodeFrontmatter(filename, strings.Join(lines[1:end], "\n"))
	if e != nil {
		return nil, e
	}
	if err := checkOrkaTree(filename, doc); err != nil {
		return nil, err
	}
	marker := fields["kmx"]
	if marker == nil || marker.Kind != yaml.MappingNode {
		return nil, unsupportedOrka(filename, "kmx", "select schemaVersion 1 and runtime orka in a kmx mapping")
	}
	profile := orkaMapping(marker)
	version, rt := profile["schemaVersion"], profile["runtime"]
	if version == nil || version.Tag != "!!int" || version.Value != "1" || rt == nil || rt.Tag != "!!str" || rt.Value != "orka" {
		return nil, unsupportedOrka(filename, "kmx", "select schemaVersion 1 and runtime orka")
	}
	p := orkaParser{file: filename}
	s := &OrkaSource{Source: bytes.Clone(data), Revision: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), Protocol: "openai-compatible", Skills: []OrkaSkill{}, Tools: []json.RawMessage{}}
	if s.Name, err = p.text(fields["name"], "name", true); err != nil {
		return nil, err
	}
	if scaffold.ValidateName(s.Name) != nil {
		return nil, p.fail("name", "set a valid native member name")
	}
	if s.Model, err = p.text(fields["model"], "model", true); err != nil {
		return nil, err
	}
	if !ValidModel(s.Model) || scaffold.ValidateModelIdentifier(s.Model) != nil {
		return nil, p.fail("model", "set one concrete exact native model")
	}
	if s.Description, err = p.text(fields["description"], "description", false); err != nil {
		return nil, err
	}
	if scaffold.ValidateSingleLineText(s.Description) != nil {
		return nil, p.fail("description", "use single-line description text")
	}
	s.Instructions = strings.Join(lines[end+1:], "\n")
	if strings.TrimSpace(s.Instructions) == "" || scaffold.ValidateBlockText(s.Instructions) != nil {
		return nil, p.fail("Instructions", "provide nonblank native role instructions")
	}
	mixed := fields["tools"] != nil
	walk(lines, end+1, func(i int, line string) {
		if line == "## Model" || line == "## Tools" || line == "## Examples" && structuredExamples(lines[i+1:]) {
			mixed = true
		}
	})
	if mixed {
		return nil, p.fail("Sections", "use native frontmatter and companion tests, not preview control sections")
	}
	if n := profile["soul"]; n != nil {
		if s.Soul, err = p.soul(n); err != nil {
			return nil, err
		}
	}
	if n := profile["skills"]; n != nil {
		if s.Skills, err = p.skills(n); err != nil {
			return nil, err
		}
	}
	if n := profile["tools"]; n != nil {
		if n.Kind != yaml.SequenceNode {
			return nil, p.fail("kmx.tools", "use an ordered list of native declarations")
		}
		for _, item := range n.Content {
			var value any
			if item.Decode(&value) != nil {
				return nil, p.fail("kmx.tools", "use JSON-representable native declarations")
			}
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, p.fail("kmx.tools", "use JSON-representable native declarations")
			}
			s.Tools = append(s.Tools, raw)
		}
	}
	if unknownOrkaFields(fields, "name", "description", "model", "kmx") {
		s.Unsupported = append(s.Unsupported, OrkaUnsupported{"Frontmatter", "unrepresented"})
	}
	if unknownOrkaFields(profile, "schemaVersion", "runtime", "soul", "skills", "tools") {
		s.Unsupported = append(s.Unsupported, OrkaUnsupported{"kmx", "unrepresented"})
	}
	if len(s.Tools) > 0 {
		s.Unsupported = append(s.Unsupported, OrkaUnsupported{"kmx.tools", "not_yet_supported"})
	}
	values := []string{s.Name, s.Description, s.Model, s.Instructions}
	if s.Soul != nil {
		values = append(values, s.Soul.Inline, s.Soul.Digest)
		if r := s.Soul.ConfigMapRef; r != nil {
			values = append(values, r.Name, r.Key)
		}
	}
	for _, skill := range s.Skills {
		values = append(values, skill.Name)
		if r := skill.ConfigMapRef; r != nil {
			values = append(values, r.Name, r.Key)
		}
	}
	if secretshapes.Match(strings.Join(values, "\n")) != nil {
		return nil, p.fail("Source", "remove credential-shaped content from decoded native values")
	}
	return s, nil
}
