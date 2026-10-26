package agentfile

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

// ApplyRecordings fills pending `mode: recorded, calls: []` entries in exact
// source bytes. It only inserts JSON-quoted text, so comments, layout, tags and
// each target line's ending survive, then reparses the whole candidate under the
// unchanged bounds. All recordings apply or none do. It grants no write authority.
// A block-style entry gets block YAML instead when that form passes the same check.
func ApplyRecordings(source []byte, recordings []Recording) ([]byte, error) {
	if block, err := applyRecordings(source, recordings, true); err == nil {
		return block, nil
	}
	return applyRecordings(source, recordings, false)
}

func applyRecordings(source []byte, recordings []Recording, block bool) ([]byte, error) {
	before, err := Parse("recording.agent.md", source)
	if err != nil {
		return nil, ErrRecordingChanged
	}
	if len(recordings) == 0 {
		return nil, ErrRecordingInvalid
	}
	text := string(source)
	lines := strings.SplitAfter(text, "\n")
	starts := make([]int, len(lines))
	for i := 1; i < len(lines); i++ {
		starts[i] = starts[i-1] + len(lines[i-1])
	}
	plain := func(i int) string { return strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r") }
	// Parse accepted exactly one fenced YAML document after the Examples heading.
	open := before.Locations["Examples"].Line
	for strings.TrimSpace(plain(open)) == "" {
		open++
	}
	marker, _, _ := openFence(plain(open))
	var yamlLines []string
	for i := open + 1; !closeFence(plain(i), marker); i++ {
		yamlLines = append(yamlLines, plain(i))
	}
	var root yaml.Node
	if yaml.Unmarshal([]byte(strings.Join(yamlLines, "\n")+"\n"), &root) != nil {
		return nil, ErrRecordingChanged
	}
	// YAML columns count runes on the LF-normalized line; map them onto the
	// original line so CRLF and multibyte text keep exact byte offsets.
	offset := func(n *yaml.Node) int {
		at := starts[open+n.Line]
		for range n.Column - 1 {
			_, size := utf8.DecodeRuneInString(text[at:])
			at += size
		}
		return at
	}
	value := func(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
		for i := 0; m != nil && m.Kind == yaml.MappingNode && i < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i], m.Content[i+1]
			}
		}
		return nil, nil
	}
	type edit struct {
		at, end int
		text    string
	}
	var edits []edit
	seen := map[[2]string]bool{}
	for _, r := range recordings {
		tool, target := r.Tool, [2]string{r.ExampleID, r.Tool.Name}
		if seen[target] || tool.Mode != "recorded" || len(tool.Calls) != 1 || strings.TrimSpace(tool.Description) == "" || tool.Parameters == nil || tool.Calls[0].Arguments == nil || tool.Calls[0].Result == nil ||
			!decodedJSON(tool.Description) || !decodedJSON(tool.Parameters) || !decodedJSON(tool.Calls[0].Arguments) || !decodedJSON(tool.Calls[0].Result) {
			return nil, ErrRecordingInvalid
		}
		seen[target] = true
		description, err1 := json.Marshal(tool.Description)
		parameters, err2 := json.Marshal(tool.Parameters)
		call, err3 := json.Marshal(tool.Calls[0])
		if err1 != nil || err2 != nil || err3 != nil || !runtime.JSONNumbersExact(parameters) || !runtime.JSONNumbersExact(call) {
			return nil, ErrRecordingInvalid
		}
		var key, calls, entry *yaml.Node
		_, examples := value(root.Content[0], "examples")
		for i, e := range before.Examples {
			if e.ID != r.ExampleID || e.Mocks == nil {
				continue
			}
			_, mocks := value(examples.Content[i], "mocks")
			_, entries := value(mocks, "tools")
			for j, saved := range e.Mocks.Tools {
				if saved.Name == tool.Name && saved.Mode == "recorded" && len(saved.Calls) == 0 {
					key, calls = value(entries.Content[j], "calls")
					entry = entries.Content[j]
					before.Examples[i].Mocks.Tools[j] = tool
					separator := ", "
					if entries.Content[j].Style&yaml.FlowStyle == 0 {
						eol := "\n"
						if strings.HasSuffix(lines[open+key.Line], "\r\n") {
							eol = "\r\n"
						}
						separator = eol + strings.Repeat(" ", key.Column-1)
					}
					if !block {
						edits = append(edits, edit{offset(key), offset(key), `"description": ` + string(description) + separator + `"parameters": ` + string(parameters) + separator})
					}
				}
			}
		}
		if calls == nil {
			return nil, ErrRecordingChanged
		}
		// Skip only an actual tag, whitespace and comments before the empty
		// sequence's opening bracket; comments inside it remain after the call.
		at := offset(calls)
		if text[at] == '!' {
			at += strings.IndexAny(text[at:], " \t\r\n")
		}
		for at < len(text) && strings.IndexByte(" \t\r\n#", text[at]) >= 0 {
			if text[at] == '#' {
				at += strings.IndexByte(text[at:], '\n')
			}
			at++
		}
		if at >= len(text) || text[at] != '[' {
			return nil, ErrRecordingInvalid
		}
		if block {
			// Replace `calls: []` with the three fields; refuse anything less plain.
			end := at + 1 + strings.IndexByte(text[at+1:], ']')
			fields := ordered{"description", tool.Description, "parameters", json.RawMessage(parameters), "calls", []any{json.RawMessage(call)}}
			value, err := blockYAML(fields, strings.Repeat(" ", key.Column-1), "\n")
			if entry.Style&yaml.FlowStyle != 0 || end <= at || strings.TrimSpace(text[at+1:end]) != "" || err != nil || !plainCalls.MatchString(text[offset(key):at]) {
				return nil, ErrRecordingInvalid
			}
			eol := "\n"
			if strings.HasSuffix(lines[open+key.Line], "\r\n") {
				eol = "\r\n"
			}
			edits = append(edits, edit{offset(key), end + 1, strings.ReplaceAll(value, "\n", eol)})
			continue
		}
		edits = append(edits, edit{at + 1, at + 1, string(call)})
	}
	slices.SortFunc(edits, func(a, b edit) int { return b.at - a.at })
	for _, e := range edits {
		text = text[:e.at] + e.text + text[e.end:]
	}
	after, err := Parse("recording.agent.md", []byte(text))
	if want := projectAgent(before); err != nil || want == nil || !bytes.Equal(want, projectAgent(after)) {
		return nil, ErrRecordingInvalid
	}
	return []byte(text), nil
}

// plainCalls is an untagged, uncommented `calls:` key before its empty list.
var plainCalls = regexp.MustCompile(`^(calls|"calls"|'calls')[ \t]*:[ \t]*$`)

// decodedJSON admits only decoded JSON shapes with valid UTF-8 text, which
// encoding/json would otherwise coerce on both sides of the final comparison.
func decodedJSON(value any) bool {
	switch v := value.(type) {
	case nil, bool, float64, json.Number:
		return true
	case string:
		return utf8.ValidString(v)
	case []any:
		return !slices.ContainsFunc(v, func(x any) bool { return !decodedJSON(x) })
	case map[string]any:
		for key, x := range v {
			if !utf8.ValidString(key) || !decodedJSON(x) {
				return false
			}
		}
		return true
	}
	return false
}

// projectAgent compares authored meaning without locations or revision. The
// JSON round trip treats lexical forms such as 1.0 and 1 as the same value.
func projectAgent(a *Agent) []byte {
	examples := make([]any, len(a.Examples))
	for i, e := range a.Examples {
		examples[i] = []any{e.ID, e.Input, e.Mocks, e.Assertions}
	}
	raw, err := json.Marshal([]any{a.Identity, a.Title, a.Instructions, a.Model, a.Tools, examples})
	var value any
	if err != nil || json.Unmarshal(raw, &value) != nil {
		return nil
	}
	raw, _ = json.Marshal(value)
	return raw
}
