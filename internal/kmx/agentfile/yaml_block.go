package agentfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// yamlNode converts JSON to a YAML node, keeping object key order and exact
// number literals. Strings stay strings even when they look like other values.
func yamlNode(raw []byte) (*yaml.Node, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func() (*yaml.Node, error)
	read = func() (*yaml.Node, error) {
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case json.Delim:
			n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			if t == '{' {
				n.Kind, n.Tag = yaml.MappingNode, "!!map"
			}
			for d.More() {
				if n.Kind == yaml.MappingNode {
					key, err := d.Token()
					if err != nil {
						return nil, err
					}
					n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key.(string)})
				}
				child, err := read()
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, child)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			if len(n.Content) == 0 {
				n.Style = yaml.FlowStyle
			}
			return n, nil
		case string:
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}, nil
		case json.Number:
			tag := "!!int"
			if strings.ContainsAny(t.String(), ".eE") {
				tag = "!!float"
			}
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: t.String()}, nil
		case bool:
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: map[bool]string{true: "true", false: "false"}[t]}, nil
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	n, err := read()
	if err == nil && d.More() {
		err = errors.New("trailing JSON")
	}
	return n, err
}

// ordered is a JSON object written in key order: key, value, key, value...
type ordered []any

func (o ordered) MarshalJSON() ([]byte, error) {
	b := []byte{'{'}
	for i := 0; i+1 < len(o); i += 2 {
		k, err1 := json.Marshal(o[i])
		v, err2 := json.Marshal(o[i+1])
		if err := errors.Join(err1, err2); err != nil {
			return nil, err
		}
		if i > 0 {
			b = append(b, ',')
		}
		b = append(append(append(b, k...), ':'), v...)
	}
	return append(b, '}'), nil
}

// blockYAML renders a JSON value as block YAML. Its first line goes where the caller
// inserts it; later lines get the given indent. No trailing line ending.
func blockYAML(value any, indent, eol string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	n, err := yamlNode(raw)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(n); err != nil {
		return "", err
	}
	if err := e.Close(); err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(unescapeWide(b.String()), "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = indent + lines[i]
		}
	}
	return strings.Join(lines, eol), nil
}

// unescapeWide writes back the characters yaml.v3 escapes as \UXXXXXXXX (emoji
// and other non-BMP text) so files stay readable. Callers verify the result.
func unescapeWide(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '\\' {
			b.WriteString(`\\`)
			i++
			continue
		}
		if s[i] == '\\' && i+10 <= len(s) && s[i+1] == 'U' {
			if r, err := strconv.ParseUint(s[i+2:i+10], 16, 32); err == nil && utf8.ValidRune(rune(r)) {
				b.WriteRune(rune(r))
				i += 9
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
