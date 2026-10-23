package agentfile

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/runtime"
	"go.yaml.in/yaml/v3"
)

func parseExamples(file string, lines []string, firstLine int) ([]Example, error) {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start == len(lines) {
		return nil, refuse(file, firstLine, 1, "Examples", "add one fenced yaml examples document")
	}
	open, info, ok := openFence(lines[start])
	if !ok || info != "yaml" {
		return nil, refuse(file, firstLine+start, 1, "Examples", "use one fenced yaml examples document")
	}
	end := start + 1
	for end < len(lines) && !closeFence(lines[end], open) {
		end++
	}
	if end == len(lines) {
		return nil, refuse(file, firstLine+start, 1, "Examples", "close the yaml fence")
	}
	for i, line := range lines[end+1:] {
		if strings.TrimSpace(line) != "" {
			return nil, refuse(file, firstLine+end+1+i, 1, "Examples", "remove text or additional fences after the examples document")
		}
	}
	// The LF before the closing fence belongs to the YAML document: clipping
	// block scalars must retain it, including for shared assertion validation.
	data := []byte(strings.Join(lines[start+1:end], "\n") + "\n")
	base := firstLine + start // YAML line 1 is the line after the opening fence.
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return nil, refuse(file, base+1, 1, "Examples", "provide valid YAML without unsupported syntax")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, refuse(file, base+1, 1, "Examples", "keep exactly one YAML document")
	}
	location := func(n *yaml.Node) Location {
		column := n.Column
		if index := start + n.Line; index < len(lines) && index >= 0 {
			runes := 0
			for offset := range lines[index] {
				runes++
				if runes == n.Column {
					column = offset + 1
					break
				}
			}
		}
		return Location{file, base + n.Line, column}
	}
	errAt := func(n *yaml.Node, problem string) *Error {
		return &Error{location(n), "Examples", problem}
	}
	nodes := 0
	var check func(*yaml.Node, int) error
	check = func(n *yaml.Node, depth int) error {
		nodes++
		if depth > 12 || nodes > 32768 {
			return errAt(n, "reduce YAML complexity (12 levels and 32768 nodes maximum)")
		}
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return errAt(n, "remove YAML anchors and aliases")
		}
		if n.Tag != "" && n.Tag != "!!str" && n.Tag != "!!int" && n.Tag != "!!map" && n.Tag != "!!seq" && n.Tag != "!!null" && n.Tag != "!!bool" && n.Tag != "!!float" {
			return errAt(n, "use documented YAML types; quote timestamp-like strings and remove custom tags/merge keys")
		}
		if len(n.Value) > MaxInputBytes {
			return errAt(n, "keep each YAML scalar within 16 KiB")
		}
		for _, child := range n.Content {
			if err := check(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(&root, 0); err != nil {
		return nil, err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, refuse(file, base+1, 1, "Examples", "provide one examples mapping")
	}
	fields := func(n *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
		if n.Kind != yaml.MappingNode {
			return nil, errAt(n, "use a mapping with the documented required fields")
		}
		values := map[string]*yaml.Node{}
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !slices.Contains(allowed, key.Value) {
				return nil, errAt(key, "remove unsupported fields; use the documented examples/assertion fields")
			}
			if _, exists := values[key.Value]; exists {
				return nil, errAt(key, "remove duplicate YAML fields")
			}
			values[key.Value] = value
		}
		for _, name := range allowed {
			if values[name] == nil {
				return nil, errAt(n, "add all required examples fields: "+strings.Join(allowed, ", "))
			}
		}
		return values, nil
	}
	doc, err := fields(root.Content[0], "examples")
	if err != nil {
		return nil, err
	}
	sequence := doc["examples"]
	// An explicit empty sequence is a saved zero-test charter. Missing/null values
	// remain refusals; Test, export and evidence callers refuse zero examples.
	if sequence.Kind != yaml.SequenceNode || len(sequence.Content) > MaxExamples {
		return nil, errAt(sequence, "provide a list of 0–128 ordered examples")
	}
	examples := make([]Example, 0, len(sequence.Content))
	seen := map[string]bool{}
	for _, node := range sequence.Content {
		values, err := fields(node, "id", "input", "assertions")
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"id", "input"} {
			n := values[key]
			if n.Kind != yaml.ScalarNode || n.Tag != "!!str" || strings.TrimSpace(n.Value) == "" {
				return nil, errAt(n, "example id and input must be nonblank strings")
			}
		}
		if strings.IndexFunc(values["input"].Value, unsupportedControl) >= 0 {
			return nil, errAt(values["input"], "remove control characters except newline/tab and use LF or CRLF line breaks")
		}
		id := values["id"].Value
		if !exampleID.MatchString(id) || seen[id] {
			return nil, errAt(values["id"], "use unique example IDs: 1–63 letters, digits, '.', '_' or '-', starting with a letter or digit")
		}
		seen[id] = true
		assertions := values["assertions"]
		if assertions.Kind != yaml.SequenceNode || len(assertions.Content) == 0 || len(assertions.Content) > MaxAssertions {
			return nil, errAt(assertions, "provide 1–64 assertions for each example")
		}
		e := Example{ID: id, Input: values["input"].Value, Location: location(node)}
		assertionIDs := map[string]bool{}
		for _, n := range assertions.Content {
			if n.Kind != yaml.MappingNode {
				return nil, errAt(n, "each assertion must be a mapping")
			}
			var assertion runtime.EvaluationAssertion
			// The shared unmarshaller enforces operand presence/type and rejects unknown
			// or duplicate fields. Its errors can echo keys, so never forward them.
			if err := n.Decode(&assertion); err != nil {
				return nil, errAt(n, "use documented assertion fields and typed operands without duplicate keys")
			}
			switch assertion.Type {
			case "contains", "notContains", "regex", "llmJudge":
			default:
				return nil, errAt(n, "use contains, notContains, regex or llmJudge; tool assertions are unsupported")
			}
			if assertionIDs[assertion.ID] {
				return nil, errAt(n, "use unique assertion IDs within each example")
			}
			for _, operand := range []string{assertion.Value, assertion.Pattern, assertion.Rubric} {
				if strings.IndexFunc(operand, unsupportedControl) >= 0 {
					return nil, errAt(n, "remove control characters except newline/tab and use LF or CRLF line breaks")
				}
			}
			if err := runtime.ValidateEvaluationAssertions([]runtime.EvaluationAssertion{assertion}); err != nil {
				return nil, errAt(n, "provide a valid assertion ID and operand; llmJudge requires rubric, minScore (0–100), judgeModel and judgeVersion")
			}
			assertionIDs[assertion.ID] = true
			e.Assertions = append(e.Assertions, assertion)
			e.AssertionLocations = append(e.AssertionLocations, location(n))
		}
		if err := runtime.ValidateEvaluationCase(runtime.EvaluationCase{ID: e.ID, Input: e.Input, Assertions: e.Assertions}); err != nil {
			return nil, errAt(values["input"], "provide valid example input without credential-shaped content")
		}
		examples = append(examples, e)
	}
	return examples, nil
}
