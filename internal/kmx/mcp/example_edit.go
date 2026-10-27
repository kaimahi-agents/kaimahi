package mcp

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
)

func exampleEditsSchema() *jsonschema.Schema {
	closed := func(properties map[string]*jsonschema.Schema) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "object", Properties: properties, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
	}
	tool := closed(map[string]*jsonschema.Schema{"name": {Type: "string"}, "mode": {Type: "string", Enum: []any{"live", "recorded"}}, "reset": {Type: "boolean"}})
	tool.Required = []string{"name", "mode"}
	edit := closed(map[string]*jsonschema.Schema{"beforeId": {Type: "string"}, "id": {Type: "string"}, "input": {Type: "string"}, "assertions": {Type: "array", Items: &jsonschema.Schema{Type: "object"}}, "tools": {Type: "array", MaxItems: new(32), Items: tool}, "delete": {Type: "boolean"}})
	return &jsonschema.Schema{Type: "array", MinItems: new(1), MaxItems: new(128), Items: edit}
}

// editExamples retains the original observation through publication. The content
// operand may edit other sections, but cannot smuggle a lossy Examples rewrite.
func (f *Files) editExamples(ctx context.Context, p, expected string, content []byte, edits []agentfile.ExampleEdit) (File, error) {
	requested := p
	p, err := f.rootRelative(p)
	if err != nil {
		return File{}, err
	}
	if !f.allowedPath(p) {
		return File{}, refusal("invalid_path")
	}
	parent, err := f.parent(p, false)
	if err != nil {
		return File{}, err
	}
	defer parent.Close()
	target, source, info, err := readTarget(parent, filepath.Base(p))
	if err != nil {
		return File{}, err
	}
	target.Close()
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return File{}, err
	}
	observed := &sourceIdentity{file: info, parent: parentInfo}
	if revision(source) != expected || !f.observedParent(p, parent, observed) {
		return File{}, refusal("stale_revision")
	}
	section := func(b []byte) ([]byte, error) {
		a, err := agentfile.Parse(p, b)
		if err != nil {
			return nil, err
		}
		examples, ok := a.Locations["Examples"]
		if !ok {
			return nil, nil // no tests yet; ApplyExampleEdits may append the first
		}
		lines := strings.SplitAfter(string(b), "\n")
		at := 0
		for _, line := range lines[:examples.Line-1] {
			at += len(line)
		}
		return b[at:], nil
	}
	original, err := section(source)
	if err != nil {
		return File{}, refusal("invalid_file")
	}
	supplied, err := section(content)
	if err != nil || !bytes.Equal(original, supplied) {
		return File{}, refusal("invalid_options")
	}
	candidate, err := agentfile.ApplyExampleEdits(content, edits)
	if err != nil {
		return File{}, refusal("invalid_options")
	}
	return f.editObserved(ctx, requested, expected, candidate, observed)
}
