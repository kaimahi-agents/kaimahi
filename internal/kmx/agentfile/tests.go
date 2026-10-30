package agentfile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Tests contains ordered authored cases, not a safe log or execution authority.
// ParseTests retains exact Source bytes and their Revision. ComposePair's inline
// suite instead has the agent Revision and no Source; the caller owns those bytes.
type Tests struct {
	Examples []Example
	Source   []byte
	Revision string
}

// ParseTests reads schemaVersion 1 Markdown with one yaml agent-test mapping per
// fence. Prose and unrelated fences are source-only. It performs no filesystem IO.
func ParseTests(filename string, data []byte) (*Tests, error) {
	tests, _, err := parseTests(filename, data)
	return tests, err
}

// parseTests also returns the canonical node tree with whole-source positions.
func parseTests(filename string, data []byte) (*Tests, *yaml.Node, error) {
	text, err := checkedSourceText(filename, data)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(text, "\n")
	location := func(n *yaml.Node) Location {
		column := n.Column
		if n.Line > 0 && n.Line <= len(lines) {
			runes := 0
			for offset := range lines[n.Line-1] {
				runes++
				if runes == n.Column {
					column = offset + 1
					break
				}
			}
		}
		return Location{filename, n.Line, column}
	}
	if lines[0] != "---" {
		return nil, nil, refuse(filename, 1, 1, "Frontmatter", "start with schemaVersion: 1 YAML frontmatter")
	}
	end := 1
	for end < len(lines) && lines[end] != "---" {
		end++
	}
	if end == len(lines) {
		return nil, nil, refuse(filename, 1, 1, "Frontmatter", "close the YAML frontmatter with a --- line")
	}
	header, err := decodeTestsDocument(filename, lines[1:end], 1, "Frontmatter")
	if err != nil {
		return nil, nil, err
	}
	nodes := 0
	if err := checkExamplesYAML(header, location, "Frontmatter", &nodes); err != nil {
		return nil, nil, err
	}
	if len(header.Content) != 1 || header.Content[0].Kind != yaml.MappingNode || len(header.Content[0].Content) != 2 || header.Content[0].Content[0].Tag != "!!str" || header.Content[0].Content[0].Value != "schemaVersion" {
		return nil, nil, refuse(filename, 2, 1, "Frontmatter", "use only schemaVersion: 1 in frontmatter")
	}
	version := header.Content[0].Content[1]
	if version.Kind != yaml.ScalarNode || version.Tag != "!!int" || version.Value != "1" {
		return nil, nil, &Error{location(version), "Frontmatter", "set schemaVersion to integer 1"}
	}
	// Keep the canonical wrapper so recorded call payload recognition and structural
	// depth are identical to inline Examples. Fence nodes retain whole-file positions.
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Line: 1, Column: 1}
	root := &yaml.Node{Kind: yaml.DocumentNode, Line: 1, Column: 1, Content: []*yaml.Node{
		{Kind: yaml.MappingNode, Tag: "!!map", Line: 1, Column: 1, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "examples", Line: 1, Column: 1}, sequence,
		}},
	}}
	for i := end + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.Contains(line, "<!--") {
			return nil, nil, refuse(filename, i+1, 1, "Tests", "remove HTML comments outside fenced prose")
		}
		open, info, ok := openFence(line)
		// Reserved lookalikes cannot silently become prose or hidden tests. A wider
		// unrelated fence is consumed as a unit, so its nested sample fences stay inert.
		// Test openers start at column 1; an indented list sample must not execute.
		candidate := strings.TrimLeft(line, " \t>-+*0123456789.)")
		reserved := strings.Contains(strings.ToLower(candidate), "agent-test") && (strings.HasPrefix(candidate, "```") || strings.HasPrefix(candidate, "~~~"))
		if reserved && (!ok || info != "yaml agent-test" || line != strings.TrimLeft(line, " \t")) {
			return nil, nil, refuse(filename, i+1, 1, "Tests", "use a canonical yaml agent-test fence opening at column 1")
		}
		if !ok {
			continue
		}
		close := i + 1
		for close < len(lines) && !closeFence(lines[close], open) {
			close++
		}
		if close == len(lines) {
			return nil, nil, refuse(filename, i+1, 1, "Tests", "close every Markdown fence")
		}
		if info == "yaml agent-test" {
			document, err := decodeTestsDocument(filename, lines[i+1:close], i+1, "Examples")
			if err != nil {
				return nil, nil, err
			}
			if len(document.Content) != 1 {
				return nil, nil, refuse(filename, i+2, 1, "Examples", "provide one test mapping")
			}
			sequence.Content = append(sequence.Content, document.Content[0])
		}
		i = close
	}
	examples, err := parseExamplesDocument(filename, root, location, true, nodes)
	if err != nil {
		return nil, nil, err
	}
	return &Tests{Examples: examples, Source: bytes.Clone(data), Revision: fmt.Sprintf("sha256:%x", sha256.Sum256(data))}, root, nil
}

// ApplyTestRecordings fills pending recorded calls in exact Markdown tests bytes.
// Only targeted fixtures change; prose and unrelated fixtures remain byte-exact.
// All recordings apply or none do. This function grants no filesystem authority.
func ApplyTestRecordings(source []byte, recordings []Recording) ([]byte, error) {
	if block, err := applyTestRecordings(source, recordings, true); err == nil {
		return block, nil
	}
	return applyTestRecordings(source, recordings, false)
}

func applyTestRecordings(source []byte, recordings []Recording, block bool) ([]byte, error) {
	before, root, err := parseTests("recording.agent.test.md", source)
	if err != nil {
		return nil, ErrRecordingChanged
	}
	return patchRecordings(source, recordings, before.Examples, root, 0, block, func(candidate []byte) bool {
		after, err := ParseTests("recording.agent.test.md", candidate)
		if err != nil {
			return false
		}
		want := projectJSON(projectExamples(before.Examples))
		return want != nil && bytes.Equal(want, projectJSON(projectExamples(after.Examples)))
	})
}

func decodeTestsDocument(filename string, lines []string, base int, field string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	var root, extra yaml.Node
	err := decoder.Decode(&root)
	if errors.Is(err, io.EOF) {
		return &root, nil
	}
	if err != nil {
		return nil, refuse(filename, base+1, 1, field, "provide valid YAML without unsupported syntax")
	}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, refuse(filename, base+1, 1, field, "keep exactly one YAML document")
	}
	var shift func(*yaml.Node)
	shift = func(n *yaml.Node) {
		n.Line += base
		for _, child := range n.Content {
			shift(child)
		}
	}
	shift(&root)
	return &root, nil
}
