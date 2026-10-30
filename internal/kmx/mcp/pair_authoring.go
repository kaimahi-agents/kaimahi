package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentfile"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pairEditInput struct {
	Path                    string                  `json:"path"`
	Tests                   string                  `json:"tests"`
	ExpectedSourceVersion   string                  `json:"expectedSourceVersion"`
	Content                 *string                 `json:"content"`
	TestsContent            *string                 `json:"testsContent"`
	RestoreVersionID        string                  `json:"restoreVersionId"`
	AcknowledgeCopilotTests bool                    `json:"acknowledgeCopilotTests"`
	ExampleEdits            []agentfile.ExampleEdit `json:"exampleEdits"`
}

func (f *Files) createPairBinding(ctx context.Context, p string, agentBytes, testsBytes []byte) (*pairBinding, error) {
	normalized, err := f.rootRelative(p)
	if err != nil {
		return nil, err
	}
	if !f.allowedPath(normalized) {
		return nil, refusal("unsafe_path")
	}
	physical := f.physicalSource(normalized)
	target, supported := agentfile.DefaultTestsPath(physical)
	if !supported {
		return nil, refusal("invalid_options")
	}
	relative, err := f.rootRelativeSource(target, true)
	if err != nil {
		return nil, err
	}
	target = f.physicalSource(relative)
	for _, path := range []string{physical, target} {
		normalized, err := f.rootRelativeSource(path, true)
		if err != nil {
			return nil, err
		}
		stat := os.Lstat
		if f.root != nil {
			stat = f.root.Lstat
		}
		_, err = stat(normalized)
		if err == nil {
			return nil, refusal("already_exists")
		}
		if !os.IsNotExist(err) {
			return nil, refusal("unsafe_path")
		}
	}
	a, err := agentfile.Parse(physical, agentBytes)
	if err != nil {
		return nil, refusal("invalid_file")
	}
	pair, err := agentfile.ComposePair(physical, a, agentfile.TestsSelection{Path: target}, testsBytes, true)
	if err != nil {
		return nil, refusal("invalid_file")
	}
	b := &pairBinding{files: f, pair: pair, sources: sourcesFor(pair), requested: p}
	b.agentAbsent, err = f.holdAbsentSource(physical)
	if err != nil {
		return nil, err
	}
	b.absent, err = f.holdAbsentSource(target)
	if err != nil {
		b.close()
		return nil, err
	}
	if err := b.check(ctx); err != nil {
		b.close()
		return nil, err
	}
	return b, nil
}

func (b *pairBinding) restored(ctx context.Context, id string) ([]byte, []byte, agentfile.TestsSelection, error) {
	root, key, err := b.files.historyLocation(b.sources.AgentPath)
	if err != nil {
		return nil, nil, agentfile.TestsSelection{}, err
	}
	if b.files.historyDir == nil {
		return nil, nil, agentfile.TestsSelection{}, refusal("history_unavailable")
	}
	s, err := b.files.openHistory(ctx, root, false)
	if err != nil {
		return nil, nil, agentfile.TestsSelection{}, err
	}
	defer s.close()
	r, err := s.get(key, id)
	if err != nil {
		return nil, nil, agentfile.TestsSelection{}, err
	}
	if r.Pair == nil || r.Pair.AgentPath != b.sources.AgentPath || r.Pair.TestsPath != b.sources.Tests.Selection.Path || r.Pair.SelectionKey != b.sources.Tests.Selection.Key {
		return nil, nil, agentfile.TestsSelection{}, refusal("incompatible_version")
	}
	selection := b.sources.Tests.Selection
	// Retained absence removes the current selected leaf, even when the caller
	// explicitly named that default. Present suites keep today's selector intent.
	if r.Pair.TestsState != "external" {
		selection.Explicit = false
	}
	return r.Source, r.Pair.Source, selection, nil
}

func pairAuthoringOperation(files *Files, name string) *Operation {
	properties := map[string]*jsonschema.Schema{"path": {Type: "string", MinLength: new(1)}, "content": {Type: "string", MaxLength: new(maxFileBytes)}, "testsContent": {Type: "string", MaxLength: new(maxFileBytes)}, "acknowledgeCopilotTests": {Type: "boolean", Description: copilotTestsNotice}}
	required := []string{"path", "content", "testsContent"}
	if name == "edit" {
		required = []string{"path", "expectedSourceVersion"}
		properties["expectedSourceVersion"] = &jsonschema.Schema{Type: "string", MinLength: new(1), MaxLength: new(128)}
		properties["tests"] = &jsonschema.Schema{Type: "string", MinLength: new(1)}
		properties["exampleEdits"] = exampleEditsSchema()
		properties["restoreVersionId"] = &jsonschema.Schema{Type: "string", MinLength: new(1), MaxLength: new(128)}
	}
	return &Operation{Schema: &jsonschema.Schema{Type: "object", Required: required, Properties: properties, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}, Handler: func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var input pairEditInput
		if json.Unmarshal(req.Params.Arguments, &input) != nil || !strings.HasSuffix(input.Path, ".agent.md") {
			return nil, refusal("invalid_options")
		}
		var bound *pairBinding
		var err error
		var agentBytes, testsBytes []byte
		var selection agentfile.TestsSelection
		if name == "create" {
			agentBytes, testsBytes = []byte(*input.Content), []byte(*input.TestsContent)
			bound, err = files.createPairBinding(ctx, input.Path, agentBytes, testsBytes)
			if err != nil {
				return nil, err
			}
			selection = bound.sources.Tests.Selection
		} else {
			bound, err = files.bindPairMode(ctx, input.Path, input.Tests, true)
			if err != nil {
				return nil, err
			}
			if err = bound.requireVersion(input.ExpectedSourceVersion); err != nil {
				bound.close()
				return nil, err
			}
			agentBytes = bound.agent.source
			if bound.tests != nil {
				testsBytes = bound.tests.source
			}
			selection = bound.sources.Tests.Selection
			if input.RestoreVersionID != "" {
				if input.Content != nil || input.TestsContent != nil || input.ExampleEdits != nil {
					bound.close()
					return nil, refusal("invalid_options")
				}
				agentBytes, testsBytes, selection, err = bound.restored(ctx, input.RestoreVersionID)
			} else {
				if input.Content == nil && input.TestsContent == nil && len(input.ExampleEdits) == 0 || input.TestsContent != nil && input.ExampleEdits != nil {
					bound.close()
					return nil, refusal("invalid_options")
				}
				if input.Content != nil {
					agentBytes = []byte(*input.Content)
				}
				if input.TestsContent != nil {
					testsBytes = []byte(*input.TestsContent)
				}
				if len(input.ExampleEdits) > 0 {
					if bound.sources.Tests.State == "inline" {
						agentBytes, err = agentfile.ApplyExampleEdits(agentBytes, input.ExampleEdits)
					} else {
						testsBytes, err = agentfile.ApplyTestsEdits(testsBytes, input.ExampleEdits)
					}
					if err != nil {
						err = refusal("invalid_file")
					}
				}
			}
			if err != nil {
				bound.close()
				return nil, err
			}
		}
		defer bound.close()
		parsed, err := agentfile.Parse(bound.sources.AgentPath, agentBytes)
		if err != nil {
			return nil, refusal("invalid_file")
		}
		desired, err := agentfile.ComposePair(bound.sources.AgentPath, parsed, selection, testsBytes, testsBytes != nil)
		if err != nil {
			return nil, refusal("invalid_file")
		}
		var before *agentfile.Agent
		var previous []byte
		if bound.agent != nil {
			before, previous = bound.sources.Agent, bound.agent.source
		}
		if !input.AcknowledgeCopilotTests && inlineCopilotTestsChanged(before, previous, parsed, agentBytes) {
			return nil, refusal("confirmation_required")
		}
		result := bound.publish(ctx, desired, agentBytes, testsBytes)
		return &sdk.CallToolResult{IsError: result.State != "complete", StructuredContent: result, Content: []sdk.Content{&sdk.TextContent{Text: "Selected sources: " + result.State + ". Inspect each source acknowledgement before continuing."}}}, nil
	}}
}
