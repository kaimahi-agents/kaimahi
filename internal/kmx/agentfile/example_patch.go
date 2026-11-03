package agentfile

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

type ToolModeEdit struct {
	Name  string `json:"name"`
	Mode  string `json:"mode"`
	Reset bool   `json:"reset,omitempty"`
}
type ExampleEdit struct {
	BeforeID   string          `json:"beforeId,omitempty"`
	ID         string          `json:"id,omitempty"`
	Input      *string         `json:"input,omitempty"`
	Assertions json.RawMessage `json:"assertions,omitempty"`
	Tools      []ToolModeEdit  `json:"tools,omitempty"`
	Delete     bool            `json:"delete,omitempty"`
}
type sourcePatch struct {
	start, end int
	text       string
}

// ApplyExampleEdits changes only explicitly selected fields. Existing fixtures
// never pass through JSON serialization, including when an unrelated input changes.
// New values are written as block YAML when they read back exactly like the
// JSON-only form; flow layouts and anything else keep that JSON form.
func ApplyExampleEdits(source []byte, edits []ExampleEdit) ([]byte, error) {
	if len(edits) == 0 || len(edits) > MaxExamples {
		return nil, ErrRecordingInvalid
	}
	source, edits, err := withExamples(source, edits)
	if err != nil {
		return nil, err
	}
	exact, err := applyExampleEdits(source, edits, false)
	if err != nil {
		return nil, err
	}
	if block, err := applyExampleEdits(source, edits, true); err == nil && sameAuthoring(exact, block) {
		return block, nil
	}
	return exact, nil
}

// withExamples appends a final Examples section holding one placeholder test to a
// file that has none, and deletes it in the edits, so the section is rewritten as
// ordinary block YAML containing only the new tests. Existing bytes stay untouched;
// only additions can seed one, and a prompt that swallows the section refuses.
func withExamples(source []byte, edits []ExampleEdit) ([]byte, []ExampleEdit, error) {
	before, err := Parse("edit.agent.md", source)
	if err != nil {
		return nil, nil, ErrRecordingInvalid
	}
	if _, ok := before.Locations["Examples"]; ok {
		return source, edits, nil
	}
	if slices.ContainsFunc(edits, func(e ExampleEdit) bool { return e.BeforeID != "" || e.Delete }) {
		return nil, nil, ErrRecordingChanged
	}
	eol, text := "\n", string(source)
	if bytes.Contains(source, []byte("\r\n")) {
		eol = "\r\n"
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += eol
	}
	if text != "" && !strings.HasSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		text += eol
	}
	placeholder := "kmx-first-test-placeholder"
	for i := 0; slices.ContainsFunc(edits, func(e ExampleEdit) bool { return e.ID == placeholder }); i++ {
		placeholder = "kmx-first-test-placeholder-" + strconv.Itoa(i)
	}
	text += strings.Join([]string{"## Examples", "", "```yaml", "examples:", "  - id: " + placeholder, "    input: x", "    assertions: [{id: x, type: contains, value: x}]", "```", ""}, eol)
	if seeded, err := Parse("edit.agent.md", []byte(text)); err != nil || seeded.Locations["Examples"].Line == 0 {
		return nil, nil, ErrRecordingInvalid
	}
	return []byte(text), append([]ExampleEdit{{BeforeID: placeholder, Delete: true}}, edits...), nil
}

func sameAuthoring(a, b []byte) bool {
	x, err1 := Parse("edit.agent.md", a)
	y, err2 := Parse("edit.agent.md", b)
	return err1 == nil && err2 == nil && bytes.Equal(projectAgent(x), projectAgent(y)) && projectAgent(x) != nil
}

func applyExampleEdits(source []byte, edits []ExampleEdit, block bool) ([]byte, error) {
	before, err := Parse("edit.agent.md", source)
	if err != nil || len(edits) == 0 || len(edits) > MaxExamples+1 {
		return nil, ErrRecordingInvalid
	}
	text := string(source)
	lines := strings.SplitAfter(text, "\n")
	plain := func(i int) string { return strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r") }
	open := before.Locations["Examples"].Line
	for strings.TrimSpace(plain(open)) == "" {
		open++
	}
	marker, _, _ := openFence(plain(open))
	close := open + 1
	for !closeFence(plain(close), marker) {
		close++
	}
	var root yaml.Node
	if yaml.Unmarshal([]byte(strings.Join(func() []string {
		r := []string{}
		for i := open + 1; i < close; i++ {
			r = append(r, plain(i))
		}
		return r
	}(), "\n")+"\n"), &root) != nil {
		return nil, ErrRecordingInvalid
	}
	eol := "\n"
	if strings.HasSuffix(lines[open], "\r\n") {
		eol = "\r\n"
	}
	return patchExampleNodes(source, edits, &root, open, close, eol, block, func(candidate []byte) bool {
		_, err := Parse("edit.agent.md", candidate)
		return err == nil
	})
}

// patchExampleNodes shares exact field/fixture patching between inline Examples
// and companion mappings. The caller supplies parsed nodes and source boundaries.
func patchExampleNodes(source []byte, edits []ExampleEdit, root *yaml.Node, open, close int, eol string, block bool, valid func([]byte) bool) ([]byte, error) {
	text := string(source)
	lines := strings.SplitAfter(text, "\n")
	starts := make([]int, len(lines))
	for i := 1; i < len(lines); i++ {
		starts[i] = starts[i-1] + len(lines[i-1])
	}
	offset := func(n *yaml.Node) int {
		at := starts[open+n.Line]
		for range n.Column - 1 {
			_, size := utf8.DecodeRuneInString(text[at:])
			at += size
		}
		return at
	}
	field := func(n *yaml.Node, key string) *yaml.Node {
		for i := 0; n != nil && i < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				return n.Content[i+1]
			}
		}
		return nil
	}
	// Limits follow the parsed tree, not a second interpretation of YAML. Flow
	// tokens have explicit delimiters; block values end before the next sibling.
	limits := map[*yaml.Node]int{}
	var endOf func(*yaml.Node) int
	var visit func(*yaml.Node, int)
	visit = func(n *yaml.Node, end int) {
		limits[n] = end
		for i, c := range n.Content {
			next := end
			if n.Style&yaml.FlowStyle != 0 {
				next = endOf(n) - 1
			}
			if i+1 < len(n.Content) {
				next = offset(n.Content[i+1])
				if n.Style&yaml.FlowStyle == 0 && c.Line != n.Content[i+1].Line {
					line := open + n.Content[i+1].Line
					if comment := n.Content[i+1].HeadComment; comment != "" {
						line -= strings.Count(comment, "\n") + 1
					}
					next = starts[line]
				}
			}
			visit(c, next)
		}
	}
	endOf = func(n *yaml.Node) int {
		start, end := offset(n), limits[n]
		if n.Style&yaml.FlowStyle != 0 || n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
			// Quoted strings and flow collections can span lines; comments and escaped
			// quote characters cannot terminate them.
			depth := 0
			quote := byte(0)
			begun := false
			for i := start; i < end; i++ {
				c := text[i]
				if quote != 0 {
					if quote == '"' && c == '\\' {
						i++
						continue
					}
					if c == quote {
						if quote == '\'' && i+1 < end && text[i+1] == '\'' {
							i++
							continue
						}
						quote = 0
						if depth == 0 {
							return i + 1
						}
					}
					continue
				}
				if c == '#' {
					for i < end && text[i] != '\n' {
						i++
					}
					continue
				}
				if c == '\'' || c == '"' {
					quote = c
					continue
				}
				if c == '[' || c == '{' {
					depth++
					begun = true
				}
				if c == ']' || c == '}' {
					depth--
					if begun && depth == 0 {
						return i + 1
					}
				}
			}
		}
		for end > start && strings.ContainsRune(" \t\r\n", rune(text[end-1])) {
			end--
		}
		if end > start && text[end-1] == ',' {
			end--
			for end > start && strings.ContainsRune(" \t\r\n", rune(text[end-1])) {
				end--
			}
		}
		return end
	}
	visit(root.Content[0], starts[close])
	seq := field(root.Content[0], "examples")
	if seq == nil {
		return nil, ErrRecordingInvalid
	}
	var patches []sourcePatch
	add := func(start, end int, value string) { patches = append(patches, sourcePatch{start, end, value}) }
	encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	indent := func(n *yaml.Node) string { return strings.Repeat(" ", n.Column-1) }
	// pair renders `key: value` as block YAML for a block mapping, or "" if not.
	pair := func(m *yaml.Node, key string, value any) string {
		if !block || m.Style&yaml.FlowStyle != 0 {
			return ""
		}
		text, _ := blockYAML(ordered{key, value}, indent(m), eol)
		return text
	}
	set := func(m *yaml.Node, key string, value any) {
		text := pair(m, key, value)
		if v := field(m, key); v != nil {
			if text != "" {
				add(offset(m.Content[slices.Index(m.Content, v)-1]), endOf(v), text)
				return
			}
			add(offset(v), endOf(v), encode(value))
			return
		}
		at := offset(m)
		if text != "" {
			// After the last value, so it never meets a replaced first key.
			at = endOf(m.Content[len(m.Content)-1])
			add(at, at, eol+indent(m)+text)
		} else if m.Style&yaml.FlowStyle != 0 {
			at = endOf(m) - 1
			sep := ", "
			if len(m.Content) == 0 {
				sep = ""
			}
			add(at, at, sep+encode(key)+": "+encode(value))
		} else {
			add(at, at, encode(key)+": "+encode(value)+eol+strings.Repeat(" ", m.Column-1))
		}
	}
	clearAll := false
	var replacement []any
	appendTo := func(s *yaml.Node, value any) {
		if s == seq && clearAll {
			replacement = append(replacement, value)
			return
		}
		at := endOf(s)
		if s.Style&yaml.FlowStyle != 0 {
			at--
			sep := ", "
			if len(s.Content) == 0 {
				sep = ""
			}
			add(at, at, sep+encode(value))
		} else if text, err := blockYAML([]any{value}, indent(s), eol); block && err == nil {
			at = limits[s]
			add(at, at, indent(s)+text+eol)
		} else {
			at = limits[s]
			add(at, at, strings.Repeat(" ", s.Column-1)+"- "+encode(value)+eol)
		}
	}
	seen := map[string]bool{}
	clearAll = len(seq.Content) > 0
	for _, item := range seq.Content {
		clearAll = clearAll && slices.ContainsFunc(edits, func(e ExampleEdit) bool { return e.Delete && e.BeforeID == field(item, "id").Value })
	}

	for _, edit := range edits {
		identity := edit.BeforeID
		if identity == "" {
			identity = edit.ID
		}
		if identity == "" || seen[identity] || len(edit.Tools) > 32 {
			return nil, ErrRecordingInvalid
		}
		seen[identity] = true
		var node *yaml.Node
		for _, item := range seq.Content {
			if field(item, "id").Value == edit.BeforeID {
				node = item
			}
		}
		if edit.BeforeID != "" && node == nil {
			return nil, ErrRecordingChanged
		}
		if edit.Delete {
			if edit.BeforeID == "" || edit.ID != "" || edit.Input != nil || edit.Assertions != nil || len(edit.Tools) > 0 {
				return nil, ErrRecordingInvalid
			}
			if clearAll {
				continue
			}
			if len(seq.Content) == 1 {
				add(offset(seq), endOf(seq), "[]")
			} else {
				start, end := offset(node), endOf(node)
				if seq.Style&yaml.FlowStyle != 0 {
					at := end
					for at < len(text) && strings.ContainsRune(" \t\r\n", rune(text[at])) {
						at++
					}
					if text[at] == ',' {
						end = at + 1
					} else {
						for start > 0 && text[start-1] != ',' {
							start--
						}
						start--
					}
				} else {
					start = starts[open+node.Line]
					end = limits[node]
				}
				add(start, end, "")
			}
			continue
		}
		var assertions any
		if edit.Assertions != nil && (json.Unmarshal(edit.Assertions, &assertions) != nil || !json.Valid(edit.Assertions)) {
			return nil, ErrRecordingInvalid
		}
		tools := []any{}
		names := map[string]bool{}
		for _, change := range edit.Tools {
			if names[change.Name] || (change.Mode != "live" && change.Mode != "recorded") || change.Reset && change.Mode != "recorded" {
				return nil, ErrRecordingInvalid
			}
			names[change.Name] = true
			value := ordered{"name", change.Name, "mode", change.Mode}
			if change.Mode == "recorded" {
				value = append(value, "calls", []any{})
			}
			tools = append(tools, value)
		}
		if node == nil {
			if edit.ID == "" || edit.Input == nil || edit.Assertions == nil {
				return nil, ErrRecordingInvalid
			}
			value := ordered{"id", edit.ID, "input", *edit.Input, "assertions", edit.Assertions}
			if len(tools) > 0 {
				value = append(value, "mocks", ordered{"tools", tools})
			}
			appendTo(seq, value)
			continue
		}
		if edit.ID != "" && edit.ID != edit.BeforeID {
			set(node, "id", edit.ID)
		}
		if edit.Input != nil && *edit.Input != field(node, "input").Value {
			set(node, "input", *edit.Input)
		}
		if edit.Assertions != nil {
			set(node, "assertions", edit.Assertions)
		}
		if len(tools) == 0 {
			continue
		}
		mocks := field(node, "mocks")
		if mocks == nil {
			set(node, "mocks", ordered{"tools", tools})
			continue
		}
		entries := field(mocks, "tools")
		for i, change := range edit.Tools {
			var entry *yaml.Node
			for _, item := range entries.Content {
				if field(item, "name").Value == change.Name {
					entry = item
				}
			}
			if entry == nil {
				appendTo(entries, tools[i])
				continue
			}
			if field(entry, "mode").Value == change.Mode && !change.Reset {
				continue
			}
			if text, err := blockYAML(tools[i], indent(entry), eol); block && err == nil && entry.Style&yaml.FlowStyle == 0 {
				add(offset(entry), endOf(entry), text)
			} else {
				add(offset(entry), endOf(entry), encode(tools[i]))
			}
		}
	}
	if clearAll {
		if replacement == nil {
			replacement = []any{}
		}
		if text := pair(root.Content[0], "examples", replacement); text != "" {
			add(offset(root.Content[0].Content[slices.Index(root.Content[0].Content, seq)-1]), endOf(seq), text)
		} else {
			add(offset(seq), endOf(seq), encode(replacement))
		}
	}
	// Coalesce insertions before sorting, otherwise two new fields/rows at one
	// offset could reverse or overlap. All replacements remain non-overlapping.
	slices.SortStableFunc(patches, func(a, b sourcePatch) int { return b.start - a.start })
	last := len(text) + 1
	for i := 0; i < len(patches); i++ {
		p := patches[i]
		for i+1 < len(patches) && p.text == "" && patches[i+1].text == "" && patches[i+1].end >= p.start {
			i++
			p.start = patches[i].start
			p.end = max(p.end, patches[i].end)
		}
		for i+1 < len(patches) && p.start == p.end && patches[i+1].start == p.start && patches[i+1].end == p.end {
			i++
			p.text += patches[i].text
		}
		if p.start < 0 || p.end > len(text) || p.end > last {
			return nil, ErrRecordingInvalid
		}
		last = p.start
		text = text[:p.start] + p.text + text[p.end:]
	}
	if !valid([]byte(text)) {
		return nil, ErrRecordingInvalid
	}
	if bytes.Equal(source, []byte(text)) {
		return bytes.Clone(source), nil
	}
	return []byte(text), nil
}
