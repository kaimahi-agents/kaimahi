package agentfile

import (
	"bytes"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

const emptyTestsSource = "---\nschemaVersion: 1\n---\n"

type testsFence struct{ start, end, close int }

// testsFences uses the parser's fence recognizers, not another Markdown grammar.
// Its input has already passed parseTests, including all closing-fence checks.
func testsFences(source []byte) []testsFence {
	lines := strings.SplitAfter(string(source), "\n")
	starts := make([]int, len(lines)+1)
	for i, line := range lines {
		starts[i+1] = starts[i] + len(line)
	}
	plain := func(i int) string { return strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r") }
	var spans []testsFence
	for i := 0; i < len(lines); i++ {
		marker, info, ok := openFence(plain(i))
		if !ok {
			continue
		}
		close := i + 1
		for close < len(lines) && !closeFence(plain(close), marker) {
			close++
		}
		if info == "yaml agent-test" {
			spans = append(spans, testsFence{starts[i], starts[close+1], close})
		}
		i = close
	}
	return spans
}

// editTestsFence delegates to the existing field/fixture patcher. Validate each
// mapping independently, then the whole final batch: a valid replacement at the
// case limit or an ID swap must not fail on an intermediate document.
func editTestsFence(source []byte, root *yaml.Node, span testsFence, index int, edit ExampleEdit, eol string) ([]byte, error) {
	node := root.Content[0].Content[1].Content[index]
	one := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "examples", Line: 1, Column: 1}, {Kind: yaml.SequenceNode, Line: 1, Column: 1, Content: []*yaml.Node{node}}}}}}
	extract := func(candidate []byte) []byte { return candidate[span.start : span.end+len(candidate)-len(source)] }
	parse := func(candidate []byte) (*Tests, error) {
		return ParseTests("edit.agent.test.md", append([]byte(emptyTestsSource), extract(candidate)...))
	}
	valid := func(candidate []byte) bool { _, err := parse(candidate); return err == nil }
	exact, err := patchExampleNodes(source, []ExampleEdit{edit}, one, -1, span.close, eol, false, valid)
	if err != nil {
		return nil, err
	}
	if block, err := patchExampleNodes(source, []ExampleEdit{edit}, one, -1, span.close, eol, true, valid); err == nil {
		x, _ := parse(exact)
		y, _ := parse(block)
		if bytes.Equal(projectJSON(projectExamples(x.Examples)), projectJSON(projectExamples(y.Examples))) {
			return bytes.Clone(extract(block)), nil
		}
	}
	return bytes.Clone(extract(exact)), nil
}

// ApplyTestsEdits applies the existing sparse authoring intents to companion
// fences. Nil source seeds the approved empty container; deleting the last case
// keeps that container present. It grants no filesystem or publication authority.
func ApplyTestsEdits(source []byte, edits []ExampleEdit) ([]byte, error) {
	if len(edits) == 0 || len(edits) > MaxExamples {
		return nil, ErrRecordingInvalid
	}
	if source == nil {
		source = []byte(emptyTestsSource)
	}
	before, root, err := parseTests("edit.agent.test.md", source)
	if err != nil {
		return nil, ErrRecordingInvalid
	}
	spans, seen := testsFences(source), map[string]bool{}
	eol := "\n"
	if bytes.Contains(source, []byte("\r\n")) {
		eol = "\r\n"
	}
	var patches []sourcePatch
	additions := ""
	for _, edit := range edits {
		identity := edit.BeforeID
		if identity == "" {
			identity = edit.ID
		}
		if identity == "" || seen[identity] {
			return nil, ErrRecordingInvalid
		}
		seen[identity] = true
		index := -1
		for i, e := range before.Examples {
			if e.ID == edit.BeforeID {
				index = i
			}
		}
		if edit.BeforeID != "" && index < 0 {
			return nil, ErrRecordingChanged
		}
		if edit.Delete {
			if edit.BeforeID == "" || edit.ID != "" || edit.Input != nil || edit.Assertions != nil || len(edit.Tools) > 0 {
				return nil, ErrRecordingInvalid
			}
			span := spans[index]
			if bytes.HasPrefix(source[span.end:], []byte("\r\n")) {
				span.end += 2
			} else if bytes.HasPrefix(source[span.end:], []byte("\n")) {
				span.end++
			}
			patches = append(patches, sourcePatch{span.start, span.end, ""})
		} else if index >= 0 {
			span := spans[index]
			fence, err := editTestsFence(source, root, span, index, edit, eol)
			if err != nil {
				return nil, err
			}
			patches = append(patches, sourcePatch{span.start, span.end, string(fence)})
		} else {
			if edit.ID == "" || edit.Input == nil || edit.Assertions == nil {
				return nil, ErrRecordingInvalid
			}
			seed := "kmx-new-test-placeholder"
			body, err := blockYAML(ordered{"id", seed, "input", "placeholder", "assertions", []any{ordered{"id", "placeholder", "type", "contains", "value", "placeholder"}}}, "", eol)
			if err != nil {
				return nil, ErrRecordingInvalid
			}
			mini := []byte(emptyTestsSource + "```yaml agent-test" + eol + body + eol + "```" + eol)
			_, tree, err := parseTests("edit.agent.test.md", mini)
			if err != nil {
				return nil, ErrRecordingInvalid
			}
			edit.BeforeID = seed
			fence, err := editTestsFence(mini, tree, testsFences(mini)[0], 0, edit, eol)
			if err != nil {
				return nil, err
			}
			additions += eol + string(fence)
		}
	}
	if additions != "" {
		if !bytes.HasSuffix(source, []byte("\n")) {
			additions = eol + additions
		}
		patches = append(patches, sourcePatch{len(source), len(source), additions})
	}
	slices.SortStableFunc(patches, func(a, b sourcePatch) int { return b.start - a.start })
	candidate := string(source)
	for _, p := range patches {
		candidate = candidate[:p.start] + p.text + candidate[p.end:]
	}
	if _, err := ParseTests("edit.agent.test.md", []byte(candidate)); err != nil {
		return nil, ErrRecordingInvalid
	}
	return []byte(candidate), nil
}
